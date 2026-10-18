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
		"audit_dynamic_range", "audit_subtitles", "audit_disk_space",
		"movie_add", "movie_edit", "movie_batch_edit", "movie_refresh", "movie_rescan", "movie_rename", "movie_credits",
		"moviefile_edit", "import_scan", "import_identify", "import_apply", "release_parse",
		"collection_list", "collection_edit", "naming_get", "naming_edit",
		"qualitydefinition_list", "qualitydefinition_edit", "customformat_list",
		"queue_remove", "history_mark_failed",
	},
	// getting films: searching the indexers, grabbing, following a download
	// through the queue into the history, and what is coming out
	"acquire": {
		"movie_download", "release_search", "release_grab", "release_push", "queue_list", "queue_grab", "history_list",
		"blocklist_list", "blocklist_remove", "calendar_list", "exclusion_list", "exclusion_add", "exclusion_remove",
	},
	// the labels and places films are kept in: tags, root folders and the
	// quality profiles films are held to
	"organise": {
		"tag_list", "tag_create", "tag_rename", "tag_delete", "rootfolder_add", "rootfolder_delete",
		"qualityprofile_edit", "qualityprofile_add", "qualityprofile_delete",
	},
	// setting Radarr up rather than using it: where it searches and what it
	// downloads with, the lists it adds films from, who it tells, what it
	// writes for a media player, the rules a release is held to before it is
	// grabbed, and Radarr's own settings
	"setup": {
		"provider_kinds", "provider_options", "indexer_list", "indexer_test", "indexer_add", "indexer_edit", "indexer_delete",
		"downloadclient_list", "downloadclient_test", "downloadclient_add", "downloadclient_edit", "downloadclient_delete",
		"importlist_list", "importlist_add", "importlist_edit", "importlist_delete", "importlist_test", "importlist_movies",
		"notification_list", "notification_add", "notification_edit", "notification_delete", "notification_test",
		"metadata_list", "metadata_edit", "metadata_test",
		"customformat_add", "customformat_edit", "customformat_delete",
		"delayprofile_list", "delayprofile_add", "delayprofile_edit", "delayprofile_reorder", "delayprofile_delete",
		"releaseprofile_list", "releaseprofile_add", "releaseprofile_edit", "releaseprofile_delete",
		"autotagging_list", "autotagging_add", "autotagging_edit", "autotagging_delete",
		"remotepathmapping_list", "remotepathmapping_add", "remotepathmapping_edit", "remotepathmapping_delete",
		"customfilter_list", "customfilter_add", "customfilter_edit", "customfilter_delete",
		"server_settings", "server_settings_edit",
	},
	// running Radarr rather than using it: health, disk space, logs,
	// updates, backups and putting one back, scheduled tasks, a restart, the
	// folders it can reach, and the tools that remove films and files
	"admin": {
		"server_health", "server_disk_space", "server_logs", "server_log_file", "server_updates",
		"server_backups", "server_backup_create", "server_backup_restore", "server_backup_delete", "server_restart", "server_shutdown",
		"task_list", "task_run", "task_cancel", "filesystem_browse",
		"movie_delete", "moviefile_delete",
	},
}

