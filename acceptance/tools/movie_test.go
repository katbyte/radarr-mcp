//go:build acceptance

package tools_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/radarr-mcp/lib/testenv"
)

// The scratch film: added, edited, moved, renamed and deleted by the tests
// that need a film they can do anything to, so the fixtures the audits read
// are never touched. Its file is a copy of a clean fixture's, laid out fresh
// by each test that adds it.
const (
	scratchTitle  = "Moon"
	scratchYear   = 2009
	scratchTmdb   = 17431
	scratchFolder = moviesRoot + "/Moon (2009)"
	scratchFile   = scratchFolder + "/Moon (2009) Bluray-1080p.mkv"
)

// addScratch lays out the scratch film's folder and adds it, removing it
// (and, when the test deleted nothing itself, its folder) afterwards.
func addScratch(t *testing.T) map[string]any {
	t.Helper()

	return addScratchAs(t, scratchFile)
}

// addScratchAs is addScratch with the film's file under a name of the test's
// own, for one that needs Radarr to find it named some other way. The file
// is laid out under that name from the start: one renamed on this machine
// after Radarr has seen it can still be there, for a moment, as the
// container sees its media, and Radarr then refuses to rename a file to a
// name it believes is taken.
func addScratchAs(t *testing.T, file string) map[string]any {
	t.Helper()

	skipUnlessReady(t)
	makeVideo(t, file, 97, "1920x1080")
	added := suite.Call(t, "movie_add", map[string]any{
		"movie": "tmdb:17431", "quality_profile": profileHD, "root_folder": moviesRoot, "folder": "Moon (2009)", "tags": []any{"scratch"},
	})
	t.Cleanup(func() {
		_, _ = suite.Invoke("movie_delete", map[string]any{"movie": "tmdb:17431", "delete_files": true, "confirm": true})
		_ = os.RemoveAll(testenv.HostPath(scratchFolder))
		_ = os.RemoveAll(testenv.HostPath(messyRoot + "/Moon (2009)"))
		_, _ = suite.Invoke("tag_delete", map[string]any{"tag": "scratch"})
	})

	return added
}

