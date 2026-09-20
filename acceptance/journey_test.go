//go:build integration

// Journeys: the tools chained the way a real session chains them, against
// state Radarr changes underneath, and repeated. The per-tool tests prove
// each tool answers; these prove that what one tool changed is what the next
// one sees, that reading never changes anything, and that every audit a
// tool can fix is cleared by that tool - a 202 from Radarr is not proof, so
// every write here is read back.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// snapshot is the state of the library a read must leave alone: every film
// with its file, tags, profile and monitoring, the root folders, the tags,
// the profiles and the naming settings. A root folder's free space is left
// out: the disk it is on is shared with everything else the host does.
func snapshot(t *testing.T) string {
	t.Helper()

	var b strings.Builder
	for _, name := range []string{"movie_list", "rootfolder_list", "tag_list", "qualityprofile_list", "qualitydefinition_list", "naming_get", "exclusion_list", "collection_list"} {
		args := map[string]any{}
		if name == "movie_list" {
			args["limit"] = 500
		}
		out := call(t, name, args)
		if name == "rootfolder_list" {
			for _, f := range rows(t, out["root_folders"], "root_folders") {
				delete(f, "free_space")
			}
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(name + ": ")
		b.Write(raw)
		b.WriteString("\n")
	}

	return b.String()
}

// Every read tool, called the way a session calls it, leaves the library
// exactly as it found it.
func TestJourneyLookupsChangeNothing(t *testing.T) {
	before := snapshot(t)
	for name, args := range map[string]map[string]any{
		"server_info":               nil,
		"server_health":             nil,
		"server_disk_space":         nil,
		"server_logs":               {"level": "warn"},
		"server_log_file":           nil,
		"server_backups":            nil,
		"task_list":                 nil,
		"movie_list":                {"query": "dune", "sort": "rating"},
		"movie_get":                 {"movie": "Arrival"},
		"movie_lookup":              {"term": "tmdb:348"},
		"movie_credits":             {"movie": "Arrival"},
		"calendar_list":             {"days_back": 3650},
		"collection_list":           {"missing_only": true},
		"customformat_list":         nil,
		"indexer_list":              nil,
		"downloadclient_list":       nil,
		"history_list":              {"limit": 5},
		"blocklist_list":            nil,
		"queue_list":                {"include_unknown": true},
		"release_parse":             {"title": "Arrival.2016.1080p.BluRay.x264-GRP"},
		"import_scan":               {"movie": "Contact"},
		"audit_all":                 {"deep": true},
		"audit_missing_metadata":    {"field": "studio"},
		"audit_collection_gaps":     {"include_unreleased": true},
		"audit_language":            {"languages": []any{"French"}},
		"audit_quality":             {"min_resolution": 2160},
		"audit_runtime":             {"tolerance_percent": 1},
		"audit_resolution_mismatch": {"root_folder": messyRoot},
	} {
		call(t, name, args)
	}
	if after := snapshot(t); after != before {
		b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
		for i := range min(len(a), len(b)) {
			if a[i] != b[i] {
				t.Errorf("reading changed the library:\nbefore %s\nafter  %s", b[i], a[i])
			}
		}
	}
}

// Every audit a tool can fix is cleared by it, and put back: the fix is read
// back through the audit that found the problem, not taken on trust.
//
//nolint:paralleltest // the fixes change shared fixtures in turn, each restored
func TestJourneyEveryFixableAudit(t *testing.T) {
	skipUnlessReady(t)
	cleared := func(t *testing.T, audit string, args map[string]any, film string) {
		t.Helper()
		if slices.Contains(findings(t, call(t, audit, args)), film) {
			t.Errorf("%s still finds %s after its fix", audit, film)
		}
	}

	t.Run("unmonitored: monitor it", func(t *testing.T) {
		call(t, "movie_edit", map[string]any{"movie": "Alien Resurrection", "monitored": true})
		t.Cleanup(func() { _, _ = invoke("movie_edit", map[string]any{"movie": "Alien Resurrection", "monitored": false}) })
		cleared(t, "audit_unmonitored", nil, "Alien Resurrection (1997)")
		// and, monitored with no file, it is now what Radarr is looking for
		if got := findings(t, call(t, "audit_missing_files", nil)); !slices.Contains(got, "Alien Resurrection (1997)") {
			t.Errorf("monitored, it is not missing: %v", got)
		}
	})

	t.Run("missing files: stop wanting it", func(t *testing.T) {
		call(t, "movie_edit", map[string]any{"movie": "tmdb:8077", "monitored": false})
		t.Cleanup(func() { _, _ = invoke("movie_edit", map[string]any{"movie": "tmdb:8077", "monitored": true}) })
		cleared(t, "audit_missing_files", nil, "Alien³ (1992)")
	})

	t.Run("profile mismatch: a profile that allows the file", func(t *testing.T) {
		call(t, "movie_edit", map[string]any{"movie": "Contact", "quality_profile": profileUHD})
		t.Cleanup(func() { _, _ = invoke("movie_edit", map[string]any{"movie": "Contact", "quality_profile": profileHD}) })
		cleared(t, "audit_profile_mismatch", nil, "Contact (1997)")
	})

	t.Run("unmapped folder: add the film for it", func(t *testing.T) {
		added := call(t, "movie_add", map[string]any{"movie": "tmdb:8195", "quality_profile": profileHD, "root_folder": moviesRoot, "folder": "Ronin (1998)"})
		t.Cleanup(func() { _, _ = invoke("movie_delete", map[string]any{"movie": "tmdb:8195"}) })
		if str(added["path"]) != unmappedRonin || added["has_file"] != true {
			t.Errorf("Ronin added = %v", added)
		}
		cleared(t, "audit_unmapped_folders", nil, "Ronin (1998)")
	})

	t.Run("collection gaps: add one film, exclude the rest", func(t *testing.T) {
		// the film added is no longer missing from its collection
		call(t, "movie_add", map[string]any{
			"movie": "tmdb:604", "quality_profile": profileHD, "root_folder": moviesRoot, "monitored": false,
		})
		t.Cleanup(func() { _, _ = invoke("movie_delete", map[string]any{"movie": "tmdb:604", "delete_files": true}) })
		gap := finding(t, call(t, "audit_collection_gaps", nil), "The Matrix Collection")
		for _, m := range rows(t, gap["missing"], "missing") {
			if num(t, m["tmdb_id"], "tmdb_id") == 604 {
				t.Errorf("the film added is still a gap: %v", gap)
			}
		}
		if !strings.Contains(str(gap["detail"]), "holds 2 of 4") {
			t.Errorf("the collection now = %v", gap["detail"])
		}

		var ids []any
		for _, tmdb := range []string{"605", "624860"} {
			out := call(t, "exclusion_add", map[string]any{"movie": "tmdb:" + tmdb})
			ids = append(ids, out["id"])
		}
		t.Cleanup(func() { _, _ = invoke("exclusion_remove", map[string]any{"ids": ids}) })
		for _, f := range rows(t, call(t, "audit_collection_gaps", nil)["findings"], "findings") {
			if str(f["title"]) == "The Matrix Collection" {
				t.Errorf("the excluded films are still gaps: %v", f)
			}
		}
	})

	t.Run("year mismatch: the folder renamed for the film", func(t *testing.T) {
		wrong, right := messyRoot+"/Dune (2021)", messyRoot+"/Dune (1984)"
		id := movieID(t, "Dune", 1984)
		t.Cleanup(func() { moveTo(t, id, wrong) })
		// a move to the root folder the film is already in rebuilds its
		// folder's name from the naming scheme
		out := call(t, "movie_edit", map[string]any{"movie": messyDune, "root_folder": messyRoot})
		if moved := rows(t, out["movies"], "movies"); len(moved) != 1 || str(moved[0]["path"]) != right {
			t.Fatalf("movie_edit into its own root folder = %v", out)
		}
		if _, err := os.Stat(hostPath(right)); err != nil {
			t.Errorf("the folder was not renamed on disk: %v", err)
		}
		cleared(t, "audit_year_mismatch", nil, messyDune)
		cleared(t, "audit_naming", nil, messyDune)
	})

	t.Run("a runtime that cannot be right: let go of the file", func(t *testing.T) {
		addScratch(t)
		// the same film, with a file that stops after 40 of its 97 minutes
		makeVideo(t, scratchFile, 40, "1920x1080")
		call(t, "movie_rescan", map[string]any{"movies": []any{"Moon"}})
		f := finding(t, call(t, "audit_runtime", nil), "Moon (2009)")
		if !strings.Contains(str(f["detail"]), "40 minutes") {
			t.Fatalf("the short file = %v", f)
		}
		call(t, "moviefile_delete", map[string]any{"movie": "Moon"})
		cleared(t, "audit_runtime", nil, "Moon (2009)")
		// and Radarr is looking for the film again
		if !slices.Contains(findings(t, call(t, "audit_missing_files", nil)), "Moon (2009)") {
			t.Error("with its file gone, the film is not what Radarr is looking for")
		}
	})

	t.Run("an untracked file: import it over the one the film has", func(t *testing.T) {
		addScratch(t)
		extra := scratchFolder + "/Moon.2009.720p.WEBDL.x264-GRP.mkv"
		makeVideo(t, extra, 97, "1280x720")
		f := finding(t, call(t, "audit_untracked_files", nil), "Moon (2009)")
		if str(f["file"]) != "Moon.2009.720p.WEBDL.x264-GRP.mkv" {
			t.Fatalf("the untracked file = %v", f)
		}
		call(t, "import_apply", map[string]any{"files": []any{map[string]any{"path": extra, "movie": "Moon"}}})
		cleared(t, "audit_untracked_files", nil, "Moon (2009)")
		// and the film now has the file that was untracked
		file := object(t, call(t, "movie_get", map[string]any{"movie": "Moon"})["file"], "file")
		if str(file["quality"]) != "WEBDL-720p" {
			t.Errorf("the film's file after the import = %v", file)
		}
	})

	t.Run("naming and a resolution lie: rename by the scheme", func(t *testing.T) {
		call(t, "naming_edit", map[string]any{"rename_movies": true})
		t.Cleanup(func() { _, _ = invoke("naming_edit", map[string]any{"rename_movies": false}) })
		before := map[string]string{
			messyMatrix: messyRoot + "/The Matrix (1999)/the.matrix.1999.1080p.bluray.x264-GRP.mkv",
			messy2049:   messyRoot + "/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) Bluray-2160p.mkv",
		}
		after := map[string]string{
			messyMatrix: messyRoot + "/The Matrix (1999)/The Matrix (1999) Bluray-1080p.mkv",
			messy2049:   messyRoot + "/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) Bluray-720p.mkv",
		}
		// put the fixtures' names back on disk, and have Radarr read them
		t.Cleanup(func() {
			for film := range before {
				_ = os.Rename(hostPath(after[film]), hostPath(before[film]))
				_, _ = invoke("movie_rescan", map[string]any{"movies": []any{film}})
			}
		})
		call(t, "movie_rename", map[string]any{"movies": []any{messyMatrix, messy2049}, "preview": false})
		for film, path := range after {
			if _, err := os.Stat(hostPath(path)); err != nil {
				t.Errorf("%s was not renamed to %s: %v", film, path, err)
			}
		}
		cleared(t, "audit_naming", map[string]any{"folders": false}, "The Matrix (1999)")
		// a file named for what it is no longer claims to be 4K
		cleared(t, "audit_resolution_mismatch", nil, "Blade Runner 2049 (2017)")
	})

	// every fix put back: the audits find what they found before
	for audit, want := range filmAudits {
		if got := findings(t, call(t, audit, nil)); !slices.Equal(got, want) {
			t.Errorf("after the fixes were put back, %s = %v, want %v", audit, got, want)
		}
	}
}

// moveTo points a film at a folder and has Radarr move it there on disk, the
// way its own edit form does. No tool gives a film a folder the naming scheme
// would not, which is what putting a fixture back needs, so this is the API
// by hand.
func moveTo(t *testing.T, id int, folder string) {
	t.Helper()

	url := os.Getenv("RADARR_SERVER") + "/api/v3/movie/" + strconv.Itoa(id)
	send := func(method, url string, body io.Reader) []byte {
		req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), method, url, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Api-Key", os.Getenv("RADARR_TOKEN"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode >= 300 {
			t.Fatalf("%s %s: HTTP %d %s %v", method, url, resp.StatusCode, raw, err)
		}
		return raw
	}
	var film map[string]any
	if err := json.Unmarshal(send(http.MethodGet, url, nil), &film); err != nil {
		t.Fatal(err)
	}
	film["path"] = folder
	body, err := json.Marshal(film)
	if err != nil {
		t.Fatal(err)
	}
	send(http.MethodPut, url+"?moveFiles=true", bytes.NewReader(body))
	// Radarr moves the folder in a command of its own, after the edit answers
	if !eventually(func() bool {
		_, err := os.Stat(hostPath(folder))
		return err == nil
	}) {
		t.Errorf("film %d never reached %s", id, folder)
	}
}

