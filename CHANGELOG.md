# Changelog

## 0.1.0 (2026-09-19)

The first version: an MCP server, CLI and Go SDK for curating a Radarr film library, built
on the same pattern as embyfin-mcp and abs-mcp.

- **18 audits and `audit_all`**, each a sweep over the whole library for one thing that goes
  wrong: missing and unmonitored films, files below their cutoff or outside their profile,
  runtimes, resolutions, sizes and languages that do not add up, folders named for the wrong
  year or film, folders Radarr does not know about and folders it believes in that are gone,
  files it does not track, gaps in collections, films TMDB dropped, and downloads stuck in
  the queue. Each names the tool that fixes what it finds.
- **Taking folders already on disk into the library**: `import_identify` says which film each
  folder no film is in holds - what is in it, and what TMDB offers for its name - and
  `movie_add` with that folder takes it in. `audit_missing_folders` and `movie_edit`'s
  `folder` handle the reverse, a folder renamed or moved behind Radarr's back.
- **78 tools in toolsets** (`core`, `curation`, `acquire`, `organise`, `admin`), with
  `--read-only`, `--enable-delete`, allow and deny patterns, and an HTTP transport behind a
  bearer token.
- **`lib/radarr`**, a typed client for every one of Radarr's 237 v3 API operations,
  generated from Radarr's OpenAPI document with named workarounds for where the document and
  the server disagree.
- **Live suites against a real Radarr in Docker**: one for the SDK, one for the tools, the
  built binary and journeys through them, with a fake indexer so grabs really download and
  import, and Radarr's metadata calls recorded and replayed so CI needs no network.
