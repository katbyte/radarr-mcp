# Tool roadmap

Design rules, in priority order:

1. **Everything Radarr can do, with the judgment on top.** Whatever can be done in Radarr's own interface can be done with a tool: its setup as much as its library. The audits and the composite tools are still the reason the server exists; the rest is there so a session never has to send someone back to the web interface. A test holds the tools to it: every operation of Radarr's API is called by a tool, or is listed with the reason none calls it.
2. **Trim every response.** Tools return the fields a decision needs, never raw resources (`MovieResource` has 50 fields before its file; `movie_list` returns 12).
3. **Composite over chatty.** If a task always takes N calls (look up → add → wait for the refresh → read it back), it is one tool, not N. Tools that start a Radarr command wait for it to finish and say how it ended.
4. **Names, not just ids.** Every tool that takes a film, profile, root folder, tag or collection resolves a name, and an unknown one lists what exists.
5. **Resource-first names** (`movie_*`, `audit_*`, `queue_*`, `tag_*`) so tools group by what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries MCP annotations; anything that changes Radarr says so in its description; anything that deletes is disabled unless the operator sets `--enable-delete`, and then changes nothing without `confirm`. What can cut the server off from Radarr - a restore, a restart, a shutdown, a change to its port, address or sign-in - waits for `confirm` too.
7. **Settings by the names Radarr gives them.** An indexer, a download client, a notification and the rest are added by kind, with their settings and switches named as Radarr's API names them, and `provider_kinds` says what each kind takes. A name that does not exist is refused with the ones that do. A credential is taken and never shown again.
8. **Behave as Radarr's own form does.** Where the API and the web interface differ, the tools follow the interface a person would have used: a new indexer starts switched on, though the API describes it switched off; and the tools say what Radarr really does, checked against its source - a test of everything tests only what is switched on, a command can only be cancelled while it waits. Radarr's own force lets a warning through on an add and not a failure, so a forced add is made switched off and then switched on, as prowlarr-mcp's is, and `force` means one thing everywhere: save it untested.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `movie_list`, `movie_get`, `movie_lookup`, `movie_credits`, `rootfolder_list`, `qualityprofile_list`, `collection_list`, `calendar_list` | "what do I have, and what shape is it in" |
| curation | `audit_all` + 18 audits, `movie_add`, `movie_edit`, `movie_batch_edit`, `movie_refresh`, `movie_rescan`, `movie_rename`, `moviefile_edit`, `import_identify`, `import_scan` → `import_apply`, `release_parse`, `collection_edit`, `naming_get`, `naming_edit`, `qualitydefinition_list`, `qualitydefinition_edit`, `customformat_list`, `queue_remove`, `history_mark_failed` | "what is wrong, and fix it" |
| acquire | `movie_download`, `release_search` → `release_grab`, `release_push`, `queue_list`, `queue_grab`, `history_list`, `blocklist_list`, `blocklist_remove`, `exclusion_list`, `exclusion_add`, `exclusion_remove` | "get it, and see where it is" |
| organise | `tag_*`, `rootfolder_add`, `rootfolder_delete`, `qualityprofile_add`, `qualityprofile_edit`, `qualityprofile_delete` | "group and hold these" |
| setup | `provider_kinds` and `provider_options`, then list, add, edit, test and delete for `indexer_*`, `downloadclient_*`, `importlist_*` (and `importlist_movies`), `notification_*` and `metadata_*`; list, add, edit and delete for `delayprofile_*` (and `delayprofile_reorder`), `releaseprofile_*`, `autotagging_*`, `remotepathmapping_*`, `customfilter_*` and `customformat_*`; `server_settings`, `server_settings_edit` | "set Radarr up, and change how it is set up" |
| admin | `server_health`, `server_disk_space`, `server_logs`, `server_log_file`, `server_updates`, `server_backups`, `server_backup_create`, `server_backup_restore`, `server_backup_delete`, `server_restart`, `server_shutdown`, `task_list`, `task_run`, `task_cancel`, `filesystem_browse`, `movie_delete`, `moviefile_delete` | "keep it running" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| a cross-check with the media server | this SDK plus embyfin-mcp's | films Radarr holds that Emby or Jellyfin does not list, and the other way round; wants both servers in one place, so likely a tool of its own |
| signing a list or a notification in | `/api/v3/{importlist,notification}/action/{name}` | Trakt, Plex and the like are set up by signing in through a browser, and the form's steps for it take what the browser comes back with as query parameters. Radarr's API document does not declare those, so the generated client cannot send them; `provider_options` runs the steps that only list choices. A tool would hand the person a link and take back what they return with |
| restoring a backup from a file | `/api/v3/system/backup/restore/upload` | a backup kept somewhere other than Radarr's own disk; needs a way to hand a tool a file |

## Shared with the sibling servers

sonarr-mcp, prowlarr-mcp, embyfin-mcp and abs-mcp are sibling projects. What they share lives in two modules, so a fix lands once: the client generator in [pandorest](https://github.com/katbyte/pandorest), and the record/replay proxy, the base client and the CLI's flag, toolset and HTTP plumbing in [go-kt](https://github.com/katbyte/go-kt).

## Guarded / deliberately excluded

- Every tool that deletes - a film, a file, an indexer, a download client, an import list, a notification, a profile, a custom format, a rule, a mapping, a saved filter, a backup - is only registered when `--enable-delete` (`RADARR_ENABLE_DELETE`) is set, and changes nothing without `confirm`, answering what a confirmed call would remove and what goes with it.
- `server_backup_restore`, `server_restart` and `server_shutdown` change nothing without `confirm`, and neither does a change through `server_settings_edit` to how Radarr is reached or who may sign in: each can take Radarr away from the session that asked.
- No tool calls, and `tools/coverage_test.go` says why for each: what Radarr's own web interface is made of (its pages, sign-in, translations), one record of a list a tool reads whole, and the bulk form of a call a tool makes one at a time. `sdk/radarr` covers all of it - it is a complete client.
