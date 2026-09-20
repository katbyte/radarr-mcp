//go:build integration

package integration

// The read sweep: every GET operation in the definitions, called against the
// running server with arguments resolved from the fixtures, and its answer
// decoded (or its file read) - and then decoded again, strictly, against the
// model's own fields, so a field the server sends that the document does not
// declare is a failure rather than a value the SDK silently drops. The bespoke
// tests prove the shapes the tools rely on field by field; the sweep proves
// the rest of the read surface answers in its documented status and shape,
// and keeps doing so as the definitions change: a GET the importer adds is
// swept on the next run with nothing to write, and one that fails must be
// classified here.
//
// Every operation either answers, or has a sweepCase that says why not. A
// case whose operation starts answering fails the sweep, so a stale case is
// noticed and removed, the way a stale importer workaround is.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/radarr-mcp/internal/pandorest/definitions"
	"github.com/katbyte/radarr-mcp/lib/client"
)

// sweepCase is how the sweep treats one operation.
type sweepCase struct {
	// Skip leaves the operation uncalled, for the reason given.
	Skip string
	// Status is the error status the server answers with, and Decode is set
	// when it answers a body the documented model cannot decode; Why says
	// why that is the server's behaviour rather than a bug to fix.
	Status int
	Decode bool
	Why    string
	// Path and Options supply arguments by parameter name and options field,
	// beyond what the fixtures resolve.
	Path    map[string]string
	Options map[string]any
}

// sweepFixtures resolves arguments from the suite's fixtures. A path
// parameter is looked up by the literal path segments before the placeholder
// and the placeholder's name, the two segments before it first and then the
// one (config/indexer/id, then indexer/id: the indexer settings and an
// indexer are both {id} after "indexer"), then as the name alone; all
// case-insensitively. Required options are looked up by name.
type sweepFixtures struct {
	path    map[string]string
	options map[string]string
}

func (f sweepFixtures) resolvePath(path, param string) (string, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if !strings.Contains(seg, "{"+param+"}") {
			continue
		}
		for n := min(i, 2); n > 0; n-- {
			prefix := segments[i-n : i]
			if slices.ContainsFunc(prefix, func(s string) bool { return strings.Contains(s, "{") }) {
				continue
			}
			if v, ok := lookupFold(f.path, strings.Join(prefix, "/")+"/"+param); ok {
				return v, true
			}
		}
	}

	return lookupFold(f.path, param)
}

func lookupFold(m map[string]string, key string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}

	return "", false
}

// sweep calls every GET operation on the SDK.
func sweep(t *testing.T, fixtures sweepFixtures, cases map[string]sweepCase) {
	t.Helper()

	svc, err := definitions.Load(filepath.Join("..", "api-definitions", "radarr"))
	if err != nil {
		t.Fatal(err)
	}

	gets := map[string]bool{}
	for _, op := range svc.Operations() {
		if op.Method != http.MethodGet {
			continue
		}
		gets[op.Name] = true
		c := cases[op.Name]

		t.Run(op.Name, func(t *testing.T) {
			if c.Skip != "" {
				t.Skip(c.Skip)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()

			args, err := sweepArgs(ctx, op, fixtures, c)
			if err != nil {
				t.Fatalf("%s: %v; resolve it in the fixtures or give it a sweepCase", op.Key(), err)
			}
			status, decodeErr, unknown, err := sweepCall(op, args)
			switch {
			case err == nil && (c.Status != 0 || c.Decode):
				t.Errorf("%s now answers; drop its sweepCase (%s)", op.Key(), c.Why)
			case err == nil && len(unknown) > 0:
				t.Errorf("%s answers fields the document does not declare: %s", op.Key(), strings.Join(unknown, ", "))
			case err == nil:
			case c.Status != 0 && status == c.Status:
				t.Logf("%s: HTTP %d, as expected: %s", op.Key(), status, c.Why)
			case c.Decode && decodeErr:
				t.Logf("%s: does not decode, as expected: %s", op.Key(), c.Why)
			case decodeErr:
				t.Errorf("%s answers what its model cannot decode: %v", op.Key(), err)
			default:
				t.Errorf("%s: %v", op.Key(), err)
			}
		})
	}

	for name := range cases {
		if !gets[name] {
			t.Errorf("the sweepCase for %s names no GET operation", name)
		}
	}
}

// sweepArgs builds the call: the context, the path parameters, and an options
// struct with the required options and the case's options set.
func sweepArgs(ctx context.Context, op *definitions.Operation, fixtures sweepFixtures, c sweepCase) ([]reflect.Value, error) {
	method := reflect.ValueOf(*sdk).MethodByName(op.Name)
	if !method.IsValid() {
		return nil, errors.New("the SDK has no such method")
	}
	mt := method.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}

	for _, p := range op.PathParameters {
		value, ok := c.Path[p.Name]
		if !ok {
			value, ok = fixtures.resolvePath(op.Path, p.Name)
		}
		if !ok {
			return nil, fmt.Errorf("no value for path parameter {%s}", p.Name)
		}
		v := reflect.New(mt.In(len(args))).Elem()
		if err := setValue(v, value); err != nil {
			return nil, fmt.Errorf("{%s}: %w", p.Name, err)
		}
		args = append(args, v)
	}
	if op.Request != nil {
		return nil, errors.New("a GET with a request body")
	}

	if len(op.Options) > 0 {
		options := reflect.New(mt.In(len(args))).Elem()
		for _, o := range op.Options {
			value, ok := c.Options[o.Field]
			if !ok && o.Required {
				value, ok = lookupFold(fixtures.options, o.Name)
				if !ok {
					return nil, fmt.Errorf("no value for required option %s", o.Name)
				}
			}
			if !ok {
				continue
			}
			if err := setValue(options.FieldByName(o.Field), value); err != nil {
				return nil, fmt.Errorf("option %s: %w", o.Name, err)
			}
		}
		args = append(args, options)
	}
	if len(args) != mt.NumIn() {
		return nil, fmt.Errorf("built %d arguments for a method that takes %d", len(args), mt.NumIn())
	}

	return args, nil
}

