package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/go-kt/lock"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Several of the things Radarr is set up with are plain records: a delay
// profile, a release profile, an auto tagging rule, a remote path mapping, a
// filter saved in its web interface, a custom format. Each is a handful of
// values by name, some with tags and two with a list of conditions, and each
// is listed, added, changed and deleted the same way. So they share the code
// here, and take and show their values by the names Radarr's API gives them.

// recordKind is one kind of record, and the calls it is read and written by.
type recordKind[T any] struct {
	// family is the tools' prefix (delayprofile), noun what one is called
	// and plural several
	family, noun, plural string
	// does says what one is for, and takes names its values, for the tools'
	// own descriptions
	does, takes string
	// nameKey is the value one is known by (name, label); "" when it has
	// none and is known by its id alone
	nameKey string
	// loses is what stops when one is deleted
	loses string
	// keep says why one cannot be deleted, "" when it can
	keep func(values map[string]any) string
	// listed is set when the kind's list tool is written by hand
	listed bool
	// given names what an edit of one can be given, which depends on
	// whether the kind has tags and conditions
	given string
	// conditions reads the kinds of condition a record of this kind is made
	// of, each with the settings it takes; nil for a kind that has none
	conditions func(ctx context.Context) ([]map[string]any, error)

	list   func(ctx context.Context) ([]T, error)
	create func(ctx context.Context, v T) (*T, error)
	update func(ctx context.Context, id int, v T) (*T, error)
	remove func(ctx context.Context, id int) error
}

// all reads every record of the kind, each as a map.
func (k *recordKind[T]) all(ctx context.Context) ([]map[string]any, error) {
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

	return out, nil
}

// label is what a record is called in an answer: its name when it has one,
// and its id.
func (k *recordKind[T]) label(values map[string]any) string {
	if name := textOf(values, k.nameKey); k.nameKey != "" && name != "" {
		return name
	}

	return k.noun + " " + strconv.Itoa(idOf(values))
}

// find picks one record of the kind by id, or by name when the kind has one,
// and otherwise answers an error naming the ones there are.
func (k *recordKind[T]) find(ctx context.Context, ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("name the %s", k.noun)
	}
	all, err := k.all(ctx)
	if err != nil {
		return nil, err
	}
	have := make([]string, 0, len(all))
	for _, m := range all {
		if strconv.Itoa(idOf(m)) == ref || (k.nameKey != "" && strings.EqualFold(textOf(m, k.nameKey), ref)) {
			return m, nil
		}
		have = append(have, fmt.Sprintf("%d (%s)", idOf(m), k.label(m)))
	}
	if len(have) == 0 {
		return nil, fmt.Errorf("no %s %q: Radarr has none", k.noun, ref)
	}

	return nil, fmt.Errorf("no %s %q (have: %s)", k.noun, ref, strings.Join(have, ", "))
}

// shownRecord is a record as the tools show it: its values by name, its tags
// by label where it has any, and each condition as its kind and settings in
// place of everything Radarr says about the kind.
func shownRecord(values map[string]any, lk lookups) map[string]any {
	out := map[string]any{}
	for name, v := range values {
		switch name {
		case "tags":
			out[name] = lk.tagLabels(tagIDsOf(values))
		case "specifications":
			out["conditions"] = shownConditions(v)
		default:
			out[name] = outsideValue(v)
		}
	}

	return out
}

// conditionRow is one condition of a custom format or an auto tagging rule.
type conditionRow struct {
	Kind     string         `json:"kind"               jsonschema:"what it matches on, as provider_kinds names the kinds"`
	Name     string         `json:"name,omitempty"`
	Negate   bool           `json:"negate,omitempty"   jsonschema:"it holds when the thing does NOT match"`
	Required bool           `json:"required,omitempty" jsonschema:"it must hold whatever the others do; of the ones that are not required, one of each kind is enough"`
	Settings map[string]any `json:"settings,omitempty" jsonschema:"what it matches: value for most kinds, and min and max for a range"`
}

// shownConditions are a record's conditions as rows.
func shownConditions(v any) []conditionRow {
	list, ok := v.([]any)
	if !ok {
		return []conditionRow{}
	}
	out := make([]conditionRow, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		row := conditionRow{Kind: textOf(m, "implementation"), Name: outsideName(textOf(m, "name")), Negate: isSet(m["negate"]), Required: isSet(m["required"])}
		if fields, err := fieldsOf(m); err == nil {
			row.Settings, _ = shownFields(fields)
		}
		out = append(out, row)
	}

	return out
}

