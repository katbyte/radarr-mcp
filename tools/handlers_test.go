package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools end to end against a canned Radarr: the requests a handler makes
// and the answer it projects, without a container. The live suite proves the
// canned shapes match a real server; these pin the behaviour its fixtures
// cannot reach - a command that fails, an indexer test that fails, a film
// TMDB deleted, renaming switched on and off - and run in milliseconds.

// fakeRadarr is a canned Radarr API: routes on a ServeMux plus a record of
// every request the tools made to it, with their bodies.
type fakeRadarr struct {
	mux *http.ServeMux
	srv *httptest.Server

	mu     sync.Mutex
	seen   []request
	movies []map[string]any
}

type request struct {
	Method, Path, Query, Body string
}

func newFakeRadarr(t *testing.T) *fakeRadarr {
	t.Helper()

	f := &fakeRadarr{mux: http.NewServeMux()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
		f.mu.Unlock()
		if r.Header.Get("X-Api-Key") != "k" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// requests returns the calls made to a method and path, in order.
func (f *fakeRadarr) requests(method, path string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []request
	for _, r := range f.seen {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}

	return out
}

// answer registers a route that answers v as JSON with a status.
func (f *fakeRadarr) answer(pattern string, status int, v any) {
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	})
}

// library serves films at /api/v3/movie and /api/v3/movie/{id}, and the
// lists every projection reads (profiles, tags, root folders).
func (f *fakeRadarr) library(movies ...map[string]any) {
	f.movies = movies
	f.answer("GET /api/v3/movie", http.StatusOK, movies)
	f.mux.HandleFunc("GET /api/v3/movie/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, m := range movies {
			if fmt.Sprint(m["id"]) == r.PathValue("id") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(m)
				return
			}
		}
		http.Error(w, "NotFound", http.StatusNotFound)
	})
	f.answer("GET /api/v3/qualityprofile", http.StatusOK, []any{fakeProfile})
	f.answer("GET /api/v3/tag", http.StatusOK, []any{map[string]any{"id": 1, "label": "4k"}})
	f.answer("GET /api/v3/rootfolder", http.StatusOK, []any{
		map[string]any{"id": 1, "path": "/media/movies", "accessible": true, "freeSpace": 1 << 40, "unmappedFolders": []any{
			map[string]any{"name": "Ronin (1998)", "path": "/media/movies/Ronin (1998)"},
			map[string]any{"name": "Alien (1979) Directors Cut", "path": "/media/movies/Alien (1979) Directors Cut"},
		}},
	})
	// the root folder as the filesystem browser sees it: every film's folder
	// is there, so nothing is missing from disk
	dirs := []any{
		map[string]any{"name": "Ronin (1998)", "path": "/media/movies/Ronin (1998)"},
		map[string]any{"name": "Alien (1979) Directors Cut", "path": "/media/movies/Alien (1979) Directors Cut"},
	}
	for _, m := range movies {
		if path, ok := m["path"].(string); ok && path != "" {
			dirs = append(dirs, map[string]any{"name": baseName(path), "path": path})
		}
	}
	f.answer("GET /api/v3/filesystem", http.StatusOK, map[string]any{"parent": "/media", "directories": dirs})
}

// fakeProfile is HD-1080p as Radarr answers it: worst first, cutoff
// Bluray-1080p.
var fakeProfile = map[string]any{
	"id": 4, "name": "HD-1080p", "cutoff": 7, "upgradeAllowed": true,
	"items": []any{
		map[string]any{"allowed": false, "quality": map[string]any{"id": 2, "name": "DVD"}},
		map[string]any{"allowed": true, "quality": map[string]any{"id": 4, "name": "HDTV-720p"}},
		map[string]any{"allowed": true, "quality": map[string]any{"id": 7, "name": "Bluray-1080p"}},
		map[string]any{"allowed": false, "quality": map[string]any{"id": 19, "name": "Bluray-2160p"}},
	},
}

// movie is a film as Radarr answers it, with a file when a quality is given.
func movie(id int, title string, year int, quality, resolution, runtime string) map[string]any {
	m := map[string]any{
		"id": id, "title": title, "year": year, "tmdbId": id * 10, "imdbId": "tt" + strconv.Itoa(id), "monitored": true,
		"isAvailable": true, "status": "released", "qualityProfileId": 4, "runtime": 117, "overview": "A film.",
		"genres": []string{"Drama"}, "images": []any{map[string]any{"coverType": "poster"}},
		"path": "/media/movies/" + titleYear(title, year), "rootFolderPath": "/media/movies", "sortTitle": strings.ToLower(title),
		"originalLanguage": map[string]any{"id": 1, "name": "English"},
	}
	if quality != "" {
		m["hasFile"] = true
		m["sizeOnDisk"] = 30000
		m["movieFile"] = map[string]any{
			"id": id * 100, "relativePath": titleYear(title, year) + " " + quality + ".mkv", "size": 30000,
			"quality":   map[string]any{"quality": map[string]any{"name": quality}},
			"mediaInfo": map[string]any{"resolution": resolution, "runTime": runtime, "videoCodec": "x264", "audioLanguages": "eng", "audioStreamCount": 1},
		}
	} else {
		m["hasFile"] = false
	}

	return m
}

