//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/radarr-mcp/internal/fakeindexer"
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
	primerBluray = fakeindexer.Release{Title: "Primer.2004.1080p.BluRay.x264-GRP", TmdbID: downloadTmdb, ImdbID: downloadImdb, Size: 8 << 30}
	primerHDTV   = fakeindexer.Release{Title: "Primer.2004.720p.HDTV.x264-GRP", TmdbID: downloadTmdb, ImdbID: downloadImdb, Size: 3 << 30}
	primerCam    = fakeindexer.Release{Title: "Primer.2004.CAM.XviD-BAD", TmdbID: downloadTmdb, ImdbID: downloadImdb, Size: 700 << 20}
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
		mediaMkdir(t, hostPath(dir))
		if err := os.WriteFile(hostPath(dir+"/readme.txt"), []byte("nothing to see here\n"), 0o666); err != nil { //nolint:gosec // the container reads it as another user
			t.Fatal(err)
		}
	}
}

// checkDownloads has Radarr ask its download clients what finished, and
// waits for it to be done with them.
func checkDownloads(t *testing.T) {
	t.Helper()

	out := call(t, "task_run", map[string]any{"task": "RefreshMonitoredDownloads"})
	if cmd := object(t, out["command"], "command"); str(cmd["status"]) != "completed" {
		t.Fatalf("RefreshMonitoredDownloads = %v", out)
	}
}

