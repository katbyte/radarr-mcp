//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// The catalogue the script laid out is what Radarr holds: every film with
// the TMDB id, year and runtime it was added for, the file Radarr took from
// its folder graded as expected, and the films with no folder without one.
// Every other test assumes this.
func TestFixturesAreWhatTheScriptLaidOut(t *testing.T) {
	out := call(t, "movie_list", map[string]any{"limit": 500})
	byTmdb := map[int]map[string]any{}
	for _, row := range rows(t, out["movies"], "movies") {
		byTmdb[num(t, row["tmdb_id"], "tmdb_id")] = row
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
		if str(row["title"]) != f.Title || num(t, row["year"], "year") != f.Year {
			t.Errorf("tmdb %d is %v (%v), want %s", f.TmdbID, row["title"], row["year"], name)
		}
		if row["monitored"] != f.Monitored || str(row["quality_profile"]) != f.Profile {
			t.Errorf("%s: monitored %v on %v, want %v on %s", name, row["monitored"], row["quality_profile"], f.Monitored, f.Profile)
		}
		if f.Folder != "" && str(row["path"]) != f.Root+"/"+f.Folder {
			t.Errorf("%s is at %v, want %s/%s", name, row["path"], f.Root, f.Folder)
		}
		if got := str(row["quality"]); got != f.Quality || (row["has_file"] == true) != (f.Quality != "") {
			t.Errorf("%s: file graded %q (has_file %v), want %q", name, got, row["has_file"], f.Quality)
		}
	}

	// each film's runtime is TMDB's, and each file's is what ffmpeg wrote:
	// the clean ones match, which is what audit_runtime leaves alone
	for _, f := range clean {
		got := call(t, "movie_get", map[string]any{"movie": "tmdb:" + itoa(f.TmdbID)})
		if num(t, got["runtime"], "runtime") != f.Runtime {
			t.Errorf("%s runs %v minutes by TMDB, want %d", f.Title, got["runtime"], f.Runtime)
		}
		file := object(t, got["file"], "file")
		media := object(t, file["media"], "media")
		if int(num0(media["runtime_minutes"])) != f.Runtime || str(media["resolution"]) != "1920x1080" {
			t.Errorf("%s's file = %v", f.Title, media)
		}
		want := "eng"
		if f.Title == "Princess Mononoke" {
			want = "jpn"
		}
		if langs := strs(t, media["audio_languages"], "audio_languages"); !slices.Equal(langs, []string{want}) {
			t.Errorf("%s's audio = %v, want [%s]", f.Title, langs, want)
		}
		if str(got["overview"]) == "" || str(got["imdb_id"]) == "" || !strings.HasPrefix(str(got["imdb_id"]), "tt") {
			t.Errorf("%s came back from TMDB without an overview or imdb id: %v %v", f.Title, got["overview"], got["imdb_id"])
		}
	}
}