func TestMovieList(t *testing.T) {
	all := suite.Call(t, "movie_list", map[string]any{"limit": 500})
	if acctest.Num(t, all["total"], "total") != len(fixtures()) || len(acctest.Rows(t, all["movies"], "movies")) != len(fixtures()) {
		t.Fatalf("the library = %v films", all["total"])
	}
	// title order is Radarr's sort title, which files "The Matrix" under M
	rows := acctest.Rows(t, all["movies"], "movies")
	titles := make([]string, 0, len(rows))
	for _, row := range rows {
		titles = append(titles, acctest.Str(row["title"]))
	}
	if i, j := slices.Index(titles, "The Matrix"), slices.Index(titles, "Princess Mononoke"); i < 0 || j < 0 || i > j {
		t.Errorf("title order = %v", titles)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want []string
	}{
		{"part of a title", map[string]any{"query": "alien"}, []string{"Alien (1979)", "Alien Resurrection (1997)", "Aliens (1986)", "Alien³ (1992)"}},
		{"no file", map[string]any{"has_file": false}, []string{"Alien Resurrection (1997)", "Alien³ (1992)"}},
		{"unmonitored", map[string]any{"monitored": false}, []string{"Alien Resurrection (1997)"}},
		{"a profile", map[string]any{"quality_profile": profileUHD}, []string{"Blade Runner 2049 (2017)"}},
		{"a year range", map[string]any{"year_from": 2016, "year_to": 2021}, []string{"Arrival (2016)", "Blade Runner 2049 (2017)", "Dune (2021)"}},
		{"a genre", map[string]any{"genre": "animation", "root_folder": moviesRoot}, []string{"Princess Mononoke (1997)"}},
	} {
		out := suite.Call(t, "movie_list", tc.args)
		var got []string
		for _, row := range acctest.Rows(t, out["movies"], "movies") {
			got = append(got, titleYear(acctest.Str(row["title"]), acctest.Num(t, row["year"], "year")))
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}

	// by year, newest first, a page at a time
	page := suite.Call(t, "movie_list", map[string]any{"sort": "year", "descending": true, "limit": 2, "offset": 1})
	got := acctest.Rows(t, page["movies"], "movies")
	if len(got) != 2 || acctest.Str(got[0]["title"]) != "Dune" || acctest.Num(t, got[0]["year"], "year") != 2021 || acctest.Num(t, page["total"], "total") != len(fixtures()) {
		t.Errorf("the second and third newest = %v (total %v)", got, page["total"])
	}
	// the biggest file first
	biggest := 0
	for _, row := range acctest.Rows(t, all["movies"], "movies") {
		biggest = max(biggest, acctest.NumOr0(row["size_on_disk"]))
	}
	if first := acctest.Rows(t, suite.Call(t, "movie_list", map[string]any{"sort": "size", "descending": true, "limit": 1})["movies"], "movies")[0]; acctest.Num(t, first["size_on_disk"], "size_on_disk") != biggest {
		t.Errorf("the largest film = %v, want %d bytes", first, biggest)
	}

	for _, bad := range []map[string]any{{"quality_profile": "Nope"}, {"root_folder": "/nope"}, {"sort": "colour"}, {"status": "lost"}} {
		suite.CallErr(t, "movie_list", bad)
	}
}

func TestMovieGet(t *testing.T) {
	out := suite.Call(t, "movie_get", map[string]any{"movie": "Princess Mononoke"})
	if acctest.Str(out["original_language"]) != "Japanese" || acctest.Num(t, out["runtime"], "runtime") != 134 || acctest.Str(out["imdb_id"]) != "tt0119698" || !acctest.BoolOf(out["is_available"]) {
		t.Errorf("Princess Mononoke = %v", out)
	}
	file := acctest.Object(t, out["file"], "file")
	media := acctest.Object(t, file["media"], "media")
	if acctest.Str(file["quality"]) != "Bluray-1080p" || acctest.Str(file["relative_path"]) != "Princess Mononoke (1997) Bluray-1080p.mkv" ||
		acctest.Str(media["resolution"]) != "1920x1080" || acctest.Str(media["video_codec"]) != "x264" || !slices.Equal(acctest.Strs(t, acctest.OrEmptyList(media["audio_languages"]), "audio_languages"), []string{"jpn"}) {
		t.Errorf("its file = %v", file)
	}

	// a collection, ratings and alternate titles come from TMDB
	alien := suite.Call(t, "movie_get", map[string]any{"movie": "tmdb:348"})
	col := acctest.Object(t, alien["collection"], "collection")
	if acctest.Str(col["title"]) != "Alien Collection" || acctest.Num(t, col["tmdb_id"], "tmdb_id") != 8091 || acctest.Object(t, alien["ratings"], "ratings")["imdb"] == nil {
		t.Errorf("Alien = %v", alien)
	}

	// every way of naming a film finds the same one, and two films of one
	// title have to be told apart
	id := acctest.Num(t, alien["id"], "id")
	for _, ref := range []string{itoa(id), "tmdb:348", "imdb:tt0078748", "Alien", "alien (1979)"} {
		if got := suite.Call(t, "movie_get", map[string]any{"movie": ref}); acctest.Num(t, got["id"], "id") != id {
			t.Errorf("%q found %v", ref, got["id"])
		}
	}
	if msg := suite.CallErr(t, "movie_get", map[string]any{"movie": "Dune"}); !strings.Contains(msg, `"Dune" matches 2 films: Dune (1984) id`) || !strings.Contains(msg, "Dune (2021) id") {
		t.Errorf("two Dunes = %q", msg)
	}
	for ref, want := range map[string]string{
		"99999":      "no film in the library has id 99999",
		"tmdb:17431": "no film in the library has tmdb id 17431",
		"Moon":       `no film in the library is titled "Moon"`,
	} {
		if msg := suite.CallErr(t, "movie_get", map[string]any{"movie": ref}); !strings.Contains(msg, want) {
			t.Errorf("%q = %q, want %q", ref, msg, want)
		}
	}
}

func TestMovieLookup(t *testing.T) {
	out := suite.Call(t, "movie_lookup", map[string]any{"term": "tmdb:17431"})
	res := acctest.Rows(t, out["results"], "results")
	if len(res) != 1 || acctest.Str(res[0]["title"]) != scratchTitle || acctest.Num(t, res[0]["year"], "year") != scratchYear || !acctest.IsBool(res[0]["in_library"], false) ||
		acctest.Num(t, res[0]["runtime"], "runtime") != 97 || acctest.Str(res[0]["imdb_id"]) != "tt1182345" {
		t.Fatalf("tmdb:17431 = %v", res)
	}
	byImdb := acctest.Rows(t, suite.Call(t, "movie_lookup", map[string]any{"term": "imdb:tt0078748"})["results"], "results")
	if len(byImdb) != 1 || acctest.Str(byImdb[0]["title"]) != "Alien" || !acctest.BoolOf(byImdb[0]["in_library"]) || acctest.Num(t, byImdb[0]["library_id"], "library_id") != movieID(t, "Alien", 1979) {
		t.Errorf("imdb:tt0078748 = %v", byImdb)
	}
	dune := acctest.Rows(t, suite.Call(t, "movie_lookup", map[string]any{"term": "Dune", "limit": 20})["results"], "results")
	var years []int
	for _, r := range dune {
		year := acctest.Num(t, r["year"], "year")
		if acctest.Str(r["title"]) == "Dune" && (year == 1984 || year == 2021) {
			years = append(years, year)
			if !acctest.BoolOf(r["in_library"]) {
				t.Errorf("both Dunes are in the library: %v", r)
			}
		}
	}
	if !slices.Contains(years, 1984) || !slices.Contains(years, 2021) {
		t.Errorf("a title search for Dune = %v", years)
	}
	if n := len(acctest.Rows(t, suite.Call(t, "movie_lookup", map[string]any{"term": "Dune", "limit": 2})["results"], "results")); n != 2 {
		t.Errorf("limit 2 = %d results", n)
	}
	suite.CallErr(t, "movie_lookup", map[string]any{"term": "tmdb:not-a-number"})
}

func TestMovieCredits(t *testing.T) {
	out := suite.Call(t, "movie_credits", map[string]any{"movie": "Alien (1979)", "limit": 5})
	cast := acctest.Rows(t, out["cast"], "cast")
	crew := acctest.Rows(t, out["crew"], "crew")
	if len(cast) != 5 || acctest.Str(cast[0]["name"]) != "Tom Skerritt" && acctest.Str(cast[0]["name"]) != "Sigourney Weaver" {
		t.Errorf("cast = %v", cast)
	}
	if len(crew) == 0 || acctest.Str(crew[0]["job"]) != "Director" || acctest.Str(crew[0]["name"]) != "Ridley Scott" {
		t.Errorf("crew = %v, want the director first", crew)
	}
	if onlyCast := suite.Call(t, "movie_credits", map[string]any{"movie": "Alien (1979)", "type": "cast"}); onlyCast["crew"] != nil {
		t.Errorf("type cast listed crew: %v", onlyCast["crew"])
	}
	suite.CallErr(t, "movie_credits", map[string]any{"movie": "Alien (1979)", "type": "extras"})
}

// A film added into a folder already on disk takes the file there, and is
// answered with it.
func TestMovieAdd(t *testing.T) {
	added := addScratch(t)
	file := acctest.Object(t, added["file"], "file")
	if acctest.Str(added["title"]) != scratchTitle || acctest.Str(added["path"]) != scratchFolder || acctest.Str(added["quality_profile"]) != profileHD ||
		!slices.Equal(acctest.Strs(t, acctest.OrEmptyList(added["tags"]), "tags"), []string{"scratch"}) || acctest.Str(file["relative_path"]) != "Moon (2009) Bluray-1080p.mkv" {
		t.Errorf("the added film = %v", added)
	}

	// adding it again is refused, naming where it is
	if msg := suite.CallErr(t, "movie_add", map[string]any{"movie": "tmdb:17431", "quality_profile": profileHD, "root_folder": moviesRoot}); !strings.Contains(msg, "Moon (2009) is already in the library as id") {
		t.Errorf("a second add = %q", msg)
	}
	// Ronin is on disk but not in the library, so each refusal is its own
	for args, want := range map[string]map[string]any{
		`missing properties: ["quality_profile"]`: {"movie": "tmdb:8195"},
		`no quality profile "Best"`:               {"movie": "tmdb:8195", "quality_profile": "Best"},
		"so name one":                             {"movie": "tmdb:8195", "quality_profile": profileHD},
		`no root folder "/elsewhere"`:             {"movie": "tmdb:8195", "quality_profile": profileHD, "root_folder": "/elsewhere"},
	} {
		if msg := suite.CallErr(t, "movie_add", want); !strings.Contains(msg, args) {
			t.Errorf("movie_add %v = %q, want %q", want, msg, args)
		}
	}
}

// movie_edit changes only what it is given, and each change is read back
// from Radarr.
func TestMovieEdit(t *testing.T) {
	addScratch(t)
	edit := func(args map[string]any) map[string]any {
		t.Helper()
		args["movie"] = "Moon"
		out := suite.Call(t, "movie_edit", args)
		movies := acctest.Rows(t, out["movies"], "movies")
		if len(movies) != 1 {
			t.Fatalf("movie_edit answered %v", out)
		}
		return movies[0]
	}

	if got := edit(map[string]any{"monitored": false}); !acctest.IsBool(got["monitored"], false) || acctest.Str(got["quality_profile"]) != profileHD {
		t.Errorf("unmonitored = %v", got)
	}
	if got := edit(map[string]any{"quality_profile": profileUHD, "minimum_availability": "inCinemas"}); acctest.Str(got["quality_profile"]) != profileUHD || !acctest.IsBool(got["monitored"], false) {
		t.Errorf("a profile change = %v", got)
	}
	if got := suite.Call(t, "movie_get", map[string]any{"movie": "Moon"}); acctest.Str(got["minimum_availability"]) != "inCinemas" {
		t.Errorf("the minimum availability = %v", got["minimum_availability"])
	}
	if got := edit(map[string]any{"add_tags": []any{"edit-a", "edit-b"}}); !slices.Equal(acctest.Strs(t, acctest.OrEmptyList(got["tags"]), "tags"), []string{"edit-a", "edit-b", "scratch"}) {
		t.Errorf("tags added = %v", got["tags"])
	}
	if got := edit(map[string]any{"remove_tags": []any{"edit-a"}}); !slices.Equal(acctest.Strs(t, acctest.OrEmptyList(got["tags"]), "tags"), []string{"edit-b", "scratch"}) {
		t.Errorf("a tag removed = %v", got["tags"])
	}
	if got := edit(map[string]any{"tags": []any{"scratch"}}); !slices.Equal(acctest.Strs(t, acctest.OrEmptyList(got["tags"]), "tags"), []string{"scratch"}) {
		t.Errorf("tags replaced = %v", got["tags"])
	}
	for _, label := range []string{"edit-a", "edit-b"} {
		_, _ = suite.Invoke("tag_delete", map[string]any{"tag": label})
	}

	// a move of root folder moves the folder on disk
	moved := edit(map[string]any{"root_folder": messyRoot})
	if acctest.Str(moved["path"]) != messyRoot+"/Moon (2009)" {
		t.Errorf("moved to %v", moved["path"])
	}
	if _, err := os.Stat(testenv.HostPath(messyRoot + "/Moon (2009)/Moon (2009) Bluray-1080p.mkv")); err != nil {
		t.Errorf("the file did not move: %v", err)
	}
	if _, err := os.Stat(testenv.HostPath(scratchFolder)); !os.IsNotExist(err) {
		t.Errorf("the old folder is still there: %v", err)
	}
	back := edit(map[string]any{"root_folder": moviesRoot})
	if acctest.Str(back["path"]) != scratchFolder || !acctest.BoolOf(back["has_file"]) {
		t.Errorf("moved back = %v", back)
	}

	if msg := suite.CallErr(t, "movie_edit", map[string]any{"movie": "Moon"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an empty edit = %q", msg)
	}
	if msg := suite.CallErr(t, "movie_edit", map[string]any{"movie": "Moon", "minimum_availability": "soon"}); !strings.Contains(msg, "want announced, inCinemas or released") {
		t.Errorf("a bad availability = %q", msg)
	}
}

// movie_batch_edit makes one change to many films; add_tags and remove_tags
// change each film's own tags, leaving the rest.
func TestMovieBatchEdit(t *testing.T) {
	films := []any{"Alien (1979)", "Aliens (1986)", "tmdb:8077"}
	suite.Call(t, "movie_edit", map[string]any{"movie": "Aliens (1986)", "add_tags": []any{"batch-own"}})
	t.Cleanup(func() {
		_, _ = suite.Invoke("movie_batch_edit", map[string]any{"movies": films, "remove_tags": []any{"batch-own", "batch-all"}, "monitored": true})
		_, _ = suite.Invoke("tag_delete", map[string]any{"tag": "batch-own"})
		_, _ = suite.Invoke("tag_delete", map[string]any{"tag": "batch-all"})
	})

	out := suite.Call(t, "movie_batch_edit", map[string]any{"movies": films, "add_tags": []any{"batch-all"}, "monitored": false})
	got := acctest.Rows(t, out["movies"], "movies")
	if len(got) != 3 {
		t.Fatalf("batch = %v", out)
	}
	for _, row := range got {
		tags := acctest.Strs(t, acctest.OrEmptyList(row["tags"]), "tags")
		want := []string{"batch-all"}
		if acctest.Str(row["title"]) == "Aliens" {
			want = []string{"batch-all", "batch-own"}
		}
		if !slices.Equal(tags, want) || !acctest.IsBool(row["monitored"], false) {
			t.Errorf("%v = monitored %v, tags %v; want %v", row["title"], row["monitored"], tags, want)
		}
	}
	if msg := suite.CallErr(t, "movie_batch_edit", map[string]any{"movies": []any{"Alien (1979)", "Nothing At All"}, "monitored": true}); !strings.Contains(msg, `"Nothing At All"`) {
		t.Errorf("a batch naming a missing film = %q", msg)
	}
}

// A refresh reads TMDB again and a rescan reads the folder again: a file
// taken away is dropped, and one put back is picked up.
func TestMovieRefreshAndRescan(t *testing.T) {
	addScratch(t)
	out := suite.Call(t, "movie_refresh", map[string]any{"movies": []any{"Moon"}})
	if cmd := acctest.Rows(t, out["commands"], "commands"); len(cmd) != 1 || acctest.Str(cmd[0]["name"]) != "RefreshMovie" || acctest.Str(cmd[0]["status"]) != "completed" {
		t.Errorf("refresh = %v", out)
	}

	aside := testenv.HostPath(scratchFile) + ".aside"
	if err := os.Rename(testenv.HostPath(scratchFile), aside); err != nil {
		t.Fatal(err)
	}
	out = suite.Call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}})
	if movies := acctest.Rows(t, out["movies"], "movies"); !acctest.IsBool(movies[0]["has_file"], false) {
		t.Errorf("a rescan of an empty folder kept the file: %v", movies[0])
	}
	if err := os.Rename(aside, testenv.HostPath(scratchFile)); err != nil {
		t.Fatal(err)
	}
	out = suite.Call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}})
	if movies := acctest.Rows(t, out["movies"], "movies"); !acctest.BoolOf(movies[0]["has_file"]) || acctest.Str(movies[0]["quality"]) != "Bluray-1080p" {
		t.Errorf("a rescan did not pick the file up again: %v", movies[0])
	}

	// no wait: the command is started and answered as it stands
	quick := suite.Call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}, "wait": -1})
	if st := acctest.Str(acctest.Rows(t, quick["commands"], "commands")[0]["status"]); st == "" {
		t.Errorf("an unwaited rescan = %v", quick)
	}
}

