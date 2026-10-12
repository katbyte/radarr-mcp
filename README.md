# radarr-mcp - a Radarr MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/radarr-mcp?color=blueviolet)](https://github.com/katbyte/radarr-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/radarr-mcp?color=00ADD8)](https://github.com/katbyte/radarr-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/radarr-mcp?color=blue)](https://github.com/katbyte/radarr-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/radarr-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/radarr-mcp/actions/workflows/pr-acceptance.yaml/badge.svg)
![lint](https://github.com/katbyte/radarr-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/radarr-mcp/badges/coverage.json)](https://github.com/katbyte/radarr-mcp/actions/workflows/coverage.yaml)

An [MCP](https://modelcontextprotocol.io) server, CLI and Go SDK that **audit a [Radarr](https://radarr.video) film library for the things that actually go wrong, and fix what they find** - from Claude Code, Claude Desktop, or any other MCP client.

Radarr already exposes a large API, and an MCP server that wraps it lets a model list films and start downloads. This one does that too, but the reason it exists is the layer above: **18 audits**, each a sweep over the whole library for one thing that goes wrong in a real collection - a folder matched to the wrong film, a folder renamed behind Radarr's back, a 720p file named 2160p, a download cut off halfway, a 4K file on a 1080p profile, a dubbed-only copy of a Japanese film, a second copy nobody told Radarr about, a trilogy with a hole in it, a download stuck in the queue - returning a worklist rather than a dump, and naming the tool that fixes it.

### The audits

| audit | what it catches |
|---|---|
| `audit_all` | every audit in one call, counts only, so one call says where the library needs work - start here |
| `audit_missing_files` | monitored films that are available but have no file: what Radarr is still looking for, each with whether a download is queued and when Radarr last searched |
| `audit_unmonitored` | unmonitored films with no file: Radarr will never download them; `movie_edit` monitors them |
| `audit_cutoff_unmet` | files below their profile's cutoff, by quality or custom format score, and whether the profile upgrades at all - Radarr's own profiles are made with upgrades off, so without `qualityprofile_edit` it never replaces them |
| `audit_profile_mismatch` | files whose quality their profile does not allow: a 4K file on a 1080p profile, a DVD rip on an HD one; `movie_edit` moves the film to a profile that fits, `moviefile_edit` corrects a wrong grade |
| `audit_runtime` | files whose runtime disagrees with the film's: a truncated download, a sample, a TV cut, or another film under this one's name; `history_mark_failed` rejects the release so Radarr looks for another |
| `audit_resolution_mismatch` | files whose name claims a resolution the video does not have - a 720p file named 2160p, an upscale sold as 4K - or whose grade disagrees with the video |
| `audit_quality` | files worth replacing: below a resolution (720 lines by default), in a legacy codec (XviD, DivX, MPEG-2, VC-1, WMV), or below a bitrate when one is given, lowest resolution first |
| `audit_size` | files whose size per minute of runtime is outside the limits Radarr sets for their quality: a tiny fake or a sample graded as a full film, or a bloated mislabelled remux. Limits set after a file was imported never touched it; this finds what they would have refused |
| `audit_language` | files with no audio in a language they should have: by default the film's original language, so a dubbed-only copy of a Japanese or French film; a track with no language tag is never taken as lacking one |
| `audit_year_mismatch` | films whose folder names a year two or more away from the film's: the folder says `(2021)`, the match says 1984 - the wrong edition, or the wrong film. A right match in a misnamed folder is fixed by `movie_edit` into the root folder it is already in, which renames the folder by the naming scheme |
| `audit_naming` | files and folders not named the way Radarr's naming scheme would: a scene-named file, a folder named for the wrong film; `movie_rename` renames files, `movie_edit` into a film's own root folder renames its folder |
| `audit_unmapped_folders` | folders in the root folders that no film is in: films on disk Radarr has never heard of, and second copies of films it keeps elsewhere, each named with the copy it duplicates. `import_identify` says which film each one holds, and `movie_add` with the folder takes it in |
| `audit_missing_folders` | the reverse: films whose folder is not on disk, renamed or moved behind Radarr's back, each named with the folder in the same root folder that reads as the film; `movie_edit` with that folder points the film at it |
| `audit_untracked_files` | video files in the films' folders that Radarr does not track - a second copy, a failed upgrade, a file dropped in by hand - each with why Radarr would not take it; `import_apply` imports one |
| `audit_missing_metadata` | films whose TMDB metadata came back without an overview, an IMDb id, a poster, genres, a runtime or a year; `movie_refresh` fetches it again |
| `audit_removed` | films TMDB no longer lists: Radarr can no longer refresh them, and they usually mean a duplicate TMDB entry was merged away |
| `audit_collection_gaps` | TMDB collections the library holds some but not all of, each with the released films it is missing, leaving out ones on the exclusions; `movie_add` fills a gap, `collection_edit` has Radarr fill them all |
| `audit_queue` | downloads that will not finish on their own: imports Radarr blocked (no video file, not an upgrade, the wrong film) and failed downloads, with the reasons it gives; `import_apply` imports one by hand, `queue_remove` clears it |

The design principle: **detection is code, correction is judgment.** The server runs cheap deterministic checks over the whole library and produces worklists; the AI reasons only about the anomalies. Most audits are one request for the whole library, because Radarr answers every film with its file and what its probe found inside it. Every response is a trimmed projection of what a decision needs, never the raw API object (`MovieResource` has 50 fields before its file and what is inside it; `movie_list` returns 12).

### What else is in the box

- **137 tools, in toolsets: everything Radarr can do.** Find, read and add films, edit them one at a time or in bulk, correct a file's grade, import files by hand, search the indexers and grab a release yourself, watch the queue and the history, blocklist and exclude, collections, tags, root folders, quality profiles and size limits, naming. And Radarr's own setup: indexers, download clients, import lists, notifications, metadata, custom formats, delay and release profiles, auto tagging, its settings, backups and restores, updates, restarts and tasks. A test holds the tools to it: every one of the 237 operations of Radarr's API is called by a tool, or listed with the reason none calls it. Each tool sits in a toolset a session can load on its own, so a client spends about 950 tokens of context by default rather than 26,500.
- **A Go SDK.** `sdk/radarr` is a complete typed client for Radarr's v3 API - all 237 operations, generated from Radarr's own OpenAPI document, standard library only, no knowledge of MCP. Useful on its own, whether or not you care about AI.
- **Tested against a real Radarr.** Every tool runs against a real Radarr in Docker, with a fake indexer and a download client, both set up by the tools themselves, so grabs really download and import. One journey builds a whole setup from nothing with the tools alone, uses it, lets the audits find what is wrong and fixes it, then takes it all down and checks Radarr against a recording taken before. The suite fails if a registered tool has no test or if any answer shows a key it gave Radarr, and Radarr's calls out to its metadata service are recorded once and replayed, so CI needs no network.

## Installation

```bash
go install github.com/katbyte/radarr-mcp@latest
```

Tested against Radarr 6.4; the live suites run the `lscr.io/linuxserver/radarr` image of that version.

## Configuration

All options can be passed as command-line flags, environment variables, or via a configuration file.

| Variable | Flag | Description |
|---|---|---|
| `RADARR_SERVER` | `--server`, `-s` | Radarr's URL, e.g. `http://nas:7878`, with its URL base if it has one |
| `RADARR_TOKEN` | `--token`, `-t` | API key (Radarr: Settings → General → Security → API Key) |
| `RADARR_READ_ONLY` | `--read-only` | register only tools that never change server state |
| `RADARR_ENABLE_DELETE` | `--enable-delete` | register `movie_delete` and `moviefile_delete`, the two tools that can remove films and their files |
| `RADARR_TOOLSETS` | `--toolsets` | groups of tools to register, default `core`: `all`, `core`, `curation`, `acquire`, `organise`, `setup`, `admin`, or a resource family like `movie` (`core` is always included) |
| `RADARR_ALLOW_TOOLS` | `--allow-tools` | register these tools as well as the toolsets asked for, or only these when no toolset is (names, `movie_*` globs, or `essential`) |
| `RADARR_DENY_TOOLS` | `--deny-tools` | never register these tools, whatever asked for them (names or globs such as `*_delete`) |
| `RADARR_LOG` | | log level (`WARN` default; `INFO` adds a line for every call of a tool that writes or deletes; `DEBUG`, `TRACE`, ...) |
| `RADARR_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `RADARR_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint (required with `--listen`) |
| `RADARR_ALLOW_NO_AUTH` | `--allow-no-auth` | serve HTTP with no bearer token at all: anyone who can reach the port can use every tool |

Paths are always as Radarr sees them. When Radarr runs in a container, a root folder is the path inside that container (`/movies`), not the one on the host.

### Configuration File

You can place a `.radarr-mcp` file in your home directory `~/.radarr-mcp` (for global settings) or in your current directory `./.radarr-mcp` (for per-project settings). Keys match the long flag names using the `env` format:

```env
SERVER=http://nas:7878
TOKEN=0123456789abcdef0123456789abcdef
```

## Usage

Quick connectivity check:

```bash
radarr-mcp info
```

### Register with Claude Code

`.mcp.json`:

```json
{
  "mcpServers": {
    "radarr": {
      "command": "radarr-mcp",
      "args": ["serve"],
      "env": {
        "RADARR_SERVER": "http://nas:7878",
        "RADARR_TOKEN": "...",
        "RADARR_TOOLSETS": "curation"
      }
    }
  }
}
```

Or from the shell:

```bash
claude mcp add radarr -e RADARR_SERVER=http://nas:7878 -e RADARR_TOKEN=... -- radarr-mcp serve
```

### Run as a service (HTTP transport)

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`) instead of stdio. `RADARR_AUTH_TOKEN` is required: clients must send `Authorization: Bearer
<token>`, and the server refuses to start without one unless `RADARR_ALLOW_NO_AUTH=true` says
that anyone who can reach the port may use every tool. Register it from any machine:

```bash
claude mcp add --transport http radarr http://nas:8080/mcp \
  --header "Authorization: Bearer $RADARR_AUTH_TOKEN"
```

### Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/radarr-mcp`, tagged `vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust `RADARR_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in RADARR_TOKEN and RADARR_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `radarr-mcp`, with version info from git. The image is alpine-based (so `docker exec -it radarr-mcp sh` works), runs as a non-root user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm ghcr.io/katbyte/radarr-mcp info` works as a connectivity check with the `RADARR_*` variables passed via `-e`.

## MCP Tools

Tools are named resource-first (`movie_*`, `audit_*`, `queue_*`...) so they group by what they act on. Every tool carries MCP annotations (read-only or destructive) and tools that change server state say so in their descriptions. Wherever a tool takes a film it accepts its Radarr id, `tmdb:<id>`, `imdb:<tt id>` or its title (`"Alien (1979)"` when two share one), and a profile, root folder, tag or collection by name as well as id; an unknown name comes back as an error listing what exists, and a title that matches two films as one naming both.

| Resource | Tools |
|---|---|
| server | `server_info`, `server_health`, `server_disk_space`, `server_logs`, `server_log_file` (Radarr's own logs, or with `update` its updater's), `server_updates` (the releases there are to update to, and whether this install is behind), `server_backups`, `server_backup_create`, `server_backup_restore`, `server_backup_delete`, `server_restart`, `server_shutdown`, `filesystem_browse` (the folders Radarr can reach) |
| settings | `server_settings` (Radarr's own settings by section: host, ui, mediamanagement, and what holds for every indexer, download client, import list and metadata consumer), `server_settings_edit` |
| tasks | `task_list`, `task_run` (an RSS sync, a check for finished downloads, a refresh or a search of everything, an update, waiting for it to finish), `task_cancel` (a command still waiting its turn) |
| films | `movie_list` (filters: title, monitored, has a file, status, profile, root folder, tag, genre, year; sorted and paged), `movie_get` (metadata, the file and what is inside it, its downloads and recent history), `movie_lookup` (TMDB, to find one to add), `movie_credits`, `movie_add`, `movie_edit` (including `folder`, which points a film at a folder already on disk), `movie_batch_edit` (the same change to many films; tags replaced, added or removed), `movie_refresh`, `movie_rescan`, `movie_rename` (previews unless told otherwise), `movie_download` (Radarr's automatic search), `movie_delete` |
| files | `moviefile_edit` (the quality, languages, release group or edition Radarr believes a file has), `moviefile_delete` |
| import | `import_identify` (which film each folder no film is in holds: what is in it, and what TMDB offers for its name) → `movie_add`; `import_scan` (what Radarr makes of a folder's files, and why it would refuse each) → `import_apply` |
| audits | `audit_all` and the 18 audits in [the table above](#the-audits) |
| releases | `release_search` (every release the indexers offer, with why Radarr would reject each) → `release_grab`, `release_push` (a release found elsewhere, for Radarr to decide on), `release_parse` (a name read the way Radarr reads it) |
| downloads | `queue_list`, `queue_grab` (a release Radarr is holding back, sent now), `queue_remove` (optionally blocklisting the release), `history_list`, `history_mark_failed`, `blocklist_list`, `blocklist_remove`, `calendar_list` |
| collections | `collection_list`, `collection_edit` (monitor one, so Radarr adds the films it is missing) |
| exclusions | `exclusion_list`, `exclusion_add`, `exclusion_remove` |
| tags | `tag_list` (with what carries each), `tag_create`, `tag_rename`, `tag_delete` (taking it off its films first) |
| root folders | `rootfolder_list`, `rootfolder_add`, `rootfolder_delete` |
| quality | `qualityprofile_list`, `qualityprofile_add` (a copy of another, or from nothing), `qualityprofile_edit` (its name, the qualities it accepts, upgrades, cutoff, format scores), `qualityprofile_delete`, `qualitydefinition_list`, `qualitydefinition_edit` (size limits), `customformat_list`, `customformat_add`, `customformat_edit`, `customformat_delete` |
| naming | `naming_get` (with an example of each pattern), `naming_edit` |
| indexers and clients | `indexer_list`, `indexer_test`, `indexer_add`, `indexer_edit`, `indexer_delete`, and the same five for `downloadclient_*` |
| import lists | `importlist_list`, `importlist_test`, `importlist_add`, `importlist_edit`, `importlist_delete`, `importlist_movies` (what the lists offer, and with it what TMDB recommends, and what is trending and popular there) |
| notifications and metadata | `notification_list`, `notification_test`, `notification_add`, `notification_edit`, `notification_delete`; `metadata_list`, `metadata_test`, `metadata_edit` (the files written beside a film for Kodi, Emby and the like) |
| what there is to set up | `provider_kinds` (the kinds of each, and the settings a kind takes), `provider_options` (what a setting can be set to when the service itself decides: an indexer's categories, another Radarr's profiles) |
| rules for grabbing | `delayprofile_list`, `delayprofile_add`, `delayprofile_edit`, `delayprofile_reorder`, `delayprofile_delete`; list, add, edit and delete for `releaseprofile_*` (words a release must or must not have) |
| rules and records | list, add, edit and delete for `autotagging_*` (tags given to films that meet conditions), `remotepathmapping_*` (where a download client on another machine puts its downloads) and `customfilter_*` (filters saved in the web interface) |

Every list is paged the same way: it takes `limit` (100 unless given, 1000 at most) and `offset`, and answers `total`, `offset` and, while there is another page, the `next_offset` to pass back. `server_logs` is paged that way too.

Text that came from outside Radarr - a release's name, a download's title and messages, a history or blocklist entry, an overview, a log line - is shown with the characters that do not display taken out, and cut short when very long: strangers write it and a model reads it. A path, a release's guid and a film's title are left as they came, because a later call hands them back.

The 14 tools that delete - `movie_delete` (a film, and with `delete_files` its folder), `moviefile_delete`, and the `_delete` of an indexer, download client, import list, notification, quality profile, custom format, delay profile, release profile, auto tagging rule, remote path mapping, saved filter and backup - are only registered when `--enable-delete` / `RADARR_ENABLE_DELETE` is set, and even then change nothing without `confirm`: called without it, each answers `would_delete`, what a confirmed call would remove, and what goes with it. `server_backup_restore`, `server_restart` and `server_shutdown` wait for `confirm` the same way, and so does a change through `server_settings_edit` to how Radarr is reached or who may sign in, since each can take Radarr away from the session that asked. `--read-only` registers the 67 read tools and nothing else, so a write tool is absent from `tools/list` rather than refused when called.

### Setting Radarr up

An indexer, a download client, an import list, a notification and a metadata consumer are each one of the kinds Radarr knows - Newznab, qBittorrent, a Trakt list, Discord, Kodi - and are handled one way. `provider_kinds` lists the kinds and, for one kind, every setting it takes: its name, what it holds, what a select accepts, and whether it is a credential. The `_add` and `_edit` tools take those settings by name under `settings`, the thing's own switches (`enableRss`, `priority`, `onGrab`...) under `options`, and tags by label. A name that does not exist is refused with the ones that do, so a typo cannot silently save nothing.

- **A credential is taken and never shown again.** An API key, a password, a token, a webhook's own headers or the addresses Apprise sends to is listed by name under `credentials` when it is set, and an edit that does not name it leaves it as it is. Radarr itself quotes an address back with a stored key on the end when an indexer or a download client answers an error, so wherever a tool shows what Radarr said went wrong, and wherever a setting is an address, the address is shown without who it signs in as, the values after its `?` and what follows its `#`. The server's own log keeps to the same: the line written for a write blanks every credential, and a trace of what is sent to Radarr (`RADARR_LOG=TRACE`) hides them too, along with the value of every setting of an indexer, a download client, an import list or a notification, since Radarr's API carries a key there under no name of its own.
- **A new one starts as it does in Radarr's own form**: an indexer and a download client switched on, a notification sent for everything its kind can tell of except health. Radarr's API describes them switched off.
- **Radarr tests what is switched on before it saves it**, and refuses one whose test fails. `force` saves it untested, on an add as on an edit, for something that cannot be reached yet; `off` adds it switched off. Radarr's own force does less on an add - it lets a warning through and not a failure - so a forced add is made switched off, which Radarr does not test, and then switched on.
- **A test of everything tests what is switched on.** Naming one tests it whether it is on or not.

The plainer records - a delay profile, a release profile, an auto tagging rule, a remote path mapping, a saved filter, a custom format - take their values by the names Radarr's API gives them under `values`, and a custom format and an auto tagging rule their conditions by kind under `conditions`. Radarr's own settings are read by section with `server_settings` and changed by name with `server_settings_edit`, which reads back what Radarr kept.

With `RADARR_LOG=info`, the line logged for a write shows which settings were set and none of their values.

### Choosing which tools load

**The default is `core`: six read-only tools, about 950 tokens.** The whole surface is around 26,500 tokens of tool definitions before anyone asks a question, which is a poor way to spend a client's context by default. `--toolsets` / `RADARR_TOOLSETS` loads the groups a session actually needs, and `core` comes along with whatever else is asked for, because nothing else can find a film.

**Curating a library needs `RADARR_TOOLSETS=curation`** - the audits and everything that fixes what they find. **Setting Radarr up needs `RADARR_TOOLSETS=setup`** - its indexers, download clients, lists, notifications, rules and settings: more than a third of all the tools, and worth loading only for that job. `RADARR_TOOLSETS=all` restores every tool.

| toolset | tools | with core | ~tokens |
|---|---|---|---|
| `core` *(default)* | 6 | 6 | 950 |
| `organise` | 9 | 15 | 2,550 |
| `acquire` | 13 | 19 | 3,000 |
| `admin` | 17 | 23 | 3,500 |
| `curation` | 40 | 46 | 8,450 |
| `setup` | 52 | 58 | 12,800 |
| `all` | 137 | 137 | 26,500 |

Tokens are what the model sees: each tool's name, description and input schema, measured over a real `tools/list` at four bytes a token, with `--enable-delete` on (the 14 delete tools are among them). Every tool also carries an output schema, another 34,000 tokens across `all`, but clients keep that to themselves to validate results rather than sending it to the model.

`--toolsets` also takes a resource family - `movie`, `moviefile`, `audit`, `import`, `release`, `queue`, `history`, `blocklist`, `calendar`, `collection`, `exclusion`, `tag`, `rootfolder`, `qualityprofile`, `qualitydefinition`, `customformat`, `naming`, `indexer`, `downloadclient`, `importlist`, `notification`, `metadata`, `provider`, `delayprofile`, `releaseprofile`, `autotagging`, `remotepathmapping`, `customfilter`, `filesystem`, `server`, `task` - which is every tool with that prefix:

```sh
RADARR_TOOLSETS=all                 # every tool
RADARR_TOOLSETS=curation            # audits plus everything that fixes what they find
RADARR_TOOLSETS=acquire             # searching, grabbing and following downloads
RADARR_TOOLSETS=setup               # indexers, download clients, lists, notifications, rules and settings
RADARR_TOOLSETS=audit               # read-only detection, nothing that writes
RADARR_TOOLSETS=core,movie,queue    # core plus two whole families
```

`radarr-mcp tools` prints what the current flags would register, grouped by toolset, and needs no server:

```sh
radarr-mcp tools                    # the default set
radarr-mcp tools --toolsets all     # every tool
radarr-mcp tools --read-only -q     # names only
```

### One tool more, or a few less

`--allow-tools` asks for tools by name, as `--toolsets` asks for them by set, and a session gets what either names: "these sets, and this tool as well". On its own, with no toolset beside it, it is only the tools it names. `--deny-tools` takes tools out of whatever was asked for, which is how a toolset is narrowed. Both take comma-separated tool names, globs with a leading or trailing `*`, and `--allow-tools` the `essential` preset (`movie_list`, `movie_get`, `movie_lookup`, `movie_add`, `queue_list`). Neither gets a tool past `--read-only` or the delete gate.

```sh
RADARR_TOOLSETS=curation RADARR_ALLOW_TOOLS=queue_list    # the curation set, and the queue as well
RADARR_ALLOW_TOOLS=essential                              # the five essential tools and nothing else
RADARR_ALLOW_TOOLS=movie_*,queue_list                     # only these
RADARR_TOOLSETS=curation RADARR_DENY_TOOLS=audit_*        # the curation set without its audits
RADARR_DENY_TOOLS=*_delete,release_grab
```

Before 0.2 an allow list beside toolsets narrowed them, so a tool the sets did not hold was silently left out.

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a tool.

### A typical curation session

1. `audit_all` says where the library needs work.
2. `audit_unmapped_folders` lists the films on disk Radarr has never heard of; `import_identify` says which film each folder holds and what is in it, `movie_add` with the folder takes each in, and the second copies it finds are for `import_scan`. `audit_missing_folders` is the reverse - folders renamed or moved behind Radarr's back - and `movie_edit` with the folder points each film at where it is now.
3. `audit_year_mismatch` finds folders matched to the wrong film; `movie_get` shows the match. A wrong match is replaced by adding the right film for the folder; a right one in a misnamed folder is renamed by `movie_edit` into the root folder it is already in.
4. `audit_runtime`, `audit_resolution_mismatch` and `audit_size` find files that are not what they claim; `moviefile_edit` corrects a wrong grade, and `history_mark_failed` rejects a bad release so Radarr looks for another.
5. `audit_quality` and `audit_cutoff_unmet` list what is worth upgrading; `qualityprofile_edit upgrade_allowed=true` lets Radarr do it, and `movie_download` searches now rather than waiting for the next RSS sync.
6. `audit_naming` and `movie_rename` (a preview, then `preview=false`) tidy the names.
7. `audit_collection_gaps` lists what is missing from each franchise; `movie_add` or `collection_edit` fill the gaps worth filling and `exclusion_add` rules out the rest.
8. `audit_queue` shows the downloads stuck in the queue; `import_scan` and `import_apply` import one by hand, `queue_remove` clears it.

## Using the client on its own

`sdk/radarr` is a complete Go client for Radarr's v3 API that knows nothing of MCP: it is built on pandorest's base client and, under that, go-kt's HTTP client, which is standard library only. If you only want to talk to Radarr from Go, take the package and ignore the rest:

```go
import "github.com/katbyte/radarr-mcp/sdk/radarr"

c, err := radarr.New("http://nas:7878", os.Getenv("RADARR_TOKEN"))
res, err := c.GetMovie(ctx)
for _, m := range res.Answer { ... } // every film, each with its file and media info

all, err := c.GetHistoryComplete(ctx, radarr.GetHistoryOptions{PageSize: new(250)}) // every page
for _, h := range all.Items { ... }

done, err := c.RescanMovieAndWait(ctx, radarr.RescanMovieCommand{MovieId: m.Id}, time.Minute) // queue a command and wait for it
```

A read is asked again when a gateway could not reach Radarr or the connection dropped, three times in all, and a write is sent once. An error says what Radarr said went wrong (`Path: Folder '/nowhere' does not exist`) rather than quoting its JSON, a redirect is refused rather than followed unless it is the answer (a login's is, and comes back as it was sent), and `radarr.New(url, key, client.WithLog(logger))` traces every request and answer with the API key hidden.

It is generated from Radarr's own OpenAPI document (`api-defs/`, see [api-defs/README.md](api-defs/README.md)) by [pandorest](https://github.com/katbyte/pandorest), a generator modelled on [hashicorp/pandora](https://github.com/hashicorp/pandora) and shared with the other servers in this family: an importer normalises the spec into checked-in definitions (`api-defs/radarr-<release>/`, one file per tag) through named workarounds for the spec's known bugs, a differ reports what a spec refresh changes, and a generator writes one file per operation and model from the definitions. What is this repository's own is the service's settings and its thirteen workarounds in `sdk/pandorest`, and `sdk/radarr/service.go`, the one file in the package written by hand: what is Radarr's own about being talked to, from where its API key is found to how it words a refusal. That is **a method for every one of Radarr's 237 operations**, each with typed options, a typed body, an `{Answer, HttpResponse}` result, the status codes the operation answers (anything else is an error), and a `Complete` pager on every paged list. A film's id, a film file's and TMDB's are types of their own (`MovieID`, `MovieFileID`, `TmdbID`), so one cannot be passed where another is wanted, and sixteen of Radarr's commands have a method each, with one that waits for the command to finish. `make apicheck` proves the coverage claim against the spec, `make gencheck` (and the unit tests) fail when the generated code is stale, and the SDK suite (`acceptance/radarr/`) proves the shapes **against a running Radarr** - which is the only thing that catches the server changing shape underneath a spec that says otherwise.

## Development

```bash
make            # fmt + build
make check-all  # build + unit tests + both live suites (needs docker) + every linter
```

### Tests

`make test` is hermetic and fast. It covers the pure logic - which tools a session gets and what each says of itself, name resolution, the audit heuristics, the CLI's flags and config files - and, against a canned server, the requests the base client and the tools build and the answers they decode, including what a real Radarr is hard to make do: a command that fails, an indexer test that fails, a film TMDB deleted, two calls creating the same tag at once. It also re-imports the spec and regenerates the SDK to check the checked-in code is current, and applies every workaround twice to prove each one notices when its bug is fixed.

Everything else runs against **a real Radarr in Docker**, because a stub can only confirm what you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `acceptance/radarr/` | the `sdk/radarr` client against the server it was generated for: all 125 GETs called and decoded strictly (a field the SDK lacks fails the run), 108 of the 112 writes called - all but restart, shutdown and the two backup restores, which would take Radarr out from under the suite - with every answer checked against what the SDK declares, and bespoke tests of what the tools rely on: a release grabbed from the fake indexer into a download client, failed, blocklisted and taken off the blocklist, held back by a delay profile and grabbed from the queue, imported by hand, and a queued command cancelled | `make testacc-integration` |
| `acceptance/tools/` | the tools: name resolution, projections, every audit against fixtures seeded with what it exists to find, the settings tools, and journeys that chain them (a film searched for, grabbed by hand, downloaded and imported; every fixable audit fixed and re-audited; files imported by hand; writes repeated and made at once; a deleted film leaving nothing behind; lookups that must change nothing), and the built binary itself over stdio and HTTP (flags and environment reaching the server, nothing but protocol on stdout, the bearer check, clean shutdown, refusing to start without a key) | `make testacc-acceptance` |

```bash
make testacc        # both suites, each in a throwaway container
make check-all      # build + unit + live suites + every linter
make cover          # every suite merged into one coverage number
```

Coverage has to span every suite or it lies: `go test -cover ./...` reports a fraction for `tools/`, because almost everything real happens in the live suites behind the `acceptance` tag. `make cover` runs each into its own binary coverage directory and merges them with `go tool covdata` - stdlib tooling, no third-party merger - which is what the badge reports. The generated `sdk/radarr` is left out of the number and reported on a line of its own: it is one method per operation, and the integration suite exercises the ones the tools rely on rather than all 237.

**Every tool is exercised.** Tool coverage is enforced rather than claimed: the tool suite records every tool it calls and fails if the server registered one that nothing called, or one that only ever failed, so a new tool cannot ship untested.

**Every audit is followed to its fix.** Fifteen of the eighteen are found on the fixtures and then cleared by the tool the audit names - the film monitored, re-profiled, renamed, re-pointed at its folder, imported, excluded, added, downloaded better or let go of - with the fixture put back and the audits checked again to prove it. Three are not: no fixture can make a film's TMDB metadata incomplete (`audit_missing_metadata`) or have TMDB delete a film (`audit_removed`), and `audit_language` is cleared only by a copy with different audio, which the fake indexer does not offer. All three checks are tested against a canned library, and `audit_language` is found live as well. Radarr's own calls out to its metadata service (`api.radarr.video`) and TMDB's image CDN go through a record/replay proxy (go-kt's `test/replayproxy`, set up in `lib/testenv`) - the container is started with `HTTPS_PROXY` pointing at it and trusts its certificate authority - so neither suite needs a network:

```bash
make record         # re-record every cassette against the real services
make record-check   # check the cassettes still match, without rewriting them
RADARR_TEST_RECORD=1 make testacc-tools        # record only the requests no cassette holds
```

`record-check` compares the *shape* of live responses against the recordings - renamed fields, vanished fields, changed types - and ignores values, so it goes red when a service changes its contract rather than when a poster changes.

Downloads are real too. The suites start a small Newznab indexer on the host (go-kt's `test/services/newznab`, shared with sonarr-mcp and prowlarr-mcp) and hand it to Radarr, offering the releases a test chooses; Radarr grabs from it into a Usenet Blackhole folder, the test writes the finished download where the blackhole expects it, and Radarr imports it. So searching, grabbing, the queue, blocklisting, history and imports all run through Radarr's own code, not a stub of it.

Fixtures are generated, never committed: `.scripts/testenv.sh` writes tiny videos with `ffmpeg` - a still frame for the film's real running time, at the resolution and in the codec the test needs, a few tens of kilobytes each - under `~/.cache/radarr-mcp` (`RADARR_TEST_DATA` to move them - not `$TMPDIR`, which Docker Desktop does not share), starts Radarr with an API key it wrote into its config, and prints the environment. The suites add the root folders and the films through `rootfolder_add` and `movie_add`, so building the fixtures is itself part of the coverage. One root folder is clean, so the audits have something to leave alone; the other is seeded with every defect the audits exist to find - a folder named for the wrong year, a file that stops at 40 minutes, a 720p file named 2160p, an XviD DVD rip, a Japanese film with only English audio, a file below its profile's cutoff, a scene-named file, a second copy beside the one Radarr tracks, a film on disk Radarr has never heard of and a second copy of one it has - and each audit has a test against them. Requires docker, ffmpeg, curl and openssl; the suites skip when `RADARR_SERVER`, `RADARR_TOKEN` and `RADARR_TEST_DATA` are unset, so they never fail for want of a daemon.

Dev tools are pinned in `.tools/go.mod` and built into `.tools/bin` by make. On a noexec checkout point `TOOLS_BIN` somewhere local, e.g. `make TOOLS_BIN=~/.cache/radarr-mcp/bin lint`.
