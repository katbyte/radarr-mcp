# Configuring and running

The short version is in the [README](../README.md#configure). This page has every setting and the detail of running it as a service.

## Configuration

All options can be passed as command-line flags, environment variables, or via a configuration file.

| Variable | Flag | Description |
|---|---|---|
| `RADARR_SERVER` | `--server`, `-s` | Radarr's URL, e.g. `http://nas:7878`, with its URL base if it has one |
| `RADARR_TOKEN` | `--token`, `-t` | API key (Radarr: Settings → General → Security → API Key) |
| `RADARR_READ_ONLY` | `--read-only` | register only tools that never change server state |
| `RADARR_ENABLE_DELETE` | `--enable-delete` | register the 14 tools that delete: `movie_delete`, `moviefile_delete` and the `_delete` of each thing Radarr is set up with |
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

## Checking the connection

Quick connectivity check:

```bash
radarr-mcp info
```

## Register with Claude Code

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

## Run as a service (HTTP transport)

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`) instead of stdio. `RADARR_AUTH_TOKEN` is required: clients must send `Authorization: Bearer <token>`, and the server refuses to start without one unless `RADARR_ALLOW_NO_AUTH=true` says that anyone who can reach the port may use every tool. Register it from any machine:

```bash
claude mcp add --transport http radarr http://nas:8080/mcp \
  --header "Authorization: Bearer $RADARR_AUTH_TOKEN"
```

## Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/radarr-mcp`, tagged `vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust `RADARR_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in RADARR_TOKEN and RADARR_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `radarr-mcp`, with version info from git. The image is alpine-based (so `docker exec -it radarr-mcp sh` works), runs as a non-root user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm ghcr.io/katbyte/radarr-mcp info` works as a connectivity check with the `RADARR_*` variables passed via `-e`.

Tested against Radarr 6.4; the live suites run the `lscr.io/linuxserver/radarr` image of that version.
