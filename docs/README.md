# docs

## The API spec

Radarr publishes an OpenAPI document of its v3 API, vendored here as the
reference for `lib/radarr` - and as a build input. `internal/pandorest` (see
its [README](../internal/pandorest/README.md)) imports it into checked-in
definitions under `api-definitions/radarr/`, fixing the document's known bugs
with named workarounds on the way, and generates the client from those
definitions. `make generate` runs both steps, `make pandorest-diff` reports
what a refreshed document would change, `make gencheck` (and the unit tests)
fail when the generated code is stale, and `make apicheck` proves every
operation in the spec has a method.

| File | Source | Version vendored |
|---|---|---|
| `radarr-openapi.json` | Radarr's own repository, `src/Radarr.Api.V3/openapi.json` on `develop` | "3.0.0" (Radarr never bumps it) - 164 paths, 237 operations |

The live suites run against the image `scripts/testenv.sh` pins,
`lscr.io/linuxserver/radarr:version-6.4.4.10685`. To move to a newer
document, `make spec-update` fetches it, `make pandorest-diff` shows what
changed in API terms (breaking changes are marked), and `make generate`
writes it through; review the diff. A workaround whose bug the new document
fixes fails the import and names itself: delete it.

The spec is documentation of intent, not of behaviour. Swashbuckle writes it
from the controllers' signatures, so it can only say what a signature says:
every action that returns `object` or `IActionResult` comes out as an empty
200. The live suites (`integration/`, `acceptance/`) are what prove the
shapes against a real Radarr, and what they found is recorded below.

## Where the spec is wrong

These are shape bugs, fixed in the generated client by the importer's
workarounds (`internal/pandorest/importer/workarounds`, listed in
`api-definitions/radarr/Service.json`). Each checks its bug is still in the
document and fails the import once it is not.

