# Using the Go client on its own

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

It is generated from Radarr's own OpenAPI document (`api-defs/`, see [api-defs/README.md](../api-defs/README.md)) by [pandorest](https://github.com/katbyte/pandorest), a generator modelled on [hashicorp/pandora](https://github.com/hashicorp/pandora) and shared with the other servers in this family: an importer normalises the spec into checked-in definitions (`api-defs/radarr-<release>/`, one file per tag) through named workarounds for the spec's known bugs, a differ reports what a spec refresh changes, and a generator writes one file per operation and model from the definitions. What is this repository's own is the service's settings and its thirteen workarounds in `sdk/pandorest`, and `sdk/radarr/service.go`, the one file in the package written by hand: what is Radarr's own about being talked to, from where its API key is found to how it words a refusal. That is **a method for every one of Radarr's 237 operations**, each with typed options, a typed body, an `{Answer, HttpResponse}` result, the status codes the operation answers (anything else is an error), and a `Complete` pager on every paged list. The ids of films, film files, TMDB entries, quality profiles, tags, indexers, download clients and root folders are each a type of their own (`MovieID`, `TagID` and so on), so one cannot be passed where another is wanted. Every date is a `radarr.Time`, a Go time read and written the way Radarr writes it. A call that takes one option takes it directly (`c.GetMovieLookup(ctx, "alien")`). Sixteen of Radarr's commands have a method each, with one that waits for the command to finish, and `radarr.Operations` lists every operation with its method and path, for a program that needs to go by them. `make apicheck` proves the coverage claim against the spec, `make gencheck` (and the unit tests) fail when the generated code is stale, and the SDK suite (`acceptance/radarr/`) proves the shapes **against a running Radarr** - which is the only thing that catches the server changing shape underneath a spec that says otherwise.
