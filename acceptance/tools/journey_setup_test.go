//go:build acceptance

package tools_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/radarr-mcp/lib/testenv"
)

// The one journey from nothing to nothing: a Radarr set up with the tools
// alone, in the order a person would do it - a tag, a profile, a format, the
// rules, an indexer, a download client, a root folder - then used: a folder
// already on disk is found by the audits and taken in, a film with no file is
// searched for, grabbed through the new indexer into the new client,
// downloaded and imported, and the one thing wrong with what came in is
// found by the audits and fixed. Then every piece is deleted, in the order
// that lets it go, and Radarr is as it was recorded before the journey.
// Every audit's count is checked against the recording as well: nothing the
// journey made may be left for an audit to find.

const (
	journeyRoot   = "/media/journey"
	journeyTag    = "journey"
	journeyFolder = journeyRoot + "/Primer (2004)"
	journeyFile   = journeyFolder + "/Primer (2004) Bluray-1080p.mkv"
)

// recorded is the state of Radarr the journey must leave as it found it:
// everything it is set up with, its films and its backups, each as the tools
// list it. A root folder's free space is left out, as the disk is shared.
func recorded(t *testing.T) (state string, audits map[string]int) {
	t.Helper()

	var b strings.Builder
	for _, name := range []string{"tag_list", "qualityprofile_list", "customformat_list", "delayprofile_list", "releaseprofile_list", "indexer_list", "downloadclient_list", "importlist_list", "notification_list", "autotagging_list", "remotepathmapping_list", "customfilter_list", "rootfolder_list", "movie_list", "exclusion_list", "server_backups"} {
		args := map[string]any{}
		if name == "movie_list" {
			args["limit"] = 500
		}
		out := suite.Call(t, name, args)
		if name == "rootfolder_list" {
			for _, f := range acctest.Rows(t, out["root_folders"], "root_folders") {
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

	audits = map[string]int{}
	for _, row := range acctest.Rows(t, suite.Call(t, "audit_all", map[string]any{"deep": true})["audits"], "audits") {
		audits[acctest.Str(row["audit"])] = acctest.NumOr0(row["total_findings"])
	}

	return b.String(), audits
}

func TestJourneyFromNothingToNothing(t *testing.T) {
	skipUnlessReady(t)

	before, auditsBefore := recorded(t)

	// a second indexer site, for the journey's own indexer
	site, err := indexers.Add(newznab.Site{Name: "journey", APIKey: "journey-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Offer(background); err != nil {
		t.Fatal(err)
	}
	env.Mkdir(t, testenv.DataDir(), testenv.HostPath(journeyRoot))

	// what the journey makes that is known by an id alone
	var delayProfile, mapping int
	var excluded []any

	// whatever the journey leaves behind when a step fails is taken away, in the reverse of the order it was made
	t.Cleanup(func() {
		for _, q := range acctest.RowsOf(mustInvokeOf("queue_list", map[string]any{"movie": "tmdb:17431"})["items"]) {
			_, _ = suite.Invoke("queue_remove", map[string]any{"ids": []any{q["id"]}, "remove_from_client": true})
		}
		for _, film := range []string{"tmdb:17431", "tmdb:14337"} {
			_, _ = suite.Invoke("movie_delete", map[string]any{"movie": film, "delete_files": true, "confirm": true})
		}
		if len(excluded) > 0 {
			_, _ = suite.Invoke("exclusion_remove", map[string]any{"ids": excluded})
		}
		_, _ = suite.Invoke("rootfolder_delete", map[string]any{"root_folder": journeyRoot})
		for tool, name := range map[string]string{"importlist_delete": "Journey List", "notification_delete": "Journey Hook", "indexer_delete": "Journey Indexer", "downloadclient_delete": "Journey Blackhole"} {
			_, _ = suite.Invoke(tool, map[string]any{"name": name, "confirm": true})
		}
		for tool, id := range map[string]string{"autotagging_delete": "Journey Rule", "remotepathmapping_delete": "", "customfilter_delete": "Journey Filter", "releaseprofile_delete": "Journey No Cams"} {
			if id != "" {
				_, _ = suite.Invoke(tool, map[string]any{"id": id, "confirm": true})
			}
		}
		for _, row := range acctest.RowsOf(mustInvokeOf("remotepathmapping_list", nil)["items"]) {
			if acctest.Str(row["host"]) == "journey.example" {
				_, _ = suite.Invoke("remotepathmapping_delete", map[string]any{"id": itoa(acctest.NumOr0(row["id"])), "confirm": true})
			}
		}
		for _, row := range acctest.RowsOf(mustInvokeOf("delayprofile_list", nil)["items"]) {
			if slices.Contains(acctest.Texts(row["tags"]), journeyTag) {
				_, _ = suite.Invoke("delayprofile_delete", map[string]any{"id": itoa(acctest.NumOr0(row["id"])), "confirm": true})
			}
		}
		_, _ = suite.Invoke("qualityprofile_delete", map[string]any{"profile": "Journey HD", "confirm": true})
		_, _ = suite.Invoke("customformat_delete", map[string]any{"id": "Journey x265", "confirm": true})
		for _, tag := range []string{journeyTag, "journey-auto"} {
			_, _ = suite.Invoke("tag_delete", map[string]any{"tag": tag})
		}
		_, _ = suite.Invoke("downloadclient_edit", map[string]any{"name": "Blackhole", "options": map[string]any{"enable": true}})
		_, _ = suite.Invoke("naming_edit", map[string]any{"rename_movies": false})
		for _, dir := range []string{watchFolder, nzbFolder} {
			entries, _ := os.ReadDir(testenv.HostPath(dir))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "Moon") {
					_ = os.RemoveAll(testenv.HostPath(dir + "/" + e.Name()))
				}
			}
		}
		_ = os.RemoveAll(testenv.HostPath(journeyRoot))
		site.Withdraw("Moon.2009.1080p.BluRay.x265-JRN", "Moon.2009.CAM.x264-BAD")
	})

	t.Run("set Radarr up: a tag, a profile and a format, the rules, an indexer and a download client", func(t *testing.T) {
		suite.Call(t, "tag_create", map[string]any{"label": journeyTag})
		profile := suite.Call(t, "qualityprofile_add", map[string]any{"name": "Journey HD", "copy_of": profileHD, "upgrade_allowed": true})
		if acctest.Str(profile["cutoff"]) == "" {
			t.Fatalf("the profile added = %v", profile)
		}
		suite.Call(t, "customformat_add", map[string]any{"values": map[string]any{"name": "Journey x265"}, "conditions": []any{map[string]any{"kind": "ReleaseTitleSpecification", "name": "x265", "settings": map[string]any{"value": `\bx265\b`}}}})
		if scored := suite.Call(t, "qualityprofile_edit", map[string]any{"profile": "Journey HD", "format_scores": map[string]any{"Journey x265": 10}}); acctest.Num(t, acctest.Object(t, scored["format_scores"], "format_scores")["Journey x265"], "score") != 10 {
			t.Errorf("the profile's score for the format = %v", scored["format_scores"])
		}

		delay := acctest.Object(t, suite.Call(t, "delayprofile_add", map[string]any{"values": map[string]any{"enableUsenet": true, "enableTorrent": true, "preferredProtocol": "usenet", "usenetDelay": 0, "bypassIfHighestQuality": true, "bypassIfAboveCustomFormatScore": false}, "tags": []any{journeyTag}})["saved"], "saved")
		delayProfile = acctest.Num(t, delay["id"], "id")
		suite.Call(t, "releaseprofile_add", map[string]any{"values": map[string]any{"name": "Journey No Cams", "enabled": true, "ignored": []any{"CAM"}, "required": []any{}}, "tags": []any{journeyTag}})

		added := suite.Call(t, "indexer_add", map[string]any{"kind": "Newznab", "name": "Journey Indexer", "tags": []any{journeyTag}, "settings": map[string]any{"baseUrl": site.URL(), "apiPath": "/api", "apiKey": suite.Secret("journey-key"), "categories": []any{2000, 2040}}})
		if !slices.Equal(acctest.Strs(t, added["tags"], "tags"), []string{journeyTag}) || !acctest.BoolOf(acctest.Object(t, added["options"], "options")["enableAutomaticSearch"]) {
			t.Fatalf("the indexer added = %v, want it tagged and switched on", added)
		}
		// the new client is the one the journey's grabs go to: the suite's own is switched off while it is tried
		suite.Call(t, "downloadclient_edit", map[string]any{"name": "Blackhole", "options": map[string]any{"enable": false}})
		client := suite.Call(t, "downloadclient_add", map[string]any{"kind": "UsenetBlackhole", "name": "Journey Blackhole", "tags": []any{journeyTag}, "settings": map[string]any{"nzbFolder": nzbFolder, "watchFolder": watchFolder}, "options": map[string]any{"removeCompletedDownloads": true}})
		if !acctest.BoolOf(acctest.Object(t, client["options"], "options")["enable"]) {
			t.Fatalf("the download client added = %v, want it switched on", client)
		}
		for _, tool := range []string{"indexer_test", "downloadclient_test"} {
			if results := acctest.Rows(t, suite.Call(t, tool, nil)["results"], "results"); slices.ContainsFunc(results, func(r map[string]any) bool { return !acctest.BoolOf(r["valid"]) }) {
				t.Errorf("%s = %v, want every one valid", tool, results)
			}
		}
	})

	t.Run("a root folder with a film already in it: the audits find it, and it is taken in", func(t *testing.T) {
		makeVideo(t, journeyFile, 77, "1920x1080")
		folder := suite.Call(t, "rootfolder_add", map[string]any{"path": journeyRoot})
		if acctest.Str(folder["path"]) == "" {
			t.Fatalf("the root folder added = %v", folder)
		}
		found := suite.Call(t, "audit_unmapped_folders", map[string]any{"root_folder": journeyRoot})
		unmapped := acctest.Rows(t, found["findings"], "findings")
		if len(unmapped) != 1 || !strings.HasSuffix(acctest.Str(unmapped[0]["folder"])+acctest.Str(unmapped[0]["path"]), "/Primer (2004)") {
			t.Fatalf("the folders no film is in = %v, want Primer's", found)
		}
		identified := acctest.Rows(t, suite.Call(t, "import_identify", map[string]any{"folders": []any{journeyFolder}})["folders"], "folders")
		if len(identified) != 1 || len(acctest.Rows(t, identified[0]["candidates"], "candidates")) == 0 || acctest.NumOr0(acctest.Rows(t, identified[0]["candidates"], "candidates")[0]["tmdb_id"]) != downloadTmdb {
			t.Fatalf("what the folder holds = %v, want Primer", identified)
		}
		taken := suite.Call(t, "movie_add", map[string]any{"movie": "tmdb:14337", "quality_profile": "Journey HD", "root_folder": journeyRoot, "folder": "Primer (2004)", "tags": []any{journeyTag}})
		if !acctest.BoolOf(taken["has_file"]) {
			t.Fatalf("Primer taken in = %v, want it with its file", taken)
		}
		if left := acctest.Rows(t, suite.Call(t, "audit_unmapped_folders", map[string]any{"root_folder": journeyRoot})["findings"], "findings"); len(left) != 0 {
			t.Errorf("after Primer was taken in, the audit still finds %v", left)
		}
	})

	t.Run("a film with no file: searched for, grabbed through the new indexer into the new client, and imported", func(t *testing.T) {
		added := suite.Call(t, "movie_add", map[string]any{"movie": "tmdb:17431", "quality_profile": "Journey HD", "root_folder": journeyRoot, "tags": []any{journeyTag}})
		if !acctest.IsBool(added["has_file"], false) {
			t.Fatalf("Moon added = %v", added)
		}
		if missing := acctest.Rows(t, suite.Call(t, "audit_missing_files", map[string]any{"root_folder": journeyRoot})["findings"], "findings"); len(missing) != 1 || acctest.Str(missing[0]["title"]) != "Moon" {
			t.Fatalf("the films with no file under the new root = %v, want Moon", missing)
		}

		good := newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Moon.2009.1080p.BluRay.x265-JRN", TMDBID: 17431, Size: 6 << 30}
		cam := newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Moon.2009.CAM.x264-BAD", TMDBID: 17431, Size: 700 << 20}
		if err := site.Offer(good, cam); err != nil {
			t.Fatal(err)
		}
		releases := acctest.Rows(t, suite.Call(t, "release_search", map[string]any{"movie": "Moon"})["releases"], "releases")
		if best, worst := named(releases, "title", good.Title), named(releases, "title", cam.Title); best == nil || !acctest.BoolOf(best["approved"]) || acctest.Str(best["indexer"]) != "Journey Indexer" || !slices.Contains(acctest.Strs(t, best["custom_formats"], "custom_formats"), "Journey x265") || worst == nil || !acctest.IsBool(worst["approved"], false) {
			t.Fatalf("the releases = %v, want the x265 approved from the new indexer with the new format on it, and the CAM turned down", releases)
		}

		grabbed := suite.Call(t, "movie_download", map[string]any{"movies": []any{"Moon"}})
		if acctest.Str(acctest.Object(t, grabbed["command"], "command")["status"]) != "completed" {
			t.Fatalf("the search = %v", grabbed)
		}
		if !acctest.EventuallyWithin(time.Minute, func() bool {
			_, err := os.Stat(testenv.HostPath(nzbFolder + "/" + good.Title + ".nzb"))
			return err == nil
		}) {
			t.Fatalf("the new download client never got %s", good.Title)
		}
		grab := acctest.Rows(t, suite.Call(t, "history_list", map[string]any{"movie": "Moon", "event": "grabbed"})["events"], "events")
		if len(grab) == 0 || acctest.Str(acctest.Object(t, grab[0]["details"], "details")["indexer"]) != "Journey Indexer" || acctest.Str(acctest.Object(t, grab[0]["details"], "details")["downloadClientName"]) != "Journey Blackhole" {
			t.Errorf("the grab in the history = %v, want it through the new indexer into the new client", grab)
		}

		finish(t, good.Title, 97, "1920x1080", true)
		checkDownloads(t)
		if !acctest.EventuallyWithin(2*time.Minute, func() bool {
			return acctest.BoolOf(suite.Call(t, "movie_get", map[string]any{"movie": "Moon"})["has_file"])
		}) {
			t.Fatalf("Moon was never imported: queue %v", suite.Call(t, "queue_list", map[string]any{"movie": "Moon"}))
		}
	})

	t.Run("what came in is named as the release was: the audits find it, and it is renamed", func(t *testing.T) {
		suite.Call(t, "naming_edit", map[string]any{"rename_movies": true})
		if found := acctest.Rows(t, suite.Call(t, "audit_naming", map[string]any{"root_folder": journeyRoot})["findings"], "findings"); len(found) != 1 || acctest.Str(found[0]["title"]) != "Moon" {
			t.Fatalf("the files not named by the scheme under the new root = %v, want Moon's", found)
		}
		renamed := suite.Call(t, "movie_rename", map[string]any{"movies": []any{"Moon"}, "preview": false})
		if !acctest.BoolOf(renamed["renamed"]) {
			t.Fatalf("the rename = %v", renamed)
		}
		if left := acctest.Rows(t, suite.Call(t, "audit_naming", map[string]any{"root_folder": journeyRoot})["findings"], "findings"); len(left) != 0 {
			t.Errorf("after the rename, the audit still finds %v", left)
		}
		// renaming was off before the journey, and the fixtures' scene names are only findings while it is on
		suite.Call(t, "naming_edit", map[string]any{"rename_movies": false})
	})

	t.Run("a collection held in part: the audits find it, and the rest is excluded", func(t *testing.T) {
		// Moon is one of a TMDB collection: the audit says what the library lacks of it, and a person who does not want the rest excludes it
		gap := finding(t, suite.Call(t, "audit_collection_gaps", nil), "Moon Collection")
		missing := acctest.Rows(t, gap["missing"], "missing")
		if len(missing) == 0 {
			t.Fatalf("Moon's collection = %v, want what it lacks", gap)
		}
		for _, m := range missing {
			excluded = append(excluded, suite.Call(t, "exclusion_add", map[string]any{"movie": "tmdb:" + itoa(acctest.Num(t, m["tmdb_id"], "tmdb_id"))})["id"])
		}
		for _, f := range acctest.Rows(t, suite.Call(t, "audit_collection_gaps", nil)["findings"], "findings") {
			if acctest.Str(f["title"]) == "Moon Collection" {
				t.Errorf("with the rest excluded, the audit still finds %v", f)
			}
		}
	})

	t.Run("the rest of a setup: a list, a notification, a rule, a mapping, a filter and a backup", func(t *testing.T) {
		suite.Call(t, "importlist_add", map[string]any{"kind": "RadarrImport", "name": "Journey List", "off": true, "settings": map[string]any{"baseUrl": "http://localhost:7878", "apiKey": testenv.Token()}, "options": map[string]any{"qualityProfileId": acctest.NumOr0(named(rowsOf(t, "qualityprofile_list", "profiles"), "name", "Journey HD")["id"]), "rootFolderPath": journeyRoot}})
		suite.Call(t, "notification_add", map[string]any{"kind": "Webhook", "name": "Journey Hook", "off": true, "settings": map[string]any{"url": "http://127.0.0.1:9/journey", "method": "POST"}})
		// a rule that matches no film, so no tag lands on the fixtures
		suite.Call(t, "autotagging_add", map[string]any{"values": map[string]any{"name": "Journey Rule"}, "tags": []any{"journey-auto"}, "conditions": []any{map[string]any{"kind": "GenreSpecification", "name": "jousting", "settings": map[string]any{"value": []any{"Jousting"}}}}})
		mapping = acctest.Num(t, acctest.Object(t, suite.Call(t, "remotepathmapping_add", map[string]any{"values": map[string]any{"host": "journey.example", "remotePath": "/remote/journey/", "localPath": journeyRoot + "/"}})["saved"], "saved")["id"], "id")
		suite.Call(t, "customfilter_add", map[string]any{"values": map[string]any{"label": "Journey Filter", "type": "movieIndex", "filters": []any{map[string]any{"key": "tags", "value": []any{1}, "type": "contains"}}}})
		backup := acctest.Object(t, suite.Call(t, "server_backup_create", nil)["backup"], "backup")
		suite.Call(t, "server_backup_delete", map[string]any{"backup": acctest.Str(backup["name"]), "confirm": true})
	})

	t.Run("every audit: nothing of the journey's own is left to find", func(t *testing.T) {
		for _, row := range acctest.Rows(t, suite.Call(t, "audit_all", map[string]any{"deep": true})["audits"], "audits") {
			if name, count := acctest.Str(row["audit"]), acctest.NumOr0(row["total_findings"]); count != auditsBefore[name] {
				t.Errorf("%s finds %d, where it found %d before the journey: something the journey made is wrong: %v", name, count, auditsBefore[name], findings(t, suite.Call(t, name, nil)))
			}
		}
	})

	t.Run("everything taken down again, each piece asking first", func(t *testing.T) {
		if len(excluded) > 0 {
			suite.Call(t, "exclusion_remove", map[string]any{"ids": excluded})
		}
		for _, film := range []string{"Moon", "Primer"} {
			gone := suite.Call(t, "movie_delete", map[string]any{"movie": film, "delete_files": true, "confirm": true})
			if gone["deleted"] == nil {
				t.Errorf("%s deleted = %v", film, gone)
			}
		}
		// Radarr removes a deleted film's folder after it has answered
		if !acctest.EventuallyWithin(time.Minute, func() bool { _, err := os.Stat(testenv.HostPath(journeyFolder)); return os.IsNotExist(err) }) {
			t.Error("Primer's folder is still there a minute after the film was deleted with its files")
		}
		if gone := suite.Call(t, "rootfolder_delete", map[string]any{"root_folder": journeyRoot}); gone["deleted"] == nil && gone["path"] == nil {
			t.Errorf("the root folder deleted = %v", gone)
		}

		// each thing Radarr is set up with asks first; what a tag limits goes before the tag
		still := func(tool, key, name string) func() bool {
			return func() bool { return named(rowsOf(t, tool, key), "name", name) != nil }
		}
		deleted(t, "importlist_delete", map[string]any{"name": "Journey List"}, "Journey List", still("importlist_list", "items", "Journey List"))
		deleted(t, "notification_delete", map[string]any{"name": "Journey Hook"}, "Journey Hook", still("notification_list", "items", "Journey Hook"))
		deleted(t, "autotagging_delete", map[string]any{"id": "Journey Rule"}, "Journey Rule", still("autotagging_list", "items", "Journey Rule"))
		deleted(t, "remotepathmapping_delete", map[string]any{"id": itoa(mapping)}, "remote path mapping "+itoa(mapping), func() bool {
			return slices.ContainsFunc(rowsOf(t, "remotepathmapping_list", "items"), func(r map[string]any) bool { return acctest.NumOr0(r["id"]) == mapping })
		})
		deleted(t, "customfilter_delete", map[string]any{"id": "Journey Filter"}, "Journey Filter", func() bool { return named(rowsOf(t, "customfilter_list", "items"), "label", "Journey Filter") != nil })
		deleted(t, "indexer_delete", map[string]any{"name": "Journey Indexer"}, "Journey Indexer", still("indexer_list", "indexers", "Journey Indexer"))
		deleted(t, "downloadclient_delete", map[string]any{"name": "Journey Blackhole"}, "Journey Blackhole", still("downloadclient_list", "download_clients", "Journey Blackhole"))
		suite.Call(t, "downloadclient_edit", map[string]any{"name": "Blackhole", "options": map[string]any{"enable": true}})
		deleted(t, "releaseprofile_delete", map[string]any{"id": "Journey No Cams"}, "Journey No Cams", still("releaseprofile_list", "items", "Journey No Cams"))
		deleted(t, "delayprofile_delete", map[string]any{"id": itoa(delayProfile)}, "delay profile "+itoa(delayProfile), func() bool {
			return slices.ContainsFunc(rowsOf(t, "delayprofile_list", "items"), func(r map[string]any) bool { return acctest.NumOr0(r["id"]) == delayProfile })
		})
		deleted(t, "qualityprofile_delete", map[string]any{"profile": "Journey HD"}, "Journey HD", still("qualityprofile_list", "profiles", "Journey HD"))
		deleted(t, "customformat_delete", map[string]any{"id": "Journey x265"}, "Journey x265", still("customformat_list", "formats", "Journey x265"))
		for _, tag := range []string{journeyTag, "journey-auto"} {
			suite.Call(t, "tag_delete", map[string]any{"tag": tag})
		}
		suite.Call(t, "naming_edit", map[string]any{"rename_movies": false})
	})

	after, auditsAfter := recorded(t)
	if after != before {
		b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
		for i := range min(len(a), len(b)) {
			if a[i] != b[i] {
				t.Errorf("Radarr is not as it was before the journey:\nbefore %s\nafter  %s", b[i], a[i])
			}
		}
	}
	for name, count := range auditsAfter {
		if count != auditsBefore[name] {
			t.Errorf("%s finds %d after the journey, where it found %d before", name, count, auditsBefore[name])
		}
	}
}

// mustInvokeOf calls a tool for a clean-up, which has no test to fail: what
// it cannot read it treats as nothing.
func mustInvokeOf(name string, args map[string]any) map[string]any {
	out, err := suite.Invoke(name, args)
	if err != nil || out == nil {
		return map[string]any{}
	}

	return out
}
