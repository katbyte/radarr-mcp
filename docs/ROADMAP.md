# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where an AI has a decision to
   make. Radarr's UI settings, notifications, authentication and updates stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw resources
   (`MovieResource` has 50 fields before its file; `movie_list` returns 12).
3. **Composite over chatty.** If a task always takes N calls (look up → add → wait for the
   refresh → read it back), it is one tool, not N. Tools that start a Radarr command wait
   for it to finish and say how it ended.
4. **Names, not just ids.** Every tool that takes a film, profile, root folder, tag or
   collection resolves a name, and an unknown one lists what exists.
5. **Resource-first names** (`movie_*`, `audit_*`, `queue_*`, `tag_*`) so tools group by
   what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries
   MCP annotations; anything that changes Radarr says so in its description; anything that
   removes films or their files is disabled unless the operator sets `--enable-delete`.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `movie_list`, `movie_get`, `movie_lookup`, `movie_credits`, `rootfolder_list`, `qualityprofile_list`, `collection_list`, `calendar_list` | "what do I have, and what shape is it in" |
| curation | `audit_all` + 18 audits, `movie_add`, `movie_edit`, `movie_batch_edit`, `movie_refresh`, `movie_rescan`, `movie_rename`, `moviefile_edit`, `import_identify`, `import_scan` → `import_apply`, `release_parse`, `collection_edit`, `naming_get`, `naming_edit`, `qualitydefinition_list`, `qualitydefinition_edit`, `customformat_list`, `queue_remove`, `history_mark_failed` | "what is wrong, and fix it" |
| acquire | `movie_download`, `release_search` → `release_grab`, `queue_list`, `history_list`, `blocklist_list`, `blocklist_remove`, `exclusion_list`, `exclusion_add`, `exclusion_remove` | "get it, and see where it is" |
| organise | `tag_*`, `rootfolder_add`, `rootfolder_delete`, `qualityprofile_edit` | "group and hold these" |
| admin | `server_health`, `server_disk_space`, `server_logs`, `server_log_file`, `server_backups`, `server_backup_create`, `task_list`, `task_run`, `indexer_list`, `indexer_test`, `downloadclient_list`, `downloadclient_test`, `movie_delete`, `moviefile_delete` | "keep it healthy" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| `importlist_list` / `importlist_test` | `/api/v3/importlist` | which lists keep adding films, and whether they still answer |
| `customformat_edit` and per-format scores on `qualityprofile_edit` | `/api/v3/customformat`, `/api/v3/qualityprofile/{id}` | tune what a profile prefers, not only the thresholds |
| a cross-check with the media server | this SDK plus embyfin-mcp's | films Radarr holds that Emby or Jellyfin does not list, and the other way round; wants both servers in one place, so likely a tool of its own |

## Before sonarr-mcp

sonarr-mcp should be a sibling project, as this one is to embyfin-mcp and abs-mcp. What the
three copy between them - pandorest, the record/replay proxy, the base client, the CLI's
flag, toolset and HTTP plumbing - belongs in go-kt first, so a fix lands once rather than
four times.

## Guarded / deliberately excluded

- `movie_delete` (removes a film, and with `delete_files` its folder) and `moviefile_delete`:
  only registered when `--enable-delete` (`RADARR_ENABLE_DELETE`) is set.
- Not wrapping **as tools**, ever: authentication and the API key, the UI settings,
  notifications, the updater, the system restart and shutdown, metadata consumers and
  remote path mappings. `lib/radarr` covers all of it - it is a complete client - but none
  of it is judgment an AI should be making.
