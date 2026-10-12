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
	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/radarr-mcp/lib/testenv"
)

// The download film: added with no file for the journey to fetch, and
// removed at the end with whatever it fetched.
const (
	downloadTitle  = "Primer"
	downloadYear   = 2004
	downloadTmdb   = 14337
	downloadImdb   = "tt0390384"
	downloadFolder = moviesRoot + "/Primer (2004)"
)

// The releases the fake indexer offers for it: one the HD-1080p profile
// wants, and two it rejects.
var (
	primerBluray = newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Primer.2004.1080p.BluRay.x264-GRP", TMDBID: downloadTmdb, IMDBID: downloadImdb, Size: 8 << 30}
	primerHDTV   = newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Primer.2004.720p.HDTV.x264-GRP", TMDBID: downloadTmdb, IMDBID: downloadImdb, Size: 3 << 30}
	primerCam    = newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Primer.2004.CAM.XviD-BAD", TMDBID: downloadTmdb, IMDBID: downloadImdb, Size: 700 << 20}
)

// finish lays a release's download out in the blackhole's watch folder, the
// way a download client leaves a finished one: a folder named for the
// release, and in it a file (or, for a broken download, only a readme).
func finish(t *testing.T, release string, minutes int, size string, video bool) {
	t.Helper()

	dir := watchFolder + "/" + release
	if video {
		makeVideo(t, dir+"/"+release+".mkv", minutes, size)
	} else {
		env.Mkdir(t, testenv.DataDir(), testenv.HostPath(dir))
		if err := os.WriteFile(testenv.HostPath(dir+"/readme.txt"), []byte("nothing to see here\n"), 0o666); err != nil { //nolint:gosec // the container reads it as another user
			t.Fatal(err)
		}
	}
}

// checkDownloads has Radarr ask its download clients what finished, and
// waits for it to be done with them.
func checkDownloads(t *testing.T) {
	t.Helper()

	out := suite.Call(t, "task_run", map[string]any{"task": "RefreshMonitoredDownloads"})
	if cmd := acctest.Object(t, out["command"], "command"); acctest.Str(cmd["status"]) != "completed" {
		t.Fatalf("RefreshMonitoredDownloads = %v", out)
	}
}

// events are the kinds of the download film's history, newest first.
func events(t *testing.T) []string {
	t.Helper()

	var kinds []string
	for _, e := range acctest.Rows(t, suite.Call(t, "history_list", map[string]any{"movie": "tmdb:14337"})["events"], "events") {
		kinds = append(kinds, acctest.Str(e["event"]))
	}

	return kinds
}

