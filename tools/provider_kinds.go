package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/go-kt/lock"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Five of the things Radarr is set up with work the same way: an indexer, a
// download client, an import list, a notification and a metadata consumer are
// each one of a number of kinds Radarr knows (Newznab, qBittorrent, a TMDb
// list, Discord, Kodi...), with a name, settings held as named fields,
// switches of its own (enableRss, priority, onGrab...) and tags that limit it
// to some films. So they are added, changed, tested and deleted by the same
// code, each through its own calls of Radarr's API.

// providerKind is one of the five, and the calls it is read and written by.
type providerKind[T any] struct {
	// family is the tools' prefix (indexer), noun what one is called and
	// plural several
	family, noun, plural string
	// does says what one is for, kinds names a few of the kinds there are,
	// and switches the switches it has, for the tools' own descriptions
	does, kinds, switches string
	// loses is what stops when one is deleted
	loses string
	// fixed is a set Radarr comes with, none of which can be added or
	// deleted: the metadata consumers
	fixed bool
	// on are the switches that make one switched on: Radarr tests one that
	// is on before it saves it, and tests only those when asked to test all
	on []string
	// starts says how a new one's switches start when none is given, and
	// begin sets them: as Radarr's own form does, which switches on what
	// its API describes switched off
	starts string
	begin  func(values map[string]any)

	list    func(ctx context.Context) ([]T, error)
	schema  func(ctx context.Context) ([]T, error)
	create  func(ctx context.Context, v T, force bool) (*T, error)
	update  func(ctx context.Context, id int, v T, force bool) (*T, error)
	remove  func(ctx context.Context, id int) error
	test    func(ctx context.Context, v T) (*http.Response, error)
	testAll func(ctx context.Context) ([]radarr.ProviderTestAllResult, error)
	// action runs one of the steps the kind's setup form runs against the
	// service it talks to, with the settings given
	action func(ctx context.Context, name string, v T) (json.RawMessage, error)
}

// providerStructural are the values every provider carries that are not
// switches of its own: what it is, what Radarr says about it, and the parts
// the tools take by other names.
var providerStructural = []string{"id", "name", "fields", "implementation", "implementationName", "configContract", "infoLink", "message", "tags", "presets", "protocol", "listType", "listOrder", "minRefreshInterval"}

// providerSwitches are the switches a provider of this type has: its single
// values, less what is structural and what only says what its kind can do.
func providerSwitches[T any]() []string {
	return slices.DeleteFunc(plainNamesOf[T](), func(n string) bool { return slices.Contains(providerStructural, n) || strings.HasPrefix(n, "supports") })
}

// providerRow is a provider as the tools show it.
type providerRow struct {
	ID          int            `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"        jsonschema:"which of the kinds Radarr knows it is: Newznab, qBittorrent, Discord..."`
	Options     map[string]any `json:"options"     jsonschema:"its switches, by the name the edit tool takes them under"`
	Settings    map[string]any `json:"settings"    jsonschema:"its settings by name, without credentials and without the ones that are unset"`
	Credentials []string       `json:"credentials" jsonschema:"the credentials that are set, by name; their values are never shown"`
	Tags        []string       `json:"tags"        jsonschema:"only films with one of these tags use it; none means every film"`
}

// fieldsOf are a provider's fields, out of the map it was read as.
func fieldsOf(values map[string]any) ([]radarr.Field, error) {
	raw, err := json.Marshal(values["fields"])
	if err != nil {
		return nil, err
	}
	var fields []radarr.Field
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}

	return fields, nil
}

// tagIDsOf are the ids of the tags a record read as a map carries.
func tagIDsOf(values map[string]any) []int {
	list, ok := values["tags"].([]any)
	if !ok {
		return nil
	}
	ids := make([]int, 0, len(list))
	for _, v := range list {
		if id, ok := v.(float64); ok {
			ids = append(ids, int(id))
		}
	}

	return ids
}

