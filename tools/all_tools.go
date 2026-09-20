// Package tools defines the MCP tools exposed by radarr-mcp, split by the
// resource they act on (server.go, movies.go, queue.go, ...). Tools are named
// resource-first (movie_*, queue_*, audit_*) so they group by what they act
// on, and the resources are spelled the way Radarr's API spells them
// (rootfolder, qualityprofile, downloadclient).
//
// Every tool is registered through add with a kind: read tools never change
// server state, write tools do (and are dropped under --read-only), and delete
// tools remove films or their files from disk (and are only registered with
// --enable-delete). --toolsets picks the groups a session needs, and
// --allow-tools / --deny-tools narrow the set further.
package tools

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options controls which tools are registered.
type Options struct {
	// ReadOnly registers only tools that never change server state.
	ReadOnly bool
	// EnableDelete registers the tools that delete films and their files.
	// Off unless the operator opts in.
	EnableDelete bool
	// Toolsets, when set, restricts registration to the named groups (see
	// Toolsets). "core" is always included, so a set can be asked for on its
	// own. Allow and Deny narrow whatever is left.
	Toolsets []string
	// Allow, when set, restricts registration to matching tools: exact names,
	// prefix/suffix globs (movie_*, *_delete) or the "essential" preset.
	Allow []string
	// Deny removes matching tools from whatever Allow left.
	Deny []string
}

// Toolsets group the tools by the job someone is doing, so a client can load a
// working subset instead of all of them. The whole surface is several
// thousand tokens of tool definitions (name, description, input schema)
// before anyone has asked a question; core alone is a fraction of that.
//
// "all" is every tool, which is what the library does when no toolset is asked
// for; the radarr-mcp binary defaults to core instead (see
// cli.FlagData.ToolOptions).
//
// --toolsets also takes a resource family - movie, audit, queue, tag,
// rootfolder, ... - which is every tool with that prefix. Those are derived
// from the registered names rather than listed here, so they cannot go stale.
//
// Every tool belongs to exactly one set (TestToolsetsPartition proves it), and
// core is added to whatever else is asked for, because none of the other sets
// can find a film on their own.
var Toolsets = map[string][]string{
	// enough to find films and read them, and the two lists every add and
	// edit needs: the base every other set assumes
	"core": {
		"server_info", "movie_list", "movie_get", "movie_lookup", "rootfolder_list", "qualityprofile_list",
	},
	// find what is wrong with the library and fix it: every audit, and the
	// edits, rescans, renames, imports and settings that fix what they find
	"curation": {
		"audit_all", "audit_missing_files", "audit_unmonitored", "audit_cutoff_unmet", "audit_profile_mismatch",
		"audit_runtime", "audit_resolution_mismatch", "audit_quality", "audit_size", "audit_language",
		"audit_year_mismatch", "audit_naming", "audit_unmapped_folders", "audit_missing_folders", "audit_untracked_files",
		"audit_missing_metadata", "audit_removed", "audit_collection_gaps", "audit_queue",
		"movie_add", "movie_edit", "movie_batch_edit", "movie_refresh", "movie_rescan", "movie_rename", "movie_credits",
		"moviefile_edit", "import_scan", "import_identify", "import_apply", "release_parse",
		"collection_list", "collection_edit", "naming_get", "naming_edit",
		"qualitydefinition_list", "qualitydefinition_edit", "customformat_list",
		"queue_remove", "history_mark_failed",
	},
	// getting films: searching the indexers, grabbing, following a download
	// through the queue into the history, and what is coming out
	"acquire": {
		"movie_download", "release_search", "release_grab", "queue_list", "history_list",
		"blocklist_list", "blocklist_remove", "calendar_list", "exclusion_list", "exclusion_add", "exclusion_remove",
	},
	// the labels and places films are kept in: tags, root folders and the
	// quality profiles films are held to
	"organise": {
		"tag_list", "tag_create", "tag_rename", "tag_delete", "rootfolder_add", "rootfolder_delete", "qualityprofile_edit",
	},
	// running Radarr rather than using it: health, disk space, logs,
	// backups, scheduled tasks, the indexers and download clients, and the
	// tools that remove films and files
	"admin": {
		"server_health", "server_disk_space", "server_logs", "server_log_file", "server_backups", "server_backup_create",
		"task_list", "task_run", "indexer_list", "indexer_test", "downloadclient_list", "downloadclient_test",
		"movie_delete", "moviefile_delete",
	},
}

