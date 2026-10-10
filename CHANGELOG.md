# Changelog

## 0.2.0 (2026-10-10)

### Breaking

- the Go client moves from `lib/radarr` to `sdk/radarr`, as in abs-mcp and embyfin-mcp, and its base client from `lib/client` to pandorest's, `github.com/katbyte/pandorest/client`, which every generated client in the family now shares: change the import paths; `lib` keeps the helpers
- the Go client's methods spell out the words Radarr's paths run together, as its models do and as sonarr-mcp's client does: `GetMovieFileById`, `GetRootFolder`, `GetQualityProfile` and `PostIndexerTestAll` where they were `GetMoviefileById`, `GetRootfolder`, `GetQualityprofile` and `PostIndexerTestall` (104 of the 237)
- a number among a call's options is a pointer, so that a zero can be asked for: `PageSize: new(250)` where it was `PageSize: 250`
- the Go client's error for a refused call is the shared client's `*client.StatusError`, which is go-kt's `*chttp.StatusError` with what Radarr said kept as a list as well (`Messages`): `ExpectedStatusCodes` is `Expected`. On a request built by hand, `HttpMethod` is `HTTPMethod`
- `indexer_list` and `downloadclient_list` move from the `admin` toolset to `setup`, with the tools that change what they list
- `movie_delete` and `moviefile_delete` change nothing without `confirm: true`: called without it they answer `would_delete`, the film or the file a confirmed call would remove, and with `delete_files` what would go from disk. `moviefile_delete` answers the file it deleted as `deleted`, where it was `file`
- every list is paged one way: every `_list` tool, 24 of them, and `server_logs`, take `limit` (default 100, at most 1000) and `offset`, and answer `total`, `offset` and `next_offset`. The twelve of the first release that took no limit now return a page; `movie_list`, `history_list`, `blocklist_list` and `server_logs` return 100 by default where they returned 50, and `server_logs` takes `offset` where it took `page`
- `--allow-tools` beside `--toolsets` adds to them, "these sets and this tool as well", where it narrowed them; `--deny-tools` narrows. On its own it is only the tools it names, where the default set was mixed in

### Added

