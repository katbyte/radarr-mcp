# radarr-mcp

[![GitHub release](https://img.shields.io/github/v/release/katbyte/radarr-mcp?color=blueviolet)](https://github.com/katbyte/radarr-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/radarr-mcp?color=00ADD8)](https://github.com/katbyte/radarr-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/radarr-mcp?color=blue)](https://github.com/katbyte/radarr-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/radarr-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/radarr-mcp/actions/workflows/pr-acceptance.yaml/badge.svg)
![lint](https://github.com/katbyte/radarr-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/radarr-mcp/badges/coverage.json)](https://github.com/katbyte/radarr-mcp/actions/workflows/coverage.yaml)

Lets an AI assistant look after a [Radarr](https://radarr.video) film library: find what is wrong with it, and fix it.

It is an [MCP](https://modelcontextprotocol.io) server, so it works with Claude Code, Claude Desktop and any other MCP client. A command-line tool and a Go client for Radarr come with it.

**Detection is code, correction is judgment.** The server runs 18 audits, each a cheap, exact sweep of the whole library for one kind of problem. The assistant only has to think about what they turn up.

```bash
go install github.com/katbyte/radarr-mcp@latest

printf 'SERVER=http://nas:7878\nTOKEN=<your Radarr API key>\n' > ~/.radarr-mcp
radarr-mcp info                                                         # check the connection
claude mcp add radarr -e RADARR_TOOLSETS=curation -- radarr-mcp serve   # register with Claude Code
```

Then ask it to run `audit_all`. Tested against Radarr 6.4.

## The audits

| Audit | What it finds, and what fixes it |
|---|---|
| [`audit_all`](docs/audits.md#audit_all) | Runs the audits and gives a count for each. Start here. |
| [`audit_missing_files`](docs/audits.md#audit_missing_files) | Wanted films with no file yet. `movie_download` searches for one. |
| [`audit_unmonitored`](docs/audits.md#audit_unmonitored) | Films with no file that Radarr is not looking for. `movie_edit` monitors them. |
| [`audit_cutoff_unmet`](docs/audits.md#audit_cutoff_unmet) | Files below the quality their profile aims for. `qualityprofile_edit` lets Radarr upgrade them. |
| [`audit_profile_mismatch`](docs/audits.md#audit_profile_mismatch) | Files of a quality their profile does not allow. `movie_edit` moves the film to a profile that fits. |
| [`audit_runtime`](docs/audits.md#audit_runtime) | Files much shorter or longer than the film. `history_mark_failed` rejects the release. |
| [`audit_resolution_mismatch`](docs/audits.md#audit_resolution_mismatch) | Files that claim a resolution the video does not have. `moviefile_edit` corrects the grade. |
| [`audit_quality`](docs/audits.md#audit_quality) | Low-resolution files and old video formats. `movie_download` searches for better. |
| [`audit_size`](docs/audits.md#audit_size) | Files far too small or too large for their quality. `history_mark_failed` rejects the release. |
| [`audit_language`](docs/audits.md#audit_language) | Files with no audio in the film's original language. `release_search` finds another copy. |
| [`audit_year_mismatch`](docs/audits.md#audit_year_mismatch) | Folders whose year disagrees with the film's. `movie_edit` renames the folder. |
| [`audit_naming`](docs/audits.md#audit_naming) | Files and folders not named the way Radarr would name them. `movie_rename` renames them. |
| [`audit_unmapped_folders`](docs/audits.md#audit_unmapped_folders) | Folders on disk that no film in Radarr uses. `movie_add` takes one in. |
| [`audit_missing_folders`](docs/audits.md#audit_missing_folders) | Films whose folder is no longer on disk. `movie_edit` points the film at where it is now. |
| [`audit_untracked_files`](docs/audits.md#audit_untracked_files) | Extra video files sitting in a film's folder. `import_apply` imports one. |
| [`audit_missing_metadata`](docs/audits.md#audit_missing_metadata) | Films missing a poster, an overview, a runtime or the like. `movie_refresh` fetches it again. |
| [`audit_removed`](docs/audits.md#audit_removed) | Films TMDB no longer lists. Usually a duplicate entry that was merged away. |
| [`audit_collection_gaps`](docs/audits.md#audit_collection_gaps) | Collections you hold only part of. `movie_add` or `collection_edit` fills the gaps. |
| [`audit_queue`](docs/audits.md#audit_queue) | Downloads that will not finish on their own. `import_apply` imports one, `queue_remove` clears it. |

The full rules for each, with its edge cases, are in [docs/audits.md](docs/audits.md). A walk through a whole session is in [docs/curation.md](docs/curation.md).

## Configure

It needs Radarr's address and its API key. The key is in Radarr under Settings → General → Security.

The simplest way is a file named `.radarr-mcp` in your home directory:

```env
SERVER=http://nas:7878
TOKEN=0123456789abcdef0123456789abcdef
```

Every setting can also be an environment variable or a flag:

| Variable | Flag | What it does |
|---|---|---|
| `RADARR_SERVER` | `--server` | Radarr's address, such as `http://nas:7878` |
| `RADARR_TOKEN` | `--token` | Radarr's API key |
| `RADARR_TOOLSETS` | `--toolsets` | Which groups of tools to load; see [Tools](#tools) |
| `RADARR_READ_ONLY` | `--read-only` | Load only tools that change nothing |
| `RADARR_ENABLE_DELETE` | `--enable-delete` | Also load the tools that delete |
| `RADARR_ALLOW_TOOLS` | `--allow-tools` | Load these tools as well, by name |
| `RADARR_DENY_TOOLS` | `--deny-tools` | Never load these tools |
| `RADARR_LISTEN` | `--listen` | Serve over HTTP on this address, such as `:8080` |
| `RADARR_AUTH_TOKEN` | `--auth-token` | The password clients must send over HTTP |
| `RADARR_ALLOW_NO_AUTH` | `--allow-no-auth` | Serve over HTTP with no password |
| `RADARR_LOG` | | How much to log: `WARN` (default), `INFO`, `DEBUG` or `TRACE` |

Paths are always as Radarr sees them. If Radarr runs in a container, a folder is its path inside the container, such as `/movies`.

Every setting in full, and running it as a service or in Docker, is in [docs/running.md](docs/running.md).

## Use

### With Claude Code

The quick start above registers it from the shell. The same in `.mcp.json`:

```json
{
  "mcpServers": {
    "radarr": {
      "command": "radarr-mcp",
      "args": ["serve"],
      "env": { "RADARR_TOOLSETS": "curation" }
    }
  }
}
```

It reads the address and key from `~/.radarr-mcp`.

### As a service

`radarr-mcp serve --listen :8080` serves over HTTP at `/mcp`, for clients on other machines. It will not start without `RADARR_AUTH_TOKEN`, which every client must then send:

```bash
claude mcp add --transport http radarr http://nas:8080/mcp --header "Authorization: Bearer $RADARR_AUTH_TOKEN"
```

### Docker

Each release publishes an image to `ghcr.io/katbyte/radarr-mcp`. The `docker-compose.yml` here runs it as an always-on HTTP service. Set `RADARR_SERVER` in that file, then:

```bash
cp .env.example .env      # fill in RADARR_TOKEN and RADARR_AUTH_TOKEN
docker compose up -d
```

## Tools

There are 137 tools: everything Radarr's own web interface can do. Loading them all would fill much of an assistant's memory before it had done anything, so they come in groups called toolsets and only `core` loads by default.

| Toolset | What it is for | Tools |
|---|---|---|
| `core` | Finding and reading films. Always loaded. | 6 |
| `organise` | Tags, root folders and quality profiles | 15 |
| `acquire` | Searching, grabbing and following downloads | 19 |
| `admin` | Health, logs, backups and tasks | 23 |
| `curation` | The audits and everything that fixes what they find | 46 |
| `setup` | Indexers, download clients, lists, notifications, settings | 58 |
| `all` | Everything | 137 |

The counts include `core`. To curate a library, use `RADARR_TOOLSETS=curation`.

- Every tool, by what it acts on: [docs/tools.md](docs/tools.md), or run `radarr-mcp tools --toolsets all`.
- Picking toolsets, and adding or removing single tools: [docs/toolsets.md](docs/toolsets.md).

### Safety

- **Nothing is deleted by accident.** The 14 tools that delete are not loaded unless `--enable-delete` is set. Even then, each only says what it would delete until called again with `confirm`.
- **Restarts and restores ask twice too.** So does any change to how Radarr is reached or who can sign in.
- **`--read-only` loads only the 67 tools that change nothing.**
- **Passwords and keys are never shown.** A tool says that one is set, not what it is. The same holds for the log.

## The Go client

`sdk/radarr` is a complete Go client for Radarr's API, with a method for every one of its 237 operations. It knows nothing of MCP and can be used on its own. See [docs/go-client.md](docs/go-client.md).

## Development

```bash
make            # format and build
make test       # unit tests; fast, needs nothing
make testacc    # the live tests; needs Docker, ffmpeg, curl and openssl
make check-all  # everything, with the linters
```

The live tests run against a real Radarr in Docker, not a stand-in. How they work is in [docs/development.md](docs/development.md).

- What is planned: [docs/ROADMAP.md](docs/ROADMAP.md).
- What Radarr's API description gets wrong, and how the client is generated: [api-defs/README.md](api-defs/README.md).
- How releases are cut: [CONTRIBUTING.md](CONTRIBUTING.md).