// events are the kinds of the download film's history, newest first.
func events(t *testing.T) []string {
	t.Helper()

	var kinds []string
	for _, e := range rows(t, call(t, "history_list", map[string]any{"movie": "tmdb:14337"})["events"], "events") {
		kinds = append(kinds, str(e["event"]))
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
	indexer.Offer(primerBluray, primerHDTV, primerCam)
	call(t, "movie_add", map[string]any{"movie": "tmdb:14337", "quality_profile": profileHD, "root_folder": moviesRoot})
	t.Cleanup(func() {
		indexer.Withdraw(primerBluray.Title, primerHDTV.Title, primerCam.Title)
		_, _ = invoke("movie_delete", map[string]any{"movie": "tmdb:14337", "delete_files": true})
		for _, dir := range []string{downloadFolder, watchFolder, nzbFolder} {
			entries, _ := os.ReadDir(hostPath(dir))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "Primer") {
					_ = os.RemoveAll(filepath.Join(hostPath(dir), e.Name()))
				}
			}
		}
		_ = os.RemoveAll(hostPath(downloadFolder))
	})

	var releases []map[string]any
	t.Run("the indexers offer three releases, and Radarr wants one", func(t *testing.T) {
		out := call(t, "release_search", map[string]any{"movie": "Primer"})
		releases = rows(t, out["releases"], "releases")
		if num(t, out["total"], "total") != 3 || len(releases) != 3 {
			t.Fatalf("release_search = %v", out)
		}
		byTitle := map[string]map[string]any{}
		for _, r := range releases {
			byTitle[str(r["title"])] = r
			if str(r["indexer"]) != "Fake Indexer" || num(t, r["indexer_id"], "indexer_id") == 0 || str(r["guid"]) == "" || str(r["protocol"]) != "usenet" {
				t.Errorf("release = %v", r)
			}
		}
		if bluray := byTitle[primerBluray.Title]; bluray == nil || bluray["approved"] != true || str(bluray["quality"]) != "Bluray-1080p" || num(t, bluray["size"], "size") != int(primerBluray.Size) {
			t.Errorf("the 1080p BluRay = %v", bluray)
		}
		for _, title := range []string{primerHDTV.Title, primerCam.Title} {
			if r := byTitle[title]; r == nil || r["approved"] != false || len(strs(t, r["rejections"], "rejections")) == 0 {
				t.Errorf("%s should be rejected with reasons: %v", title, r)
			}
		}
		// Radarr's own order puts what it would take first
		if str(releases[0]["title"]) != primerBluray.Title {
			t.Errorf("the first release = %v", releases[0]["title"])
		}
		approved := rows(t, call(t, "release_search", map[string]any{"movie": "Primer", "approved": true})["releases"], "releases")
		if len(approved) != 1 || str(approved[0]["title"]) != primerBluray.Title {
			t.Errorf("approved only = %v", approved)
		}
		// and the indexer was asked by id, the way Radarr searches for a film
		var byID bool
		for _, r := range indexer.Requests() {
			if r.Query["t"] == "movie" && (r.Query["tmdbid"] == "14337" || strings.TrimPrefix(r.Query["imdbid"], "tt") == "0390384") {
				byID = true
			}
		}
		if !byID {
			t.Errorf("Radarr never searched the indexer by id: %v", indexer.Requests())
		}
	})

	t.Run("a release parses the way Radarr reads it", func(t *testing.T) {
		out := call(t, "release_parse", map[string]any{"title": primerHDTV.Title})
		movie := object(t, out["movie"], "movie")
		if str(out["quality"]) != "HDTV-720p" || str(out["release_group"]) != "GRP" || num(t, out["year"], "year") != downloadYear || str(movie["title"]) != downloadTitle {
			t.Errorf("release_parse = %v", out)
		}
		stranger := call(t, "release_parse", map[string]any{"title": "Some.Other.Film.2011.1080p.WEB-DL-XYZ"})
		if stranger["movie"] != nil || str(stranger["quality"]) != "WEBDL-1080p" || num(t, stranger["year"], "year") != 2011 {
			t.Errorf("an unknown film = %v", stranger)
		}
		callErr(t, "release_parse", map[string]any{"title": " "})
	})

	t.Run("a rejected release is grabbed by hand, and the download client gets it", func(t *testing.T) {
		var hdtv map[string]any
		for _, r := range releases {
			if str(r["title"]) == primerHDTV.Title {
				hdtv = r
			}
		}
		out := call(t, "release_grab", map[string]any{"guid": hdtv["guid"], "indexer_id": hdtv["indexer_id"]})
		// Radarr's answer to a grab is only what it was sent: the release's
		// name, the film and the indexer come from the history it wrote
		grabbed := object(t, out["grabbed"], "grabbed")
		if str(out["guid"]) != str(hdtv["guid"]) || str(grabbed["event"]) != "grabbed" || str(grabbed["source_title"]) != primerHDTV.Title ||
			str(grabbed["movie"]) != "Primer (2004)" || object(t, grabbed["details"], "details")["indexer"] != "Fake Indexer" {
			t.Errorf("release_grab = %v", out)
		}
		if !slices.Contains(indexer.Grabbed(), primerHDTV.Title) {
			t.Errorf("Radarr never fetched the .nzb: %v", indexer.Grabbed())
		}
		if _, err := os.Stat(hostPath(nzbFolder + "/" + primerHDTV.Title + ".nzb")); err != nil {
			t.Errorf("the blackhole has no .nzb: %v", err)
		}
		if ev := events(t); len(ev) == 0 || ev[0] != "grabbed" {
			t.Errorf("history = %v, want a grab first", ev)
		}
		grab := rows(t, call(t, "history_list", map[string]any{"movie": "Primer", "event": "grabbed"})["events"], "events")
		if len(grab) != 1 || str(grab[0]["source_title"]) != primerHDTV.Title || object(t, grab[0]["details"], "details")["indexer"] != "Fake Indexer" {
			t.Errorf("the grab in history = %v", grab)
		} else if grab[0]["id"] != grabbed["id"] {
			t.Errorf("release_grab answered history %v, the history holds %v", grabbed["id"], grab[0]["id"])
		}
		if msg := callErr(t, "release_grab", map[string]any{"guid": "", "indexer_id": 0}); !strings.Contains(msg, "guid and indexer_id are both required") {
			t.Errorf("a grab of nothing = %q", msg)
		}
	})

	t.Run("the download finishes and Radarr imports it", func(t *testing.T) {
		finish(t, primerHDTV.Title, 77, "1280x720", true)
		if !eventually(func() bool {
			checkDownloads(t)
			got := call(t, "movie_get", map[string]any{"movie": "Primer"})
			return got["has_file"] == true
		}) {
			t.Fatalf("Primer was never imported; queue %v, history %v", call(t, "queue_list", map[string]any{"include_unknown": true}), events(t))
		}
		got := call(t, "movie_get", map[string]any{"movie": "Primer"})
		file := object(t, got["file"], "file")
		if str(file["quality"]) != "HDTV-720p" || !strings.HasPrefix(str(file["path"]), downloadFolder+"/") || str(file["release_group"]) != "GRP" {
			t.Errorf("the imported file = %v", file)
		}
		if ev := events(t); !slices.Contains(ev, "downloadFolderImported") {
			t.Errorf("history = %v, want an import", ev)
		}
		imported := rows(t, call(t, "history_list", map[string]any{"event": "downloadFolderImported", "days": 1})["events"], "events")
		if len(imported) == 0 || str(imported[0]["movie"]) != "Primer (2004)" {
			t.Errorf("the library's imports today = %v", imported)
		}
		// the movie's recent history is part of movie_get
		if h := rows(t, got["history"], "history"); len(h) < 2 {
			t.Errorf("movie_get history = %v", h)
		}
		// downloading it is what audit_missing_files asked for, and the file
		// it got is what audit_cutoff_unmet and audit_quality ask to replace
		if slices.Contains(findings(t, call(t, "audit_missing_files", nil)), "Primer (2004)") {
			t.Error("Primer is still missing after its download imported")
		}
		if !slices.Contains(findings(t, call(t, "audit_cutoff_unmet", nil)), "Primer (2004)") {
			t.Error("a 720p file below the profile's Bluray-1080p cutoff is not flagged")
		}
		if !slices.Contains(findings(t, call(t, "audit_quality", map[string]any{"min_resolution": 1080})), "Primer (2004)") {
			t.Error("a 720p file is not flagged below 1080 lines")
		}
	})

	t.Run("an automatic search grabs the release the profile wants", func(t *testing.T) {
		// Radarr's profiles are made with upgrades off, and with them off it
		// never replaces a file it has; the journey's film needs one on
		call(t, "qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": true})
		t.Cleanup(func() {
			_, _ = invoke("qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": false})
		})
		out := call(t, "movie_download", map[string]any{"movies": []any{"Primer"}})
		if cmd := object(t, out["command"], "command"); str(cmd["name"]) != "MoviesSearch" || str(cmd["status"]) != "completed" {
			t.Fatalf("movie_download = %v", out)
		}
		if !eventually(func() bool { return slices.Contains(indexer.Grabbed(), primerBluray.Title) }) {
			t.Fatalf("the automatic search grabbed %v", indexer.Grabbed())
		}
		grabs := rows(t, call(t, "history_list", map[string]any{"movie": "Primer", "event": "grabbed"})["events"], "events")
		if len(grabs) != 2 || str(grabs[0]["source_title"]) != primerBluray.Title {
			t.Errorf("grabs = %v", grabs)
		}
	})

	t.Run("a grab marked failed is blocklisted, and taken off the blocklist", func(t *testing.T) {
		out := call(t, "history_mark_failed", map[string]any{"movie": "Primer"})
		if marked := object(t, out["marked"], "marked"); str(marked["source_title"]) != primerBluray.Title || str(marked["event"]) != "grabbed" {
			t.Fatalf("history_mark_failed = %v", out)
		}
		var entry map[string]any
		if !eventually(func() bool {
			for _, b := range rows(t, call(t, "blocklist_list", map[string]any{"movie": "Primer"})["entries"], "entries") {
				if str(b["source_title"]) == primerBluray.Title {
					entry = b
				}
			}
			return entry != nil
		}) {
			t.Fatalf("the failed release is not on the blocklist")
		}
		if str(entry["movie"]) != "Primer (2004)" || str(entry["indexer"]) != "Fake Indexer" {
			t.Errorf("the blocklist entry = %v", entry)
		}
		all := call(t, "blocklist_list", nil)
		if num(t, all["total"], "total") < 1 {
			t.Errorf("the whole blocklist = %v", all)
		}
		if ev := events(t); !slices.Contains(ev, "downloadFailed") {
			t.Errorf("history = %v, want the failure", ev)
		}
		// a blocklisted release is one Radarr no longer takes on its own
		var blocked bool
		for _, r := range rows(t, call(t, "release_search", map[string]any{"movie": "Primer"})["releases"], "releases") {
			if str(r["title"]) == primerBluray.Title && r["approved"] == false && strings.Contains(strings.Join(strs(t, r["rejections"], "rejections"), " "), "blocklist") {
				blocked = true
			}
		}
		if !blocked {
			t.Error("the blocklisted release is still approved")
		}
		removed := call(t, "blocklist_remove", map[string]any{"ids": []any{entry["id"]}})
		if got := removed["removed"].([]any); len(got) != 1 { //nolint:forcetypeassert // the tool's own output
			t.Errorf("blocklist_remove = %v", removed)
		}
		if left := rows(t, call(t, "blocklist_list", map[string]any{"movie": "Primer"})["entries"], "entries"); len(left) != 0 {
			t.Errorf("still blocklisted: %v", left)
		}
		// marking an import failed is refused: only a grab can fail
		for _, e := range rows(t, call(t, "history_list", map[string]any{"movie": "Primer", "event": "downloadFolderImported"})["events"], "events") {
			if msg := callErr(t, "history_mark_failed", map[string]any{"id": e["id"]}); !strings.Contains(msg, "only a grab can be marked failed") {
				t.Errorf("marking an import failed = %q", msg)
			}
		}
	})

	t.Run("a download with nothing to import is stuck, found, and cleared", func(t *testing.T) {
		finish(t, primerBluray.Title, 0, "", false)
		var stuck map[string]any
		if !eventually(func() bool {
			checkDownloads(t)
			for _, f := range rows(t, call(t, "audit_queue", nil)["findings"], "findings") {
				if str(f["title"]) == primerBluray.Title {
					stuck = f
				}
			}
			return stuck != nil
		}) {
			t.Fatalf("the empty download never showed as stuck: %v", call(t, "queue_list", map[string]any{"include_unknown": true}))
		}
		if num(t, stuck["queue_id"], "queue_id") == 0 || !strings.Contains(str(stuck["detail"]), "Primer (2004)") {
			t.Errorf("the stuck download = %v", stuck)
		}
		queue := call(t, "queue_list", map[string]any{"movie": "Primer"})
		items := rows(t, queue["items"], "items")
		if len(items) != 1 || str(items[0]["health"]) == "ok" || len(strs(t, items[0]["messages"], "messages")) == 0 || str(items[0]["movie"]) != "Primer (2004)" {
			t.Errorf("queue_list = %v", queue)
		}
		// nothing in it to import by hand
		if files := rows(t, call(t, "import_scan", map[string]any{"download_id": items[0]["download_id"]})["files"], "files"); len(files) != 0 {
			t.Errorf("import_scan of the empty download = %v", files)
		}
		out := call(t, "queue_remove", map[string]any{"ids": []any{stuck["queue_id"]}, "remove_from_client": true})
		if removed := rows(t, out["removed"], "removed"); len(removed) != 1 || str(removed[0]["title"]) != primerBluray.Title {
			t.Errorf("queue_remove = %v", out)
		}
		if !eventually(func() bool {
			_, err := os.Stat(hostPath(watchFolder + "/" + primerBluray.Title))
			return os.IsNotExist(err)
		}) {
			t.Error("removed from the client, the download is still in the watch folder")
		}
		if got := findings(t, call(t, "audit_queue", nil)); len(got) != 0 {
			t.Errorf("the queue is still not clear: %v", got)
		}
		if msg := callErr(t, "queue_remove", map[string]any{"ids": []any{999999}}); !strings.Contains(msg, "no queue item 999999") {
			t.Errorf("removing what is not there = %q", msg)
		}
	})

	t.Run("a better copy clears what the quality audits asked for", func(t *testing.T) {
		// Radarr refuses to import over a file it has while its profile does
		// not upgrade, however the release was grabbed
		call(t, "qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": true})
		t.Cleanup(func() {
			_, _ = invoke("qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": false})
		})
		// the automatic search grabbed the Bluray, the blocklist test marked
		// it failed: grab it by hand and let it finish this time
		releases := rows(t, call(t, "release_search", map[string]any{"movie": "Primer"})["releases"], "releases")
		var bluray map[string]any
		for _, r := range releases {
			if str(r["title"]) == primerBluray.Title {
				bluray = r
			}
		}
		if bluray == nil {
			t.Fatalf("the Bluray is not on offer: %v", releases)
		}
		call(t, "release_grab", map[string]any{"guid": bluray["guid"], "indexer_id": bluray["indexer_id"]})
		finish(t, primerBluray.Title, 77, "1920x1080", true)
		if !eventually(func() bool {
			checkDownloads(t)
			file, ok := call(t, "movie_get", map[string]any{"movie": "Primer"})["file"].(map[string]any)
			return ok && str(file["quality"]) == "Bluray-1080p"
		}) {
			t.Fatalf("the better copy never imported: %v", call(t, "movie_get", map[string]any{"movie": "Primer"})["file"])
		}
		for _, tc := range []struct {
			audit string
			args  map[string]any
		}{
			{"audit_cutoff_unmet", nil},
			{"audit_quality", map[string]any{"min_resolution": 1080}},
		} {
			if got := findings(t, call(t, tc.audit, tc.args)); slices.Contains(got, "Primer (2004)") {
				t.Errorf("%s still finds Primer with a Bluray-1080p file: %v", tc.audit, got)
			}
		}
	})
}