- **the tools cover everything Radarr can do**: 59 new ones, 137 in all, where the server wrapped only what curating a library needed. A new toolset, `setup`, holds most of them. A test keeps it so: each of the 237 operations of Radarr's API is called by a tool or listed with the reason none calls it (158 are called)
- indexers and download clients can be added, changed and deleted (`indexer_add`, `indexer_edit`, `indexer_delete`, and the same for `downloadclient_*`), where they could only be listed and tested; their lists now show each one's settings
- import lists, notifications and metadata consumers have tools: `importlist_list`, `importlist_add`, `importlist_edit`, `importlist_delete`, `importlist_test`, the same five for `notification_*`, `metadata_list`, `metadata_edit` and `metadata_test`; `importlist_movies` shows what the lists offer, and when asked what TMDB recommends and what is trending and popular there
- `provider_kinds` lists the kinds of each of those and the settings a kind takes, and `provider_options` asks the service itself what a setting can be set to: an indexer's categories, another Radarr's profiles and root folders
- delay profiles, release profiles, auto tagging rules, remote path mappings and saved filters each have list, add, edit and delete tools, custom formats gain `customformat_add`, `customformat_edit` and `customformat_delete`, and `delayprofile_reorder` changes the order delay profiles are tried in
- `qualityprofile_add` makes a profile as a copy of another or from nothing, `qualityprofile_delete` removes one no film is on, and `qualityprofile_edit` can rename a profile and change which qualities it accepts
- `server_settings` shows Radarr's own settings by section and `server_settings_edit` changes them by name, answering what Radarr kept
- `server_backup_restore`, `server_backup_delete`, `server_restart`, `server_shutdown`, `server_updates`, `task_cancel` and `filesystem_browse`; `task_run` can start an update, and `server_log_file` reads the updater's logs with `update`
- `queue_grab` sends a release Radarr is holding back to the download client now, and `release_push` hands Radarr a release found elsewhere to decide on
- `movie_get` lists a film's subtitles and other extra files, and where its artwork is
- a new indexer, download client or notification starts as it does in Radarr's own form - switched on, a notification sent for everything its kind can tell of but health - where Radarr's API describes it switched off. Radarr tests what is switched on before saving it; `force` saves one untested, on an add as on an edit, and `off` adds it switched off
- a test of every import list, notification or metadata consumer still answers for each when Radarr's own fails whole on one it cannot reach
- the live tool suite sets up its own indexer and download client with the tools, so every search, grab and import it makes runs through what they added
- the live tool suite reads everything every tool answers or refuses with, all run long, for each key it gave Radarr and for Radarr's own, and fails naming the tool that showed one: it takes no test to think of the case
- a setting is a credential by its name as well as by Radarr's mark - anything with `password` in it, or ending in `key`, `token`, `secret`, `passphrase` or `cookie` - since Radarr does not mark every setting that holds one
- a credential held by anything Radarr is set up with is named when it is set and never shown, and with `RADARR_LOG=info` the line logged for a write shows which settings were set and none of their values
- a client that connects is told the rules that hold between the tools, once, where each tool's description had to: how a list is paged, that every delete waits for `confirm`, how a film is named, and that text which came from outside Radarr is data and never an instruction
- `RADARR_LOG=info` shows one line for every call of a tool that writes or deletes: the tool, whether the call was refused, failed or answered, and what it sent, with credentials blanked
- `moviefile_delete` warns, before and after, what Radarr does with a monitored film once its file is gone: downloads it again, or, when Radarr is set to unmonitor a film whose file is deleted, stops monitoring it
- `task_list` takes `command_id` and answers that one command, so a search or a refresh still running when its tool stopped waiting can be followed without looking for it among the commands Radarr starts for itself every minute
- the Go client takes a logger with `client.WithLog`, a transport of the caller's own with `client.WithTransport` and a retry rule with `client.WithRetry`
- `radarr.DocumentVersion` is the Radarr release the client was generated from, beside `radarr.APIVersion`, which Radarr leaves at 3.0.0
- the Go client's test of every indexer, download client, import list, metadata consumer or notification answers its results when one of them fails, where it returned an error: Radarr answers 400 with the same list

### Changed