- **Most GETs do not say what they answer** (`radarr-undeclared-responses`).
  Their 200 has no content, so nothing says whether it is JSON or a file. A
  table says which answer JSON of no fixed shape (the filesystem browser, the
  naming examples, a film's folder), which answer a list of a known schema
  (`GET /credit`), and which answer files: the UI's pages, log files, media
  covers, the calendar feed, and `GET /system/routes`, which is a graph of
  every route as plain text.
- **JSON answers also list `text/plain`** (`radarr-text-plain-twin`). About a
  hundred responses offer both, and Radarr only ever sends JSON; left in, the
  second reads as "this answers a file".
- **`GET /` has a parameter it cannot take** (`radarr-root-path-parameter`).
  It shares its action with the catch-all `GET /{path}`, and inherits its
  `{path}` parameter.
- **A command's body is the wrong schema, both ways**
  (`radarr-command-body`, `radarr-command-resource-body`). Radarr reads a
  command's name and fills that command's own class from the body, so a
  `RefreshMovie` carries `movieIds` and a `ManualImport` carries `files`; the
  document names a schema with none of those fields. `POST /command` takes,
  and a command's `body` answers, raw JSON.
- **Creates and updates answer other statuses** (`radarr-statuses`). Every
  create answers 201 and every update 202 - the bulk and editor updates too -
  where the document says 200 for all of them, which made every one an error.
- **The server sends fields the document lacks** (`radarr-undeclared-fields`):
  `allowedHosts` and `trustedNetworks` on the host settings,
  `isContainerized` on the system status, `movieFileQualities` on a film's
  statistics, `isExcluded` on a film a lookup finds.
- **Parameters the server cannot do without are optional**
  (`radarr-required-parameters`). A lookup without its term (503), a rename
  preview without its film (400), a parse without a title (an empty 204), and
  the filesystem calls without a path (500).
- **Writes that answer something say they answer nothing**
  (`radarr-write-responses`). The movie and file editors and bulk edits, the
  collection and quality definition bulk edits, a bulk exclusion and an add
  from an import list's discoveries answer what they changed; a grab
  (`POST /release`) answers a release; a manual import's reprocess answers its
  candidates; a provider action answers whatever that provider's action
  returns; and a test of every provider answers a result each. That result
  had no schema at all, so the workaround adds one (`ProviderTestAllResult`).
- **Bulk provider edits answer a list** (`radarr-bulk-responses`). The bulk
  edits of custom formats, download clients, import lists and indexers
  declare the single resource the single edit answers.
- **The host settings cannot be saved without ten of their fields**
  (`radarr-host-required`). Radarr saves them into `config.xml` field by
  field, comparing each value it is sent with the one it has; a field that is
  missing (or null) crashes the save with a 500 (the log level, the instance
  name, the URL base, the certificate path and password, the update script)
  or fails validation (the allowed hosts, bind address and branch). The
  document calls them all optional, and the models leave an empty string out,
  so the settings a fresh Radarr starts with - no URL base, no certificate -
  could never be sent back. The workaround marks them required, which the
  generator turns into fields that are always sent.
- **Updates take their id as a string** (`radarr-put-ids`). Twenty-one PUTs
  declare the `{id}` in their path a string, where every other operation by
  id declares the same ids as integers.

## Radarr behaviour

What the suites found that no document could say. The tools work with it;
anyone using `lib/radarr` directly should know it.

- **Settings.**
  - A fresh Radarr refuses its own host settings back. With authentication
    off for local addresses (the default) it wants the hosts it may be
    reached by, and starts with none, so a save must either name some or
    require authentication everywhere (with External authentication that
    changes nothing).
  - The other settings groups tolerate missing fields: only the host
    settings are saved into `config.xml`.
  - Validation errors cannot be forced through: `forceSave` skips warnings
    only.
- **Providers** (indexers, download clients, import lists, notifications,
  metadata consumers).
  - Radarr tests one before it saves it, so a provider that fails its test
    cannot be saved; one that stops working after is caught by a test.
  - A test of one answers the JSON string `"{}"` when it passes, and 400 with
    a list of failures when it does not (each with `isWarning` and a
    `detailedDescription`).
  - A test of every enabled provider answers a result each; when any fails
    it answers 400, with the same list. Those failures carry a `severity`
    ("error", "warning") and no `isWarning`.
  - An action a provider does not have answers `null`. Newznab's
    `newznabCategories` asks the indexer for its categories, and the
    "another Radarr" list's `getProfiles` asks that Radarr for its profiles,
    each answering `{"options": [...]}`.
- **Grabs and downloads.**
  - A grab (`POST /release`) answers the release it was sent - the guid and
    indexer id - not the release it grabbed: the title, quality and
    rejections are not in it.
  - A Usenet Blackhole grab has no download id in the history.
  - Marking a grab failed blocklists the release and, unless "redownload
    failed" is off in the download client settings, searches for the film
    again.
  - A delay profile holds back releases the RSS sync finds, never those of a
    search someone asked for: those are grabbed at once. What it holds is in
    the queue as `delay` until it is grabbed from there
    (`POST /queue/grab/{id}`).
  - Everything in a blackhole's watch folder is in the queue, a film Radarr
    knows or not; ask with `includeUnknownMovieItems` for the ones it does
    not know. Removing one from the client deletes it from the folder.
  - A pushed release (`POST /release/push`, as Prowlarr pushes one) gets the
    decision a search would give it, and is answered with that decision.
  - A blocklisted Usenet release is known by its title and publish date: an
    indexer that dates the same release afresh on every search gets it
    grabbed again.
  - Radarr's own quality profiles are made with upgrades off, so neither a
    search nor the RSS sync replaces a file a film has, however far below
    the cutoff it is. `audit_cutoff_unmet` says which profiles do not
    upgrade.
- **Films and files.**
  - An import of a film Radarr already has fails (400); an add from an
    import list's discoveries skips it.
  - Adding a film starts a refresh of it, which scans its folder; a rescan
    started alongside imports its file a second time. `movie_add` waits for
    Radarr's refresh rather than starting one.
  - Deleting a film with its files removes its folder in the background,
    shortly after the delete has answered; the exclusion a delete adds is
    added in the background too.
  - A file's quality is graded from the resolution its probe finds over the
    one its name claims: a 720p file named 2160p is graded 720p, so the name
    is caught only by `audit_resolution_mismatch`.
  - An XviD video in a Matroska file probes with no codec at all (in an AVI
    it reads `XviD`), so no check of the codec can see it there.
  - A rename preview is empty while renaming is off, which it is on a fresh
    Radarr.
  - A folder scan's filter of files the library already has works only for
    a file whose name reads as its own film: in "Blade Runner 2049 (2017)"
    the file reads as a film of 2049, and the scan offers it again. The
    tools take tracked files out themselves.
  - `GET /moviefile` needs a film or a list of files (400 without);
    `GET /manualimport` needs a folder or a download id (500 without), and
    given a film it ignores the folder and the filter.
- **Tags.**
  - A label may hold only a-z, 0-9 and `-`.
  - A tag films carry cannot be deleted; `tag_delete` takes it off them
    first.
  - Two creates of one label at once: the second is refused (409). The tools
    use the tag the first made.
- **Caches.**
  - A deleted custom format or auto tagging rule reads as a 500 (a
    `KeyNotFoundException` from the cache Radarr looks it up in), never a
    404.
  - The bulk quality definition update saves the change but answers, and
    for about five seconds reads as, the definitions as they were.
- **Commands.**
  - Radarr runs its commands on two threads, each as soon as it can, and
    cancels only a command still waiting for one: cancelling a command that
    has started or finished answers 409 ("Unable to cancel task").
  - A cancelled command never runs, but reads as queued, then and after.
- **The rest.**
  - The login form (`POST /login`) answers with a redirect, which a failed
    login points back to `/login?loginFailed=true`. With External
    authentication there are no users, so every login fails.
  - `GET /update` answers 500 when Radarr cannot reach its update server
    (in the test container, which answers it empty).

## What the integration suite leaves alone

It calls every GET, and every POST, PUT and DELETE but four, which it lists
in `integration/coverage_test.go` with why: restarting and shutting Radarr
down, and restoring a backup (by id, or uploaded), which replace the
database the suite is using and restart it. The restore upload also takes a
multipart file the document does not declare; with nothing exercising it, no
workaround adds it.