// A write done twice leaves the same state as once, and a create done twice
// is refused rather than doubled.
func TestJourneyWritesDoneTwice(t *testing.T) {
	skipUnlessReady(t)
	for range 2 {
		call(t, "movie_edit", map[string]any{"movie": "Arrival", "monitored": false, "add_tags": []any{"twice"}})
	}
	t.Cleanup(func() {
		_, _ = invoke("movie_edit", map[string]any{"movie": "Arrival", "monitored": true, "remove_tags": []any{"twice"}})
		_, _ = invoke("tag_delete", map[string]any{"tag": "twice"})
	})
	got := call(t, "movie_get", map[string]any{"movie": "Arrival"})
	if got["monitored"] != false || !slices.Equal(strs(t, got["tags"], "tags"), []string{"twice"}) {
		t.Errorf("after two edits = monitored %v, tags %v", got["monitored"], got["tags"])
	}

	for name, args := range map[string]map[string]any{
		"tag_create":     {"label": "twice"},
		"movie_add":      {"movie": "tmdb:329865", "quality_profile": profileHD, "root_folder": moviesRoot},
		"rootfolder_add": {"path": moviesRoot},
	} {
		callErr(t, name, args)
	}
	out := call(t, "exclusion_add", map[string]any{"movie": "tmdb:604"})
	t.Cleanup(func() { _, _ = invoke("exclusion_remove", map[string]any{"ids": []any{out["id"]}}) })
	if msg := callErr(t, "exclusion_add", map[string]any{"movie": "tmdb:604"}); !strings.Contains(msg, "already excluded") {
		t.Errorf("a second exclusion = %q", msg)
	}
}