// buildConditions makes the conditions a record is saved with, each from the
// kind of that name as Radarr describes it, with its settings filled in.
func buildConditions(kinds []map[string]any, asked []conditionRow) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(asked))
	for _, a := range asked {
		var kind map[string]any
		names := make([]string, 0, len(kinds))
		for _, s := range kinds {
			if strings.EqualFold(textOf(s, "implementation"), strings.TrimSpace(a.Kind)) || strings.EqualFold(textOf(s, "implementationName"), strings.TrimSpace(a.Kind)) {
				kind = s
			}
			names = append(names, textOf(s, "implementation"))
		}
		if kind == nil {
			slices.Sort(names)
			return nil, fmt.Errorf("no kind of condition %q (have: %s)", a.Kind, strings.Join(names, ", "))
		}
		fields, err := fieldsOf(kind)
		if err != nil {
			return nil, err
		}
		if err := setFields(fields, a.Settings); err != nil {
			return nil, fmt.Errorf("condition %s: %w", a.Kind, err)
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = textOf(kind, "implementationName")
		}
		out = append(out, map[string]any{"name": name, "implementation": textOf(kind, "implementation"), "negate": a.Negate, "required": a.Required, "fields": fields})
	}

	return out, nil
}

// describeConditions is what provider_kinds answers for the conditions of a
// custom format or an auto tagging rule.
func describeConditions(kinds []map[string]any, noun, kind string) (kindDoc, error) {
	names := make([]string, 0, len(kinds))
	for _, s := range kinds {
		names = append(names, textOf(s, "implementation"))
		if kind == "" || (!strings.EqualFold(textOf(s, "implementation"), strings.TrimSpace(kind)) && !strings.EqualFold(textOf(s, "implementationName"), strings.TrimSpace(kind))) {
			continue
		}
		fields, err := fieldsOf(s)
		if err != nil {
			return kindDoc{}, err
		}

		return kindDoc{Kind: textOf(s, "implementation"), About: textOf(s, "implementationName"), Settings: fieldDocs(fields)}, nil
	}
	slices.Sort(names)
	if kind != "" {
		return kindDoc{}, fmt.Errorf("no kind of %s condition %q (have: %s)", noun, kind, strings.Join(names, ", "))
	}

	return kindDoc{Kinds: names}, nil
}

// setValues writes values into a record read as a map, by the names Radarr's
// API gives them. A name the record does not have is an error that lists the
// ones it does; what the tools take by another argument is refused with which.
func setValues[T any](values, asked map[string]any, noun string) error {
	names := namesOf[T]()
	for _, name := range sortedKeys(asked) {
		i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, strings.TrimSpace(name)) })
		switch {
		case i < 0:
			return fmt.Errorf("a %s has no value %q (it has: %s)", noun, name, strings.Join(names, ", "))
		case names[i] == "id":
			return fmt.Errorf("a %s's id is Radarr's to give", noun)
		case names[i] == "tags":
			return errors.New("give tags with the tags argument, by label")
		case names[i] == "specifications":
			return errors.New("give conditions with the conditions argument")
		}
		values[names[i]] = asked[name]
	}

	return nil
}

type recordListOut struct {
	Items []map[string]any `json:"items"`
	pageOut
}

type recordOut struct {
	Saved map[string]any `json:"saved" jsonschema:"the record as Radarr now holds it"`
}

// recordArgs is what an add or an edit of a record was given.
type recordArgs struct {
	ID         string
	Values     map[string]any
	Tags       []string
	Conditions []conditionRow
}

// The arguments of a record's add and edit tools come in the shape of the
// kind of record: only a kind that has tags takes tags, and only one made of
// conditions takes conditions, so no tool offers an argument it would refuse.
type (
	idArg struct {
		ID string `json:"id" jsonschema:"the one to change: its id, or its name when it has one"`
	}
	valuesArg struct {
		Values map[string]any `json:"values,omitempty" jsonschema:"its values, by the names the tool's description lists and its list tool shows; an edit changes only the ones named. A name it does not have is refused with the ones it does"`
	}
	tagsArg struct {
		Tags []string `json:"tags,omitempty" jsonschema:"its tags by label, in place of any it had; one that does not exist is created"`
	}
	conditionsArg struct {
		Conditions []conditionRow `json:"conditions,omitempty" jsonschema:"the conditions it is made of, in place of any it had"`
	}

	addPlainIn  struct{ valuesArg }
	addTaggedIn struct {
		valuesArg
		tagsArg
	}
	addConditionsIn struct {
		valuesArg
		conditionsArg
	}
	addTaggedConditionsIn struct {
		valuesArg
		tagsArg
		conditionsArg
	}
	editPlainIn struct {
		idArg
		valuesArg
	}
	editTaggedIn struct {
		idArg
		valuesArg
		tagsArg
	}
	editConditionsIn struct {
		idArg
		valuesArg
		conditionsArg
	}
	editTaggedConditionsIn struct {
		idArg
		valuesArg
		tagsArg
		conditionsArg
	}
)