// Instructions are what a client is told on connecting, after what go-kt
// says of every server: the rules that hold between these tools, said once
// so that no tool's description has to. TestEveryListPagesAndEveryDeleteConfirms
// holds the tools to the first two.
var Instructions = []string{
	"Every list takes limit (100 unless given, 1000 at most) and offset, and answers total, offset and, while there is another page, the next_offset to pass back as offset.",
	"A tool that deletes changes nothing without confirm: called without it, it answers would_delete, what a confirmed call would remove. Those are movie_delete, moviefile_delete, qualityprofile_delete, customformat_delete, indexer_delete, downloadclient_delete, importlist_delete, notification_delete, delayprofile_delete, releaseprofile_delete, autotagging_delete, remotepathmapping_delete, customfilter_delete and server_backup_delete. server_backup_restore, server_restart and server_shutdown ask the same way, and so does server_settings_edit for a setting that decides how Radarr is reached or who may sign in.",
	"A film is named by its Radarr id, tmdb:<id>, imdb:<tt id> or its title. Start with audit_all, which says where the library needs work, then run the audit whose count is not zero for its worklist.",
	"A release's name, a download's title and messages, a history or blocklist entry, an overview and a log line were written outside Radarr. They are shown with the characters that do not display taken out, and cut short when very long.",
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
	// writeLog is where the line for each call of a write or delete tool
	// goes once its credentials are blanked (writelog.go); nil is the
	// process's own log
	writeLog func(format string, args ...any)
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
	// the database and settings written over with a backup's
	"server_backup_restore": {},
	// what is under way is cut off, and Radarr is away until it is back, or for good
	"server_restart":  {},
	"server_shutdown": {},
	// a command stopped part way; asked again, it has already stopped
	"task_cancel": {Idempotent: true},
	// settings written over, the same ones again the second time
	"server_settings_edit": {Idempotent: true},
	// a release sent to the download client, imported over the file a film had; sent again it is another download
	"queue_grab":   {},
	"release_push": {},
	// a new profile beside the others; a name there already is refused before anything is sent
	"qualityprofile_add": {Additive: true, Idempotent: true},
	// a new one beside the others, and another the second time
	"indexer_add":           {Additive: true},
	"downloadclient_add":    {Additive: true},
	"importlist_add":        {Additive: true},
	"notification_add":      {Additive: true},
	"customformat_add":      {Additive: true},
	"delayprofile_add":      {Additive: true},
	"releaseprofile_add":    {Additive: true},
	"autotagging_add":       {Additive: true},
	"remotepathmapping_add": {Additive: true},
	"customfilter_add":      {Additive: true},
	// settings, switches and tags written over, the same ones again the second time
	"indexer_edit":           {Idempotent: true},
	"downloadclient_edit":    {Idempotent: true},
	"importlist_edit":        {Idempotent: true},
	"notification_edit":      {Idempotent: true},
	"metadata_edit":          {Idempotent: true},
	"customformat_edit":      {Idempotent: true},
	"delayprofile_edit":      {Idempotent: true},
	"releaseprofile_edit":    {Idempotent: true},
	"autotagging_edit":       {Idempotent: true},
	"remotepathmapping_edit": {Idempotent: true},
	"customfilter_edit":      {Idempotent: true},
	// an order written over, the same again the second time
	"delayprofile_reorder": {Idempotent: true},
}

// radarrSaid are the values in an answer that are Radarr's own words for
// what went wrong: why a test failed, why a download is stuck, what a health
// check found, a line of its log. Radarr quotes an address back in such
// words with whatever the address carried still on it - an indexer that
// answers an error is reported as "HTTP request failed:
// [500:InternalServerError] [GET] at [http://indexer/api?t=movie&apikey=...]",
// the indexer's stored API key and all. So each address under one of these
// names is answered without who it signs in as, the value of any parameter,
// or what follows its "#". A failure's words get the same from go-kt around
// every tool. TestRadarrSaidIsAnswered holds each name to a tool that
// answers under it.
var radarrSaid = []string{"failures", "message", "messages", "error_message", "error", "details", "lines", "exception", "health_errors", "unavailable"}

// shownArguments are the free-form arguments that hold no credential, by
// tool, which the line written for a write shows as they were sent: a
// thing's switches, a record's values, and Radarr's own settings, where what
// a port was changed to is the point of the line. Everything under a
// provider's settings stays blanked, names kept: an indexer's cookie or
// passkey is there under a name of its kind's own choosing.
var shownArguments = func() map[string][]string {
	shown := map[string][]string{"server_settings_edit": {"settings"}, "metadata_edit": {"options"}}
	for _, family := range []string{"indexer", "downloadclient", "importlist", "notification"} {
		shown[family+"_add"], shown[family+"_edit"] = []string{"options"}, []string{"options"}
	}
	for _, family := range []string{"delayprofile", "releaseprofile", "autotagging", "remotepathmapping", "customfilter", "customformat"} {
		shown[family+"_add"], shown[family+"_edit"] = []string{"values"}, []string{"values"}
	}

	return shown
}()

// queued is every tool queued so far, in go-kt's registry, which is made
// when the first tool is.
func (r *registry) queued() *mcpregistry.Registry {
	if r.tools == nil {
		r.tools = mcpregistry.New(mcpregistry.Config{
			Toolsets:  Toolsets,
			Essential: EssentialTools,
			Hints:     toolHints,
			LogError:  r.errorLog,
			LogWrite:  r.writeLog,
			// the line written for a write shows a thing's switches, a record's values and Radarr's own settings as they were sent, and blanks everything under a provider's settings
			ShownArguments: shownArguments,
			BlankAddresses: radarrSaid,
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
	registerSettingsTools(r)
	registerQualityProfileTools(r)
	registerAcquireMoreTools(r)
	registerAdminTools(r)
	kinds, options := registerProviderFamilyTools(r)
	registerKindsTool(r, kinds, registerRecordKindTools(r))
	registerOptionsTool(r, options)
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
