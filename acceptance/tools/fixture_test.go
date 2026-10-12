//go:build acceptance

package tools_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
)

// The catalogue the script laid out is what Radarr holds: every film with
// the TMDB id, year and runtime it was added for, the file Radarr took from
// its folder graded as expected, and the films with no folder without one.
// Every other test assumes this.
func TestFixturesAreWhatTheScriptLaidOut(t *testing.T) {
	out := suite.Call(t, "movie_list", map[string]any{"limit": 500})
	byTmdb := map[int]map[string]any{}
	for _, row := range acctest.Rows(t, out["movies"], "movies") {
		byTmdb[acctest.Num(t, row["tmdb_id"], "tmdb_id")] = row
	}
	if len(byTmdb) < len(fixtures()) {
		t.Fatalf("the library holds %d films, want at least %d", len(byTmdb), len(fixtures()))
	}
	for _, f := range fixtures() {
		row := byTmdb[f.TmdbID]
		if row == nil {
			t.Errorf("%s is not in the library", titleYear(f.Title, f.Year))
			continue
		}
		name := titleYear(f.Title, f.Year)
		if acctest.Str(row["title"]) != f.Title || acctest.Num(t, row["year"], "year") != f.Year {
			t.Errorf("tmdb %d is %v (%v), want %s", f.TmdbID, row["title"], row["year"], name)
		}
		if row["monitored"] != f.Monitored || acctest.Str(row["quality_profile"]) != f.Profile {
			t.Errorf("%s: monitored %v on %v, want %v on %s", name, row["monitored"], row["quality_profile"], f.Monitored, f.Profile)
		}
		if f.Folder != "" && acctest.Str(row["path"]) != f.Root+"/"+f.Folder {
			t.Errorf("%s is at %v, want %s/%s", name, row["path"], f.Root, f.Folder)
		}
		if got := acctest.Str(row["quality"]); got != f.Quality || (row["has_file"] == true) != (f.Quality != "") {
			t.Errorf("%s: file graded %q (has_file %v), want %q", name, got, row["has_file"], f.Quality)
		}
	}

	// each film's runtime is TMDB's, and each file's is what ffmpeg wrote:
	// the clean ones match, which is what audit_runtime leaves alone
	for _, f := range clean {
		got := suite.Call(t, "movie_get", map[string]any{"movie": "tmdb:" + itoa(f.TmdbID)})
		if acctest.Num(t, got["runtime"], "runtime") != f.Runtime {
			t.Errorf("%s runs %v minutes by TMDB, want %d", f.Title, got["runtime"], f.Runtime)
		}
		file := acctest.Object(t, got["file"], "file")
		media := acctest.Object(t, file["media"], "media")
		if acctest.NumOr0(media["runtime_minutes"]) != f.Runtime || acctest.Str(media["resolution"]) != "1920x1080" {
			t.Errorf("%s's file = %v", f.Title, media)
		}
		want := "eng"
		if f.Title == "Princess Mononoke" {
			want = "jpn"
		}
		if langs := acctest.Strs(t, acctest.OrEmptyList(media["audio_languages"]), "audio_languages"); !slices.Equal(langs, []string{want}) {
			t.Errorf("%s's audio = %v, want [%s]", f.Title, langs, want)
		}
		if acctest.Str(got["overview"]) == "" || acctest.Str(got["imdb_id"]) == "" || !strings.HasPrefix(acctest.Str(got["imdb_id"]), "tt") {
			t.Errorf("%s came back from TMDB without an overview or imdb id: %v %v", f.Title, got["overview"], got["imdb_id"])
		}
	}
}