// movie_rename previews by default, renames with preview false, and says so
// when Radarr's renaming is off.
func TestMovieRename(t *testing.T) {
	// the scratch film's file under a scene name, which the scheme renames
	scene := scratchFolder + "/moon.2009.1080p.bluray.x264-GRP.mkv"
	addScratchAs(t, scene)

	off := suite.Call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}})
	if len(acctest.Rows(t, off["renames"], "renames")) != 0 || !strings.Contains(acctest.Str(off["note"]), "renaming is switched off") {
		t.Errorf("with renaming off = %v", off)
	}

	suite.Call(t, "naming_edit", map[string]any{"rename_movies": true})
	t.Cleanup(func() { _, _ = suite.Invoke("naming_edit", map[string]any{"rename_movies": false}) })

	preview := suite.Call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}})
	renames := acctest.Rows(t, preview["renames"], "renames")
	if len(renames) != 1 || acctest.Str(renames[0]["existing"]) != "moon.2009.1080p.bluray.x264-GRP.mkv" || acctest.Str(renames[0]["new"]) != "Moon (2009) Bluray-1080p.mkv" || !acctest.IsBool(preview["renamed"], false) {
		t.Fatalf("the preview = %v", preview)
	}
	if _, err := os.Stat(testenv.HostPath(scene)); err != nil {
		t.Fatalf("a preview renamed the file: %v", err)
	}

	done := suite.Call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}, "preview": false})
	if !acctest.BoolOf(done["renamed"]) {
		t.Errorf("the rename = %v", done)
	}
	if _, err := os.Stat(testenv.HostPath(scratchFile)); err != nil {
		t.Errorf("the file was not renamed: %v", err)
	}
	if again := suite.Call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}}); len(acctest.Rows(t, again["renames"], "renames")) != 0 {
		t.Errorf("a renamed file still has a rename: %v", again)
	}
}

