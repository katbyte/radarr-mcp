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
	"sync"

	"github.com/katbyte/go-kt/lock"
	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
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
	// own.
	Toolsets []string
	// Allow asks for tools by name: exact names, prefix/suffix globs
	// (movie_*, *_delete) or the "essential" preset. Beside Toolsets it adds
	// to them, these sets and these tools as well; on its own it is only the
	// tools it names, with no core put beside them.
	Allow []string
	// Deny removes matching tools from whatever Toolsets and Allow asked
	// for: it is how a toolset is narrowed.
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

// The kinds a tool is added with, in go-kt's registry's terms: a read tool
// never changes what the server holds, a write tool does, and a delete tool
// removes what cannot be put back.
const (
	readTool   = mcpregistry.Read
	writeTool  = mcpregistry.Write
	deleteTool = mcpregistry.Delete
)

// registry is this application's tools and what they share. Which of them a
// session gets, and what each tells a client about itself, is go-kt's
// registry's work (tools), so the allow and deny patterns are checked
// against every tool before any is registered.
type registry struct {
	client *radarr.Client
	opts   Options
	// tools holds every tool queued, made when the first is (see queued)
	tools *mcpregistry.Registry
	// the locks that make two edits of one record in a turn take turns; see
	// locks.go
	locksOnce sync.Once
	locks     *lock.Set
	// errorLog is where a handler's panic is logged; nil is the process's
	// own log, which writes to stderr
	errorLog func(format string, args ...any)
}

// toolHints are what a tool tells a client about itself beyond its kind, in
// the MCP annotations' terms. A write tool is destructive when it can change
// or take away what was there - a value written over, a file moved, replaced
// or renamed, an entry taken off a list, a download stopped - and additive
// when it only ever adds; idempotent when calling it again with the same
// arguments changes nothing more.
//
// Every write tool has an entry, each decided on its own rather than taken
// from the kind: marked all alike, a grab (the download it starts replaces
// the file a film has), a rescan (a file that is gone is dropped) and a task
// (one can search for and replace every film below its cutoff) read as
// additive to a client. A write tool missing from here is marked
// destructive, and fails TestEveryWriteToolIsHinted.
var toolHints = map[string]mcpregistry.Hints{
	// a film that was not there, with its folder; one in the library already
	// is refused before anything is sent
	"movie_add": {Additive: true, Idempotent: true},
	// values written over, the same again the second time; a change of root
	// folder moves the film's folder on disk
	"movie_edit":       {Idempotent: true},
	"movie_batch_edit": {Idempotent: true},
	// the title, overview and artwork written over from TMDB, and files gone
	// from disk forgotten
	"movie_refresh": {},
	// files gone from disk are forgotten, and their films become missing
	"movie_rescan": {},
	// files renamed on disk; once they match the format there is nothing
	// left to rename
	"movie_rename": {Idempotent: true},
	// what a search grabs is imported over the file a film had when it is an
	// upgrade, and a second search can grab another release
	"movie_download": {},
	// a release sent to the download client, imported over the file a film
	// had; sent again it is another download
	"release_grab": {},
	// what Radarr records about a file written over, the same again the
	// second time
	"moviefile_edit": {Idempotent: true},
	// a file moved into the library, over the one a film had
	"import_apply": {},
	// values written over, the same again the second time; a collection
	// monitored adds the films of it the library lacks
	"collection_edit": {Idempotent: true},
	// settings written over, the same again the second time
	"naming_edit":            {Idempotent: true},
	"qualityprofile_edit":    {Idempotent: true},
	"qualitydefinition_edit": {Idempotent: true},
	// the download and its data go from the download client too; asked
	// again, the id is not in the queue and nothing is sent
	"queue_remove": {Idempotent: true},
	// the release is blocklisted and, with failed download handling on,
	// another searched for
	"history_mark_failed": {},
	// entries taken off; asked again, there are none of those ids
	"blocklist_remove": {Idempotent: true},
	"exclusion_remove": {Idempotent: true},
	// an exclusion that was not there; a film excluded already is refused
	// before anything is sent
	"exclusion_add": {Additive: true, Idempotent: true},
	// a tag that was not there; a label there already is refused before
	// anything is sent
	"tag_create": {Additive: true, Idempotent: true},
	// a label written over, the same again the second time
	"tag_rename": {Idempotent: true},
	// a tag taken off every film that carries it, then taken away; asked
	// again, there is no tag of that label to take
	"tag_delete": {Idempotent: true},
	// a root folder that was not there; nothing on disk is touched
	"rootfolder_add": {Additive: true},
	// a root folder taken out of Radarr; asked again, there is none at that
	// path
	"rootfolder_delete": {Idempotent: true},
	// a task can search for and grab a release for every film below its
	// cutoff, or forget the files a rescan no longer finds
	"task_run": {},
	// a new backup beside the ones kept, and another the second time: Radarr
	// removes those past its retention only when it takes one on its schedule
	"server_backup_create": {Additive: true},
}

// queued is every tool queued so far, in go-kt's registry, which is made
// when the first tool is.
func (r *registry) queued() *mcpregistry.Registry {
	if r.tools == nil {
		r.tools = mcpregistry.New(mcpregistry.Config{
			Toolsets:  Toolsets,
			Essential: EssentialTools,
			Hints:     toolHints,
			LogError:  r.errorLog,
		})
	}

	return r.tools
}

// add queues a typed tool for registration. go-kt's registry sets the MCP
// annotations from kind and the tool's hints so clients can tell read-only
// from destructive tools without parsing descriptions, turns a panic in the
// handler into an ordinary tool error, and sends empty collections as []
// rather than null: an AI client reading "films": null cannot tell "none"
// from "not fetched", and Go leaves un-appended slices nil.
func add[In, Out any](r *registry, kind mcpregistry.Kind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcpregistry.Add(r.queued(), kind, t, h)
}

// queuedFor is every tool queued for these options: registration never calls
// the client, only the handlers do.
func queuedFor(client *radarr.Client, opts Options) *registry {
	r := &registry{client: client, opts: opts}
	queueTools(r)

	return r
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *radarr.Client, opts Options) ([]string, error) {
	return queuedFor(client, opts).queued().Register(server, opts.selection())
}

// selection is the part of the options that chooses tools, as go-kt's
// registry takes it.
func (o *Options) selection() mcpregistry.Selection {
	return mcpregistry.Selection{
		ReadOnly:     o.ReadOnly,
		EnableDelete: o.EnableDelete,
		Toolsets:     o.Toolsets,
		Allow:        o.Allow,
		Deny:         o.Deny,
	}
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

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo = mcpregistry.Info

// described is every tool queued with no server behind it, for the answers
// that need none.
func described(opts Options) (*registry, error) {
	client, err := radarr.New("https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}

	return queuedFor(client, opts), nil
}

// Describe lists the tools opts would register, for `radarr-mcp tools`. It
// needs no connectivity, and makes the same choice RegisterAll does.
func Describe(opts Options) ([]ToolInfo, error) {
	r, err := described(opts)
	if err != nil {
		return nil, err
	}

	return r.queued().Describe(opts.selection())
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string {
	return mcpregistry.New(mcpregistry.Config{Toolsets: Toolsets}).ToolsetNames()
}

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	r, err := described(Options{})
	if err != nil {
		return nil
	}

	return r.queued().FamilyNames()
}