// setValue sets a path argument or options field from a fixture: a string,
// a bool, a number, or a list.
func setValue(v reflect.Value, value any) error {
	if !v.IsValid() {
		return errors.New("no such field")
	}
	s := fmt.Sprint(value)
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		v.SetFloat(n)
	case reflect.Pointer:
		b, err := strconv.ParseBool(s)
		if err != nil || v.Type().Elem().Kind() != reflect.Bool {
			return fmt.Errorf("cannot set %s from %q", v.Type(), s)
		}
		v.Set(reflect.ValueOf(&b))
	case reflect.Slice:
		parts := strings.Split(s, ",")
		list := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, part := range parts {
			if err := setValue(list.Index(i), part); err != nil {
				return err
			}
		}
		v.Set(list)
	default:
		return fmt.Errorf("cannot set a %s", v.Type())
	}

	return nil
}

// sweepCall calls the operation, reads a streamed file, classifies a
// failure - the status of a *client.StatusError, or a decode error (the
// server answered in a documented status, but not the documented shape) -
// and checks a JSON answer strictly against its model.
func sweepCall(op *definitions.Operation, args []reflect.Value) (status int, decodeErr bool, unknown []string, err error) {
	results := reflect.ValueOf(*sdk).MethodByName(op.Name).Call(args)
	resp, ok := reflect.TypeAssert[*http.Response](results[0].FieldByName("HttpResponse"))
	if !ok {
		return 0, false, nil, errors.New("the response has no *http.Response")
	}
	if !results[1].IsNil() {
		if err, ok = reflect.TypeAssert[error](results[1]); !ok {
			return 0, false, nil, errors.New("the second result is not an error")
		}
	}

	if err == nil && resp != nil && op.Response != nil && op.Response.Type.Type == definitions.RawFile {
		defer func() { _ = resp.Body.Close() }()
		if _, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)); readErr != nil {
			return 0, false, nil, fmt.Errorf("reading the file: %w", readErr)
		}
		return 0, false, nil, nil
	}
	if err == nil {
		if model := results[0].FieldByName("Model"); model.IsValid() && resp != nil {
			unknown, err = strictFields(resp, model.Type())
		}
		return 0, false, unknown, err
	}
	if status = client.StatusCode(err); status != 0 {
		return status, false, nil, err
	}

	return 0, resp != nil, nil, err
}

// strictFields decodes a response body against a model's type and returns
// every field the body has that the model does not, as Type.field.
func strictFields(resp *http.Response, t reflect.Type) ([]string, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("the answer is not JSON: %w", err)
	}
	seen := map[string]bool{}
	undeclared(t, v, seen)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)

	return out, nil
}

var rawMessage = reflect.TypeFor[json.RawMessage]()

// undeclared walks a decoded JSON value beside the Go type it decodes into,
// and records each object key the type has no field for.
func undeclared(t reflect.Type, v any, seen map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessage || t.Kind() == reflect.Interface {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		fields := map[string]reflect.Type{}
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for key, child := range obj {
			ft, ok := fields[key]
			if !ok {
				seen[t.Name()+"."+key] = true
				continue
			}
			undeclared(ft, child, seen)
		}
	case reflect.Slice:
		list, ok := v.([]any)
		if !ok {
			return
		}
		for _, e := range list {
			undeclared(t.Elem(), e, seen)
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		for _, e := range obj {
			undeclared(t.Elem(), e, seen)
		}
	default:
	}
}