func (in addPlainIn) args() recordArgs {
	return recordArgs{Values: in.Values}
}

func (in addTaggedIn) args() recordArgs {
	return recordArgs{Values: in.Values, Tags: in.Tags}
}

func (in addConditionsIn) args() recordArgs {
	return recordArgs{Values: in.Values, Conditions: in.Conditions}
}

func (in addTaggedConditionsIn) args() recordArgs {
	return recordArgs{Values: in.Values, Tags: in.Tags, Conditions: in.Conditions}
}

func (in editPlainIn) args() recordArgs {
	return recordArgs{ID: in.ID, Values: in.Values}
}

func (in editTaggedIn) args() recordArgs {
	return recordArgs{ID: in.ID, Values: in.Values, Tags: in.Tags}
}

func (in editConditionsIn) args() recordArgs {
	return recordArgs{ID: in.ID, Values: in.Values, Conditions: in.Conditions}
}

func (in editTaggedConditionsIn) args() recordArgs {
	return recordArgs{ID: in.ID, Values: in.Values, Tags: in.Tags, Conditions: in.Conditions}
}

// recordInput is any of those shapes.
type recordInput interface{ args() recordArgs }

// addRecordTool queues an add or an edit tool that takes its arguments in one
// of those shapes.
func addRecordTool[In recordInput](r *registry, t *mcp.Tool, run func(ctx context.Context, in recordArgs) (recordOut, error)) {
	add(r, writeTool, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, recordOut, error) {
		out, err := run(ctx, in.args())

		return nil, out, err
	})
}

type recordDeleteIn struct {
	ID string `json:"id" jsonschema:"the one to delete: its id, or its name when it has one"`
	confirmIn
}