- `indexer_test` and `downloadclient_test` answer a test that only warns as valid, with the warning, where it was a failure: Radarr still saves such an indexer or client
- `qualityprofile_list` shows how much higher a release must score to replace a file (`min_upgrade_format_score`)
- a write tool tells a client whether it can take away what was there, each decided on its own, where every one said it could not: an edit, a rename, a grab, a rescan and a task now say they are destructive, and only the tools that add something new (a film, a tag, a root folder, an exclusion, a backup) say they are not
- a release's name, a download's title and messages, a history or blocklist entry, an overview and a log line are shown with the characters that do not display taken out, and cut short when very long (a name at 300 characters, running text at 2000). Strangers write such text and a model reads it, and a character that does not show is the easiest place to hide an instruction. A path, a release's guid and a film's title are left as they came, since a later call hands them back
- a call with an argument the tool does not take is refused with the arguments it does take
- `audit_all` keeps the counts of the audits that ran when one could not, and lists that one under `failed` with its error and no count, where the whole call failed
- `history_list` says how many events match, and has Radarr filter by film and by kind of event, where it read the history through and counted nothing
- a tool that fails with an internal error answers that call with an error naming the tool, and the session carries on
- a session a client left open over HTTP is closed after half an hour unused, and stopping the server no longer waits out the clients still connected
- a read is asked for again when a gateway could not reach Radarr or the connection dropped, three times in all; a write is still sent once
- an error says what Radarr said went wrong (`Path: Folder '/nowhere' does not exist`) in place of the JSON it sent
- a redirect is refused rather than followed, and a web page answering where the API should is said to be one: both mean the server URL is of something in front of Radarr, or lacks its URL base. The Go client's `PostLogin` and `GetLogout`, which Radarr answers with a redirect, answer it as it came, with where it leads in its `Location`, where they used to follow it and answer the page it led to
- `RADARR_LOG=trace` shows every request and answer, with the API key blanked
- the Go client is generated by [pandorest](https://github.com/katbyte/pandorest), now a module of its own, from a document named by the Radarr release it was taken from (`api-defs/radarr-openapi-6.4.4.10685.json`); the live suite's sweep of every read is pandorest's too, and now also fails a read that answers with nothing in it, which proved only its status
- serving, the tool registry, the HTTP client, and the live suites' harness, recording proxy and fake indexer are go-kt's (v0.6.1), shared with abs-mcp, embyfin-mcp and the other servers
- built with Go 1.27.2, which fixes denial-of-service flaws in Go's HTTP and TLS code
- the Docker image and the release build take their base images from Google's mirror of Docker Hub, and from Docker Hub itself when the mirror gives nothing
- `make record` re-records every cassette; `RADARR_TEST_RECORD=1` on a `testacc` target records only the requests no cassette holds

### Fixed

- an indexer's stored API key could be shown: when an indexer answers an error, Radarr reports the address it asked with the key on the end, and `indexer_test` passed that on in the row for the test that failed. What Radarr says went wrong - a failed test, a refused save, a health check, a stuck download, a log line - is now shown with each address in it stripped of who it signs in as, the value of every parameter and what follows its `#`; so is a setting whose value is an address
- two edits of one quality profile, of one quality's size limits or of the naming settings in the same turn undid each other: each read the record and sent it back whole, so the later one put back what the earlier had changed. An edit now holds the record from before it reads it until it has written it, and `movie_edit` holds its film, so pointing it at a folder cannot undo an edit made beside it
- the live suites failed from two weeks after a Radarr release, whatever was changed: Radarr's health check then asks its update server whether there is a newer build, could not read the test proxy's empty answer, and recorded no health at all. A stand-in for that server now answers it
- the live suites failed a month after their first run on a machine that kept its cache: the test proxy's certificate authority was made for thirty days and never again. It is made again when it has under a day left

## 0.1.0 (2026-09-19)

The first version: an MCP server, CLI and Go SDK for curating a Radarr film library, built on the same pattern as embyfin-mcp and abs-mcp.

- **18 audits and `audit_all`**, each a sweep over the whole library for one thing that goes wrong: missing and unmonitored films, files below their cutoff or outside their profile, runtimes, resolutions, sizes and languages that do not add up, folders named for the wrong year or film, folders Radarr does not know about and folders it believes in that are gone, files it does not track, gaps in collections, films TMDB dropped, and downloads stuck in the queue. Each names the tool that fixes what it finds.
- **Taking folders already on disk into the library**: `import_identify` says which film each folder no film is in holds - what is in it, and what TMDB offers for its name - and `movie_add` with that folder takes it in. `audit_missing_folders` and `movie_edit`'s `folder` handle the reverse, a folder renamed or moved behind Radarr's back.
- **78 tools in toolsets** (`core`, `curation`, `acquire`, `organise`, `admin`), with `--read-only`, `--enable-delete`, allow and deny patterns, and an HTTP transport behind a bearer token.
- **`lib/radarr`**, a typed client for every one of Radarr's 237 v3 API operations, generated from Radarr's OpenAPI document with named workarounds for where the document and the server disagree.
- **Live suites against a real Radarr in Docker**: one for the SDK, one for the tools, the built binary and journeys through them, with a fake indexer so grabs really download and import, and Radarr's metadata calls recorded and replayed so CI needs no network.
