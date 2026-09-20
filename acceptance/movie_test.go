//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

	skipUnlessReady(t)
	makeVideo(t, scratchFile, 97, "1920x1080")
	added := call(t, "movie_add", map[string]any{
		"movie": "tmdb:17431", "quality_profile": profileHD, "root_folder": moviesRoot, "folder": "Moon (2009)", "tags": []any{"scratch"},
	})
	t.Cleanup(func() {
		_, _ = invoke("movie_delete", map[string]any{"movie": "tmdb:17431", "delete_files": true})
		_ = os.RemoveAll(hostPath(scratchFolder))
		_ = os.RemoveAll(hostPath(messyRoot + "/Moon (2009)"))
		_, _ = invoke("tag_delete", map[string]any{"tag": "scratch"})
	})

	return added
}

func TestMovieList(t *testing.T) {
	all := call(t, "movie_list", map[string]any{"limit": 500})
	if num(t, all["total"], "total") != len(fixtures()) || len(rows(t, all["movies"], "movies")) != len(fixtures()) {
		t.Fatalf("the library = %v films", all["total"])
	}
	// title order is Radarr's sort title, which files "The Matrix" under M
	var titles []string
	for _, row := range rows(t, all["movies"], "movies") {
		titles = append(titles, str(row["title"]))
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
		out := call(t, "movie_list", tc.args)
		var got []string
		for _, row := range rows(t, out["movies"], "movies") {
			got = append(got, titleYear(str(row["title"]), num(t, row["year"], "year")))
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}

	// by year, newest first, a page at a time
	page := call(t, "movie_list", map[string]any{"sort": "year", "descending": true, "limit": 2, "offset": 1})
	got := rows(t, page["movies"], "movies")
	if len(got) != 2 || str(got[0]["title"]) != "Dune" || num(t, got[0]["year"], "year") != 2021 || num(t, page["total"], "total") != len(fixtures()) {
		t.Errorf("the second and third newest = %v (total %v)", got, page["total"])
	}
	// the biggest file first
	biggest := 0
	for _, row := range rows(t, all["movies"], "movies") {
		biggest = max(biggest, int(num0(row["size_on_disk"])))
	}
	if first := rows(t, call(t, "movie_list", map[string]any{"sort": "size", "descending": true, "limit": 1})["movies"], "movies")[0]; num(t, first["size_on_disk"], "size_on_disk") != biggest {
		t.Errorf("the largest film = %v, want %d bytes", first, biggest)
	}

	for _, bad := range []map[string]any{{"quality_profile": "Nope"}, {"root_folder": "/nope"}, {"sort": "colour"}, {"status": "lost"}} {
		callErr(t, "movie_list", bad)
	}
}

func TestMovieGet(t *testing.T) {
	out := call(t, "movie_get", map[string]any{"movie": "Princess Mononoke"})
	if str(out["original_language"]) != "Japanese" || num(t, out["runtime"], "runtime") != 134 || str(out["imdb_id"]) != "tt0119698" || out["is_available"] != true {
		t.Errorf("Princess Mononoke = %v", out)
	}
	file := object(t, out["file"], "file")
	media := object(t, file["media"], "media")
	if str(file["quality"]) != "Bluray-1080p" || str(file["relative_path"]) != "Princess Mononoke (1997) Bluray-1080p.mkv" ||
		str(media["resolution"]) != "1920x1080" || str(media["video_codec"]) != "x264" || !slices.Equal(strs(t, media["audio_languages"], "audio_languages"), []string{"jpn"}) {
		t.Errorf("its file = %v", file)
	}

	// a collection, ratings and alternate titles come from TMDB
	alien := call(t, "movie_get", map[string]any{"movie": "tmdb:348"})
	col := object(t, alien["collection"], "collection")
	if str(col["title"]) != "Alien Collection" || num(t, col["tmdb_id"], "tmdb_id") != 8091 || object(t, alien["ratings"], "ratings")["imdb"] == nil {
		t.Errorf("Alien = %v", alien)
	}

	// every way of naming a film finds the same one, and two films of one
	// title have to be told apart
	id := num(t, alien["id"], "id")
	for _, ref := range []string{itoa(id), "tmdb:348", "imdb:tt0078748", "Alien", "alien (1979)"} {
		if got := call(t, "movie_get", map[string]any{"movie": ref}); num(t, got["id"], "id") != id {
			t.Errorf("%q found %v", ref, got["id"])
		}
	}
	if msg := callErr(t, "movie_get", map[string]any{"movie": "Dune"}); !strings.Contains(msg, `"Dune" matches 2 films: Dune (1984) id`) || !strings.Contains(msg, "Dune (2021) id") {
		t.Errorf("two Dunes = %q", msg)
	}
	for ref, want := range map[string]string{
		"99999":      "no film in the library has id 99999",
		"tmdb:17431": "no film in the library has tmdb id 17431",
		"Moon":       `no film in the library is titled "Moon"`,
	} {
		if msg := callErr(t, "movie_get", map[string]any{"movie": ref}); !strings.Contains(msg, want) {
			t.Errorf("%q = %q, want %q", ref, msg, want)
		}
	}
}

func TestMovieLookup(t *testing.T) {
	out := call(t, "movie_lookup", map[string]any{"term": "tmdb:17431"})
	res := rows(t, out["results"], "results")
	if len(res) != 1 || str(res[0]["title"]) != scratchTitle || num(t, res[0]["year"], "year") != scratchYear || res[0]["in_library"] != false ||
		num(t, res[0]["runtime"], "runtime") != 97 || str(res[0]["imdb_id"]) != "tt1182345" {
		t.Fatalf("tmdb:17431 = %v", res)
	}
	byImdb := rows(t, call(t, "movie_lookup", map[string]any{"term": "imdb:tt0078748"})["results"], "results")
	if len(byImdb) != 1 || str(byImdb[0]["title"]) != "Alien" || byImdb[0]["in_library"] != true || num(t, byImdb[0]["library_id"], "library_id") != movieID(t, "Alien", 1979) {
		t.Errorf("imdb:tt0078748 = %v", byImdb)
	}
	dune := rows(t, call(t, "movie_lookup", map[string]any{"term": "Dune", "limit": 20})["results"], "results")
	var years []int
	for _, r := range dune {
		year := num(t, r["year"], "year")
		if str(r["title"]) == "Dune" && (year == 1984 || year == 2021) {
			years = append(years, year)
			if r["in_library"] != true {
				t.Errorf("both Dunes are in the library: %v", r)
			}
		}
	}
	if !slices.Contains(years, 1984) || !slices.Contains(years, 2021) {
		t.Errorf("a title search for Dune = %v", years)
	}
	if n := len(rows(t, call(t, "movie_lookup", map[string]any{"term": "Dune", "limit": 2})["results"], "results")); n != 2 {
		t.Errorf("limit 2 = %d results", n)
	}
	callErr(t, "movie_lookup", map[string]any{"term": "tmdb:not-a-number"})
}

func TestMovieCredits(t *testing.T) {
	out := call(t, "movie_credits", map[string]any{"movie": "Alien (1979)", "limit": 5})
	cast := rows(t, out["cast"], "cast")
	crew := rows(t, out["crew"], "crew")
	if len(cast) != 5 || str(cast[0]["name"]) != "Tom Skerritt" && str(cast[0]["name"]) != "Sigourney Weaver" {
		t.Errorf("cast = %v", cast)
	}
	if len(crew) == 0 || str(crew[0]["job"]) != "Director" || str(crew[0]["name"]) != "Ridley Scott" {
		t.Errorf("crew = %v, want the director first", crew)
	}
	if onlyCast := call(t, "movie_credits", map[string]any{"movie": "Alien (1979)", "type": "cast"}); onlyCast["crew"] != nil {
		t.Errorf("type cast listed crew: %v", onlyCast["crew"])
	}
	callErr(t, "movie_credits", map[string]any{"movie": "Alien (1979)", "type": "extras"})
}

// A film added into a folder already on disk takes the file there, and is
// answered with it.
func TestMovieAdd(t *testing.T) {
	added := addScratch(t)
	file := object(t, added["file"], "file")
	if str(added["title"]) != scratchTitle || str(added["path"]) != scratchFolder || str(added["quality_profile"]) != profileHD ||
		!slices.Equal(strs(t, added["tags"], "tags"), []string{"scratch"}) || str(file["relative_path"]) != "Moon (2009) Bluray-1080p.mkv" {
		t.Errorf("the added film = %v", added)
	}

	// adding it again is refused, naming where it is
	if msg := callErr(t, "movie_add", map[string]any{"movie": "tmdb:17431", "quality_profile": profileHD, "root_folder": moviesRoot}); !strings.Contains(msg, "Moon (2009) is already in the library as id") {
		t.Errorf("a second add = %q", msg)
	}
	// Ronin is on disk but not in the library, so each refusal is its own
	for args, want := range map[string]map[string]any{
		`missing properties: ["quality_profile"]`: {"movie": "tmdb:8195"},
		`no quality profile "Best"`:               {"movie": "tmdb:8195", "quality_profile": "Best"},
		"so name one":                             {"movie": "tmdb:8195", "quality_profile": profileHD},
		`no root folder "/elsewhere"`:             {"movie": "tmdb:8195", "quality_profile": profileHD, "root_folder": "/elsewhere"},
	} {
		if msg := callErr(t, "movie_add", want); !strings.Contains(msg, args) {
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
		out := call(t, "movie_edit", args)
		movies := rows(t, out["movies"], "movies")
		if len(movies) != 1 {
			t.Fatalf("movie_edit answered %v", out)
		}
		return movies[0]
	}

	if got := edit(map[string]any{"monitored": false}); got["monitored"] != false || str(got["quality_profile"]) != profileHD {
		t.Errorf("unmonitored = %v", got)
	}
	if got := edit(map[string]any{"quality_profile": profileUHD, "minimum_availability": "inCinemas"}); str(got["quality_profile"]) != profileUHD || got["monitored"] != false {
		t.Errorf("a profile change = %v", got)
	}
	if got := call(t, "movie_get", map[string]any{"movie": "Moon"}); str(got["minimum_availability"]) != "inCinemas" {
		t.Errorf("the minimum availability = %v", got["minimum_availability"])
	}
	if got := edit(map[string]any{"add_tags": []any{"edit-a", "edit-b"}}); !slices.Equal(strs(t, got["tags"], "tags"), []string{"edit-a", "edit-b", "scratch"}) {
		t.Errorf("tags added = %v", got["tags"])
	}
	if got := edit(map[string]any{"remove_tags": []any{"edit-a"}}); !slices.Equal(strs(t, got["tags"], "tags"), []string{"edit-b", "scratch"}) {
		t.Errorf("a tag removed = %v", got["tags"])
	}
	if got := edit(map[string]any{"tags": []any{"scratch"}}); !slices.Equal(strs(t, got["tags"], "tags"), []string{"scratch"}) {
		t.Errorf("tags replaced = %v", got["tags"])
	}
	for _, label := range []string{"edit-a", "edit-b"} {
		_, _ = invoke("tag_delete", map[string]any{"tag": label})
	}

	// a move of root folder moves the folder on disk
	moved := edit(map[string]any{"root_folder": messyRoot})
	if str(moved["path"]) != messyRoot+"/Moon (2009)" {
		t.Errorf("moved to %v", moved["path"])
	}
	if _, err := os.Stat(hostPath(messyRoot + "/Moon (2009)/Moon (2009) Bluray-1080p.mkv")); err != nil {
		t.Errorf("the file did not move: %v", err)
	}
	if _, err := os.Stat(hostPath(scratchFolder)); !os.IsNotExist(err) {
		t.Errorf("the old folder is still there: %v", err)
	}
	back := edit(map[string]any{"root_folder": moviesRoot})
	if str(back["path"]) != scratchFolder || back["has_file"] != true {
		t.Errorf("moved back = %v", back)
	}

	if msg := callErr(t, "movie_edit", map[string]any{"movie": "Moon"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an empty edit = %q", msg)
	}
	if msg := callErr(t, "movie_edit", map[string]any{"movie": "Moon", "minimum_availability": "soon"}); !strings.Contains(msg, "want announced, inCinemas or released") {
		t.Errorf("a bad availability = %q", msg)
	}
}

// movie_batch_edit makes one change to many films; add_tags and remove_tags
// change each film's own tags, leaving the rest.
func TestMovieBatchEdit(t *testing.T) {
	films := []any{"Alien (1979)", "Aliens (1986)", "tmdb:8077"}
	call(t, "movie_edit", map[string]any{"movie": "Aliens (1986)", "add_tags": []any{"batch-own"}})
	t.Cleanup(func() {
		_, _ = invoke("movie_batch_edit", map[string]any{"movies": films, "remove_tags": []any{"batch-own", "batch-all"}, "monitored": true})
		_, _ = invoke("tag_delete", map[string]any{"tag": "batch-own"})
		_, _ = invoke("tag_delete", map[string]any{"tag": "batch-all"})
	})

	out := call(t, "movie_batch_edit", map[string]any{"movies": films, "add_tags": []any{"batch-all"}, "monitored": false})
	got := rows(t, out["movies"], "movies")
	if len(got) != 3 {
		t.Fatalf("batch = %v", out)
	}
	for _, row := range got {
		tags := strs(t, row["tags"], "tags")
		want := []string{"batch-all"}
		if str(row["title"]) == "Aliens" {
			want = []string{"batch-all", "batch-own"}
		}
		if !slices.Equal(tags, want) || row["monitored"] != false {
			t.Errorf("%v = monitored %v, tags %v; want %v", row["title"], row["monitored"], tags, want)
		}
	}
	if msg := callErr(t, "movie_batch_edit", map[string]any{"movies": []any{"Alien (1979)", "Nothing At All"}, "monitored": true}); !strings.Contains(msg, `"Nothing At All"`) {
		t.Errorf("a batch naming a missing film = %q", msg)
	}
}

// A refresh reads TMDB again and a rescan reads the folder again: a file
// taken away is dropped, and one put back is picked up.
func TestMovieRefreshAndRescan(t *testing.T) {
	addScratch(t)
	out := call(t, "movie_refresh", map[string]any{"movies": []any{"Moon"}})
	if cmd := rows(t, out["commands"], "commands"); len(cmd) != 1 || str(cmd[0]["name"]) != "RefreshMovie" || str(cmd[0]["status"]) != "completed" {
		t.Errorf("refresh = %v", out)
	}

	aside := hostPath(scratchFile) + ".aside"
	if err := os.Rename(hostPath(scratchFile), aside); err != nil {
		t.Fatal(err)
	}
	out = call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}})
	if movies := rows(t, out["movies"], "movies"); movies[0]["has_file"] != false {
		t.Errorf("a rescan of an empty folder kept the file: %v", movies[0])
	}
	if err := os.Rename(aside, hostPath(scratchFile)); err != nil {
		t.Fatal(err)
	}
	out = call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}})
	if movies := rows(t, out["movies"], "movies"); movies[0]["has_file"] != true || str(movies[0]["quality"]) != "Bluray-1080p" {
		t.Errorf("a rescan did not pick the file up again: %v", movies[0])
	}

	// no wait: the command is started and answered as it stands
	quick := call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}, "wait": -1})
	if st := str(rows(t, quick["commands"], "commands")[0]["status"]); st == "" {
		t.Errorf("an unwaited rescan = %v", quick)
	}
}

