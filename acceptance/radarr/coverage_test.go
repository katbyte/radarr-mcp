//go:build acceptance

package radarr_test

// The write coverage: every POST, PUT and DELETE the definitions hold is
// called by some test, or is listed here with why not. The recorder on the
// SDK client's transport saw every request; each is matched to the most
// specific operation whose path template it fits (PUT /api/v3/movie/editor is
// the editor, not PUT /api/v3/movie/{id} with an id of "editor"). A listed
// operation that a test does call fails as stale, so the list stays true.
// The GETs are the read sweep's to cover.
//
// And the write answers: what every successful write answered is checked
// against its documented response the way the sweep checks a GET's, so a
// write that answers more than its document says - a body where it declares
// none, a field its model lacks - fails the run, whichever test made it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// notExercised are the writes the suite does not call, and why.
var notExercised = map[string]string{
	"PostSystemRestart":             "restarts Radarr, taking the container out from under the rest of the suite",
	"PostSystemShutdown":            "stops Radarr, taking the container out from under the rest of the suite",
	"PostSystemBackupRestoreById":   "replaces the database the suite is using with a backup's, and restarts Radarr",
	"PostSystemBackupRestoreUpload": "replaces the database the suite is using with an uploaded backup's, and restarts Radarr",
	"ApplicationUpdate":             "installs another release of Radarr over the one the suite is running against",
	"MissingMoviesSearch":           "searches for every film with no file and grabs what it finds, changing the library under the rest of the suite; MoviesSearch does it for one film",
	"CutOffUnmetMoviesSearch":       "searches for an upgrade to every file and grabs what it finds, changing the library under the rest of the suite; MoviesSearch does it for one film",
	"RefreshMovie":                  "asks Radarr's metadata service and the poster host for more than the recorded answers hold; the tool suite's movie_refresh runs it for one film",
	"RefreshCollections":            "asks Radarr's metadata service for every collection, which the recorded answers do not hold",
	"RenameFiles":                   "renames a fixture's file under the rest of the suite; the tool suite's movie_rename runs it",
}

// writeCoverage returns every write operation neither called nor listed in
// notExercised, and every listed one that was called after all, and says
// how many were called.
func writeCoverage() (problems []string, summary string) {
	ops, err := operations()
	if err != nil {
		return []string{err.Error()}, ""
	}

	called := map[string]bool{}
	for _, call := range requests.calls() {
		method, path, _ := strings.Cut(call, " ")
		if o := ops.match(method, path); o != nil {
			called[o.Name] = true
		}
	}

	// every command is queued at one address, so a command's method was called when Radarr answered that it queued one of its name
	for _, a := range requests.answers() {
		var queued struct{ Name string }
		if a.method != http.MethodPost || !strings.HasSuffix(a.path, "/api/v3/command") || json.Unmarshal(a.body, &queued) != nil {
			continue
		}
		for _, o := range ops {
			if o.Command != "" && o.Command == queued.Name {
				called[o.Name] = true
			}
		}
	}

	writes := 0
	for _, o := range ops {
		if o.Method == http.MethodGet {
			continue
		}
		writes++
		reason, listed := notExercised[o.Name]
		switch {
		case called[o.Name] && listed:
			problems = append(problems, o.Name+" is called now; take it out of notExercised ("+reason+")")
		case !called[o.Name] && !listed:
			problems = append(problems, o.Name+" ("+o.Method+") is never called; test it, or say why not in notExercised")
		}
	}
	for name := range notExercised {
		if !slices.ContainsFunc(ops, func(o operation) bool { return o.Name == name }) {
			problems = append(problems, "notExercised names "+name+", which is not an operation")
		}
	}
	slices.Sort(problems)

	return problems, fmt.Sprintf("write coverage: %d of %d writes called, %d not exercised (see notExercised)", writes-len(notExercised), writes, len(notExercised))
}

// nothing is what Radarr's controllers answer when they have nothing to
// say: an empty object, null, or - from a provider test, which returns the
// string "{}" rather than an object - a JSON string holding an empty object.
var nothing = []string{`{}`, `null`, `"{}"`}

// writeShapes returns every successful write whose answer its operation does
// not document: JSON with a field its model lacks, or a body other than
// nothing from an operation documented as answering none.
func writeShapes() []string {
	ops, err := operations()
	if err != nil {
		return []string{err.Error()}
	}

	found := map[string]bool{}
	for _, a := range requests.answers() {
		o := ops.match(a.method, a.path)
		if o == nil || a.status < 200 || a.status > 299 {
			continue
		}
		body := bytes.TrimSpace(a.body)
		if o.Response == nil {
			if len(body) > 0 && !slices.Contains(nothing, string(body)) {
				found[fmt.Sprintf("%s answers what its document does not declare: %.200s", o.Key(), body)] = true
			}
			continue
		}
		if o.Response.Type.Type == definitions.RawFile || len(body) == 0 {
			continue
		}
		method, ok := reflect.TypeFor[radarr.Client]().MethodByName(o.Name)
		if !ok {
			continue
		}
		model, ok := method.Type.Out(0).FieldByName("Model")
		if !ok {
			continue
		}
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			found[fmt.Sprintf("%s answers what is not JSON: %.200s", o.Key(), body)] = true
			continue
		}
		seen := map[string]bool{}
		undeclared(model.Type, v, seen)
		for field := range seen {
			found[fmt.Sprintf("%s answers a field the document does not declare: %s", o.Key(), field)] = true
		}
	}

	problems := make([]string, 0, len(found))
	for p := range found {
		problems = append(problems, p)
	}
	slices.Sort(problems)

	return problems
}

// operation is an operation of the definitions, with the pattern its paths
// match.
type operation struct {
	*definitions.Operation
	re     *regexp.Regexp
	params int
}

type operationList []operation

func operations() (operationList, error) {
	svc, err := apiDefinitions()
	if err != nil {
		return nil, err
	}
	var ops operationList
	for _, o := range svc.Operations() {
		ops = append(ops, operation{Operation: o, re: templateRegexp(o.Path), params: strings.Count(o.Path, "{")})
	}

	return ops, nil
}

// match is the most specific operation a request fits, or nil.
func (ops operationList) match(method, path string) *operation {
	var best *operation
	for i, o := range ops {
		// a command's own method shares its address with every other's: writeCoverage tells those apart by name
		if o.Command != "" || o.Method != method || !o.re.MatchString(path) {
			continue
		}
		if best == nil || o.params < best.params {
			best = &ops[i]
		}
	}

	return best
}

// templateRegexp matches the escaped paths a path template produces:
// /api/v3/movie/{id} matches /api/v3/movie/7, a placeholder one segment.
func templateRegexp(template string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i, part := range strings.Split(template, "/") {
		if i > 0 {
			b.WriteString("/")
		}
		for part != "" {
			open := strings.IndexByte(part, '{')
			if open < 0 {
				b.WriteString(regexp.QuoteMeta(part))
				break
			}
			b.WriteString(regexp.QuoteMeta(part[:open]))
			closeIdx := strings.IndexByte(part, '}')
			b.WriteString("[^/]+")
			part = part[closeIdx+1:]
		}
	}
	b.WriteString("$")

	return regexp.MustCompile(b.String())
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