// moviefile_edit corrects what Radarr believes about a file, and Radarr
// decides the file's standing from it: graded 720p, it falls under its
// profile's cutoff.
func TestMovieFileEdit(t *testing.T) {
	addScratch(t)
	out := suite.Call(t, "moviefile_edit", map[string]any{
		"movie": "Moon", "quality": "Bluray-720p", "languages": []any{"English", "Japanese"}, "release_group": "FIXED", "edition": "Director's Cut",
	})
	file := acctest.Object(t, out["file"], "file")
	if acctest.Str(file["quality"]) != "Bluray-720p" || !slices.Equal(acctest.Strs(t, acctest.OrEmptyList(file["languages"]), "languages"), []string{"English", "Japanese"}) ||
		acctest.Str(file["release_group"]) != "FIXED" || acctest.Str(file["edition"]) != "Director's Cut" || !acctest.BoolOf(file["cutoff_not_met"]) {
		t.Errorf("the edited file = %v", file)
	}
	if got := findings(t, suite.Call(t, "audit_cutoff_unmet", map[string]any{"tag": "scratch"})); !slices.Equal(got, []string{"Moon (2009)"}) {
		t.Errorf("the regraded file's cutoff = %v", got)
	}
	back := acctest.Object(t, suite.Call(t, "moviefile_edit", map[string]any{"movie": "Moon", "quality": "Bluray-1080p"})["file"], "file")
	if acctest.Str(back["quality"]) != "Bluray-1080p" || !acctest.IsBool(back["cutoff_not_met"], false) || acctest.Str(back["release_group"]) != "FIXED" {
		t.Errorf("graded back = %v", back)
	}

	for args, want := range map[string]map[string]any{
		`no quality "Bluray-9000p"`: {"movie": "Moon", "quality": "Bluray-9000p"},
		`no language "Klingon"`:     {"movie": "Moon", "languages": []any{"Klingon"}},
		"nothing to change":         {"movie": "Moon"},
		"has no file":               {"movie": "tmdb:8077", "quality": "DVD"},
	} {
		if msg := suite.CallErr(t, "moviefile_edit", want); !strings.Contains(msg, args) {
			t.Errorf("moviefile_edit %v = %q, want %q", want, msg, args)
		}
	}
}