// registerRecordTools queues the tools that list, add, change and delete a
// kind of record.
func registerRecordTools[T any](r *registry, k *recordKind[T]) {
	c := r.client
	tagged := slices.Contains(namesOf[T](), "tags")

	// fill writes what an add or an edit was given into a record read as a map
	fill := func(ctx context.Context, m, values map[string]any, tags []string, conditions []conditionRow) error {
		if err := setValues[T](m, values, k.noun); err != nil {
			return err
		}
		if tags != nil {
			ids, err := resolveTags(ctx, c, tags, true)
			if err != nil {
				return err
			}
			m["tags"] = ids
		}
		if conditions != nil {
			kinds, err := k.conditions(ctx)
			if err != nil {
				return err
			}
			built, err := buildConditions(kinds, conditions)
			if err != nil {
				return err
			}
			m["specifications"] = built
		}

		return nil
	}
	saved := func(ctx context.Context, made *T) (recordOut, error) {
		m, err := asMap(made)
		if err != nil {
			return recordOut{}, err
		}
		lk, err := loadLookups(ctx, c)
		if err != nil {
			return recordOut{}, err
		}

		return recordOut{Saved: shownRecord(whole[T](m), lk)}, nil
	}

	if !k.listed {
		add(r, readTool, &mcp.Tool{
			Name:        k.family + "_list",
			Description: fmt.Sprintf("List the %s: %s Each is shown with its id and its values by the names %s_edit takes: %s.", k.plural, k.does, k.family, k.takes),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, recordListOut, error) {
			all, err := k.all(ctx)
			if err != nil {
				return nil, recordListOut{}, err
			}
			lk, err := loadLookups(ctx, c)
			if err != nil {
				return nil, recordListOut{}, err
			}
			page, paged := pageRows(all, in)
			out := recordListOut{Items: make([]map[string]any, 0, len(page)), pageOut: paged}
			for _, m := range page {
				out.Items = append(out.Items, shownRecord(m, lk))
			}

			return nil, out, nil
		})
	}

	adding := &mcp.Tool{
		Name:        k.family + "_add",
		Description: fmt.Sprintf("Add %s: %s Its values, by name: %s.", article(k.noun), k.does, k.takes),
	}
	create := func(ctx context.Context, in recordArgs) (recordOut, error) {
		if len(in.Values) == 0 && in.Conditions == nil {
			return recordOut{}, fmt.Errorf("nothing to add: give the %s its values (%s)", k.noun, k.takes)
		}
		m := map[string]any{}
		if err := fill(ctx, m, in.Values, in.Tags, in.Conditions); err != nil {
			return recordOut{}, err
		}
		v, err := fromMap[T](m, "the "+k.noun)
		if err != nil {
			return recordOut{}, err
		}
		made, err := k.create(ctx, v)
		if err != nil {
			return recordOut{}, err
		}

		return saved(ctx, made)
	}

	editing := &mcp.Tool{
		Name:        k.family + "_edit",
		Description: fmt.Sprintf("Change %s, named by its id%s: any of %s.", article(k.noun), orName(k.nameKey), k.takes),
	}
	change := func(ctx context.Context, in recordArgs) (recordOut, error) {
		found, err := k.find(ctx, in.ID)
		if err != nil {
			return recordOut{}, err
		}
		if len(in.Values) == 0 && in.Tags == nil && in.Conditions == nil {
			return recordOut{}, errors.New("nothing to change: give " + k.given)
		}

		// it is sent back whole, so it is read again once no other edit of it can run
		unlock := r.locking().By(lock.ID[T](strconv.Itoa(idOf(found))))
		defer unlock()
		m, err := k.find(ctx, strconv.Itoa(idOf(found)))
		if err != nil {
			return recordOut{}, err
		}

		if err := fill(ctx, m, in.Values, in.Tags, in.Conditions); err != nil {
			return recordOut{}, err
		}
		v, err := fromMap[T](m, "the "+k.noun)
		if err != nil {
			return recordOut{}, err
		}
		made, err := k.update(ctx, idOf(m), v)
		if err != nil {
			return recordOut{}, err
		}

		return saved(ctx, made)
	}

	switch {
	case tagged && k.conditions != nil:
		k.given = "values, tags or conditions"
		addRecordTool[addTaggedConditionsIn](r, adding, create)
		addRecordTool[editTaggedConditionsIn](r, editing, change)
	case tagged:
		k.given = "values or tags"
		addRecordTool[addTaggedIn](r, adding, create)
		addRecordTool[editTaggedIn](r, editing, change)
	case k.conditions != nil:
		k.given = "values or conditions"
		addRecordTool[addConditionsIn](r, adding, create)
		addRecordTool[editConditionsIn](r, editing, change)
	default:
		k.given = "values"
		addRecordTool[addPlainIn](r, adding, create)
		addRecordTool[editPlainIn](r, editing, change)
	}

	add(r, deleteTool, &mcp.Tool{
		Name:        k.family + "_delete",
		Description: fmt.Sprintf("Delete %s, named by its id%s: %s. Without confirm nothing changes and the answer says what would go.", article(k.noun), orName(k.nameKey), k.loses),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordDeleteIn) (*mcp.CallToolResult, deletedOut, error) {
		m, err := k.find(ctx, in.ID)
		if err != nil {
			return nil, deletedOut{}, err
		}
		if k.keep != nil {
			if why := k.keep(m); why != "" {
				return nil, deletedOut{}, errors.New(why)
			}
		}
		out, err := deleting(in.Confirm, k.label(m), []string{k.loses}, func() error { return k.remove(ctx, idOf(m)) })

		return nil, out, err
	})
}

// orName is how a tool's description says a record may be named as well as
// by its id.
func orName(nameKey string) string {
	if nameKey == "" {
		return ""
	}

	return " or its " + nameKey
}

// conditionKinds reads the kinds of condition Radarr describes, which its
// document does not say the shape of.
func conditionKinds(raw json.RawMessage, err error) ([]map[string]any, error) {
	if err != nil {
		return nil, err
	}
	var kinds []map[string]any
	if err := json.Unmarshal(raw, &kinds); err != nil {
		return nil, fmt.Errorf("reading the kinds of condition: %w", err)
	}

	return kinds, nil
}

