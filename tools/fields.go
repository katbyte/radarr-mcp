package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// Everything Radarr is set up with that comes in kinds - an indexer, a
// download client, an import list, a notification, a metadata consumer, a
// condition of a custom format or of an auto tagging rule - keeps its
// settings as a list of named fields (baseUrl, apiKey, categories...), each
// marked with how private its value is. The tools show and take them as a map
// by name.

// field finds a field by name, ignoring case.
func field(fields []radarr.Field, name string) *radarr.Field {
	for i := range fields {
		if strings.EqualFold(fields[i].Name, strings.TrimSpace(name)) {
			return &fields[i]
		}
	}

	return nil
}

// secretNames are the settings that hold a credential and are not named for
// what they are: a webhook's address is itself the key.
var secretNames = []string{"pass", "signinurl", "webhookurl"}

// secretName reports whether a setting's name says it holds a credential.
// Radarr does not mark every one that does: its list that reads another
// Radarr keeps that Radarr's API key in a field it marks as nothing in
// particular.
func secretName(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "password") || slices.Contains(secretNames, lower) {
		return true
	}

	return slices.ContainsFunc([]string{"key", "token", "secret", "passphrase", "cookie"}, func(end string) bool { return strings.HasSuffix(lower, end) })
}

// secret reports whether a field holds a credential: an API key, a password,
// a token, a webhook address that is itself the key.
func secret(f *radarr.Field) bool {
	// a user name is marked private too, for Radarr's logs: it is who a thing signs in as, not what it signs in with, and Radarr shows it
	if f.Privacy == radarr.PrivacyLevelPassword || f.Privacy == radarr.PrivacyLevelApiKey {
		return true
	}

	return strings.EqualFold(f.Type, "password") || secretName(f.Name)
}

// shownFields are fields as a map by name, without the credentials and
// without the empty ones. A credential that is set is listed by name in
// credentials, so a caller can see that a key is there without seeing it.
// The notes a kind writes to its user (info fields) are left out.
func shownFields(fields []radarr.Field) (shown map[string]any, credentials []string) {
	shown = map[string]any{}
	for i := range fields {
		f := &fields[i]
		if strings.EqualFold(f.Type, "info") || unset(f.Value) {
			continue
		}
		if secret(f) {
			credentials = append(credentials, f.Name)
			continue
		}
		shown[f.Name] = outsideValue(f.Value)
	}
	slices.Sort(credentials)

	return shown, credentials
}

// outsideValue is a setting's value made safe to show: text is whatever
// someone typed or a site answered, so it is cleaned as any other is.
func outsideValue(v any) any {
	// an address among a thing's settings is shown as any other is: a webhook's or a tracker's can carry a key after its "?"
	if s, ok := v.(string); ok {
		return radarrSays(s)
	}

	return v
}

// unset reports whether a field value is not set: null, "", or an empty list.
func unset(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}

	return false
}

// setFields writes values into fields by name, converting each to what the
// field holds. A name there is no field for is an error that lists the ones
// there are, so a typo cannot silently save nothing.
func setFields(fields []radarr.Field, values map[string]any) error {
	for _, name := range sortedKeys(values) {
		f := field(fields, name)
		if f == nil {
			have := make([]string, 0, len(fields))
			for _, x := range fields {
				if !strings.EqualFold(x.Type, "info") {
					have = append(have, x.Name)
				}
			}
			return fmt.Errorf("no setting %q (have: %s)", name, strings.Join(have, ", "))
		}
		v, err := fieldValue(f, values[name])
		if err != nil {
			return fmt.Errorf("setting %s: %w", f.Name, err)
		}
		f.Value = v
	}

	return nil
}

// fieldValue converts a value a caller gave into what a field holds: a number
// for a number field (from "2.5" as well as 2.5), true or false for a
// checkbox, the option's value for a select given its name, and the value as
// it is otherwise.
func fieldValue(f *radarr.Field, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch strings.ToLower(f.Type) {
	case "number":
		if s, ok := v.(string); ok {
			if strings.TrimSpace(s) == "" {
				return nil, nil
			}
			n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", s)
			}
			return n, nil
		}
	case "checkbox":
		if s, ok := v.(string); ok {
			b, err := strconv.ParseBool(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("%q is not true or false", s)
			}
			return b, nil
		}
	case "select":
		if s, ok := v.(string); ok && len(f.SelectOptions) > 0 {
			names := make([]string, 0, len(f.SelectOptions))
			for _, o := range f.SelectOptions {
				if strings.EqualFold(o.Name, strings.TrimSpace(s)) || strconv.Itoa(o.Value) == strings.TrimSpace(s) {
					return o.Value, nil
				}
				names = append(names, o.Name)
			}
			return nil, fmt.Errorf("%q is not one of: %s", s, strings.Join(names, ", "))
		}
	}

	return v, nil
}

