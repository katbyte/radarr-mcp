# radarr-mcp - a Radarr MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/radarr-mcp?color=blueviolet)](https://github.com/katbyte/radarr-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/radarr-mcp?color=00ADD8)](https://github.com/katbyte/radarr-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/radarr-mcp?color=blue)](https://github.com/katbyte/radarr-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/radarr-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/radarr-mcp/actions/workflows/pr-integration.yaml/badge.svg)
![lint](https://github.com/katbyte/radarr-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/radarr-mcp/badges/coverage.json)](https://github.com/katbyte/radarr-mcp/actions/workflows/coverage.yaml)

An [MCP](https://modelcontextprotocol.io) server, CLI and Go SDK that **audit a
[Radarr](https://radarr.video) film library for the things that actually go wrong, and fix
what they find** - from Claude Code, Claude Desktop, or any other MCP client.

Radarr already exposes a large API, and an MCP server that wraps it lets a model list films
and start downloads. This one does that too, but the reason it exists is the layer above:
**18 audits**, each a sweep over the whole library for one thing that goes wrong in a real
collection - a folder matched to the wrong film, a folder renamed behind Radarr's back, a
720p file named 2160p, a download cut off halfway, a 4K file on a 1080p profile, a
dubbed-only copy of a Japanese film, a second copy nobody told Radarr about, a trilogy with a
hole in it, a download stuck in the queue - returning a worklist rather than a dump, and
naming the tool that fixes it.

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

The design principle: **detection is code, correction is judgment.** The server runs cheap
deterministic checks over the whole library and produces worklists; the AI reasons only about
the anomalies. Most audits are one request for the whole library, because Radarr answers every
film with its file and what its probe found inside it. Every response is a trimmed projection
of what a decision needs, never the raw API object (`MovieResource` has 50 fields before its
file and what is inside it; `movie_list` returns 12).

### What else is in the box

- **78 tools, in toolsets.** Find, read and add films, edit them one at a time or in bulk, correct a file's grade, import files by hand, search the indexers and grab a release yourself, watch the queue and the history, blocklist and exclude, collections, tags, root folders, quality profiles and size limits, naming, indexer and download client checks, health, logs, backups and tasks. Each sits in a toolset a session can load on its own, so a client spends about 850 tokens of context by default rather than 11,900.
- **A Go SDK.** `lib/radarr` is a complete typed client for Radarr's v3 API - all 237 operations, generated from Radarr's own OpenAPI document, standard library only, no knowledge of MCP. Useful on its own, whether or not you care about AI.
- **Tested against a real Radarr.** Every tool runs against a real Radarr in Docker, with a fake indexer and a download client so grabs really download and import. The suite fails if a registered tool has no test, and Radarr's calls out to its metadata service are recorded once and replayed, so CI needs no network.

## Installation

```bash
go install github.com/katbyte/radarr-mcp@latest
```

Tested against Radarr 6.4; the live suites run the `lscr.io/linuxserver/radarr` image of that
version.

## Configuration

All options can be passed as command-line flags, environment variables, or via a configuration file.

| Variable | Flag | Description |
|---|---|---|
| `RADARR_SERVER` | `--server`, `-s` | Radarr's URL, e.g. `http://nas:7878`, with its URL base if it has one |
| `RADARR_TOKEN` | `--token`, `-t` | API key (Radarr: Settings → General → Security → API Key) |
| `RADARR_READ_ONLY` | `--read-only` | register only tools that never change server state |
| `RADARR_ENABLE_DELETE` | `--enable-delete` | register `movie_delete` and `moviefile_delete`, the two tools that can remove films and their files |
| `RADARR_TOOLSETS` | `--toolsets` | groups of tools to register, default `core`: `all`, `core`, `curation`, `acquire`, `organise`, `admin`, or a resource family like `movie` (`core` is always included) |
| `RADARR_ALLOW_TOOLS` | `--allow-tools` | only register these tools (names, `movie_*` globs, or `essential`) |
| `RADARR_DENY_TOOLS` | `--deny-tools` | never register these tools (names or globs such as `*_delete`) |
| `RADARR_LOG` | | log level (`WARN` default; `DEBUG`, `TRACE`, ...) |
| `RADARR_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `RADARR_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint (required with `--listen`) |
| `RADARR_ALLOW_NO_AUTH` | `--allow-no-auth` | serve HTTP with no bearer token at all: anyone who can reach the port can use every tool |

Paths are always as Radarr sees them. When Radarr runs in a container, a root folder is the
path inside that container (`/movies`), not the one on the host.

### Configuration File

You can place a `.radarr-mcp` file in your home directory `~/.radarr-mcp` (for global settings)
or in your current directory `./.radarr-mcp` (for per-project settings). Keys match the long flag
names using the `env` format:

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

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`)
instead of stdio. `RADARR_AUTH_TOKEN` is required: clients must send `Authorization: Bearer
<token>`, and the server refuses to start without one unless `RADARR_ALLOW_NO_AUTH=true` says
that anyone who can reach the port may use every tool. Register it from any machine:

```bash
claude mcp add --transport http radarr http://nas:8080/mcp \
  --header "Authorization: Bearer $RADARR_AUTH_TOKEN"
```

### Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/radarr-mcp`, tagged
`vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs
that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust
`RADARR_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in RADARR_TOKEN and RADARR_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `radarr-mcp`, with version info from
git. The image is alpine-based (so `docker exec -it radarr-mcp sh` works), runs as a non-root
user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm
ghcr.io/katbyte/radarr-mcp info` works as a connectivity check with the `RADARR_*` variables
passed via `-e`.

## MCP Tools

Tools are named resource-first (`movie_*`, `audit_*`, `queue_*`...) so they group by what they
act on. Every tool carries MCP annotations (read-only or destructive) and tools that change
server state say so in their descriptions. Wherever a tool takes a film it accepts its Radarr
id, `tmdb:<id>`, `imdb:<tt id>` or its title (`"Alien (1979)"` when two share one), and a
profile, root folder, tag or collection by name as well as id; an unknown name comes back as an
error listing what exists, and a title that matches two films as one naming both.

| Resource | Tools |
|---|---|
| server | `server_info`, `server_health`, `server_disk_space`, `server_logs`, `server_log_file`, `server_backups`, `server_backup_create` |
| tasks | `task_list`, `task_run` (an RSS sync, a check for finished downloads, a refresh or a search of everything, waiting for it to finish) |
| films | `movie_list` (filters: title, monitored, has a file, status, profile, root folder, tag, genre, year; sorted and paged), `movie_get` (metadata, the file and what is inside it, its downloads and recent history), `movie_lookup` (TMDB, to find one to add), `movie_credits`, `movie_add`, `movie_edit` (including `folder`, which points a film at a folder already on disk), `movie_batch_edit` (the same change to many films; tags replaced, added or removed), `movie_refresh`, `movie_rescan`, `movie_rename` (previews unless told otherwise), `movie_download` (Radarr's automatic search), `movie_delete` |
| files | `moviefile_edit` (the quality, languages, release group or edition Radarr believes a file has), `moviefile_delete` |
| import | `import_identify` (which film each folder no film is in holds: what is in it, and what TMDB offers for its name) → `movie_add`; `import_scan` (what Radarr makes of a folder's files, and why it would refuse each) → `import_apply` |
| audits | `audit_all` and the 18 audits in [the table above](#the-audits) |
| releases | `release_search` (every release the indexers offer, with why Radarr would reject each) → `release_grab`, `release_parse` (a name read the way Radarr reads it) |
| downloads | `queue_list`, `queue_remove` (optionally blocklisting the release), `history_list`, `history_mark_failed`, `blocklist_list`, `blocklist_remove`, `calendar_list` |
| collections | `collection_list`, `collection_edit` (monitor one, so Radarr adds the films it is missing) |
| exclusions | `exclusion_list`, `exclusion_add`, `exclusion_remove` |
| tags | `tag_list` (with what carries each), `tag_create`, `tag_rename`, `tag_delete` (taking it off its films first) |
| root folders | `rootfolder_list`, `rootfolder_add`, `rootfolder_delete` |
| quality | `qualityprofile_list`, `qualityprofile_edit` (upgrades, cutoff, format scores), `qualitydefinition_list`, `qualitydefinition_edit` (size limits), `customformat_list` |
| naming | `naming_get` (with an example of each pattern), `naming_edit` |
| indexers and clients | `indexer_list`, `indexer_test`, `downloadclient_list`, `downloadclient_test` |

`movie_delete` (removes a film, and with `delete_files` its folder) and `moviefile_delete` are
only registered when `--enable-delete` / `RADARR_ENABLE_DELETE` is set. `--read-only` registers
the 50 read tools and nothing else, so a write tool is absent from `tools/list` rather than
refused when called.

### Choosing which tools load

**The default is `core`: six read-only tools, about 850 tokens.** The whole surface is around
11,900 tokens of tool definitions before anyone asks a question, which is a poor way to spend
a client's context by default. `--toolsets` / `RADARR_TOOLSETS` loads the groups a session
actually needs, and `core` comes along with whatever else is asked for, because nothing else
can find a film.

**Curating a library needs `RADARR_TOOLSETS=curation`** - the audits and everything that fixes
what they find. `RADARR_TOOLSETS=all` restores every tool.

| toolset | tools | with core | ~tokens |
|---|---|---|---|
| `core` *(default)* | 6 | 6 | 850 |
| `organise` | 7 | 13 | 1,600 |
| `admin` | 14 | 20 | 2,300 |
| `acquire` | 11 | 17 | 2,300 |
| `curation` | 40 | 46 | 8,200 |
| `all` | 78 | 78 | 11,900 |

Tokens are what the model sees: each tool's name, description and input schema, measured over
a real `tools/list` at four bytes a token, with `--enable-delete` on (the two delete tools are
`admin`'s). Every tool also carries an output schema, another 22,000 tokens across `all`, but
clients keep that to themselves to validate results rather than sending it to the model.

`--toolsets` also takes a resource family - `movie`, `moviefile`, `audit`, `import`, `release`,
`queue`, `history`, `blocklist`, `calendar`, `collection`, `exclusion`, `tag`, `rootfolder`,
`qualityprofile`, `qualitydefinition`, `customformat`, `naming`, `indexer`, `downloadclient`,
`server`, `task` - which is every tool with that prefix:

```sh
RADARR_TOOLSETS=all                 # every tool
RADARR_TOOLSETS=curation            # audits plus everything that fixes what they find
RADARR_TOOLSETS=acquire             # searching, grabbing and following downloads
RADARR_TOOLSETS=audit               # read-only detection, nothing that writes
RADARR_TOOLSETS=core,movie,queue    # core plus two whole families
```

`radarr-mcp tools` prints what the current flags would register, grouped by toolset, and
needs no server:

```sh
radarr-mcp tools                    # the default set
radarr-mcp tools --toolsets all     # every tool
radarr-mcp tools --read-only -q     # names only
```

### Narrowing further

`--allow-tools` and `--deny-tools` narrow whatever the toolsets left, and take comma-separated
tool names, globs with a leading or trailing `*`, or the `essential` preset (`movie_list`,
`movie_get`, `movie_lookup`, `movie_add`, `queue_list`):

```sh
RADARR_ALLOW_TOOLS=essential
RADARR_ALLOW_TOOLS=movie_*,queue_list
RADARR_DENY_TOOLS=*_delete,release_grab
```

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a
tool.

### A typical curation session

1. `audit_all` says where the library needs work.
2. `audit_unmapped_folders` lists the films on disk Radarr has never heard of;
   `import_identify` says which film each folder holds and what is in it, `movie_add` with
   the folder takes each in, and the second copies it finds are for `import_scan`.
   `audit_missing_folders` is the reverse - folders renamed or moved behind Radarr's back -
   and `movie_edit` with the folder points each film at where it is now.
3. `audit_year_mismatch` finds folders matched to the wrong film; `movie_get` shows the
   match. A wrong match is replaced by adding the right film for the folder; a right one in a
   misnamed folder is renamed by `movie_edit` into the root folder it is already in.
4. `audit_runtime`, `audit_resolution_mismatch` and `audit_size` find files that are not what
   they claim; `moviefile_edit` corrects a wrong grade, and `history_mark_failed` rejects a
   bad release so Radarr looks for another.
5. `audit_quality` and `audit_cutoff_unmet` list what is worth upgrading;
   `qualityprofile_edit upgrade_allowed=true` lets Radarr do it, and `movie_download`
   searches now rather than waiting for the next RSS sync.
6. `audit_naming` and `movie_rename` (a preview, then `preview=false`) tidy the names.
7. `audit_collection_gaps` lists what is missing from each franchise; `movie_add` or
   `collection_edit` fill the gaps worth filling and `exclusion_add` rules out the rest.
8. `audit_queue` shows the downloads stuck in the queue; `import_scan` and `import_apply`
   import one by hand, `queue_remove` clears it.

## Using the client on its own

`lib/radarr` is a complete Go client for Radarr's v3 API that depends on nothing but the
standard library, the shared base client in `lib/client` and `go-kt/version`, and knows
nothing of MCP. If you only want to talk to Radarr from Go, take the package and ignore the
rest:

```go
import "github.com/katbyte/radarr-mcp/lib/radarr"

c, err := radarr.New("http://nas:7878", os.Getenv("RADARR_TOKEN"))
res, err := c.GetMovie(ctx, radarr.GetMovieOperationOptions{})
for _, m := range res.Model { ... } // every film, each with its file and media info

all, err := c.GetHistoryComplete(ctx, radarr.GetHistoryOperationOptions{PageSize: 250}) // every page
for _, h := range all.Items { ... }
```

It is generated from Radarr's own OpenAPI document (`docs/`, see
[docs/README.md](docs/README.md)) by `internal/pandorest`, a generator kept in this
repository and modelled on [hashicorp/pandora](https://github.com/hashicorp/pandora): an
importer normalises the spec into checked-in definitions (`api-definitions/`, one file per
tag) through named workarounds for the spec's known bugs, a differ reports what a spec
refresh changes, and a generator writes one file per operation and model from the
definitions. That is **a method for every one of Radarr's 237 operations**, each with typed
options, a typed body, a `{Model, HttpResponse}` result, the status codes the operation
answers (anything else is an error), and a `Complete` pager on every paged list. `make
apicheck` proves the coverage claim against the spec, `make gencheck` (and the unit tests) fail
when the generated code is stale, and the integration suite proves the shapes **against a
running Radarr** - which is the only thing that catches the server changing shape underneath a
spec that says otherwise. See [internal/pandorest/README.md](internal/pandorest/README.md).

## Development

```bash
make            # fmt + build
make check-all  # build + unit tests + both live suites (needs docker) + every linter
```

### Tests

`make test` is hermetic and fast. It covers the pure logic - tool registration and toolsets,
name resolution, the audit heuristics, the CLI's flags, config files and HTTP auth, the
record/replay proxy, the fake indexer - and, against a canned server, the requests the base
client and the tools build and the answers they decode, including what a real Radarr is hard
to make do: a command that fails, an indexer test that fails, a film TMDB deleted, two calls
creating the same tag at once. It also re-imports the spec and regenerates the SDK to check the
checked-in code is current, and applies every importer workaround twice to prove each one
notices when its bug is fixed.

Everything else runs against **a real Radarr in Docker**, because a stub can only confirm what
you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `integration/` | the `lib/radarr` client against the server it was generated for: all 125 GETs called and decoded strictly (a field the SDK lacks fails the run), 108 of the 112 writes called - all but restart, shutdown and the two backup restores, which would take Radarr out from under the suite - with every answer checked against what the SDK declares, and bespoke tests of what the tools rely on: a release grabbed from the fake indexer into a download client, failed, blocklisted and taken off the blocklist, held back by a delay profile and grabbed from the queue, imported by hand, and a queued command cancelled | `make testacc-integration` |
| `acceptance/` | the tools: name resolution, projections, every audit against fixtures seeded with what it exists to find, the settings tools, and journeys that chain them (a film searched for, grabbed by hand, downloaded and imported; every fixable audit fixed and re-audited; files imported by hand; writes repeated and made at once; a deleted film leaving nothing behind; lookups that must change nothing), and the built binary itself over stdio and HTTP (flags and environment reaching the server, nothing but protocol on stdout, the bearer check, clean shutdown, refusing to start without a key) | `make testacc-acceptance` |

```bash
make testacc        # both suites, each in a throwaway container
make check-all      # build + unit + live suites + every linter
make cover          # every suite merged into one coverage number
```

Coverage has to span every suite or it lies: `go test -cover ./...` reports a fraction for
`tools/`, because almost everything real happens in the live suites behind the `integration`
tag. `make cover` runs each into its own binary coverage directory and merges them with
`go tool covdata` - stdlib tooling, no third-party merger - which is what the badge reports.
The generated `lib/radarr` is left out of the number and reported on a line of its own: it is
one method per operation, and the integration suite exercises the ones the tools rely on
rather than all 237.

**Every tool is exercised.** Tool coverage is enforced rather than claimed: the acceptance
suite records every tool it calls and fails if the server registered one nothing called, so a
new tool cannot ship untested.

**Every audit is followed to its fix.** Fifteen of the eighteen are found on the fixtures and
then cleared by the tool the audit names - the film monitored, re-profiled, renamed,
re-pointed at its folder, imported, excluded, added, downloaded better or let go of - with
the fixture put back and the audits checked again to prove it. Three are not: no fixture can
make a film's TMDB metadata incomplete (`audit_missing_metadata`) or have TMDB delete a film
(`audit_removed`), and `audit_language` is cleared only by a copy with different audio, which
the fake indexer does not offer. All three checks are tested against a canned library, and
`audit_language` is found live as well. Radarr's own calls out to its metadata service
(`api.radarr.video`) and TMDB's image CDN go through a record/replay proxy
(`lib/providerproxy`) - the container is started with `HTTPS_PROXY` pointing at it and trusts
its certificate authority - so neither suite needs a network:

```bash
make record         # re-record the cassettes against the real services
make record-check   # check the cassettes still match, without rewriting them
```

`record-check` compares the *shape* of live responses against the recordings - renamed fields,
vanished fields, changed types - and ignores values, so it goes red when a service changes its
contract rather than when a poster changes.

Downloads are real too. `internal/fakeindexer` is a small Newznab indexer the suites start on
the host and hand to Radarr, offering the releases a test chooses; Radarr grabs from it into a
Usenet Blackhole folder, the test writes the finished download where the blackhole expects it,
and Radarr imports it. So searching, grabbing, the queue, blocklisting, history and imports
all run through Radarr's own code, not a stub of it.

Fixtures are generated, never committed: `scripts/testenv.sh` writes tiny videos with
`ffmpeg` - a still frame for the film's real running time, at the resolution and in the codec
the test needs, a few tens of kilobytes each - under `~/.cache/radarr-mcp`
(`RADARR_TEST_DATA` to move them - not `$TMPDIR`, which Docker Desktop does not share), starts
Radarr with an API key it wrote into its config, and prints the environment. The suites add
the root folders and the films through `rootfolder_add` and `movie_add`, so building the
fixtures is itself part of the coverage. One root folder is clean, so the audits have
something to leave alone; the other is seeded with every defect the audits exist to find - a
folder named for the wrong year, a file that stops at 40 minutes, a 720p file named 2160p, an XviD DVD rip,
a Japanese film with only English audio, a file below its profile's cutoff, a scene-named file,
a second copy beside the one Radarr tracks, a film on disk Radarr has never heard of and a
second copy of one it has - and each audit has a test against them.
Requires docker, ffmpeg, curl and openssl; the suites skip when `RADARR_SERVER` and
`RADARR_TOKEN` are unset, so they never fail for want of a daemon.

Dev tools are pinned in `.tools/go.mod` (actionlint in `.tools/actionlint/go.mod`) and built
into `.tools/bin` by make. On a noexec checkout point `TOOLS_BIN` somewhere local, e.g.
`make TOOLS_BIN=~/.cache/radarr-mcp/bin lint`.