// movie_rename previews by default, renames with preview false, and says so
// when Radarr's renaming is off.
func TestMovieRename(t *testing.T) {
	addScratch(t)
	// give the scratch film a scene name, which the scheme renames
	scene := scratchFolder + "/moon.2009.1080p.bluray.x264-GRP.mkv"
	if err := os.Rename(hostPath(scratchFile), hostPath(scene)); err != nil {
		t.Fatal(err)
	}
	call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}})

	off := call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}})
	if len(rows(t, off["renames"], "renames")) != 0 || !strings.Contains(str(off["note"]), "renaming is switched off") {
		t.Errorf("with renaming off = %v", off)
	}

	call(t, "naming_edit", map[string]any{"rename_movies": true})
	t.Cleanup(func() { _, _ = invoke("naming_edit", map[string]any{"rename_movies": false}) })

	preview := call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}})
	renames := rows(t, preview["renames"], "renames")
	if len(renames) != 1 || str(renames[0]["existing"]) != "moon.2009.1080p.bluray.x264-GRP.mkv" || str(renames[0]["new"]) != "Moon (2009) Bluray-1080p.mkv" || preview["renamed"] != false {
		t.Fatalf("the preview = %v", preview)
	}
	if _, err := os.Stat(hostPath(scene)); err != nil {
		t.Fatalf("a preview renamed the file: %v", err)
	}

	done := call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}, "preview": false})
	if done["renamed"] != true {
		t.Errorf("the rename = %v", done)
	}
	if _, err := os.Stat(hostPath(scratchFile)); err != nil {
		t.Errorf("the file was not renamed: %v", err)
	}
	if again := call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}}); len(rows(t, again["renames"], "renames")) != 0 {
		t.Errorf("a renamed file still has a rename: %v", again)
	}
}