// moviefile_delete takes the file off disk and leaves the film, missing, and
// only once it is confirmed: before that it says which file it would take.
func TestMovieFileDelete(t *testing.T) {
	addScratch(t)
	would := suite.Call(t, "moviefile_delete", map[string]any{"movie": "Moon"})
	if file := acctest.Object(t, would["would_delete"], "would_delete"); acctest.Str(file["relative_path"]) != "Moon (2009) Bluray-1080p.mkv" || would["deleted"] != nil {
		t.Errorf("without confirm = %v", would)
	}
	// the film is monitored and Radarr is not set to unmonitor a film whose file is deleted, so it would fetch it again: the answer says so first
	if !strings.Contains(acctest.Str(would["warning"]), "downloads the film again") {
		t.Errorf("the warning for a monitored film = %q", would["warning"])
	}
	if _, err := os.Stat(testenv.HostPath(scratchFile)); err != nil {
		t.Fatalf("a delete that was not confirmed took the file: %v", err)
	}

	out := suite.Call(t, "moviefile_delete", map[string]any{"movie": "Moon", "confirm": true})
	if file := acctest.Object(t, out["deleted"], "deleted"); acctest.Str(file["relative_path"]) != "Moon (2009) Bluray-1080p.mkv" || out["would_delete"] != nil {
		t.Errorf("deleted = %v", out)
	}
	if _, err := os.Stat(testenv.HostPath(scratchFile)); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk: %v", err)
	}
	after := suite.Call(t, "movie_get", map[string]any{"movie": "Moon"})
	if !acctest.IsBool(after["has_file"], false) || after["file"] != nil {
		t.Errorf("the film after = %v", after)
	}
	if got := findings(t, suite.Call(t, "audit_missing_files", map[string]any{"tag": "scratch"})); !slices.Equal(got, []string{"Moon (2009)"}) {
		t.Errorf("the film is not missing: %v", got)
	}
	if msg := suite.CallErr(t, "moviefile_delete", map[string]any{"movie": "Moon", "confirm": true}); !strings.Contains(msg, "has no file") {
		t.Errorf("a second delete = %q", msg)
	}
}