// fieldDoc is what a field is, for a caller choosing a value for it.
type fieldDoc struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Type        string   `json:"type,omitempty"         jsonschema:"textbox, number, checkbox, select, password, path, tag..."`
	Help        string   `json:"help,omitempty"`
	Default     any      `json:"default,omitempty"      jsonschema:"the value it has when left alone; a credential's is not shown"`
	Options     []string `json:"options,omitempty"      jsonschema:"what a select takes, by name"`
	OptionsFrom string   `json:"options_from,omitempty" jsonschema:"its choices come from the service the kind talks to - an indexer's categories, another Radarr's profiles - and provider_options lists them"`
	Advanced    bool     `json:"advanced,omitempty"`
	Secret      bool     `json:"secret,omitempty"       jsonschema:"a credential: taken, and never shown again"`
}

// fieldDocs describes fields for a caller that has to fill them in.
func fieldDocs(fields []radarr.Field) []fieldDoc {
	out := make([]fieldDoc, 0, len(fields))
	for i := range fields {
		f := &fields[i]
		if strings.EqualFold(f.Type, "info") {
			continue
		}
		doc := fieldDoc{Name: f.Name, Label: f.Label, Type: f.Type, Help: outsideProse(f.HelpText), OptionsFrom: f.SelectOptionsProviderAction, Advanced: isTrue(f.Advanced), Secret: secret(f)}
		if !doc.Secret && !unset(f.Value) {
			doc.Default = f.Value
		}
		for _, o := range f.SelectOptions {
			doc.Options = append(doc.Options, o.Name)
		}
		out = append(out, doc)
	}

	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}

// sameJSON reports whether two values are the same once written as JSON,
// which is how 30 and 30.0 are one number.
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)

	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// Records are read and written back whole, and what a caller names in one is
// named the way Radarr's API names it (enableRss, preferredProtocol,
// backupRetention). So a record is handled as a map of those names, and put
// back through the client's own type before it is sent, which refuses a name
// the record does not have and a value of the wrong kind.

// asMap is a record as a map of the names Radarr's API gives its values.
func asMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// fromMap is a record made of a map, refusing a name the record does not
// have and a value of the wrong kind. what names the record in the error.
func fromMap[T any](values map[string]any, what string) (T, error) {
	var v T
	raw, err := json.Marshal(values)
	if err != nil {
		return v, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("%s: %w (it has: %s)", what, err, strings.Join(namesOf[T](), ", "))
	}

	return v, nil
}

// namesOf are the names Radarr's API gives a record's values, sorted.
func namesOf[T any]() []string {
	var names []string
	for f := range reflect.TypeFor[T]().Fields() {
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}
	slices.Sort(names)

	return names
}

// plainNamesOf are the names of a record's values that are a single switch,
// number or word, which is what a caller sets one at a time: its lists and
// the records inside it are left out.
func plainNamesOf[T any]() []string {
	var names []string
	for f := range reflect.TypeFor[T]().Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		t := f.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Bool, reflect.String, reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
			names = append(names, tag)
		default:
		}
	}
	slices.Sort(names)

	return names
}

// zerosOf are a record's single values at rest - a switch that is off, a
// number at 0, a word not given - by the names Radarr's API gives them.
func zerosOf[T any]() map[string]any {
	out := map[string]any{}
	for f := range reflect.TypeFor[T]().Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		t := f.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Bool:
			out[tag] = false
		case reflect.String:
			out[tag] = ""
		case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
			out[tag] = 0
		default:
		}
	}

	return out
}

// whole is a record read as a map with every single value it has. The client
// leaves a value at rest out of what it reads, and a switch that is off is
// still a switch: the tools show it, and compare against it.
func whole[T any](values map[string]any) map[string]any {
	for name, zero := range zerosOf[T]() {
		if _, there := values[name]; !there {
			values[name] = zero
		}
	}

	return values
}

// isSet reports whether a value of a record read as a map is the switch true.
func isSet(v any) bool {
	b, ok := v.(bool)

	return ok && b
}

// idOf is the id a record read as a map carries, or 0.
func idOf(values map[string]any) int {
	id, ok := values["id"].(float64)
	if !ok {
		return 0
	}

	return int(id)
}

// textOf is a value of a record read as a map, as text; "" when it is not
// there.
func textOf(values map[string]any, name string) string {
	switch v := values[name].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