// moviefile_edit corrects what Radarr believes about a file, and Radarr
// decides the file's standing from it: graded 720p, it falls under its
// profile's cutoff.
func TestMovieFileEdit(t *testing.T) {
	addScratch(t)
	out := call(t, "moviefile_edit", map[string]any{
		"movie": "Moon", "quality": "Bluray-720p", "languages": []any{"English", "Japanese"}, "release_group": "FIXED", "edition": "Director's Cut",
	})
	file := object(t, out["file"], "file")
	if str(file["quality"]) != "Bluray-720p" || !slices.Equal(strs(t, file["languages"], "languages"), []string{"English", "Japanese"}) ||
		str(file["release_group"]) != "FIXED" || str(file["edition"]) != "Director's Cut" || file["cutoff_not_met"] != true {
		t.Errorf("the edited file = %v", file)
	}
	if got := findings(t, call(t, "audit_cutoff_unmet", map[string]any{"tag": "scratch"})); !slices.Equal(got, []string{"Moon (2009)"}) {
		t.Errorf("the regraded file's cutoff = %v", got)
	}
	back := object(t, call(t, "moviefile_edit", map[string]any{"movie": "Moon", "quality": "Bluray-1080p"})["file"], "file")
	if str(back["quality"]) != "Bluray-1080p" || back["cutoff_not_met"] != false || str(back["release_group"]) != "FIXED" {
		t.Errorf("graded back = %v", back)
	}

	for args, want := range map[string]map[string]any{
		`no quality "Bluray-9000p"`: {"movie": "Moon", "quality": "Bluray-9000p"},
		`no language "Klingon"`:     {"movie": "Moon", "languages": []any{"Klingon"}},
		"nothing to change":         {"movie": "Moon"},
		"has no file":               {"movie": "tmdb:8077", "quality": "DVD"},
	} {
		if msg := callErr(t, "moviefile_edit", want); !strings.Contains(msg, args) {
			t.Errorf("moviefile_edit %v = %q, want %q", want, msg, args)
		}
	}
}