// The journey from wanting a film to having it, through every tool on the
// way: search the indexers, grab a release by hand, let the download finish,
// see Radarr import it, search again automatically for something better,
// reject what that brought, and clear a download that can never be
// imported.
//
//nolint:paralleltest // the steps share one film and one download client, in order
func TestDownloadJourney(t *testing.T) {
	skipUnlessReady(t)
	if err := indexer.Offer(primerBluray, primerHDTV, primerCam); err != nil {
		t.Fatal(err)
	}
	suite.Call(t, "movie_add", map[string]any{"movie": "tmdb:14337", "quality_profile": profileHD, "root_folder": moviesRoot})
	t.Cleanup(func() {
		indexer.Withdraw(primerBluray.Title, primerHDTV.Title, primerCam.Title)
		_, _ = suite.Invoke("movie_delete", map[string]any{"movie": "tmdb:14337", "delete_files": true, "confirm": true})
		for _, dir := range []string{downloadFolder, watchFolder, nzbFolder} {
			entries, _ := os.ReadDir(testenv.HostPath(dir))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "Primer") {
					_ = os.RemoveAll(filepath.Join(testenv.HostPath(dir), e.Name()))
				}
			}
		}
		_ = os.RemoveAll(testenv.HostPath(downloadFolder))
	})

	var releases []map[string]any
	t.Run("the indexers offer three releases, and Radarr wants one", func(t *testing.T) {
		out := suite.Call(t, "release_search", map[string]any{"movie": "Primer"})
		releases = acctest.Rows(t, out["releases"], "releases")
		if acctest.Num(t, out["total"], "total") != 3 || len(releases) != 3 {
			t.Fatalf("release_search = %v", out)
		}
		byTitle := map[string]map[string]any{}
		for _, r := range releases {
			byTitle[acctest.Str(r["title"])] = r
			if acctest.Str(r["indexer"]) != "Fake Indexer" || acctest.Num(t, r["indexer_id"], "indexer_id") == 0 || acctest.Str(r["guid"]) == "" || acctest.Str(r["protocol"]) != "usenet" {
				t.Errorf("release = %v", r)
			}
		}
		if bluray := byTitle[primerBluray.Title]; bluray == nil || bluray["approved"] != true || acctest.Str(bluray["quality"]) != "Bluray-1080p" || acctest.Num(t, bluray["size"], "size") != int(primerBluray.Size) {
			t.Errorf("the 1080p BluRay = %v", bluray)
		}
		for _, title := range []string{primerHDTV.Title, primerCam.Title} {
			if r := byTitle[title]; r == nil || r["approved"] != false || len(acctest.Strs(t, acctest.OrEmptyList(r["rejections"]), "rejections")) == 0 {
				t.Errorf("%s should be rejected with reasons: %v", title, r)
			}
		}
		// Radarr's own order puts what it would take first
		if acctest.Str(releases[0]["title"]) != primerBluray.Title {
			t.Errorf("the first release = %v", releases[0]["title"])
		}
		approved := acctest.Rows(t, suite.Call(t, "release_search", map[string]any{"movie": "Primer", "approved": true})["releases"], "releases")
		if len(approved) != 1 || acctest.Str(approved[0]["title"]) != primerBluray.Title {
			t.Errorf("approved only = %v", approved)
		}
		// and the indexer was asked by id, the way Radarr searches for a film
		var byID bool
		for _, r := range indexer.Requests() {
			if r.Query.Get("t") == "movie" && (r.Query.Get("tmdbid") == "14337" || strings.TrimPrefix(r.Query.Get("imdbid"), "tt") == "0390384") {
				byID = true
			}
		}
		if !byID {
			t.Errorf("Radarr never searched the indexer by id: %v", indexer.Requests())
		}
	})

	t.Run("a release parses the way Radarr reads it", func(t *testing.T) {
		out := suite.Call(t, "release_parse", map[string]any{"title": primerHDTV.Title})
		movie := acctest.Object(t, out["movie"], "movie")
		if acctest.Str(out["quality"]) != "HDTV-720p" || acctest.Str(out["release_group"]) != "GRP" || acctest.Num(t, out["year"], "year") != downloadYear || acctest.Str(movie["title"]) != downloadTitle {
			t.Errorf("release_parse = %v", out)
		}
		stranger := suite.Call(t, "release_parse", map[string]any{"title": "Some.Other.Film.2011.1080p.WEB-DL-XYZ"})
		if stranger["movie"] != nil || acctest.Str(stranger["quality"]) != "WEBDL-1080p" || acctest.Num(t, stranger["year"], "year") != 2011 {
			t.Errorf("an unknown film = %v", stranger)
		}
		suite.CallErr(t, "release_parse", map[string]any{"title": " "})
	})

	t.Run("a rejected release is grabbed by hand, and the download client gets it", func(t *testing.T) {
		var hdtv map[string]any
		for _, r := range releases {
			if acctest.Str(r["title"]) == primerHDTV.Title {
				hdtv = r
			}
		}
		out := suite.Call(t, "release_grab", map[string]any{"guid": hdtv["guid"], "indexer_id": hdtv["indexer_id"]})
		// Radarr's answer to a grab is only what it was sent: the release's
		// name, the film and the indexer come from the history it wrote
		grabbed := acctest.Object(t, out["grabbed"], "grabbed")
		if acctest.Str(out["guid"]) != acctest.Str(hdtv["guid"]) || acctest.Str(grabbed["event"]) != "grabbed" || acctest.Str(grabbed["source_title"]) != primerHDTV.Title ||
			acctest.Str(grabbed["movie"]) != "Primer (2004)" || acctest.Object(t, grabbed["details"], "details")["indexer"] != "Fake Indexer" {
			t.Errorf("release_grab = %v", out)
		}
		if !slices.Contains(indexer.Grabbed(), primerHDTV.Title) {
			t.Errorf("Radarr never fetched the .nzb: %v", indexer.Grabbed())
		}
		if _, err := os.Stat(testenv.HostPath(nzbFolder + "/" + primerHDTV.Title + ".nzb")); err != nil {
			t.Errorf("the blackhole has no .nzb: %v", err)
		}
		if ev := events(t); len(ev) == 0 || ev[0] != "grabbed" {
			t.Errorf("history = %v, want a grab first", ev)
		}
		grab := acctest.Rows(t, suite.Call(t, "history_list", map[string]any{"movie": "Primer", "event": "grabbed"})["events"], "events")
		if len(grab) != 1 || acctest.Str(grab[0]["source_title"]) != primerHDTV.Title || acctest.Object(t, grab[0]["details"], "details")["indexer"] != "Fake Indexer" {
			t.Errorf("the grab in history = %v", grab)
		} else if grab[0]["id"] != grabbed["id"] {
			t.Errorf("release_grab answered history %v, the history holds %v", grabbed["id"], grab[0]["id"])
		}
		if msg := suite.CallErr(t, "release_grab", map[string]any{"guid": "", "indexer_id": 0}); !strings.Contains(msg, "guid and indexer_id are both required") {
			t.Errorf("a grab of nothing = %q", msg)
		}
	})

	t.Run("the download finishes and Radarr imports it", func(t *testing.T) {
		finish(t, primerHDTV.Title, 77, "1280x720", true)
		if !acctest.EventuallyWithin(time.Minute, func() bool {
			checkDownloads(t)
			got := suite.Call(t, "movie_get", map[string]any{"movie": "Primer"})
			return got["has_file"] == true
		}) {
			t.Fatalf("Primer was never imported; queue %v, history %v", suite.Call(t, "queue_list", map[string]any{"include_unknown": true}), events(t))
		}
		got := suite.Call(t, "movie_get", map[string]any{"movie": "Primer"})
		file := acctest.Object(t, got["file"], "file")
		if acctest.Str(file["quality"]) != "HDTV-720p" || !strings.HasPrefix(acctest.Str(file["path"]), downloadFolder+"/") || acctest.Str(file["release_group"]) != "GRP" {
			t.Errorf("the imported file = %v", file)
		}
		if ev := events(t); !slices.Contains(ev, "downloadFolderImported") {
			t.Errorf("history = %v, want an import", ev)
		}
		imported := acctest.Rows(t, suite.Call(t, "history_list", map[string]any{"event": "downloadFolderImported", "days": 1})["events"], "events")
		if len(imported) == 0 || acctest.Str(imported[0]["movie"]) != "Primer (2004)" {
			t.Errorf("the library's imports today = %v", imported)
		}
		// Radarr pages a film's history: the second event asked for alone is the second of the first two
		both := suite.Call(t, "history_list", map[string]any{"movie": "Primer", "limit": 2})
		second := suite.Call(t, "history_list", map[string]any{"movie": "Primer", "limit": 1, "offset": 1})
		if two, one := acctest.Rows(t, both["events"], "events"), acctest.Rows(t, second["events"], "events"); len(two) != 2 || len(one) != 1 || one[0]["id"] != two[1]["id"] || acctest.Num(t, both["total"], "total") < 2 || acctest.Num(t, second["offset"], "offset") != 1 || second["total"] != both["total"] {
			t.Errorf("the second event alone = %v, the first two = %v", second, both)
		}
		// the movie's recent history is part of movie_get
		if h := acctest.Rows(t, got["history"], "history"); len(h) < 2 {
			t.Errorf("movie_get history = %v", h)
		}
		// downloading it is what audit_missing_files asked for, and the file
		// it got is what audit_cutoff_unmet and audit_quality ask to replace
		if slices.Contains(findings(t, suite.Call(t, "audit_missing_files", nil)), "Primer (2004)") {
			t.Error("Primer is still missing after its download imported")
		}
		if !slices.Contains(findings(t, suite.Call(t, "audit_cutoff_unmet", nil)), "Primer (2004)") {
			t.Error("a 720p file below the profile's Bluray-1080p cutoff is not flagged")
		}
		if !slices.Contains(findings(t, suite.Call(t, "audit_quality", map[string]any{"min_resolution": 1080})), "Primer (2004)") {
			t.Error("a 720p file is not flagged below 1080 lines")
		}
	})

	t.Run("an automatic search grabs the release the profile wants", func(t *testing.T) {
		// Radarr's profiles are made with upgrades off, and with them off it
		// never replaces a file it has; the journey's film needs one on
		suite.Call(t, "qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": true})
		t.Cleanup(func() {
			_, _ = suite.Invoke("qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": false})
		})
		out := suite.Call(t, "movie_download", map[string]any{"movies": []any{"Primer"}})
		if cmd := acctest.Object(t, out["command"], "command"); acctest.Str(cmd["name"]) != "MoviesSearch" || acctest.Str(cmd["status"]) != "completed" {
			t.Fatalf("movie_download = %v", out)
		}
		if !acctest.EventuallyWithin(time.Minute, func() bool { return slices.Contains(indexer.Grabbed(), primerBluray.Title) }) {
			t.Fatalf("the automatic search grabbed %v", indexer.Grabbed())
		}
		grabs := acctest.Rows(t, suite.Call(t, "history_list", map[string]any{"movie": "Primer", "event": "grabbed"})["events"], "events")
		if len(grabs) != 2 || acctest.Str(grabs[0]["source_title"]) != primerBluray.Title {
			t.Errorf("grabs = %v", grabs)
		}
	})

	t.Run("a grab marked failed is blocklisted, and taken off the blocklist", func(t *testing.T) {
		out := suite.Call(t, "history_mark_failed", map[string]any{"movie": "Primer"})
		if marked := acctest.Object(t, out["marked"], "marked"); acctest.Str(marked["source_title"]) != primerBluray.Title || acctest.Str(marked["event"]) != "grabbed" {
			t.Fatalf("history_mark_failed = %v", out)
		}
		var entry map[string]any
		if !acctest.EventuallyWithin(time.Minute, func() bool {
			for _, b := range acctest.Rows(t, suite.Call(t, "blocklist_list", map[string]any{"movie": "Primer"})["entries"], "entries") {
				if acctest.Str(b["source_title"]) == primerBluray.Title {
					entry = b
				}
			}
			return entry != nil
		}) {
			t.Fatalf("the failed release is not on the blocklist")
		}
		if acctest.Str(entry["movie"]) != "Primer (2004)" || acctest.Str(entry["indexer"]) != "Fake Indexer" {
			t.Errorf("the blocklist entry = %v", entry)
		}
		all := suite.Call(t, "blocklist_list", nil)
		if acctest.Num(t, all["total"], "total") < 1 {
			t.Errorf("the whole blocklist = %v", all)
		}
		if ev := events(t); !slices.Contains(ev, "downloadFailed") {
			t.Errorf("history = %v, want the failure", ev)
		}
		// a blocklisted release is one Radarr no longer takes on its own
		var blocked bool
		for _, r := range acctest.Rows(t, suite.Call(t, "release_search", map[string]any{"movie": "Primer"})["releases"], "releases") {
			if acctest.Str(r["title"]) == primerBluray.Title && r["approved"] == false && strings.Contains(strings.Join(acctest.Strs(t, acctest.OrEmptyList(r["rejections"]), "rejections"), " "), "blocklist") {
				blocked = true
			}
		}
		if !blocked {
			t.Error("the blocklisted release is still approved")
		}
		removed := suite.Call(t, "blocklist_remove", map[string]any{"ids": []any{entry["id"]}})
		if got := removed["removed"].([]any); len(got) != 1 { //nolint:forcetypeassert // the tool's own output
			t.Errorf("blocklist_remove = %v", removed)
		}
		if left := acctest.Rows(t, suite.Call(t, "blocklist_list", map[string]any{"movie": "Primer"})["entries"], "entries"); len(left) != 0 {
			t.Errorf("still blocklisted: %v", left)
		}
		// marking an import failed is refused: only a grab can fail
		for _, e := range acctest.Rows(t, suite.Call(t, "history_list", map[string]any{"movie": "Primer", "event": "downloadFolderImported"})["events"], "events") {
			if msg := suite.CallErr(t, "history_mark_failed", map[string]any{"id": e["id"]}); !strings.Contains(msg, "only a grab can be marked failed") {
				t.Errorf("marking an import failed = %q", msg)
			}
		}
	})

	t.Run("a download with nothing to import is stuck, found, and cleared", func(t *testing.T) {
		finish(t, primerBluray.Title, 0, "", false)
		var stuck map[string]any
		if !acctest.EventuallyWithin(time.Minute, func() bool {
			checkDownloads(t)
			for _, f := range acctest.Rows(t, suite.Call(t, "audit_queue", nil)["findings"], "findings") {
				if acctest.Str(f["title"]) == primerBluray.Title {
					stuck = f
				}
			}
			return stuck != nil
		}) {
			t.Fatalf("the empty download never showed as stuck: %v", suite.Call(t, "queue_list", map[string]any{"include_unknown": true}))
		}
		if acctest.Num(t, stuck["queue_id"], "queue_id") == 0 || !strings.Contains(acctest.Str(stuck["detail"]), "Primer (2004)") {
			t.Errorf("the stuck download = %v", stuck)
		}
		queue := suite.Call(t, "queue_list", map[string]any{"movie": "Primer"})
		items := acctest.Rows(t, queue["items"], "items")
		if len(items) != 1 || acctest.Str(items[0]["health"]) == "ok" || len(acctest.Strs(t, acctest.OrEmptyList(items[0]["messages"]), "messages")) == 0 || acctest.Str(items[0]["movie"]) != "Primer (2004)" {
			t.Errorf("queue_list = %v", queue)
		}
		// nothing in it to import by hand
		if files := acctest.Rows(t, suite.Call(t, "import_scan", map[string]any{"download_id": items[0]["download_id"]})["files"], "files"); len(files) != 0 {
			t.Errorf("import_scan of the empty download = %v", files)
		}
		out := suite.Call(t, "queue_remove", map[string]any{"ids": []any{stuck["queue_id"]}, "remove_from_client": true})
		if removed := acctest.Rows(t, out["removed"], "removed"); len(removed) != 1 || acctest.Str(removed[0]["title"]) != primerBluray.Title {
			t.Errorf("queue_remove = %v", out)
		}
		if !acctest.EventuallyWithin(time.Minute, func() bool {
			_, err := os.Stat(testenv.HostPath(watchFolder + "/" + primerBluray.Title))
			return os.IsNotExist(err)
		}) {
			t.Error("removed from the client, the download is still in the watch folder")
		}
		if got := findings(t, suite.Call(t, "audit_queue", nil)); len(got) != 0 {
			t.Errorf("the queue is still not clear: %v", got)
		}
		if msg := suite.CallErr(t, "queue_remove", map[string]any{"ids": []any{999999}}); !strings.Contains(msg, "no queue item 999999") {
			t.Errorf("removing what is not there = %q", msg)
		}
	})

	t.Run("a better copy clears what the quality audits asked for", func(t *testing.T) {
		// Radarr refuses to import over a file it has while its profile does
		// not upgrade, however the release was grabbed
		suite.Call(t, "qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": true})
		t.Cleanup(func() {
			_, _ = suite.Invoke("qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": false})
		})
		// the automatic search grabbed the Bluray, the blocklist test marked
		// it failed: grab it by hand and let it finish this time
		releases := acctest.Rows(t, suite.Call(t, "release_search", map[string]any{"movie": "Primer"})["releases"], "releases")
		var bluray map[string]any
		for _, r := range releases {
			if acctest.Str(r["title"]) == primerBluray.Title {
				bluray = r
			}
		}
		if bluray == nil {
			t.Fatalf("the Bluray is not on offer: %v", releases)
		}
		suite.Call(t, "release_grab", map[string]any{"guid": bluray["guid"], "indexer_id": bluray["indexer_id"]})
		finish(t, primerBluray.Title, 77, "1920x1080", true)
		if !acctest.EventuallyWithin(time.Minute, func() bool {
			checkDownloads(t)
			file, ok := suite.Call(t, "movie_get", map[string]any{"movie": "Primer"})["file"].(map[string]any)
			return ok && acctest.Str(file["quality"]) == "Bluray-1080p"
		}) {
			t.Fatalf("the better copy never imported: %v", suite.Call(t, "movie_get", map[string]any{"movie": "Primer"})["file"])
		}
		for _, tc := range []struct {
			audit string
			args  map[string]any
		}{
			{"audit_cutoff_unmet", nil},
			{"audit_quality", map[string]any{"min_resolution": 1080}},
		} {
			if got := findings(t, suite.Call(t, tc.audit, tc.args)); slices.Contains(got, "Primer (2004)") {
				t.Errorf("%s still finds Primer with a Bluray-1080p file: %v", tc.audit, got)
			}
		}
	})
}