// providerRowOf projects a provider read as a map.
func providerRowOf(values map[string]any, switches []string, lk lookups) (providerRow, error) {
	fields, err := fieldsOf(values)
	if err != nil {
		return providerRow{}, err
	}
	row := providerRow{ID: idOf(values), Name: outsideName(textOf(values, "name")), Kind: textOf(values, "implementation"), Options: map[string]any{}, Tags: lk.tagLabels(tagIDsOf(values))}
	for _, name := range switches {
		if v, there := values[name]; there {
			row.Options[name] = v
		}
	}
	row.Settings, row.Credentials = shownFields(fields)

	return row, nil
}

// all reads every provider of the kind, each as a map.
func (k *providerKind[T]) all(ctx context.Context) ([]map[string]any, error) {
	list, err := k.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		m, err := asMap(list[i])
		if err != nil {
			return nil, err
		}
		out = append(out, whole[T](m))
	}
	slices.SortStableFunc(out, func(a, b map[string]any) int {
		return strings.Compare(strings.ToLower(textOf(a, "name")), strings.ToLower(textOf(b, "name")))
	})

	return out, nil
}

// find picks one provider of the kind by name (any case) or id, and otherwise
// answers an error naming the ones there are.
func (k *providerKind[T]) find(ctx context.Context, ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("name the %s", k.noun)
	}
	all, err := k.all(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for _, m := range all {
		if strings.EqualFold(textOf(m, "name"), ref) || strconv.Itoa(idOf(m)) == ref {
			return m, nil
		}
		names = append(names, textOf(m, "name"))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no %s %q: Radarr has none", k.noun, ref)
	}

	return nil, fmt.Errorf("no %s %q (have: %s)", k.noun, ref, strings.Join(names, ", "))
}

