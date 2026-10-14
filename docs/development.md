# Development and tests

```bash
make            # fmt + build
make check-all  # build + unit tests + both live suites (needs docker) + every linter
```

## Tests

`make test` is hermetic and fast. It covers the pure logic - which tools a session gets and what each says of itself, name resolution, the audit heuristics, the CLI's flags and config files - and, against a canned server, the requests the base client and the tools build and the answers they decode, including what a real Radarr is hard to make do: a command that fails, an indexer test that fails, a film TMDB deleted, two calls creating the same tag at once. It also re-imports the spec and regenerates the SDK to check the checked-in code is current, and applies every workaround twice to prove each one notices when its bug is fixed.

Everything else runs against **a real Radarr in Docker**, because a stub can only confirm what you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `acceptance/radarr/` | the `sdk/radarr` client against the server it was generated for: all 125 GETs called and decoded strictly (a field the SDK lacks fails the run), 118 of the 128 writes called - all but restart, shutdown and the two backup restores, which would take Radarr out from under the suite, and six commands that would search or refresh a whole library or update Radarr - with every answer checked against what the SDK declares, and bespoke tests of what the tools rely on: a release grabbed from the fake indexer into a download client, failed, blocklisted and taken off the blocklist, held back by a delay profile and grabbed from the queue, imported by hand, and a queued command cancelled | `make testacc-sdk` |
| `acceptance/tools/` | the tools: name resolution, projections, every audit against fixtures seeded with what it exists to find, the settings tools, and journeys that chain them (a film searched for, grabbed by hand, downloaded and imported; every fixable audit fixed and re-audited; files imported by hand; writes repeated and made at once; a deleted film leaving nothing behind; lookups that must change nothing), and the built binary itself over stdio and HTTP (flags and environment reaching the server, nothing but protocol on stdout, the bearer check, clean shutdown, refusing to start without a key) | `make testacc-tools` |

```bash
make testacc        # both suites, each in a throwaway container
make check-all      # build + unit + live suites + every linter
make cover          # every suite merged into one coverage number
```

Coverage has to span every suite or it lies: `go test -cover ./...` reports a fraction for `tools/`, because almost everything real happens in the live suites behind the `acceptance` tag. `make cover` runs each into its own binary coverage directory and merges them with `go tool covdata` - stdlib tooling, no third-party merger - which is what the badge reports. The generated `sdk/radarr` is left out of the number and reported on a line of its own: it is one method per operation, and the SDK suite exercises the ones the tools rely on rather than all of them.

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