// movie_delete removes the film, and its folder only when asked, and only
// once it is confirmed: before that it says what it would remove.
func TestMovieDelete(t *testing.T) {
	addScratch(t)
	would := suite.Call(t, "movie_delete", map[string]any{"movie": "Moon", "delete_files": true})
	if film := acctest.Object(t, would["would_delete"], "would_delete"); acctest.Str(film["title"]) != scratchTitle || would["deleted"] != nil || !acctest.IsBool(would["files_deleted"], false) || !strings.Contains(acctest.Str(would["files"]), scratchFolder) {
		t.Errorf("without confirm = %v", would)
	}
	if _, err := os.Stat(testenv.HostPath(scratchFile)); err != nil {
		t.Fatalf("a delete that was not confirmed took the file: %v", err)
	}
	suite.Call(t, "movie_get", map[string]any{"movie": "tmdb:17431"})

	out := suite.Call(t, "movie_delete", map[string]any{"movie": "Moon", "confirm": true})
	if deleted := acctest.Object(t, out["deleted"], "deleted"); acctest.Str(deleted["title"]) != scratchTitle || !acctest.IsBool(out["files_deleted"], false) || out["would_delete"] != nil {
		t.Errorf("deleted = %v", out)
	}
	if _, err := os.Stat(testenv.HostPath(scratchFile)); err != nil {
		t.Errorf("a delete without delete_files took the file: %v", err)
	}
	suite.CallErr(t, "movie_get", map[string]any{"movie": "tmdb:17431"})

	// back, then gone with its files and onto the exclusions
	suite.Call(t, "movie_add", map[string]any{"movie": "tmdb:17431", "quality_profile": profileHD, "root_folder": moviesRoot, "folder": "Moon (2009)"})
	out = suite.Call(t, "movie_delete", map[string]any{"movie": "Moon", "delete_files": true, "add_exclusion": true, "confirm": true})
	if !acctest.BoolOf(out["files_deleted"]) || !acctest.BoolOf(out["excluded"]) {
		t.Errorf("deleted with files = %v", out)
	}
	// Radarr deletes the folder and adds the exclusion in event handlers of
	// their own, a moment after the delete has answered
	if !acctest.EventuallyWithin(time.Minute, func() bool {
		_, err := os.Stat(testenv.HostPath(scratchFolder))
		return os.IsNotExist(err)
	}) {
		t.Error("the folder is still on disk a minute after the delete")
	}
	var excluded int
	acctest.EventuallyWithin(time.Minute, func() bool {
		for _, e := range acctest.Rows(t, suite.Call(t, "exclusion_list", nil)["exclusions"], "exclusions") {
			if acctest.Num(t, e["tmdb_id"], "tmdb_id") == scratchTmdb {
				excluded = acctest.NumOr0(e["id"])
			}
		}
		return excluded != 0
	})
	if excluded == 0 {
		t.Fatal("the film is not on the exclusions")
	}
	suite.Call(t, "exclusion_remove", map[string]any{"ids": []any{excluded}})

	// the folder the test laid out is gone, so nothing is left to clean up
	if entries, err := os.ReadDir(filepath.Dir(testenv.HostPath(scratchFolder))); err == nil {
		for _, e := range entries {
			if e.Name() == "Moon (2009)" {
				t.Errorf("Moon (2009) is still in %s", moviesRoot)
			}
		}
	}
}