// EssentialTools is the curated preset selected by --allow-tools essential:
// enough to find a film, read it, see what is downloading and add another.
var EssentialTools = []string{
	"movie_list",
	"movie_get",
	"movie_lookup",
	"movie_add",
	"queue_list",
}

type toolKind int

const (
	readTool toolKind = iota
	writeTool
	deleteTool
)

type pending struct {
	name        string
	kind        toolKind
	description string
	register    func()
}

// registry collects tool registrations so the allow/deny patterns can be
// validated against the full tool list before anything is added.
type registry struct {
	server  *mcp.Server
	client  *radarr.Client
	opts    Options
	pending []pending
}

// add queues a typed tool for registration. It sets the MCP annotations from
// kind so clients can tell read-only from destructive tools without parsing
// descriptions, and normalises the output so that empty collections
// serialise as [] rather than null: an AI client reading "people": null
// cannot tell "none" from "not fetched", and Go leaves un-appended slices nil.
func add[In, Out any](r *registry, kind toolKind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	f := false
	switch kind {
	case readTool:
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &f, OpenWorldHint: &f}
	case writeTool:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: &f, OpenWorldHint: &f}
	case deleteTool:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: &f}
	}

	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := h(ctx, req, in)
		if err == nil {
			emptyNilSlices(reflect.ValueOf(&out).Elem())
		}

		return res, out, err
	}

	r.pending = append(r.pending, pending{
		name:        t.Name,
		kind:        kind,
		description: t.Description,
		register:    func() { mcp.AddTool(r.server, t, wrapped) },
	})
}

// emptyNilSlices walks v (structs, pointers, slices) and replaces every settable
// nil slice with an empty one.
func emptyNilSlices(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			emptyNilSlices(v.Elem())
		}
	case reflect.Struct:
		for _, f := range v.Fields() {
			emptyNilSlices(f)
		}
	case reflect.Slice:
		if v.IsNil() {
			if v.CanSet() {
				v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			}

			return
		}
		for i := range v.Len() {
			emptyNilSlices(v.Index(i))
		}
	default:
	}
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *radarr.Client, opts Options) ([]string, error) {
	r := &registry{server: server, client: client, opts: opts}
	queueTools(r)

	keep, err := selected(r, opts)
	if err != nil {
		return nil, err
	}

	var registered []string
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		p.register()
		registered = append(registered, p.name)
	}
	slices.Sort(registered)

	return registered, nil
}

// queueTools queues every tool, before any filtering.
func queueTools(r *registry) {
	registerServerTools(r)
	registerTaskTools(r)
	registerMovieTools(r)
	registerMovieEditTools(r)
	registerMovieFileTools(r)
	registerImportTools(r)
	registerReleaseTools(r)
	registerQueueTools(r)
	registerHistoryTools(r)
	registerCalendarTools(r)
	registerCollectionTools(r)
	registerExclusionTools(r)
	registerTagTools(r)
	registerRootFolderTools(r)
	registerQualityTools(r)
	registerProviderTools(r)
	registerNamingTools(r)
	registerAuditTools(r)
}

// names lists the queued tools in registration order.
func (r *registry) names() []string {
	out := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		out = append(out, p.name)
	}

	return out
}

// selected applies the kind gates and the toolset/allow/deny filters, and is
// shared by RegisterAll and Describe so `radarr-mcp tools` cannot drift from
// what the server actually registers.
func selected(r *registry, opts Options) (map[string]bool, error) {
	names := r.names()
	sets, err := compileToolsets(opts.Toolsets, names)
	if err != nil {
		return nil, err
	}
	allow, err := compilePatterns(opts.Allow, names, "allow")
	if err != nil {
		return nil, err
	}
	deny, err := compilePatterns(opts.Deny, names, "deny")
	if err != nil {
		return nil, err
	}

	keep := make(map[string]bool, len(r.pending))
	for _, p := range r.pending {
		switch {
		case p.kind == deleteTool && !opts.EnableDelete:
		case p.kind != readTool && opts.ReadOnly:
		case len(sets) > 0 && !sets[p.name]:
		case len(allow) > 0 && !matchesAny(allow, p.name):
		case matchesAny(deny, p.name):
		default:
			keep[p.name] = true
		}
	}

	return keep, nil
}