// session connects an in-memory MCP client to a server registering every
// tool against the fake, with the given options.
func session(t *testing.T, f *fakeRadarr, opts Options) *mcp.ClientSession {
	t.Helper()

	client, err := radarr.New(f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	opts.Toolsets = []string{"all"}
	if _, err := RegisterAll(srv, client, opts); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// run calls a tool and returns its structured result, or its error text.
func run(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (out map[string]any, errText string) {
	t.Helper()

	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return nil, strings.Join(msgs, "; ")
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out, ""
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()

	out, msg := run(t, cs, name, args)
	if msg != "" {
		t.Fatalf("%s: %s", name, msg)
	}

	return out
}

func mustFail(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, want string) {
	t.Helper()

	out, msg := run(t, cs, name, args)
	if msg == "" || !strings.Contains(msg, want) {
		t.Errorf("%s(%v) = %v %q, want an error containing %q", name, args, out, msg, want)
	}
}

func rowsOf(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}

	return out
}

// is reports whether a decoded JSON value is the boolean want.
func is(v any, want bool) bool {
	b, ok := v.(bool)

	return ok && b == want
}

func titles(out map[string]any, field string) []string {
	found := rowsOf(out[field])
	names := make([]string, 0, len(found))
	for _, r := range found {
		names = append(names, fmt.Sprint(r["title"]))
	}
	slices.Sort(names)

	return names
}

// server_info says which build of this server answered, beside the Radarr
// it talks to: an MCP client keeps the binary it started with, and this is
// the one call that can tell a session which that is.
func TestServerInfo(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"), movie(2, "Alien³", 1992, "", "", ""))
	f.answer("GET /api/v3/system/status", http.StatusOK, map[string]any{"appName": "Radarr", "version": "6.4.4.10685", "osName": "alpine", "isDocker": true})
	f.answer("GET /api/v3/health", http.StatusOK, []any{
		map[string]any{"source": "IndexerRssCheck", "type": "error", "message": "No indexers"},
		map[string]any{"source": "DownloadClientCheck", "type": "warning", "message": "No download client"},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "server_info", nil)
	if out["version"] != "6.4.4.10685" || out["radarr_mcp_version"] != version.Version || !is(out["docker"], true) {
		t.Errorf("server_info = %v", out)
	}
	if out["movies"] != 2.0 || out["with_files"] != 1.0 || out["missing_monitored"] != 1.0 || out["health_problems"] != 2.0 {
		t.Errorf("counts = %v", out)
	}
	if errs, ok := out["health_errors"].([]any); !ok || len(errs) != 1 || !strings.Contains(fmt.Sprint(errs[0]), "IndexerRssCheck") {
		t.Errorf("health_errors = %v", out["health_errors"])
	}

	health := mustCall(t, cs, "server_health", nil)
	if checks := rowsOf(health["checks"]); len(checks) != 2 || checks[0]["type"] != "error" {
		t.Errorf("server_health = %v, want the error first", health)
	}
}

// A film is named the way a person would, and a wrong name comes back as an
// error that says what to do instead.
func TestMovieResolution(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Dune", 1984, "Bluray-1080p", "1920x1080", "2:16:00"), movie(2, "Dune", 2021, "Bluray-1080p", "1920x1080", "2:35:00"))
	f.answer("GET /api/v3/queue", http.StatusOK, map[string]any{"records": []any{}, "totalRecords": 0})
	f.answer("GET /api/v3/history/movie", http.StatusOK, []any{})
	cs := session(t, f, Options{})

	mustFail(t, cs, "movie_get", map[string]any{"movie": "Dune"}, `"Dune" matches 2 films: Dune (1984) id 1, Dune (2021) id 2`)
	if out := mustCall(t, cs, "movie_get", map[string]any{"movie": "Dune (2021)"}); out["id"] != 2.0 {
		t.Errorf("Dune (2021) = %v", out["id"])
	}
	// an id is one request, not the whole library
	before := len(f.requests(http.MethodGet, "/api/v3/movie"))
	if out := mustCall(t, cs, "movie_get", map[string]any{"movie": "1"}); out["year"] != 1984.0 {
		t.Errorf("movie 1 = %v", out)
	}
	if after := len(f.requests(http.MethodGet, "/api/v3/movie")); after != before {
		t.Errorf("movie_get by id read the whole library (%d reads, was %d)", after, before)
	}
	mustFail(t, cs, "movie_get", map[string]any{"movie": "9"}, "no film in the library has id 9")
}

func TestMovieListFiltersAndSorts(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	unmonitored := movie(3, "Aliens", 1986, "", "", "")
	unmonitored["monitored"] = false
	unmonitored["tags"] = []int{1}
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"), movie(2, "Blade Runner", 1982, "HDTV-720p", "1280x720", "1:58:00"), unmonitored)
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "movie_list", map[string]any{"sort": "year", "descending": true})
	if got := rowsOf(out["movies"]); len(got) != 3 || got[0]["title"] != "Aliens" || got[2]["title"] != "Alien" {
		t.Errorf("by year descending = %v", out["movies"])
	}
	if got := titles(mustCall(t, cs, "movie_list", map[string]any{"has_file": true}), "movies"); !slices.Equal(got, []string{"Alien", "Blade Runner"}) {
		t.Errorf("has_file = %v", got)
	}
	if got := titles(mustCall(t, cs, "movie_list", map[string]any{"tag": "4k"}), "movies"); !slices.Equal(got, []string{"Aliens"}) {
		t.Errorf("tag 4k = %v", got)
	}
	if got := titles(mustCall(t, cs, "movie_list", map[string]any{"query": "alien"}), "movies"); !slices.Equal(got, []string{"Alien", "Aliens"}) {
		t.Errorf("query alien = %v", got)
	}
	page := mustCall(t, cs, "movie_list", map[string]any{"limit": 1, "offset": 1})
	if page["total"] != 3.0 || len(rowsOf(page["movies"])) != 1 {
		t.Errorf("a page = %v", page)
	}
	mustFail(t, cs, "movie_list", map[string]any{"tag": "nope"}, `no tag "nope"`)
	mustFail(t, cs, "movie_list", map[string]any{"sort": "colour"}, `sort "colour"`)
	mustFail(t, cs, "movie_list", map[string]any{"status": "gone"}, "want one of")
}

// A command is waited on until it stops, and one that fails comes back as
// an error carrying Radarr's own message.
func TestCommands(t *testing.T) {
	t.Parallel()

	commandPoll = time.Millisecond
	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"))
	var polls int
	var mu sync.Mutex
	f.answer("POST /api/v3/command", http.StatusCreated, map[string]any{"id": 7, "name": "RescanMovie", "status": "queued"})
	f.mux.HandleFunc("GET /api/v3/command/7", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		status := "started"
		if n >= 3 {
			status = "completed"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "name": "RescanMovie", "status": status})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "movie_rescan", map[string]any{"movies": []any{"Alien"}})
	if cmds := rowsOf(out["commands"]); len(cmds) != 1 || cmds[0]["status"] != "completed" {
		t.Errorf("commands = %v", out["commands"])
	}
	posted := f.requests(http.MethodPost, "/api/v3/command")
	if len(posted) != 1 || !strings.Contains(posted[0].Body, `"movieId":1`) || !strings.Contains(posted[0].Body, `"name":"RescanMovie"`) {
		t.Errorf("the command sent = %v", posted)
	}

	failing := newFakeRadarr(t)
	failing.library(movie(1, "Alien", 1979, "", "", ""))
	failing.answer("POST /api/v3/command", http.StatusCreated, map[string]any{"id": 8, "name": "RefreshMovie", "status": "failed", "message": "TMDB is down"})
	mustFail(t, session(t, failing, Options{}), "movie_refresh", map[string]any{"movies": []any{"1"}}, "RefreshMovie failed: TMDB is down")

	mustFail(t, cs, "task_run", map[string]any{"task": "Restart"}, `task "Restart" is not one task_run starts`)
}

// An indexer test that fails answers 400 with what failed, which the
// document does not declare: the tool reads it rather than reporting a bare
// status.
func TestProviderTests(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library()
	f.answer("GET /api/v3/indexer", http.StatusOK, []any{
		map[string]any{"id": 1, "name": "Good"}, map[string]any{"id": 2, "name": "Bad"}, map[string]any{"id": 3, "name": "Quiet"},
	})
	// a test of every indexer marks a warning with severity
	f.answer("POST /api/v3/indexer/testall", http.StatusBadRequest, []any{
		map[string]any{"id": 1, "isValid": true, "validationFailures": []any{}},
		map[string]any{"id": 2, "isValid": false, "validationFailures": []any{map[string]any{"propertyName": "ApiKey", "errorMessage": "Invalid API Key", "severity": "error"}}},
		map[string]any{"id": 3, "isValid": true, "validationFailures": []any{map[string]any{"errorMessage": "No results in the test search", "severity": "warning"}}},
	})
	// a test of one marks it with isWarning
	f.answer("POST /api/v3/indexer/test", http.StatusBadRequest, []any{
		map[string]any{"propertyName": "BaseUrl", "errorMessage": "Unable to connect", "isWarning": false},
		map[string]any{"errorMessage": "Slow to answer", "isWarning": true},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "indexer_test", nil)
	results := rowsOf(out["results"])
	if len(results) != 3 || !is(results[0]["valid"], true) || !is(results[1]["valid"], false) || results[1]["name"] != "Bad" ||
		fmt.Sprint(results[1]["failures"]) != "[Invalid API Key]" || fmt.Sprint(results[2]["failures"]) != "[warning: No results in the test search]" {
		t.Errorf("indexer_test = %v", out)
	}
	one := mustCall(t, cs, "indexer_test", map[string]any{"name": "bad"})
	if r := rowsOf(one["results"]); len(r) != 1 || !is(r[0]["valid"], false) || fmt.Sprint(r[0]["failures"]) != "[Unable to connect warning: Slow to answer]" {
		t.Errorf("indexer_test one = %v", one)
	}
	mustFail(t, cs, "indexer_test", map[string]any{"name": "nope"}, `no indexer "nope"`)
}

// movie_edit with a folder points a film at one already on disk - nothing is
// moved - and rescans it, so Radarr picks the files back up.
func TestMovieEditPointsAtAFolder(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"))
	f.answer("PUT /api/v3/movie/1", http.StatusAccepted, movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"))
	f.answer("POST /api/v3/command", http.StatusCreated, map[string]any{"id": 3, "name": "RescanMovie", "status": "completed"})
	cs := session(t, f, Options{})

	mustCall(t, cs, "movie_edit", map[string]any{"movie": "Alien", "folder": "Alien (1979) [remux]"})
	put := f.requests(http.MethodPut, "/api/v3/movie/1")
	if len(put) != 1 || !strings.Contains(put[0].Body, `"path":"/media/movies/Alien (1979) [remux]"`) || !strings.Contains(put[0].Query, "moveFiles=false") {
		t.Errorf("the film was pointed at %v", put)
	}
	if cmd := f.requests(http.MethodPost, "/api/v3/command"); len(cmd) != 1 || !strings.Contains(cmd[0].Body, `"name":"RescanMovie"`) {
		t.Errorf("the folder was not rescanned: %v", cmd)
	}
	// the editor is for what a move means, so the two do not go together
	mustFail(t, cs, "movie_edit", map[string]any{"movie": "Alien", "folder": "x", "root_folder": "/media/movies"}, "folder and root_folder do not go together")
	mustFail(t, cs, "movie_edit", map[string]any{"movie": "Alien", "folder": "Alien (1979)"}, "is already at /media/movies/Alien (1979)")
}

// import_identify reads each folder no film is in: what is in it, and what
// TMDB offers for its name. A name that finds nothing is looked up again by
// the title and year read out of it.
func TestImportIdentify(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"))
	f.answer("GET /api/v3/manualimport", http.StatusOK, []any{map[string]any{
		"path": "/media/movies/Ronin (1998)/Ronin.mkv", "relativePath": "Ronin.mkv", "size": 3000,
		"quality": map[string]any{"quality": map[string]any{"name": "Bluray-1080p"}},
	}})
	f.mux.HandleFunc("GET /api/v3/movie/lookup", func(w http.ResponseWriter, r *http.Request) {
		found := []any{}
		switch r.URL.Query().Get("term") {
		case "Ronin (1998)":
			found = []any{map[string]any{"title": "Ronin", "year": 1998, "tmdbId": 8195, "runtime": 122}}
		case "Alien 1979": // the director's cut folder, looked up again by what its name reads as
			found = []any{map[string]any{"title": "Alien", "year": 1979, "tmdbId": 10, "runtime": 117}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(found)
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "import_identify", nil)
	folders := rowsOf(out["folders"])
	if out["total"] != 2.0 || len(folders) != 2 {
		t.Fatalf("import_identify = %v", out)
	}
	ronin := folders[1]
	if ronin["name"] != "Ronin (1998)" || len(rowsOf(ronin["files"])) != 1 {
		t.Errorf("Ronin's folder = %v", ronin)
	}
	if c := rowsOf(ronin["candidates"]); len(c) != 1 || c[0]["tmdb_id"] != 8195.0 || !is(c[0]["in_library"], false) {
		t.Errorf("Ronin's candidates = %v", ronin["candidates"])
	}
	// the second copy of a film the library holds: its candidate is that film
	if c := rowsOf(folders[0]["candidates"]); len(c) != 1 || !is(c[0]["in_library"], true) || c[0]["library_id"] != 1.0 {
		t.Errorf("the director's cut candidates = %v", folders[0])
	}
}

// A film whose folder is not on disk - renamed or moved behind Radarr's back
// - is found, and named with the folder in its root folder that reads as it.
func TestAuditMissingFolders(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	// a film in a root folder inside another: its folder is listed by the
	// inner one, not by /media/movies
	inner := movie(5, "Blade Runner", 1982, "Bluray-2160p", "3840x2160", "1:57:00")
	inner["path"] = "/media/movies/4k/Blade Runner (1982)"
	renamed := movie(2, "Aliens", 1986, "Bluray-1080p", "1920x1080", "2:17:00")
	gone := movie(3, "Heat", 1995, "HDTV-720p", "1280x720", "2:50:00")
	// never downloaded, so Radarr has not made its folder: not a finding
	waiting := movie(4, "Arrival", 2016, "", "", "")
	f.answer("GET /api/v3/movie", http.StatusOK, []any{movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"), renamed, gone, waiting, inner})
	f.answer("GET /api/v3/rootfolder", http.StatusOK, []any{
		map[string]any{"id": 1, "path": "/media/movies", "accessible": true, "unmappedFolders": []any{
			map[string]any{"name": "Aliens (1986) [remux]", "path": "/media/movies/Aliens (1986) [remux]"},
		}},
		map[string]any{"id": 3, "path": "/media/movies/4k", "accessible": true},
		// a root folder Radarr cannot reach is one problem, not one a film
		map[string]any{"id": 2, "path": "/media/archive", "accessible": false},
	})
	f.mux.HandleFunc("GET /api/v3/filesystem", func(w http.ResponseWriter, r *http.Request) {
		dirs := []any{
			map[string]any{"name": "Alien (1979)", "path": "/media/movies/Alien (1979)"},
			map[string]any{"name": "Aliens (1986) [remux]", "path": "/media/movies/Aliens (1986) [remux]/"},
		}
		if strings.HasPrefix(r.URL.Query().Get("path"), "/media/movies/4k") {
			dirs = []any{map[string]any{"name": "Blade Runner (1982)", "path": "/media/movies/4k/Blade Runner (1982)"}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"parent": "/media", "directories": dirs})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_missing_folders", nil)
	found := rowsOf(out["findings"])
	if len(found) != 2 || out["scanned"] != 5.0 {
		t.Fatalf("audit_missing_folders = %v", out)
	}
	if found[0]["title"] != "Aliens" || found[0]["folder"] != "/media/movies/Aliens (1986) [remux]" ||
		!strings.Contains(fmt.Sprint(found[0]["detail"]), "reads as this film") {
		t.Errorf("the renamed film = %v", found[0])
	}
	if found[1]["title"] != "Heat" || found[1]["folder"] != nil || !strings.Contains(fmt.Sprint(found[1]["detail"]), "though Radarr has a file in it") {
		t.Errorf("the film whose folder is gone = %v", found[1])
	}
}

// Radarr answers a grab with no more than it was sent, so release_grab reads
// the grab back from the history Radarr wrote for it: the newest grab
// carrying the release's guid, not merely the newest grab.
func TestReleaseGrabReadsTheHistory(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Primer", 2004, "", "", ""))
	f.answer("POST /api/v3/release", http.StatusOK, map[string]any{"guid": "g-1", "indexerId": 7})
	f.answer("GET /api/v3/history", http.StatusOK, map[string]any{"page": 1, "pageSize": 50, "totalRecords": 2, "records": []any{
		map[string]any{"id": 12, "eventType": "grabbed", "movieId": 2, "sourceTitle": "Other.Film.2001.720p", "data": map[string]any{"guid": "g-2"}},
		map[string]any{
			"id": 11, "eventType": "grabbed", "movieId": 1, "sourceTitle": "Primer.2004.720p.HDTV.x264-GRP", "downloadId": "dl-1",
			"data": map[string]any{"guid": "g-1", "indexer": "Fake Indexer"}, "movie": map[string]any{"title": "Primer", "year": 2004},
		},
	}})
	f.answer("GET /api/v3/queue", http.StatusOK, map[string]any{"page": 1, "pageSize": 500, "totalRecords": 0, "records": []any{}})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "release_grab", map[string]any{"guid": "g-1", "indexer_id": 7})
	grabbed, ok := out["grabbed"].(map[string]any)
	if !ok || grabbed["id"] != 11.0 || grabbed["source_title"] != "Primer.2004.720p.HDTV.x264-GRP" || grabbed["movie"] != "Primer (2004)" || grabbed["download_id"] != "dl-1" {
		t.Errorf("release_grab = %v", out)
	}
	if q := f.requests(http.MethodGet, "/api/v3/queue"); len(q) != 1 || !strings.Contains(q[0].Query, "movieIds=1") {
		t.Errorf("the queue was read as %v, want the grabbed film's", q)
	}
}

// The audits against a canned library whose defects are known: each finds
// its own and nothing else, and audit_all reports each one's count.
func TestAuditsAgainstACannedLibrary(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	wrong := movie(2, "Dune", 1984, "Bluray-1080p", "1920x1080", "2:16:00")
	wrong["path"] = "/media/movies/Dune (2021)"
	wrong["runtime"] = 136
	short := movie(3, "Interstellar", 2014, "Bluray-1080p", "1920x1080", "0:40:00")
	short["runtime"] = 169
	gone := movie(4, "Merged", 2001, "", "", "")
	gone["status"] = "deleted"
	gone["monitored"] = false
	stub := movie(5, "Stub", 2020, "Bluray-1080p", "1920x1080", "1:57:00")
	stub["overview"] = ""
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"), wrong, short, gone, stub)
	f.answer("GET /api/v3/qualitydefinition", http.StatusOK, []any{})
	f.answer("GET /api/v3/collection", http.StatusOK, []any{map[string]any{
		"id": 1, "title": "Alien Collection", "tmdbId": 8091, "movies": []any{
			map[string]any{"tmdbId": 10, "title": "Alien", "year": 1979, "isExisting": true, "status": "released"},
			map[string]any{"tmdbId": 679, "title": "Aliens", "year": 1986, "isExisting": false, "status": "released"},
			map[string]any{"tmdbId": 8078, "title": "Alien Resurrection", "year": 1997, "isExisting": false, "isExcluded": true, "status": "released"},
			map[string]any{"tmdbId": 9999, "title": "Alien 9", "year": 0, "isExisting": false, "status": "announced"},
		},
	}})
	f.answer("GET /api/v3/queue", http.StatusOK, map[string]any{"totalRecords": 2, "records": []any{
		map[string]any{"id": 11, "title": "Alien.1979.1080p", "status": "downloading", "trackedDownloadState": "downloading", "trackedDownloadStatus": "ok"},
		map[string]any{
			"id": 12, "title": "Stub.2020.1080p", "status": "completed", "trackedDownloadState": "importBlocked", "trackedDownloadStatus": "warning",
			"movieId": 5, "movie": map[string]any{"title": "Stub", "year": 2020}, "statusMessages": []any{map[string]any{"title": "Stub.2020.1080p", "messages": []any{"No files found are eligible for import"}}},
		},
	}})
	cs := session(t, f, Options{})

	for audit, want := range map[string][]string{
		"audit_year_mismatch":    {"Dune"},
		"audit_runtime":          {"Interstellar"},
		"audit_removed":          {"Merged"},
		"audit_unmonitored":      {"Merged"},
		"audit_missing_metadata": {"Stub"},
		"audit_missing_files":    nil,
		"audit_language":         nil,
	} {
		if got := titles(mustCall(t, cs, audit, nil), "findings"); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", audit, got, want)
		}
	}

	gaps := mustCall(t, cs, "audit_collection_gaps", nil)
	if f := rowsOf(gaps["findings"]); len(f) != 1 || !strings.Contains(fmt.Sprint(f[0]["detail"]), "holds 1 of 2: missing Aliens (1986)") {
		t.Errorf("collection gaps (the excluded and the unreleased left out) = %v", gaps)
	}
	if got := rowsOf(mustCall(t, cs, "audit_collection_gaps", map[string]any{"include_unreleased": true})["findings"]); len(got) != 1 || !strings.Contains(fmt.Sprint(got[0]["detail"]), "Alien 9") {
		t.Errorf("collection gaps with the unreleased = %v", got)
	}

	queue := mustCall(t, cs, "audit_queue", nil)
	if f := rowsOf(queue["findings"]); len(f) != 1 || f[0]["queue_id"] != 12.0 || !strings.Contains(fmt.Sprint(f[0]["detail"]), "Stub (2020): importBlocked: No files found are eligible for import") {
		t.Errorf("audit_queue = %v", queue)
	}

	unmapped := mustCall(t, cs, "audit_unmapped_folders", nil)
	found := rowsOf(unmapped["findings"])
	if len(found) != 2 || found[0]["title"] != "Ronin" || found[0]["duplicate_of"] != nil || found[1]["duplicate_of"] != 1.0 ||
		!strings.Contains(fmt.Sprint(found[1]["detail"]), "a second copy of Alien (1979), which the library keeps at /media/movies/Alien (1979) (this one says Directors Cut)") {
		t.Errorf("audit_unmapped_folders = %v", unmapped)
	}

	// audit_all's count for each audit is what the audit itself reports
	all := mustCall(t, cs, "audit_all", nil)
	if skipped, ok := all["skipped"].([]any); !ok || len(skipped) != 2 {
		t.Errorf("audit_all ran a deep audit without deep: %v", all["skipped"])
	}
	total := 0
	for _, row := range rowsOf(all["audits"]) {
		name := fmt.Sprint(row["audit"])
		alone := mustCall(t, cs, name, nil)
		if alone["total_findings"] != row["total_findings"] {
			t.Errorf("audit_all says %s found %v, the audit says %v", name, row["total_findings"], alone["total_findings"])
		}
		n, ok := row["total_findings"].(float64)
		if !ok {
			t.Fatalf("audit_all's count for %s = %v", name, row["total_findings"])
		}
		total += int(n)
	}
	if all["total_findings"] != float64(total) {
		t.Errorf("audit_all total %v, the audits sum to %d", all["total_findings"], total)
	}
}

// Renaming off, Radarr gives no new file names, and audit_naming says so
// rather than reporting a clean library; folders are checked either way.
func TestAuditNaming(t *testing.T) {
	t.Parallel()

	for _, renaming := range []bool{false, true} {
		f := newFakeRadarr(t)
		wrong := movie(2, "Dune", 1984, "Bluray-1080p", "1920x1080", "2:16:00")
		wrong["path"] = "/media/movies/Dune (2021)"
		f.library(movie(1, "The Matrix", 1999, "Bluray-1080p", "1920x1080", "2:16:00"), wrong)
		f.answer("GET /api/v3/config/naming", http.StatusOK, map[string]any{"id": 1, "renameMovies": renaming})
		f.answer("GET /api/v3/rename", http.StatusOK, []any{map[string]any{"movieId": 1, "movieFileId": 100, "existingPath": "the.matrix.1999.mkv", "newPath": "The Matrix (1999) Bluray-1080p.mkv"}})
		f.answer("GET /api/v3/movie/1/folder", http.StatusOK, map[string]any{"folder": "The Matrix (1999)"})
		f.answer("GET /api/v3/movie/2/folder", http.StatusOK, map[string]any{"folder": "Dune (1984)"})
		cs := session(t, f, Options{})

		out := mustCall(t, cs, "audit_naming", nil)
		found := rowsOf(out["findings"])
		details := make([]string, 0, len(found))
		for _, r := range found {
			details = append(details, fmt.Sprint(r["detail"]))
		}
		folder := `folder "Dune (2021)" would be named "Dune (1984)"`
		file := `file "the.matrix.1999.mkv" would be named "The Matrix (1999) Bluray-1080p.mkv"`
		switch {
		case renaming && (!slices.Contains(details, folder) || !slices.Contains(details, file) || out["note"] != nil):
			t.Errorf("renaming on: %v", out)
		case !renaming && (!slices.Contains(details, folder) || slices.Contains(details, file) || !strings.Contains(fmt.Sprint(out["note"]), "renaming is switched off")):
			t.Errorf("renaming off: %v", out)
		}
		if len(f.requests(http.MethodGet, "/api/v3/rename")) > 0 != renaming {
			t.Errorf("renaming %v asked for previews %d times", renaming, len(f.requests(http.MethodGet, "/api/v3/rename")))
		}
		// folders false skips the one-request-a-film half
		mustCall(t, cs, "audit_naming", map[string]any{"folders": false})
		if n := len(f.requests(http.MethodGet, "/api/v3/movie/1/folder")); n != 1 {
			t.Errorf("folders false still read the folders (%d reads)", n)
		}
	}
}

// movie_add refuses a film the library already has, and a title that names
// more than one film, naming what it found.
func TestMovieAddRefuses(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"))
	f.answer("GET /api/v3/movie/lookup/tmdb", http.StatusOK, map[string]any{"title": "Alien", "year": 1979, "tmdbId": 10})
	f.answer("GET /api/v3/movie/lookup", http.StatusOK, []any{
		map[string]any{"title": "Dune", "year": 1984, "tmdbId": 841}, map[string]any{"title": "Dune", "year": 2021, "tmdbId": 438631},
	})
	cs := session(t, f, Options{})

	mustFail(t, cs, "movie_add", map[string]any{"movie": "tmdb:10", "quality_profile": "HD-1080p"}, "Alien (1979) is already in the library as id 1")
	mustFail(t, cs, "movie_add", map[string]any{"movie": "Dune", "quality_profile": "HD-1080p"}, `"Dune" names 2 films; add one by tmdb:<id>`)
	mustFail(t, cs, "movie_add", map[string]any{"movie": "Heat 1995", "quality_profile": "HD-1080p"}, `no film on TMDB is titled "Heat 1995"`)
	if n := len(f.requests(http.MethodPost, "/api/v3/movie")); n != 0 {
		t.Errorf("a refused add posted %d films", n)
	}
}

// An unlimited size is a null, which the model leaves out when the maximum
// is set to 0.
func TestQualityDefinitionEdit(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library()
	def := map[string]any{"id": 20, "title": "WEBDL-1080p", "minSize": 0, "maxSize": 100, "preferredSize": 95, "weight": 5, "quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}}
	f.answer("GET /api/v3/qualitydefinition", http.StatusOK, []any{def})
	f.answer("GET /api/v3/qualitydefinition/20", http.StatusOK, def)
	f.answer("PUT /api/v3/qualitydefinition/20", http.StatusAccepted, def)
	cs := session(t, f, Options{})

	mustCall(t, cs, "qualitydefinition_edit", map[string]any{"quality": "webdl-1080p", "min_size": 1.5, "max_size": 0})
	put := f.requests(http.MethodPut, "/api/v3/qualitydefinition/20")
	if len(put) != 1 || !strings.Contains(put[0].Body, `"minSize":1.5`) || strings.Contains(put[0].Body, "maxSize") || !strings.Contains(put[0].Body, `"preferredSize":95`) {
		t.Errorf("the definition sent = %v", put)
	}
	mustFail(t, cs, "qualitydefinition_edit", map[string]any{"quality": "Bluray-9000p", "min_size": 1}, `no quality "Bluray-9000p"`)
	mustFail(t, cs, "qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p"}, "nothing to change")
}

// The movie editor changes only what it is sent, and each tag operation is
// a call of its own.
func TestMovieEditSendsOnlyWhatChanges(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "Bluray-1080p", "1920x1080", "1:57:00"))
	f.answer("PUT /api/v3/movie/editor", http.StatusAccepted, []any{})
	cs := session(t, f, Options{})

	mustCall(t, cs, "movie_edit", map[string]any{"movie": "Alien", "monitored": false, "add_tags": []any{"4k"}})
	puts := f.requests(http.MethodPut, "/api/v3/movie/editor")
	if len(puts) != 2 {
		t.Fatalf("editor calls = %v", puts)
	}
	if !strings.Contains(puts[0].Body, `"monitored":false`) || strings.Contains(puts[0].Body, "qualityProfileId") || strings.Contains(puts[0].Body, "tags") {
		t.Errorf("the edit sent = %s", puts[0].Body)
	}
	if !strings.Contains(puts[1].Body, `"applyTags":"add"`) || !strings.Contains(puts[1].Body, `"tags":[1]`) {
		t.Errorf("the tag edit sent = %s", puts[1].Body)
	}
	mustFail(t, cs, "movie_edit", map[string]any{"movie": "Alien"}, "nothing to change")
	mustFail(t, cs, "movie_edit", map[string]any{"movie": "Alien", "quality_profile": "Ultra"}, `no quality profile "Ultra"; the profiles are HD-1080p (4)`)
}

// Radarr refuses to delete a tag anything carries, so tag_delete takes it
// off the films first and deletes it after; a tag a setting uses (an
// indexer here) is refused with what uses it, and nothing is sent.
func TestTagDelete(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library()
	f.answer("GET /api/v3/tag/detail", http.StatusOK, []any{
		map[string]any{"id": 1, "label": "4k", "movieIds": []int{3, 4}},
		map[string]any{"id": 2, "label": "limited", "indexerIds": []int{1}, "downloadClientIds": []int{1, 2}},
	})
	f.answer("PUT /api/v3/movie/editor", http.StatusAccepted, []any{})
	f.answer("DELETE /api/v3/tag/1", http.StatusOK, nil)
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "tag_delete", map[string]any{"tag": "4K"})
	if out["label"] != "4k" || out["movies"] != 2.0 {
		t.Errorf("tag_delete = %v", out)
	}
	puts := f.requests(http.MethodPut, "/api/v3/movie/editor")
	deletes := f.requests(http.MethodDelete, "/api/v3/tag/1")
	if len(puts) != 1 || !strings.Contains(puts[0].Body, `"movieIds":[3,4]`) || !strings.Contains(puts[0].Body, `"applyTags":"remove"`) || !strings.Contains(puts[0].Body, `"tags":[1]`) || len(deletes) != 1 {
		t.Errorf("the films were not untagged before the delete: editor %v, deletes %v", puts, deletes)
	}

	mustFail(t, cs, "tag_delete", map[string]any{"tag": "limited"}, `tag "limited" still limits 1 indexer, 2 download clients`)
	if n := len(f.requests(http.MethodDelete, "/api/v3/tag/2")); n != 0 {
		t.Errorf("a refused tag was sent for deletion %d times", n)
	}
}

// A label Radarr would refuse (anything but a-z, 0-9 and -) is refused
// before it is sent, with what Radarr allows.
func TestTagLabels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{{" Acceptance-2 ", "acceptance-2"}, {"UHD", "uhd"}} {
		if got, err := tagLabel(tc.in); err != nil || got != tc.want {
			t.Errorf("tagLabel(%q) = %q, %v", tc.in, got, err)
		}
	}
	for _, bad := range []string{"has space", "under_score", "4k!", "  "} {
		if _, err := tagLabel(bad); err == nil {
			t.Errorf("tagLabel(%q) accepted a label Radarr refuses", bad)
		}
	}

	f := newFakeRadarr(t)
	f.library()
	f.answer("GET /api/v3/tag/detail", http.StatusOK, []any{map[string]any{"id": 1, "label": "4k"}, map[string]any{"id": 2, "label": "uhd"}})
	cs := session(t, f, Options{})
	mustFail(t, cs, "tag_create", map[string]any{"label": "two words"}, "Radarr allows only a-z, 0-9 and -")
	mustFail(t, cs, "tag_rename", map[string]any{"tag": "4k", "label": "UHD"}, `tag "uhd" already exists (id 2)`)
	if n := len(f.requests(http.MethodPost, "/api/v3/tag")) + len(f.requests(http.MethodPut, "/api/v3/tag/1")); n != 0 {
		t.Errorf("a refused label was sent %d times", n)
	}
}

// Two calls tagging films with a new label at once both find it missing and
// both create it; Radarr refuses the second with a 409. The loser uses the
// tag the winner made rather than failing.
func TestTagCreatedByAnotherCall(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.answer("GET /api/v3/movie", http.StatusOK, []any{movie(1, "Alien", 1979, "", "", "")})
	f.answer("GET /api/v3/qualityprofile", http.StatusOK, []any{fakeProfile})
	f.answer("POST /api/v3/tag", http.StatusConflict, map[string]any{"message": "UNIQUE constraint failed: Tags.Label"})
	f.answer("PUT /api/v3/movie/editor", http.StatusAccepted, []any{})
	f.mux.HandleFunc("GET /api/v3/tag", func(w http.ResponseWriter, _ *http.Request) {
		tags := []any{map[string]any{"id": 1, "label": "4k"}}
		if len(f.requests(http.MethodPost, "/api/v3/tag")) > 0 {
			tags = append(tags, map[string]any{"id": 9, "label": "at-once"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tags)
	})
	cs := session(t, f, Options{})

	mustCall(t, cs, "movie_edit", map[string]any{"movie": "Alien", "add_tags": []any{"At-Once"}})
	puts := f.requests(http.MethodPut, "/api/v3/movie/editor")
	if len(puts) != 1 || !strings.Contains(puts[0].Body, `"tags":[9]`) {
		t.Errorf("the tag edit sent = %v", puts)
	}
}

// Radarr answers a delete of an exclusion that is not there with a 200, so
// exclusion_remove checks the ids first and says which are not there,
// rather than reporting them removed.
func TestExclusionRemoveChecksIDs(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library()
	f.answer("GET /api/v3/exclusions/paged", http.StatusOK, map[string]any{"totalRecords": 1, "records": []any{
		map[string]any{"id": 7, "tmdbId": 17431, "movieTitle": "Moon", "movieYear": 2009},
	}})
	f.answer("DELETE /api/v3/exclusions/7", http.StatusOK, nil)
	cs := session(t, f, Options{})

	mustFail(t, cs, "exclusion_remove", map[string]any{"ids": []any{7, 99}}, "no exclusion 99; exclusion_list lists them")
	if n := len(f.requests(http.MethodDelete, "/api/v3/exclusions/7")); n != 0 {
		t.Errorf("a partly wrong list still deleted %d", n)
	}
	out := mustCall(t, cs, "exclusion_remove", map[string]any{"ids": []any{7}})
	if removed := rowsOf(out["removed"]); len(removed) != 1 || removed[0]["title"] != "Moon" || removed[0]["year"] != 2009.0 {
		t.Errorf("exclusion_remove = %v", out)
	}
}