// blank is a provider of a kind as Radarr describes it before anything is
// filled in, and otherwise an error naming the kinds there are.
func (k *providerKind[T]) blank(ctx context.Context, kind string) (map[string]any, error) {
	schemas, err := k.schema(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	kinds := make([]string, 0, len(schemas))
	for i := range schemas {
		described, mapErr := asMap(schemas[i])
		if mapErr != nil {
			return nil, mapErr
		}
		if strings.EqualFold(textOf(described, "implementation"), strings.TrimSpace(kind)) || strings.EqualFold(textOf(described, "implementationName"), strings.TrimSpace(kind)) {
			m = described
		}
		kinds = append(kinds, textOf(described, "implementation"))
	}
	if m == nil {
		slices.Sort(kinds)
		return nil, fmt.Errorf("no %s kind %q (have: %s)", k.noun, kind, strings.Join(kinds, ", "))
	}
	m["name"] = textOf(m, "implementationName")
	delete(m, "presets")
	delete(m, "id")

	return m, nil
}

// optionsIn names a setting whose choices come from the service a provider
// talks to, and what to ask that service with.
type optionsIn struct {
	Provider string         `json:"provider"           jsonschema:"indexer, downloadclient, importlist, notification or metadata"`
	Name     string         `json:"name,omitempty"     jsonschema:"one Radarr already has, by name or id: the service is asked with the settings and credentials it holds"`
	Kind     string         `json:"kind,omitempty"     jsonschema:"a kind not added yet, as provider_kinds names them: the service is asked with the settings given here"`
	Setting  string         `json:"setting"            jsonschema:"the setting to list the choices of: one provider_kinds shows with options_from"`
	Settings map[string]any `json:"settings,omitempty" jsonschema:"what the service has to be asked with, by name - its address and API key - or, for one Radarr has, values to ask with in place of its own"`
}

type optionRow struct {
	Value any    `json:"value"          jsonschema:"what to give the setting"`
	Name  string `json:"name"`
	Hint  string `json:"hint,omitempty"`
}

type optionsOut struct {
	Setting string      `json:"setting"`
	Options []optionRow `json:"options" jsonschema:"what the setting can be set to, as the service answered"`
}

// optionsReader answers provider_options for one family of provider.
type optionsReader = func(ctx context.Context, in optionsIn) (optionsOut, error)

// options asks the service a provider talks to what one of its settings can
// be set to: the step its setup form runs to fill a list of choices.
func (k *providerKind[T]) options(ctx context.Context, in optionsIn) (optionsOut, error) {
	var m map[string]any
	var err error
	switch {
	case strings.TrimSpace(in.Name) != "":
		m, err = k.find(ctx, in.Name)
	case strings.TrimSpace(in.Kind) != "":
		m, err = k.blank(ctx, in.Kind)
	default:
		err = fmt.Errorf("name the %s to ask for, or the kind of one not added yet", k.noun)
	}
	if err != nil {
		return optionsOut{}, err
	}
	fields, err := fieldsOf(m)
	if err != nil {
		return optionsOut{}, err
	}
	if setErr := setFields(fields, in.Settings); setErr != nil {
		return optionsOut{}, setErr
	}

	f := field(fields, in.Setting)
	if f == nil || f.SelectOptionsProviderAction == "" {
		var asked []string
		for _, x := range fields {
			if x.SelectOptionsProviderAction != "" {
				asked = append(asked, x.Name)
			}
		}
		switch {
		case f != nil && len(f.SelectOptions) > 0:
			return optionsOut{}, fmt.Errorf("the choices of %s are fixed: provider_kinds lists them under options", f.Name)
		case len(asked) == 0:
			return optionsOut{}, fmt.Errorf("no setting of %s takes its choices from the service: each is given as it is", textOf(m, "implementation"))
		}

		return optionsOut{}, fmt.Errorf("%q is not a setting whose choices come from the service (those are: %s)", in.Setting, strings.Join(asked, ", "))
	}
	m["fields"] = fields
	v, err := fromMap[T](m, "the "+k.noun)
	if err != nil {
		return optionsOut{}, err
	}
	raw, err := k.action(ctx, f.SelectOptionsProviderAction, v)
	if err != nil {
		return optionsOut{}, fmt.Errorf("asking for the choices of %s: %w", f.Name, err)
	}
	var body struct {
		Options []optionRow `json:"options"`
	}
	if readErr := json.Unmarshal(raw, &body); readErr != nil {
		return optionsOut{}, fmt.Errorf("reading the choices of %s: %w", f.Name, readErr)
	}
	out := optionsOut{Setting: f.Name, Options: make([]optionRow, 0, len(body.Options))}
	for _, o := range body.Options {
		out.Options = append(out.Options, optionRow{Value: o.Value, Name: outsideName(o.Name), Hint: outsideName(o.Hint)})
	}

	return out, nil
}

// setSwitches writes switches into a provider read as a map, by the names
// Radarr's API gives them. A name the provider has no switch for is an error
// that lists the ones it has.
func setSwitches(values map[string]any, switches []string, asked map[string]any, noun string) error {
	for _, name := range sortedKeys(asked) {
		i := slices.IndexFunc(switches, func(n string) bool { return strings.EqualFold(n, strings.TrimSpace(name)) })
		if i < 0 {
			return fmt.Errorf("a %s has no option %q (it has: %s)", noun, name, strings.Join(switches, ", "))
		}
		values[switches[i]] = asked[name]
	}

	return nil
}

// Radarr tests a provider that is switched on before it saves it, and one
// that is switched off not at all. Its own force skips that test when
// changing one, and when adding one only lets a test that warns through: one
// that fails is refused either way. So a forced add is made switched off,
// which is not tested, and then switched on with a forced change, as
// prowlarr-mcp's is, and force means the same for both: save it untested.
const (
	addHint  = "put right what it says. If it is Radarr's test of it that failed, which it runs before saving one that is switched on, force=true saves it untested and off=true adds it switched off"
	editHint = "put right what it says. If it is Radarr's test of it that failed, which it runs before saving a change to one that is switched on, force=true saves the change untested"
)

// saveError explains a provider Radarr would not save. It refuses one whose
// settings do not hold up and one whose test fails in the same way, with
// what is wrong, so the way round a failed test is offered for what it is.
func saveError(hint string, err error) error {
	if status := client.StatusCode(err); status == http.StatusBadRequest || status >= http.StatusInternalServerError {
		return fmt.Errorf("%w (%s)", err, hint)
	}

	return err
}

// editTags is the tags an edit leaves a record with: the ones given in place
// of what it had, or what it had with some added and some taken away.
func editTags(have, set, add, remove []int, replace bool) []int {
	out := slices.Clone(have)
	if replace {
		out = slices.Clone(set)
	}
	for _, id := range add {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	out = slices.DeleteFunc(out, func(id int) bool { return slices.Contains(remove, id) })
	if out == nil {
		out = []int{}
	}

	return out
}

type providerAddIn struct {
	Kind     string         `json:"kind"               jsonschema:"which kind to add, as provider_kinds names them"`
	Name     string         `json:"name,omitempty"     jsonschema:"what to call it; default the kind's own name"`
	Settings map[string]any `json:"settings,omitempty" jsonschema:"its settings by name, as provider_kinds lists them for the kind: baseUrl, apiKey, host, port, categories and the like. A name the kind does not have is refused with the ones it does"`
	Options  map[string]any `json:"options,omitempty"  jsonschema:"its switches by name, as the list tool shows them under options. A name it does not have is refused with the ones it does"`
	Tags     []string       `json:"tags,omitempty"     jsonschema:"only films with one of these tags use it; a tag that does not exist is created"`
	Off      bool           `json:"off,omitempty"      jsonschema:"add it switched off, whatever its switches would start as; its edit tool switches it on"`
	Force    bool           `json:"force,omitempty"    jsonschema:"save it without Radarr's test of it, for one that cannot be reached yet or whose test fails"`
}

type providerEditIn struct {
	Name       string         `json:"name"                  jsonschema:"the one to change, by name or id"`
	NewName    string         `json:"new_name,omitempty"`
	Settings   map[string]any `json:"settings,omitempty"    jsonschema:"settings to change, by the name the list tool shows them under; a credential is given the same way and never shown again"`
	Options    map[string]any `json:"options,omitempty"     jsonschema:"switches to change, by the name the list tool shows them under options"`
	Tags       []string       `json:"tags,omitempty"        jsonschema:"replace its tags with these; a tag that does not exist is created"`
	AddTags    []string       `json:"add_tags,omitempty"    jsonschema:"tags to add; a tag that does not exist is created"`
	RemoveTags []string       `json:"remove_tags,omitempty"`
	Force      bool           `json:"force,omitempty"       jsonschema:"save the change without Radarr testing it"`
}

type providerDeleteIn struct {
	Name string `json:"name" jsonschema:"the one to delete, by name or id"`
	confirmIn
}

type providerListOut struct {
	Items []providerRow `json:"items"`
	pageOut
}

type providerTestIn struct {
	Name string `json:"name,omitempty" jsonschema:"test only this one, by name or id, switched on or not; default tests every one that is switched on"`
}

type providerTestOut struct {
	Results []testResult `json:"results"`
}

// kindDoc is what provider_kinds answers for one of the five: the kinds
// there are, or with one named what it takes.
type kindDoc struct {
	Kinds    []string   `json:"kinds,omitempty"    jsonschema:"the kinds there are, when none was named"`
	Kind     string     `json:"kind,omitempty"`
	About    string     `json:"about,omitempty"    jsonschema:"what Radarr says of the kind"`
	Settings []fieldDoc `json:"settings,omitempty" jsonschema:"the settings the kind takes, with what each holds when left alone"`
	Options  []string   `json:"options,omitempty"  jsonschema:"the switches one of these has, by name"`
}

// describe is what provider_kinds answers for this kind of provider.
func (k *providerKind[T]) describe(ctx context.Context, kind string) (kindDoc, error) {
	schemas, err := k.schema(ctx)
	if err != nil {
		return kindDoc{}, err
	}
	var kinds []string
	for i := range schemas {
		m, err := asMap(schemas[i])
		if err != nil {
			return kindDoc{}, err
		}
		kinds = append(kinds, textOf(m, "implementation"))
		if kind == "" || (!strings.EqualFold(textOf(m, "implementation"), strings.TrimSpace(kind)) && !strings.EqualFold(textOf(m, "implementationName"), strings.TrimSpace(kind))) {
			continue
		}
		fields, err := fieldsOf(m)
		if err != nil {
			return kindDoc{}, err
		}

		return kindDoc{Kind: textOf(m, "implementation"), About: textOf(m, "implementationName"), Settings: fieldDocs(fields), Options: providerSwitches[T]()}, nil
	}
	slices.Sort(kinds)
	if kind != "" {
		return kindDoc{}, fmt.Errorf("no %s kind %q (have: %s)", k.noun, kind, strings.Join(kinds, ", "))
	}

	return kindDoc{Kinds: kinds, Options: providerSwitches[T]()}, nil
}

// isOn reports whether a provider read as a map is switched on.
func (k *providerKind[T]) isOn(values map[string]any) bool {
	return slices.ContainsFunc(k.on, func(name string) bool { return isSet(values[name]) })
}

// testOne tests one provider read as a map, the way its Test button does. A
// test that fails is the answer, not an error: Radarr refuses it with what is
// wrong, a 400 from most kinds and a 500 from one that could not be reached.
func (k *providerKind[T]) testOne(ctx context.Context, m map[string]any) (radarr.ProviderTestAllResult, error) {
	result := radarr.ProviderTestAllResult{Id: idOf(m), IsValid: true}
	v, err := fromMap[T](m, "the "+k.noun)
	if err != nil {
		return result, err
	}
	resp, testErr := k.test(ctx, v)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if testErr != nil {
		if status := client.StatusCode(testErr); status != http.StatusBadRequest && status < http.StatusInternalServerError {
			return result, testErr
		}
		result.IsValid, result.ValidationFailures = false, failuresOf(resp, testErr)
	}

	return result, nil
}

// testEvery tests every provider that is switched on. Radarr does it in one
// call, which fails whole when one of them cannot be reached at all: they are
// then tested one at a time, so the others still get their answer.
func (k *providerKind[T]) testEvery(ctx context.Context, all []map[string]any) ([]radarr.ProviderTestAllResult, error) {
	results, err := k.testAll(ctx)
	if err == nil || client.StatusCode(err) < http.StatusInternalServerError {
		return results, err
	}
	results = results[:0]
	for _, m := range all {
		if !k.isOn(m) {
			continue
		}
		one, oneErr := k.testOne(ctx, m)
		if oneErr != nil {
			return nil, oneErr
		}
		results = append(results, one)
	}

	return results, nil
}

// registerListAndTest queues the list and test tools of a kind that has none
// written for it by hand.
func registerListAndTest[T any](r *registry, k *providerKind[T]) {
	c := r.client
	switches := providerSwitches[T]()

	add(r, readTool, &mcp.Tool{
		Name:        k.family + "_list",
		Description: fmt.Sprintf("List the %s Radarr is set up with: %s Each has its kind (%s), its switches under options, its settings without credentials, and the tags that limit it to some films.", k.plural, k.does, k.kinds),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, providerListOut, error) {
		all, err := k.all(ctx)
		if err != nil {
			return nil, providerListOut{}, err
		}
		lk, err := loadLookups(ctx, c)
		if err != nil {
			return nil, providerListOut{}, err
		}
		page, paged := pageRows(all, in)
		out := providerListOut{Items: make([]providerRow, 0, len(page)), pageOut: paged}
		for _, m := range page {
			row, err := providerRowOf(m, switches, lk)
			if err != nil {
				return nil, providerListOut{}, err
			}
			out.Items = append(out.Items, row)
		}

		return nil, out, nil
	})

	add(r, readTool, &mcp.Tool{
		Name:        k.family + "_test",
		Description: fmt.Sprintf("Test the %s the way Radarr's own Test button does - one by name, or every one that is switched on - and say what failed for each one that did.", k.plural),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in providerTestIn) (*mcp.CallToolResult, providerTestOut, error) {
		all, err := k.all(ctx)
		if err != nil {
			return nil, providerTestOut{}, err
		}
		names := map[int]string{}
		for _, m := range all {
			names[idOf(m)] = textOf(m, "name")
		}
		if in.Name == "" {
			results, allErr := k.testEvery(ctx, all)
			if allErr != nil {
				return nil, providerTestOut{}, allErr
			}

			return nil, providerTestOut{Results: toResults(results, names)}, nil
		}

		m, err := k.find(ctx, in.Name)
		if err != nil {
			return nil, providerTestOut{}, err
		}
		result, err := k.testOne(ctx, m)
		if err != nil {
			return nil, providerTestOut{}, err
		}

		return nil, providerTestOut{Results: toResults([]radarr.ProviderTestAllResult{result}, names)}, nil
	})
}

// registerProviderWrites queues the tools that add, change and delete a kind
// of provider, and returns what provider_kinds answers for it.
func registerProviderWrites[T any](r *registry, k *providerKind[T]) kindDescriber {
	c := r.client
	switches := providerSwitches[T]()

	rowOfMade := func(ctx context.Context, made *T) (providerRow, error) {
		m, err := asMap(made)
		if err != nil {
			return providerRow{}, err
		}
		lk, err := loadLookups(ctx, c)
		if err != nil {
			return providerRow{}, err
		}

		return providerRowOf(whole[T](m), switches, lk)
	}

	if !k.fixed {
		add(r, writeTool, &mcp.Tool{
			Name:        k.family + "_add",
			Description: fmt.Sprintf("Add %s: %s Name its kind (%s; provider_kinds lists them and what each takes), its settings by name, and its switches (%s). Left alone, it starts %s, as in Radarr's own form. Radarr tests one that is switched on before saving it and refuses one that fails, unless force is set; off adds it switched off.", article(k.noun), k.does, k.kinds, k.switches, k.starts),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in providerAddIn) (*mcp.CallToolResult, providerRow, error) {
			m, err := k.blank(ctx, in.Kind)
			if err != nil {
				return nil, providerRow{}, err
			}
			if name := strings.TrimSpace(in.Name); name != "" {
				m["name"] = name
			}
			if k.begin != nil {
				k.begin(m)
			}
			if err := setSwitches(m, switches, in.Options, k.noun); err != nil {
				return nil, providerRow{}, err
			}
			if in.Off {
				for _, name := range k.on {
					m[name] = false
				}
			}
			// a forced add is made switched off, which Radarr does not test, and what was to be on is switched on once it is saved
			wanted := map[string]any{}
			if in.Force && k.isOn(m) {
				for _, name := range k.on {
					wanted[name], m[name] = m[name], false
				}
			}
			fields, err := fieldsOf(m)
			if err != nil {
				return nil, providerRow{}, err
			}
			if err := setFields(fields, in.Settings); err != nil {
				return nil, providerRow{}, fmt.Errorf("%s: %w", textOf(m, "name"), err)
			}
			m["fields"] = fields
			tags, err := resolveTags(ctx, c, in.Tags, true)
			if err != nil {
				return nil, providerRow{}, err
			}
			m["tags"] = tags

			v, err := fromMap[T](m, "the "+k.noun)
			if err != nil {
				return nil, providerRow{}, err
			}
			made, err := k.create(ctx, v, in.Force)
			if err != nil {
				return nil, providerRow{}, saveError(addHint, err)
			}
			if len(wanted) > 0 {
				// it is sent again as it was given rather than as Radarr answered it, which is with its credentials blanked
				created, mapErr := asMap(made)
				if mapErr != nil {
					return nil, providerRow{}, mapErr
				}
				m["id"] = idOf(created)
				maps.Copy(m, wanted)
				on, mapErr := fromMap[T](m, "the "+k.noun)
				if mapErr != nil {
					return nil, providerRow{}, mapErr
				}
				if made, err = k.update(ctx, idOf(created), on, true); err != nil {
					return nil, providerRow{}, fmt.Errorf("%s was added switched off, and switching it on failed: %w", textOf(m, "name"), err)
				}
			}
			row, err := rowOfMade(ctx, made)

			return nil, row, err
		})
	}

	add(r, writeTool, &mcp.Tool{
		Name:        k.family + "_edit",
		Description: fmt.Sprintf("Change %s: its name, its settings by name, its switches (%s), or its tags. Radarr tests one that is switched on before saving the change, unless force is set.", article(k.noun), k.switches),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in providerEditIn) (*mcp.CallToolResult, providerRow, error) {
		found, err := k.find(ctx, in.Name)
		if err != nil {
			return nil, providerRow{}, err
		}
		if in.NewName == "" && len(in.Settings)+len(in.Options) == 0 && in.Tags == nil && len(in.AddTags)+len(in.RemoveTags) == 0 {
			return nil, providerRow{}, errors.New("nothing to change: give new_name, settings, options, tags, add_tags or remove_tags")
		}

		// it is sent back whole, so it is read again once no other edit of it can run
		unlock := r.locking().By(lock.ID[T](strconv.Itoa(idOf(found))))
		defer unlock()
		m, err := k.find(ctx, strconv.Itoa(idOf(found)))
		if err != nil {
			return nil, providerRow{}, err
		}

		if name := strings.TrimSpace(in.NewName); name != "" {
			m["name"] = name
		}
		if err := setSwitches(m, switches, in.Options, k.noun); err != nil {
			return nil, providerRow{}, err
		}
		fields, err := fieldsOf(m)
		if err != nil {
			return nil, providerRow{}, err
		}
		if err := setFields(fields, in.Settings); err != nil {
			return nil, providerRow{}, fmt.Errorf("%s: %w", textOf(m, "name"), err)
		}
		m["fields"] = fields
		if in.Tags != nil || len(in.AddTags)+len(in.RemoveTags) > 0 {
			set, tagErr := resolveTags(ctx, c, in.Tags, true)
			if tagErr != nil {
				return nil, providerRow{}, tagErr
			}
			adds, tagErr := resolveTags(ctx, c, in.AddTags, true)
			if tagErr != nil {
				return nil, providerRow{}, tagErr
			}
			removes, tagErr := resolveTags(ctx, c, in.RemoveTags, false)
			if tagErr != nil {
				return nil, providerRow{}, tagErr
			}
			m["tags"] = editTags(tagIDsOf(m), set, adds, removes, in.Tags != nil)
		}

		v, err := fromMap[T](m, "the "+k.noun)
		if err != nil {
			return nil, providerRow{}, err
		}
		saved, err := k.update(ctx, idOf(m), v, in.Force)
		if err != nil {
			return nil, providerRow{}, saveError(editHint, err)
		}
		row, err := rowOfMade(ctx, saved)

		return nil, row, err
	})

	if !k.fixed {
		add(r, deleteTool, &mcp.Tool{
			Name:        k.family + "_delete",
			Description: fmt.Sprintf("Delete %s from Radarr: %s. Without confirm nothing changes and the answer says what would go.", article(k.noun), k.loses),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in providerDeleteIn) (*mcp.CallToolResult, deletedOut, error) {
			m, err := k.find(ctx, in.Name)
			if err != nil {
				return nil, deletedOut{}, err
			}
			loses := []string{"its settings and credentials, as Radarr holds them", k.loses}
			out, err := deleting(in.Confirm, textOf(m, "name"), loses, func() error { return k.remove(ctx, idOf(m)) })

			return nil, out, err
		})
	}

	return k.describe
}

// article is a noun with the article it takes: an indexer, a notification.
func article(noun string) string {
	if strings.ContainsRune("aeiou", rune(noun[0])) {
		return "an " + noun
	}

	return "a " + noun
}