// compileToolsets turns the requested set names into the tools they hold,
// always including core. An unknown name aborts startup naming the valid ones,
// the way an allow/deny pattern that matches nothing does.
func compileToolsets(raw, known []string) (map[string]bool, error) {
	var asked []string
	for _, entry := range raw {
		for name := range strings.SplitSeq(entry, ",") {
			if name = strings.TrimSpace(name); name != "" {
				asked = append(asked, name)
			}
		}
	}
	if len(asked) == 0 {
		return nil, nil
	}

	out := map[string]bool{}
	for _, name := range asked {
		if name == "all" {
			for _, t := range known {
				out[t] = true
			}
			continue
		}
		if tools, ok := Toolsets[name]; ok {
			for _, t := range tools {
				out[t] = true
			}
			continue
		}
		// not a named set: a resource family, every tool with that prefix
		found := false
		for _, t := range known {
			if strings.HasPrefix(t, name+"_") {
				out[t], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown toolset %q (sets: all, %s; or a resource family: %s)",
				name, strings.Join(setNames(), ", "), strings.Join(resourceFamilies(known), ", "))
		}
	}
	// core is what every other set assumes: without it there is no way to find
	// a library or open an item
	for _, t := range Toolsets["core"] {
		out[t] = true
	}

	return out, nil
}

// setNames lists the curated toolsets, sorted.
func setNames() []string {
	out := make([]string, 0, len(Toolsets))
	for k := range Toolsets {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// resourceFamilies lists the resource prefixes in use, sorted.
func resourceFamilies(known []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range known {
		if i := strings.Index(t, "_"); i > 0 && !seen[t[:i]] {
			seen[t[:i]] = true
			out = append(out, t[:i])
		}
	}
	slices.Sort(out)

	return out
}

// compilePatterns expands the essential preset, splits comma-separated
// entries, and checks that every pattern matches at least one known tool.
func compilePatterns(raw, known []string, which string) ([]string, error) {
	var out []string
	for _, entry := range raw {
		for pat := range strings.SplitSeq(entry, ",") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if pat == "essential" {
				out = append(out, EssentialTools...)
				continue
			}
			if !slices.ContainsFunc(known, func(n string) bool { return matchPattern(pat, n) }) {
				return nil, fmt.Errorf("%s-tools pattern %q matches no tool (have: %s)", which, pat, strings.Join(known, ", "))
			}
			out = append(out, pat)
		}
	}

	return out, nil
}

func matchesAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return matchPattern(p, name) })
}

// matchPattern supports exact names plus a single leading or trailing '*'.
func matchPattern(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	default:
		return pattern == name
	}
}

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo struct {
	Name        string
	Kind        string // read, write or delete
	Toolset     string // the curated set it belongs to
	Description string
}

// Describe lists the tools opts would register, for `radarr-mcp tools`. It
// needs no connectivity: registration never calls the client, only the
// handlers do.
func Describe(opts Options) ([]ToolInfo, error) {
	client, err := radarr.New("https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "radarr-mcp", Version: "describe"}, nil)

	r := &registry{server: server, client: client, opts: opts}
	queueTools(r)

	keep, err := selected(r, opts)
	if err != nil {
		return nil, err
	}

	set := map[string]string{}
	for name, members := range Toolsets {
		for _, m := range members {
			// core wins: it is the set a tool is reached through most often
			if set[m] == "" || name == "core" {
				set[m] = name
			}
		}
	}

	kinds := map[toolKind]string{readTool: "read", writeTool: "write", deleteTool: "delete"}
	out := make([]ToolInfo, 0, len(r.pending))
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		out = append(out, ToolInfo{Name: p.name, Kind: kinds[p.kind], Toolset: set[p.name], Description: p.description})
	}
	slices.SortFunc(out, func(a, b ToolInfo) int { return cmp.Compare(a.Name, b.Name) })

	return out, nil
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string { return setNames() }

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	r := &registry{}
	queueTools(r)

	return resourceFamilies(r.names())
}