// moviefile_delete takes the file off disk and leaves the film, missing.
func TestMovieFileDelete(t *testing.T) {
	addScratch(t)
	out := call(t, "moviefile_delete", map[string]any{"movie": "Moon"})
	if file := object(t, out["file"], "file"); str(file["relative_path"]) != "Moon (2009) Bluray-1080p.mkv" {
		t.Errorf("deleted = %v", out)
	}
	if _, err := os.Stat(hostPath(scratchFile)); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk: %v", err)
	}
	after := call(t, "movie_get", map[string]any{"movie": "Moon"})
	if after["has_file"] != false || after["file"] != nil {
		t.Errorf("the film after = %v", after)
	}
	if got := findings(t, call(t, "audit_missing_files", map[string]any{"tag": "scratch"})); !slices.Equal(got, []string{"Moon (2009)"}) {
		t.Errorf("the film is not missing: %v", got)
	}
	if msg := callErr(t, "moviefile_delete", map[string]any{"movie": "Moon"}); !strings.Contains(msg, "has no file") {
		t.Errorf("a second delete = %q", msg)
	}
}

// movie_delete removes the film, and its folder only when asked.
func TestMovieDelete(t *testing.T) {
	addScratch(t)
	out := call(t, "movie_delete", map[string]any{"movie": "Moon"})
	if deleted := object(t, out["deleted"], "deleted"); str(deleted["title"]) != scratchTitle || out["files_deleted"] != false {
		t.Errorf("deleted = %v", out)
	}
	if _, err := os.Stat(hostPath(scratchFile)); err != nil {
		t.Errorf("a delete without delete_files took the file: %v", err)
	}
	callErr(t, "movie_get", map[string]any{"movie": "tmdb:17431"})

	// back, then gone with its files and onto the exclusions
	call(t, "movie_add", map[string]any{"movie": "tmdb:17431", "quality_profile": profileHD, "root_folder": moviesRoot, "folder": "Moon (2009)"})
	out = call(t, "movie_delete", map[string]any{"movie": "Moon", "delete_files": true, "add_exclusion": true})
	if out["files_deleted"] != true || out["excluded"] != true {
		t.Errorf("deleted with files = %v", out)
	}
	// Radarr deletes the folder and adds the exclusion in event handlers of
	// their own, a moment after the delete has answered
	if !eventually(func() bool {
		_, err := os.Stat(hostPath(scratchFolder))
		return os.IsNotExist(err)
	}) {
		t.Errorf("the folder is still on disk a minute after the delete")
	}
	var excluded float64
	eventually(func() bool {
		for _, e := range rows(t, call(t, "exclusion_list", nil)["exclusions"], "exclusions") {
			if num(t, e["tmdb_id"], "tmdb_id") == scratchTmdb {
				excluded = num0(e["id"])
			}
		}
		return excluded != 0
	})
	if excluded == 0 {
		t.Fatal("the film is not on the exclusions")
	}
	call(t, "exclusion_remove", map[string]any{"ids": []any{excluded}})

	// the folder the test laid out is gone, so nothing is left to clean up
	if entries, err := os.ReadDir(filepath.Dir(hostPath(scratchFolder))); err == nil {
		for _, e := range entries {
			if e.Name() == "Moon (2009)" {
				t.Errorf("Moon (2009) is still in %s", moviesRoot)
			}
		}
	}
}