func registerRecordKindTools(r *registry) map[string]kindDescriber {
	c := r.client

	registerRecordTools(r, &recordKind[radarr.DelayProfileResource]{
		family: "delayprofile", noun: "delay profile", plural: "delay profiles",
		does:  "how long Radarr waits after a release appears before grabbing it, so that a better one has time to turn up, and which of usenet and torrents it prefers; the one with no tags holds for every film that no other profile's tags pick out.",
		takes: "enableUsenet, enableTorrent, preferredProtocol (usenet or torrent), usenetDelay and torrentDelay (minutes), bypassIfHighestQuality, bypassIfAboveCustomFormatScore, minimumCustomFormatScore, and tags by the tags argument",
		loses: "the films its tags picked out are grabbed by the default profile's delays instead",
		keep: func(values map[string]any) string {
			if idOf(values) == 1 {
				return "delay profile 1 is the default, which holds for every film no other profile picks out: Radarr does not let it be deleted. delayprofile_edit changes its delays"
			}
			return ""
		},
		list: func(ctx context.Context) ([]radarr.DelayProfileResource, error) {
			res, err := c.GetDelayProfile(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, v radarr.DelayProfileResource) (*radarr.DelayProfileResource, error) {
			res, err := c.PostDelayProfile(ctx, v)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, v radarr.DelayProfileResource) (*radarr.DelayProfileResource, error) {
			res, err := c.PutDelayProfileById(ctx, id, v)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteDelayProfileById(ctx, id)
			return err
		},
	})

	type reorderIn struct {
		ID    int `json:"id"              jsonschema:"the delay profile to move, by id"`
		After int `json:"after,omitempty" jsonschema:"the id of the profile to put it straight after; left out, it goes first"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "delayprofile_reorder",
		Description: "Move a delay profile up or down the order Radarr tries them in: a film picked out by more than one profile's tags is held to the first of them. The default profile, which has no tags, stays last.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reorderIn) (*mcp.CallToolResult, recordListOut, error) {
		if in.ID == 0 {
			return nil, recordListOut{}, errors.New("give the id of the delay profile to move")
		}
		opts := radarr.PutDelayProfileReorderByIdOperationOptions{}
		if in.After != 0 {
			opts.After = new(in.After)
		}
		res, err := c.PutDelayProfileReorderById(ctx, in.ID, opts)
		if err != nil {
			return nil, recordListOut{}, err
		}
		lk, err := loadLookups(ctx, c)
		if err != nil {
			return nil, recordListOut{}, err
		}
		out := recordListOut{Items: make([]map[string]any, 0, len(res.Model)), Total: len(res.Model)}
		for i := range res.Model {
			m, err := asMap(res.Model[i])
			if err != nil {
				return nil, recordListOut{}, err
			}
			out.Items = append(out.Items, shownRecord(whole[radarr.DelayProfileResource](m), lk))
		}

		return nil, out, nil
	})

	registerRecordTools(r, &recordKind[radarr.ReleaseProfileResource]{
		family: "releaseprofile", noun: "release profile", plural: "release profiles", nameKey: "name",
		does:  "words a release's name must have, or must not have, for Radarr to grab it, for every film or the ones its tags pick out.",
		takes: "name, enabled, required and ignored (each a list of words or /regular expressions/), indexerId (0 for every indexer), and tags by the tags argument",
		loses: "releases it kept out may be grabbed again, and ones it insisted on are no longer preferred",
		list: func(ctx context.Context) ([]radarr.ReleaseProfileResource, error) {
			res, err := c.GetReleaseProfile(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, v radarr.ReleaseProfileResource) (*radarr.ReleaseProfileResource, error) {
			res, err := c.PostReleaseProfile(ctx, v)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, v radarr.ReleaseProfileResource) (*radarr.ReleaseProfileResource, error) {
			res, err := c.PutReleaseProfileById(ctx, id, v)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteReleaseProfileById(ctx, id)
			return err
		},
	})

	tagging := func(ctx context.Context) ([]map[string]any, error) {
		res, err := c.GetAutoTaggingSchema(ctx)
		return conditionKinds(res.Model, err)
	}
	registerRecordTools(r, &recordKind[radarr.AutoTaggingResource]{
		family: "autotagging", noun: "auto tagging rule", plural: "auto tagging rules", nameKey: "name", conditions: tagging,
		does:  "tags Radarr puts on every film that meets the rule's conditions - a genre, a year, a studio, a root folder, an original language - and takes off again when it no longer does.",
		takes: "name, removeTagsAutomatically, the tags it gives by the tags argument, and its conditions by the conditions argument (provider_kinds with provider autotagging lists the kinds)",
		loses: "Radarr stops tagging films by it; the tags it already gave stay",
		list: func(ctx context.Context) ([]radarr.AutoTaggingResource, error) {
			res, err := c.GetAutoTagging(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, v radarr.AutoTaggingResource) (*radarr.AutoTaggingResource, error) {
			res, err := c.PostAutoTagging(ctx, v)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, v radarr.AutoTaggingResource) (*radarr.AutoTaggingResource, error) {
			res, err := c.PutAutoTaggingById(ctx, id, v)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteAutoTaggingById(ctx, id)
			return err
		},
	})

	registerRecordTools(r, &recordKind[radarr.RemotePathMappingResource]{
		family: "remotepathmapping", noun: "remote path mapping", plural: "remote path mappings",
		does:  "where a download client on another machine puts its downloads, as Radarr sees that folder: without one Radarr looks for a finished download at a path that is only right on the client's own machine.",
		takes: "host (the download client's host, as its settings give it), remotePath (the folder as the client names it) and localPath (the same folder as Radarr reaches it)",
		loses: "Radarr looks for that client's downloads at the path the client reports, and fails to import what it cannot find there",
		list: func(ctx context.Context) ([]radarr.RemotePathMappingResource, error) {
			res, err := c.GetRemotePathMapping(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, v radarr.RemotePathMappingResource) (*radarr.RemotePathMappingResource, error) {
			res, err := c.PostRemotePathMapping(ctx, v)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, v radarr.RemotePathMappingResource) (*radarr.RemotePathMappingResource, error) {
			res, err := c.PutRemotePathMappingById(ctx, id, v)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteRemotePathMappingById(ctx, id)
			return err
		},
	})

	registerRecordTools(r, &recordKind[radarr.CustomFilterResource]{
		family: "customfilter", noun: "saved filter", plural: "saved filters", nameKey: "label",
		does:  "a filter someone saved in Radarr's web interface, on one of its pages, to see part of a list by; it changes nothing but what that page shows.",
		takes: "label, type (the page it is for: movieIndex, discoverMovie, queue, history, blocklist...), and filters (a list of conditions, each with key, value and type, as the web interface saves them)",
		loses: "the filter is gone from the page it was saved on",
		list: func(ctx context.Context) ([]radarr.CustomFilterResource, error) {
			res, err := c.GetCustomFilter(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, v radarr.CustomFilterResource) (*radarr.CustomFilterResource, error) {
			res, err := c.PostCustomFilter(ctx, v)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, v radarr.CustomFilterResource) (*radarr.CustomFilterResource, error) {
			res, err := c.PutCustomFilterById(ctx, id, v)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteCustomFilterById(ctx, id)
			return err
		},
	})

	formats := func(ctx context.Context) ([]map[string]any, error) {
		res, err := c.GetCustomFormatSchema(ctx)
		return conditionKinds(res.Model, err)
	}
	registerRecordTools(r, &recordKind[radarr.CustomFormatResource]{
		family: "customformat", noun: "custom format", plural: "custom formats", nameKey: "name", conditions: formats, listed: true,
		does:  "something about a release or a file that Radarr can score - HDR, a release group, a codec, a language, an edition - made of conditions on its name, size, source, resolution and the like. A quality profile gives each format a score, and prefers the release that scores highest.",
		takes: "name, includeCustomFormatWhenRenaming, and its conditions by the conditions argument (provider_kinds with provider customformat lists the kinds)",
		loses: "every quality profile's score for it, and with them the preference for releases that match it",
		list: func(ctx context.Context) ([]radarr.CustomFormatResource, error) {
			res, err := c.GetCustomFormat(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, v radarr.CustomFormatResource) (*radarr.CustomFormatResource, error) {
			res, err := c.PostCustomFormat(ctx, v)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, v radarr.CustomFormatResource) (*radarr.CustomFormatResource, error) {
			res, err := c.PutCustomFormatById(ctx, id, v)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteCustomFormatById(ctx, id)
			return err
		},
	})

	// what provider_kinds answers for the two kinds of record made of conditions
	return map[string]kindDescriber{
		"autotagging": func(ctx context.Context, kind string) (kindDoc, error) {
			kinds, err := tagging(ctx)
			if err != nil {
				return kindDoc{}, err
			}
			return describeConditions(kinds, "auto tagging", kind)
		},
		"customformat": func(ctx context.Context, kind string) (kindDoc, error) {
			kinds, err := formats(ctx)
			if err != nil {
				return kindDoc{}, err
			}
			return describeConditions(kinds, "custom format", kind)
		},
	}
}