// Calls at once - reads, audits and a write - answer as they would one at a
// time: the tools share nothing a call can trample.
func TestJourneyCallsAtOnce(t *testing.T) {
	skipUnlessReady(t)
	want := findings(t, call(t, "audit_year_mismatch", nil))
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := range 24 {
		wg.Go(func() {
			var name string
			var args map[string]any
			switch i % 4 {
			case 0:
				name, args = "audit_year_mismatch", nil
			case 1:
				name, args = "movie_get", map[string]any{"movie": "Dune (1984)"}
			case 2:
				name, args = "movie_list", map[string]any{"query": "dune"}
			default:
				name, args = "movie_edit", map[string]any{"movie": "Arrival", "add_tags": []any{"at-once"}}
			}
			out, err := invoke(name, args)
			switch {
			case err != nil:
				errs <- err.Error()
			case name == "audit_year_mismatch":
				var got []string
				for _, f := range rowsOf(out["findings"]) {
					got = append(got, titleYear(str(f["title"]), int(num0(f["year"]))))
				}
				if !slices.Equal(got, want) {
					errs <- "audit_year_mismatch at once = " + strings.Join(got, ",")
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	t.Cleanup(func() {
		_, _ = invoke("movie_edit", map[string]any{"movie": "Arrival", "remove_tags": []any{"at-once"}})
		_, _ = invoke("tag_delete", map[string]any{"tag": "at-once"})
	})
	for e := range errs {
		t.Error(e)
	}
	if tags := strs(t, call(t, "movie_get", map[string]any{"movie": "Arrival"})["tags"], "tags"); !slices.Equal(tags, []string{"at-once"}) {
		t.Errorf("six concurrent tag adds left %v", tags)
	}
}

// A film deleted with its files leaves nothing behind: not in the library,
// not on disk, not counted in its root folder, not carrying its tag.
func TestJourneyADeletedFilmLeavesNothingBehind(t *testing.T) {
	addScratch(t)
	folders := func() map[string]float64 {
		out := map[string]float64{}
		for _, f := range rows(t, call(t, "rootfolder_list", nil)["root_folders"], "root_folders") {
			out[str(f["path"])] = num0(f["movies"])
		}
		return out
	}
	with := folders()
	tagFilms := func() float64 {
		for _, tag := range rows(t, call(t, "tag_list", nil)["tags"], "tags") {
			if str(tag["label"]) == "scratch" {
				return num0(tag["movies"])
			}
		}
		return -1
	}
	if tagFilms() != 1 {
		t.Fatalf("the scratch tag carries %v films", tagFilms())
	}

	call(t, "movie_delete", map[string]any{"movie": "Moon", "delete_files": true})
	if got := folders(); got[moviesRoot] != with[moviesRoot]-1 {
		t.Errorf("%s holds %v films, was %v", moviesRoot, got[moviesRoot], with[moviesRoot])
	}
	if n := tagFilms(); n != 0 {
		t.Errorf("the tag still carries %v films", n)
	}
	// the folder goes in the background, a moment after the delete answers
	if !eventually(func() bool {
		_, err := os.Stat(hostPath(scratchFolder))
		return os.IsNotExist(err)
	}) {
		t.Errorf("the folder is still on disk a minute after the delete")
	}
	if got := rows(t, call(t, "movie_list", map[string]any{"query": "moon"})["movies"], "movies"); len(got) != 0 {
		t.Errorf("still in the library: %v", got)
	}
	// and no audit sees a trace of it
	for _, f := range rows(t, call(t, "audit_unmapped_folders", nil)["findings"], "findings") {
		if strings.Contains(str(f["path"]), "Moon") {
			t.Errorf("its folder is unmapped: %v", f)
		}
	}
}
