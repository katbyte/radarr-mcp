# Changelog

## 0.5.2 (2026-10-20)

### Fixed

- a bare `make` formats and builds, where it built a lint tool and stopped: the lint tools' rules come first in the makefile, so `make` with no target ran one of them

## 0.5.1 (2026-10-18)

### Changed

- the live tests are linted like the rest of the code, where the linter never saw them: they read a call's answer by value, so a missing one fails the test rather than crashing it, and read a yes or no from a tool's answer the way the other servers' tests do
- the generated Go client and its tests are linted too, where the linter skipped them as generated code: moved to pandorest v0.13.1, whose client passes the same checks as the rest of the code, and a request body may be up to 1,024 bytes before the linter asks for it to be passed by pointer, so a film can still be written in place

## 0.5.0 (2026-10-18)

### Added

- `audit_dynamic_range` finds files named HDR or Dolby Vision whose video is not, and Dolby Vision files with no HDR10 layer under them, which show in the wrong colours on a TV without Dolby Vision
- `audit_subtitles` finds films whose audio and subtitles are both in languages the viewer does not follow, by default the language Radarr's interface is in
- `audit_disk_space` finds root folders running out of room, or that Radarr cannot reach. All three run in `audit_all`
- `rootfolder_list` can name the folders no film is in, with `unmapped`, so the default tools can say which they are as well as how many

### Breaking

- the Go client no longer has methods for Radarr's web interface: its pages (`Get`, `GetByPath`, `GetContentByPath`) and its sign-in (`GetLogin`, `PostLogin`, `GetLogout`). They answer a browser, not a program, which signs in with its API key; Sonarr's and Prowlarr's clients already leave them out

### Fixed

- `audit_unmapped_folders` and `audit_missing_folders` match a folder to a film by any of its titles and by a year either side, where they took only its main title and exact year: a folder named for a film's title in another country read as not in the library
- `audit_unmapped_folders` called a folder "a second copy" of a film the library had with no file; it now says the folder is probably where that film is, and how to point the film at it
- `audit_missing_files` said "available (announced)", which read as a contradiction; it now says from when Radarr wants the film, such as "since it was announced"
- two tools reading the whole library at once read it twice, and on a large library that was enough to make Radarr stop answering. Calls at the same moment now share one read
- adding a film could fail when Radarr was fetching the same film's collection at that moment. `movie_add` now tries again, as Radarr's refusal is gone a moment later

### Changed

- moved to `golang.org/x/text` v0.41.0, which fixes a crash on some text (CVE-2026-56851) that the binary scan reported against v0.39.0
- moved to go-kt v0.9.0 and pandorest v0.11.0. Five small helpers that were radarr's own (limits, paging, waits, "a" or "an", and the first sentence of a description) are now the shared ones
- `radarr-mcp tools` cuts a description at its first real sentence: a full stop in an abbreviation such as "e.g." no longer ends it

## 0.4.0 (2026-10-14)

### Breaking

- the Go client's dates are real dates: every date is a `radarr.Time`, a Go time, where it was text
- the ids of quality profiles, tags, indexers, download clients and root folders are each a type of their own in the Go client (`QualityProfileID`, `TagID`, `IndexerID`, `DownloadClientID`, `RootFolderID`), like a film's already were
- a Go client call that takes one option takes it directly: `c.GetMovieLookup(ctx, "alien")`, not `c.GetMovieLookup(ctx, radarr.GetMovieLookupOptions{Term: "alien"})`

### Added

- `radarr.Operations` in the Go client lists every operation with its method and path, and whether it changes anything

### Fixed

- every audit that reads the whole library failed on a large one: Radarr answers all its films in one piece, and past about ten thousand films that was more than the client would hold. Fourteen of the sixteen audits `audit_all` runs answered nothing on a library of 29,000. The limit for Radarr is now 1 GiB
- `audit_language` reported a file whose audio is tagged as of no known language (`und`) as lacking the film's language. It is now left alone, like a file with no tag at all: on that library, 735 findings where there were 5,942
- `audit_untracked_files` reported a film's theme video, trailers and other extras as stray copies of the film. A video in a folder of extras (`backdrops`, `trailers`, `featurettes` and the like) is now left out and counted in `note`
- `movie_lookup` read the whole library on every call to say which results it already holds: a hundred megabytes a lookup on a library of 29,000 films, and a run of lookups slowed Radarr until it stopped answering. It now asks about each result alone
- `audit_resolution_mismatch` said the grade twice when the grade was the thing wrong: "graded WEBDL-1080p, but the video is 3840x2160 (2160p); Radarr graded it WEBDL-1080p"

## 0.3.0 (2026-10-12)

### Breaking

- the Go client's names are shorter: a method answers a `<Name>Response` (was `<Name>OperationResponse`), takes a `<Name>Options` (was `<Name>OperationOptions`), and what Radarr answered is `Answer` (was `Model`). Options can be left out when none is required: `c.GetMovie(ctx)`
- a film's id, a film file's id and a TMDB id are each a type of their own in the Go client (`MovieID`, `MovieFileID`, `TmdbID`), so one cannot be passed where another is wanted. Every other id is still a plain number
- a yes-or-no that Radarr only ever answers, such as a health check or a test's result, is a plain `bool` in the Go client where it was a pointer

### Added

- the Go client has a method for each of sixteen Radarr commands, such as `RescanMovie`, `MoviesSearch` and `Backup`, and an `AndWait` form of each that waits for Radarr to finish. A wait says whether the command failed or is still running
- a paged list in the Go client can be read a page at a time with `Each`
- one live test takes a Radarr from empty to set up and in use, and back to empty, with the tools alone, then checks nothing was left behind

### Changed

- stripping sign-ins and keys from addresses, the line logged for a write, and the live tests' watch for a shown key are shared with the other servers (go-kt v0.6.4), where each was radarr's own
- the Go client is generated by pandorest v0.8.2
- every test that runs against a real Radarr is under `acceptance/`: the client's in `acceptance/radarr/` (was `integration/`), the tools' in `acceptance/tools/`. They run with `make testacc-sdk` and `make testacc-tools`
- the helper scripts are in `.scripts/` (was `scripts/`)
- spelling and markdown are checked, with `make typos` and `make markdownlint`

### Fixed

- a webhook notification's headers, which is where its token goes, and the addresses an Apprise notification sends to, which carry a token each, were shown by `notification_list` and after an add or an edit. Both are now credentials: named when set, never shown. 0.2.0 is affected
- a trace of what is sent to Radarr (`RADARR_LOG=TRACE`) showed the key or password of an indexer, download client, import list or notification being added or changed, and the confirmation of a new sign-in password. It now hides them. 0.2.0 is affected, for whoever ran it at that log level
- an empty answer from Radarr is reported as an error where it used to stop the tool: looking a film up by `tmdb:` or `imdb:` id, and reading the naming settings or a quality profile before changing one

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
