//go:build integration

package acceptance

import (
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/radarr-mcp/internal/fakeindexer"
	"github.com/katbyte/radarr-mcp/lib/client"
	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// settingsRaw reads a Radarr resource as the JSON Radarr sends, through the
// base client rather than a generated model, so a comparison sees every
// field - including any a round trip through a model would drop.
func settingsRaw(t *testing.T, path string) any {
	t.Helper()

	req, err := sdk.Client.NewRequest(ctx, client.RequestOptions{HttpMethod: http.MethodGet, Path: path, ExpectedStatusCodes: []int{http.StatusOK}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := req.Execute(ctx)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	var v any
	if err := resp.Unmarshal(&v); err != nil {
		t.Fatal(err)
	}

	return v
}

// settingsPut sends a raw JSON body with PUT, for the few edits no tool makes
// (a custom format's score in a profile).
func settingsPut(t *testing.T, path string, body any) {
	t.Helper()

	req, err := sdk.Client.NewRequest(ctx, client.RequestOptions{
		ContentType: "application/json", HttpMethod: http.MethodPut, Path: path, ExpectedStatusCodes: []int{http.StatusOK, http.StatusAccepted},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Marshal(body); err != nil {
		t.Fatal(err)
	}
	if _, err := req.Execute(ctx); err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
}

// settingsUnchanged fails when a resource's JSON is not what it was before a
// test changed and restored it.
func settingsUnchanged(t *testing.T, path string, before any) {
	t.Helper()

	if after := settingsRaw(t, path); !reflect.DeepEqual(before, after) {
		t.Errorf("%s was not restored:\nbefore %v\nafter  %v", path, before, after)
	}
}

// settingsTag finds a tag_list row by label, nil when there is none.
func settingsTag(t *testing.T, label string) map[string]any {
	t.Helper()

	for _, row := range rows(t, call(t, "tag_list", nil)["tags"], "tags") {
		if str(row["label"]) == label {
			return row
		}
	}

	return nil
}

// A tag's life: created, carried by films, renamed with the films keeping
// it, and deleted - which takes it off the films first, since Radarr itself
// refuses to delete a tag anything still carries. Labels Radarr would refuse
// are refused before they reach it.
func TestTags(t *testing.T) {
	const label, renamed = "acceptance-settings", "acceptance-settings-renamed"
	alien, aliens := movieID(t, "Alien", 1979), movieID(t, "Aliens", 1986)
	t.Cleanup(func() {
		for _, l := range []string{label, renamed} {
			if row := settingsTag(t, l); row != nil {
				id := num(t, row["id"], "id")
				_, _ = sdk.PutMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: []int{alien, aliens}, Tags: []int{id}, ApplyTags: radarr.ApplyTagsRemove})
				_, _ = sdk.DeleteTagById(ctx, id)
			}
		}
	})

	created := call(t, "tag_create", map[string]any{"label": "  Acceptance-Settings "})
	if str(created["label"]) != label || num0(created["id"]) == 0 {
		t.Fatalf("tag_create = %v, want %q, lower case and trimmed", created, label)
	}
	id := num(t, created["id"], "id")
	if msg := callErr(t, "tag_create", map[string]any{"label": label}); !strings.Contains(msg, `tag "acceptance-settings" already exists`) {
		t.Errorf("a second tag_create = %s", msg)
	}
	for _, bad := range []string{"has space", "under_score", "4k!"} {
		if msg := callErr(t, "tag_create", map[string]any{"label": bad}); !strings.Contains(msg, "Radarr allows only a-z, 0-9 and -") {
			t.Errorf("tag_create %q = %s", bad, msg)
		}
	}
	if msg := callErr(t, "tag_create", map[string]any{"label": "  "}); !strings.Contains(msg, "no label") {
		t.Errorf("tag_create of nothing = %s", msg)
	}

	// carried by two films, which tag_list counts and the films show
	if _, err := sdk.PutMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: []int{alien, aliens}, Tags: []int{id}, ApplyTags: radarr.ApplyTagsAdd}); err != nil {
		t.Fatal(err)
	}
	if row := settingsTag(t, label); row == nil || num(t, row["movies"], "movies") != 2 || row["indexers"] != nil {
		t.Errorf("tag_list row = %v, want two films and nothing else", row)
	}

	out := call(t, "tag_rename", map[string]any{"tag": label, "label": renamed})
	if str(out["label"]) != renamed || num(t, out["id"], "id") != id || num(t, out["movies"], "movies") != 2 {
		t.Errorf("tag_rename = %v", out)
	}
	if tags := strs(t, call(t, "movie_get", map[string]any{"movie": strconv.Itoa(alien)})["tags"], "tags"); !slices.Equal(tags, []string{renamed}) {
		t.Errorf("Alien's tags after the rename = %v", tags)
	}
	if msg := callErr(t, "tag_rename", map[string]any{"tag": "nope", "label": "x"}); !strings.Contains(msg, `no tag "nope"; the tags are`) || !strings.Contains(msg, renamed) {
		t.Errorf("renaming an unknown tag = %s", msg)
	}
	if msg := callErr(t, "tag_rename", map[string]any{"tag": renamed, "label": "Two Words"}); !strings.Contains(msg, "Radarr allows only") {
		t.Errorf("renaming to a label Radarr refuses = %s", msg)
	}

	// deleting takes it off the films, and says how many carried it
	deleted := call(t, "tag_delete", map[string]any{"tag": strconv.Itoa(id)})
	if str(deleted["label"]) != renamed || num(t, deleted["movies"], "movies") != 2 {
		t.Errorf("tag_delete = %v", deleted)
	}
	if settingsTag(t, renamed) != nil {
		t.Error("the tag is still listed after tag_delete")
	}
	for _, m := range []int{alien, aliens} {
		if tags := strs(t, call(t, "movie_get", map[string]any{"movie": strconv.Itoa(m)})["tags"], "tags"); len(tags) != 0 {
			t.Errorf("film %d still carries %v", m, tags)
		}
	}
}

// A tag an indexer is limited by is not deleted: that would quietly change
// which films the indexer serves. tag_delete says what still uses it.
func TestTagDeleteRefusesATagInUse(t *testing.T) {
	const label = "acceptance-limited"
	created := call(t, "tag_create", map[string]any{"label": label})
	id := num(t, created["id"], "id")
	t.Cleanup(func() { _, _ = sdk.DeleteTagById(ctx, id) })

	// disabled, so Radarr saves it without asking it anything
	tagged := settingsIndexer(t, "Tagged Indexer", "http://127.0.0.1:9", "unused", false, []int{id})
	t.Cleanup(func() { _, _ = sdk.DeleteIndexerById(ctx, tagged) })

	if row := settingsTag(t, label); row == nil || num(t, row["indexers"], "indexers") != 1 {
		t.Errorf("tag_list row = %v, want one indexer", row)
	}
	if msg := callErr(t, "tag_delete", map[string]any{"tag": label}); !strings.Contains(msg, `tag "acceptance-limited" still limits 1 indexer`) {
		t.Errorf("deleting a tag an indexer uses = %s", msg)
	}
	if settingsTag(t, label) == nil {
		t.Error("the refused tag was deleted anyway")
	}
}

// rootfolder_list counts the films in each root folder and the folders in
// it no film is in; rootfolder_add and rootfolder_delete add and remove one
// without touching what is on disk.
func TestRootFolders(t *testing.T) {
	want := map[string]int{}
	for _, f := range fixtures() {
		want[f.Root]++
	}
	out := call(t, "rootfolder_list", nil)
	byPath := map[string]map[string]any{}
	for _, r := range rows(t, out["root_folders"], "root_folders") {
		byPath[str(r["path"])] = r
	}
	for root, films := range want {
		r := byPath[root]
		if r == nil {
			t.Errorf("no root folder %s among %v", root, out)
			continue
		}
		if num(t, r["movies"], "movies") != films || r["accessible"] != true || num0(r["free_space"]) <= 0 {
			t.Errorf("%s = %v, want %d films", root, r, films)
		}
		// Ronin in one, the second copy of Alien in the other
		if num(t, r["unmapped_folders"], "unmapped_folders") != 1 {
			t.Errorf("%s has %v unmapped folders, want 1", root, r["unmapped_folders"])
		}
	}

	// a new root folder over a folder laid out for it
	const extra = "/media/extra-root"
	mediaMkdir(t, hostPath(extra+"/Some Film (2001)"))
	t.Cleanup(func() {
		for _, r := range rows(t, call(t, "rootfolder_list", nil)["root_folders"], "root_folders") {
			if str(r["path"]) == extra {
				_, _ = sdk.DeleteRootfolderById(ctx, num(t, r["id"], "id"))
			}
		}
		_ = os.RemoveAll(hostPath(extra))
	})
	added := call(t, "rootfolder_add", map[string]any{"path": extra})
	if str(added["path"]) != extra || added["accessible"] != true || num(t, added["unmapped_folders"], "unmapped_folders") != 1 || num0(added["id"]) == 0 {
		t.Errorf("rootfolder_add = %v", added)
	}
	if msg := callErr(t, "rootfolder_add", map[string]any{"path": "/media/no-such-folder"}); msg == "" {
		t.Error("a folder that does not exist was added")
	}

	removed := call(t, "rootfolder_delete", map[string]any{"root_folder": extra})
	if str(removed["path"]) != extra || num(t, removed["movies"], "movies") != 0 {
		t.Errorf("rootfolder_delete = %v", removed)
	}
	for _, r := range rows(t, call(t, "rootfolder_list", nil)["root_folders"], "root_folders") {
		if str(r["path"]) == extra {
			t.Error("the root folder is still listed after rootfolder_delete")
		}
	}
	if _, err := os.Stat(hostPath(extra + "/Some Film (2001)")); err != nil {
		t.Errorf("rootfolder_delete touched the disk: %v", err)
	}

	msg := callErr(t, "rootfolder_delete", map[string]any{"root_folder": "/media/nope"})
	if !strings.Contains(msg, `no root folder "/media/nope"; the root folders are`) || !strings.Contains(msg, moviesRoot) {
		t.Errorf("deleting an unknown root folder = %s", msg)
	}
}

// qualityprofile_list shows each profile's qualities best first, its cutoff
// and how many films are on it; qualityprofile_edit changes a profile and
// is restored exactly - the round trip through the generated model keeps
// every field Radarr sent.
func TestQualityProfiles(t *testing.T) {
	films := map[string]int{}
	for _, f := range fixtures() {
		films[f.Profile]++
	}
	out := call(t, "qualityprofile_list", nil)
	profiles := map[string]map[string]any{}
	for _, p := range rows(t, out["profiles"], "profiles") {
		profiles[str(p["name"])] = p
		if num(t, p["movies"], "movies") != films[str(p["name"])] {
			t.Errorf("%s holds %v films, want %d", p["name"], p["movies"], films[str(p["name"])])
		}
	}
	hd := profiles[profileHD]
	if hd == nil || len(profiles) != 6 {
		t.Fatalf("profiles = %v", out)
	}
	allowed := strs(t, hd["allowed"], "allowed")
	if str(hd["cutoff"]) != "Bluray-1080p" || hd["upgrade_allowed"] != false || len(allowed) == 0 || allowed[0] != "Remux-1080p" || allowed[len(allowed)-1] != "HDTV-1080p" ||
		!slices.Contains(allowed, "WEBDL-1080p") || !slices.Contains(allowed, "WEBRip-1080p") || slices.Contains(allowed, "HDTV-720p") {
		t.Errorf("HD-1080p = %v, want Remux-1080p down to HDTV-1080p, the web group's qualities among them", hd)
	}
	if uhd := profiles[profileUHD]; str(uhd["cutoff"]) != "Remux-2160p" {
		t.Errorf("Ultra-HD's cutoff = %v", uhd["cutoff"])
	}

	id := num(t, hd["id"], "id")
	path := "/api/v3/qualityprofile/" + strconv.Itoa(id)
	before := settingsRaw(t, path)
	t.Cleanup(func() {
		call(t, "qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": false, "cutoff": "Bluray-1080p", "min_format_score": 0, "cutoff_format_score": 0})
		settingsUnchanged(t, path, before)
	})

	edited := call(t, "qualityprofile_edit", map[string]any{"profile": profileHD, "upgrade_allowed": true, "cutoff": "Remux-1080p", "cutoff_format_score": 10})
	if edited["upgrade_allowed"] != true || str(edited["cutoff"]) != "Remux-1080p" || num(t, edited["cutoff_format_score"], "cutoff_format_score") != 10 ||
		num(t, edited["movies"], "movies") != films[profileHD] {
		t.Errorf("qualityprofile_edit = %v", edited)
	}
	// a quality group can be the cutoff too
	if grouped := call(t, "qualityprofile_edit", map[string]any{"profile": strconv.Itoa(id), "cutoff": "web 1080p"}); str(grouped["cutoff"]) != "WEB 1080p" {
		t.Errorf("a group cutoff = %v", grouped["cutoff"])
	}

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"profile": profileHD, "cutoff": "Bluray-2160p"}, `HD-1080p does not allow "Bluray-2160p", so it cannot be the cutoff`},
		{map[string]any{"profile": profileHD}, "nothing to change"},
	} {
		if msg := callErr(t, "qualityprofile_edit", tc.args); !strings.Contains(msg, tc.want) {
			t.Errorf("qualityprofile_edit %v = %s", tc.args, msg)
		}
	}
	if msg := callErr(t, "qualityprofile_edit", map[string]any{"profile": "Best", "upgrade_allowed": true}); !strings.Contains(msg, `no quality profile "Best"; the profiles are Any (1)`) {
		t.Errorf("an unknown profile = %s", msg)
	}
}

// qualitydefinition_list gives each quality's size limits, with an unlimited
// maximum left out rather than shown as zero; qualitydefinition_edit sets
// them, and a maximum of 0 is unlimited.
func TestQualityDefinitions(t *testing.T) {
	defs := rows(t, call(t, "qualitydefinition_list", nil)["definitions"], "definitions")
	byQuality := map[string]map[string]any{}
	var order []string
	for _, d := range defs {
		byQuality[str(d["quality"])] = d
		order = append(order, str(d["quality"]))
	}
	if len(defs) < 20 || order[0] != "Unknown" {
		t.Fatalf("definitions = %v, want Radarr's qualities, worst first", order)
	}
	web := byQuality["WEBDL-1080p"]
	if num0(web["min_size"]) != 0 || num0(web["max_size"]) != 100 || num0(web["preferred_size"]) != 95 {
		t.Errorf("WEBDL-1080p = %v", web)
	}
	if bluray := byQuality["Bluray-1080p"]; bluray["max_size"] != nil || bluray["preferred_size"] != nil {
		t.Errorf("Bluray-1080p = %v, want no maximum or preference (Radarr's null)", bluray)
	}

	path := "/api/v3/qualitydefinition/" + strconv.Itoa(num(t, web["id"], "id"))
	before := settingsRaw(t, path)
	t.Cleanup(func() {
		call(t, "qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p", "min_size": 0, "preferred_size": 95, "max_size": 100})
		settingsUnchanged(t, path, before)
	})

	out := call(t, "qualitydefinition_edit", map[string]any{"quality": "webdl-1080p", "min_size": 1.5, "max_size": 0})
	if num0(out["min_size"]) != 1.5 || out["max_size"] != nil || num0(out["preferred_size"]) != 95 {
		t.Errorf("qualitydefinition_edit = %v, want min 1.5, max unlimited, the preference kept", out)
	}
	for _, d := range rows(t, call(t, "qualitydefinition_list", nil)["definitions"], "definitions") {
		if str(d["quality"]) == "WEBDL-1080p" && (num0(d["min_size"]) != 1.5 || d["max_size"] != nil) {
			t.Errorf("the list after the edit = %v", d)
		}
	}
	if raw, ok := settingsRaw(t, path).(map[string]any); !ok || raw["maxSize"] != nil {
		t.Errorf("Radarr holds maxSize %v, want null", raw["maxSize"])
	}

	if msg := callErr(t, "qualitydefinition_edit", map[string]any{"quality": "Bluray-9000p", "min_size": 1}); !strings.Contains(msg, `no quality "Bluray-9000p"; the qualities are Unknown`) {
		t.Errorf("an unknown quality = %s", msg)
	}
	if msg := callErr(t, "qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an empty edit = %s", msg)
	}
}

// customformat_list shows what each custom format matches and the score each
// profile gives it. Radarr ships none, so the test adds one, scores it in a
// profile, and removes it - after which the profile is what it was.
func TestCustomFormats(t *testing.T) {
	if formats := rows(t, call(t, "customformat_list", nil)["formats"], "formats"); len(formats) != 0 {
		t.Fatalf("custom formats before the test = %v, want Radarr's none", formats)
	}
	profilePath := "/api/v3/qualityprofile/4"
	before := settingsRaw(t, profilePath)

	res, err := sdk.PostCustomformat(ctx, radarr.CustomFormatResource{
		Name: "acceptance-x265", IncludeCustomFormatWhenRenaming: new(false),
		Specifications: []radarr.CustomFormatSpecificationSchema{{
			Name: "x265", Implementation: "ReleaseTitleSpecification", Negate: new(false), Required: new(true),
			Fields: []radarr.Field{{Name: "value", Value: `\bx265\b`}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Model.Id
	t.Cleanup(func() {
		if _, err := sdk.DeleteCustomformatById(ctx, id); err != nil {
			t.Errorf("deleting the custom format: %v", err)
		}
		settingsUnchanged(t, profilePath, before)
	})

	// scored in HD-1080p, which a new format starts at 0 in
	profile, ok := settingsRaw(t, profilePath).(map[string]any)
	if !ok {
		t.Fatal("the profile is not an object")
	}
	for _, item := range rowsOf(profile["formatItems"]) {
		if num(t, item["format"], "format") == id {
			item["score"] = 25
		}
	}
	settingsPut(t, profilePath, profile)

	formats := rows(t, call(t, "customformat_list", nil)["formats"], "formats")
	if len(formats) != 1 || str(formats[0]["name"]) != "acceptance-x265" {
		t.Fatalf("customformat_list = %v", formats)
	}
	specs := strs(t, formats[0]["specifications"], "specifications")
	if len(specs) != 1 || !strings.Contains(specs[0], `ReleaseTitleSpecification "x265" = \bx265\b`) || !strings.HasSuffix(specs[0], "(required)") {
		t.Errorf("specifications = %q", specs)
	}
	if scores := object(t, formats[0]["scores"], "scores"); len(scores) != 1 || num0(scores[profileHD]) != 25 {
		t.Errorf("scores = %v, want HD-1080p's 25 and no other profile's 0", scores)
	}
	for _, p := range rows(t, call(t, "qualityprofile_list", nil)["profiles"], "profiles") {
		scores, _ := p["format_scores"].(map[string]any)
		if (str(p["name"]) == profileHD) != (num0(scores["acceptance-x265"]) == 25) {
			t.Errorf("%s's format scores = %v", p["name"], p["format_scores"])
		}
	}
}

// settingsIndexer adds a Newznab indexer at baseURL with key and returns its
// id. Radarr tests an enabled indexer before saving it, and refuses one it
// cannot reach even with forceSave (which only waives warnings), so an
// indexer meant to fail is either added disabled or broken after it is
// added. An enabled one here is used only for interactive search, so
// nothing else in the suite ever asks it.
func settingsIndexer(t *testing.T, name, baseURL, key string, enabled bool, tags []int) int {
	t.Helper()

	res, err := sdk.PostIndexer(ctx, radarr.IndexerResource{
		Name: name, Implementation: "Newznab", ConfigContract: "NewznabSettings", Protocol: radarr.DownloadProtocolUsenet,
		EnableRss: new(false), EnableAutomaticSearch: new(false), EnableInteractiveSearch: new(enabled), Priority: 50, Tags: tags,
		Fields: []radarr.Field{{Name: "baseUrl", Value: baseURL}, {Name: "apiPath", Value: "/api"}, {Name: "apiKey", Value: key}, {Name: "categories", Value: []int{2000, 2040}}},
	}, radarr.PostIndexerOperationOptions{})
	if err != nil {
		t.Fatalf("adding %s: %v", name, err)
	}

	return res.Model.Id
}

// indexer_list and downloadclient_list show what the harness added;
// indexer_test and downloadclient_test pass the working ones and say what
// is wrong with a broken one, whether all are tested or one by name.
func TestProviders(t *testing.T) {
	indexers := rows(t, call(t, "indexer_list", nil)["indexers"], "indexers")
	if len(indexers) != 1 {
		t.Fatalf("indexers = %v, want the harness's one", indexers)
	}
	fake := indexers[0]
	if str(fake["name"]) != "Fake Indexer" || str(fake["implementation"]) != "Newznab" || str(fake["protocol"]) != "usenet" ||
		fake["enable_rss"] != true || fake["enable_automatic_search"] != true || fake["enable_interactive_search"] != true || num(t, fake["priority"], "priority") != 25 {
		t.Errorf("the fake indexer = %v", fake)
	}
	clients := rows(t, call(t, "downloadclient_list", nil)["download_clients"], "download_clients")
	if len(clients) != 1 {
		t.Fatalf("download clients = %v, want the harness's one", clients)
	}
	hole := clients[0]
	if str(hole["name"]) != "Blackhole" || str(hole["implementation"]) != "Usenet Blackhole" || hole["enabled"] != true ||
		hole["remove_completed_downloads"] != true || hole["remove_failed_downloads"] != true || num(t, hole["priority"], "priority") != 1 {
		t.Errorf("the blackhole = %v", hole)
	}

	// both pass as they are
	for tool, name := range map[string]string{"indexer_test": "Fake Indexer", "downloadclient_test": "Blackhole"} {
		results := rows(t, call(t, tool, nil)["results"], "results")
		if len(results) != 1 || str(results[0]["name"]) != name || results[0]["valid"] != true || results[0]["failures"] != nil {
			t.Errorf("%s = %v", tool, results)
		}
		one := rows(t, call(t, tool, map[string]any{"name": strings.ToLower(name)})["results"], "results")
		if len(one) != 1 || one[0]["valid"] != true {
			t.Errorf("%s by name = %v", tool, one)
		}
	}

	// a broken one of each, broken the way they break in life: an indexer
	// that went away after it was added (a second fake indexer, stopped),
	// and a blackhole whose folders were deleted
	host := os.Getenv("RADARR_TEST_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	gone, err := fakeindexer.Start(":0", host)
	if err != nil {
		t.Fatal(err)
	}
	gone.Offer(background)
	broken := settingsIndexer(t, "Broken Indexer", gone.BaseURL(), gone.APIKey, true, nil)
	t.Cleanup(func() { _, _ = sdk.DeleteIndexerById(ctx, broken) })
	if err := gone.Close(); err != nil {
		t.Fatal(err)
	}

	const holes = "/media/downloads/broken"
	for _, dir := range []string{holes + "/nzb", holes + "/complete"} {
		mediaMkdir(t, hostPath(dir))
	}
	res, err := sdk.PostDownloadclient(ctx, radarr.DownloadClientResource{
		Name: "Broken Blackhole", Implementation: "UsenetBlackhole", ConfigContract: "UsenetBlackholeSettings", Protocol: radarr.DownloadProtocolUsenet,
		Enable: new(true), Priority: 50,
		Fields: []radarr.Field{{Name: "nzbFolder", Value: holes + "/nzb"}, {Name: "watchFolder", Value: holes + "/complete"}},
	}, radarr.PostDownloadclientOperationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	brokenClient := res.Model.Id
	t.Cleanup(func() {
		_, _ = sdk.DeleteDownloadclientById(ctx, brokenClient)
		_ = os.RemoveAll(hostPath(holes))
		// a health check sees the broken ones gone, for the tests after
		call(t, "task_run", map[string]any{"task": "CheckHealth"})
	})
	if err := os.RemoveAll(hostPath(holes)); err != nil {
		t.Fatal(err)
	}

	for tool, name := range map[string]string{"indexer_test": "Broken Indexer", "downloadclient_test": "Broken Blackhole"} {
		results := rows(t, call(t, tool, nil)["results"], "results")
		byName := map[string]map[string]any{}
		for _, r := range results {
			byName[str(r["name"])] = r
		}
		if len(results) != 2 || byName[name]["valid"] != false || len(strs(t, byName[name]["failures"], "failures")) == 0 {
			t.Errorf("%s with a broken one = %v", tool, results)
		}
		if good := byName[map[string]string{"indexer_test": "Fake Indexer", "downloadclient_test": "Blackhole"}[tool]]; good["valid"] != true {
			t.Errorf("%s: the working one failed beside the broken one: %v", tool, results)
		}
		one := rows(t, call(t, tool, map[string]any{"name": name})["results"], "results")
		if len(one) != 1 || one[0]["valid"] != false || len(strs(t, one[0]["failures"], "failures")) == 0 {
			t.Errorf("%s %s = %v", tool, name, one)
		}
	}

	if msg := callErr(t, "indexer_test", map[string]any{"name": "nope"}); !strings.Contains(msg, `no indexer "nope"`) {
		t.Errorf("an unknown indexer = %s", msg)
	}
	if msg := callErr(t, "downloadclient_test", map[string]any{"name": "nope"}); !strings.Contains(msg, `no download client "nope"`) {
		t.Errorf("an unknown download client = %s", msg)
	}
}

// naming_get reads how Radarr names files and folders, with its examples;
// naming_edit changes it, the examples follow, and it is restored exactly.
// A pattern Radarr refuses is refused, and changes nothing.
func TestNaming(t *testing.T) {
	const path = "/api/v3/config/naming"
	before := settingsRaw(t, path)
	out := call(t, "naming_get", nil)
	// Radarr's example film is "The Movie: Title", so the examples show what
	// the colon becomes
	if out["rename_movies"] != false || str(out["colon_replacement"]) != "smart" || str(out["standard_movie_format"]) != "{Movie Title} ({Release Year}) {Quality Full}" ||
		str(out["movie_folder_format"]) != "{Movie Title} ({Release Year})" || str(out["example_file"]) != "The Movie - Title (2010) Bluray-1080p Proper" ||
		str(out["example_folder"]) != "The Movie - Title (2010)" {
		t.Errorf("naming_get = %v", out)
	}
	t.Cleanup(func() {
		call(t, "naming_edit", map[string]any{
			"rename_movies": false, "colon_replacement": "smart",
			"standard_movie_format": "{Movie Title} ({Release Year}) {Quality Full}", "movie_folder_format": "{Movie Title} ({Release Year})",
		})
		settingsUnchanged(t, path, before)
	})

	edited := call(t, "naming_edit", map[string]any{"rename_movies": true, "colon_replacement": "Delete", "standard_movie_format": "{Movie Title} ({Release Year}) - {Quality Full}"})
	if edited["rename_movies"] != true || str(edited["colon_replacement"]) != "delete" || str(edited["example_file"]) != "The Movie Title (2010) - Bluray-1080p Proper" ||
		str(edited["example_folder"]) != "The Movie Title (2010)" || str(edited["movie_folder_format"]) != "{Movie Title} ({Release Year})" {
		t.Errorf("naming_edit = %v", edited)
	}
	if got := call(t, "naming_get", nil); !reflect.DeepEqual(got, edited) {
		t.Errorf("naming_get after the edit = %v, naming_edit said %v", got, edited)
	}

	if msg := callErr(t, "naming_edit", map[string]any{"colon_replacement": "hyphen"}); !strings.Contains(msg, "want one of delete, dash, spaceDash, spaceDashSpace, smart") {
		t.Errorf("an unknown colon replacement = %s", msg)
	}
	if msg := callErr(t, "naming_edit", map[string]any{}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an empty edit = %s", msg)
	}
	refused := settingsRaw(t, path)
	if msg := callErr(t, "naming_edit", map[string]any{"standard_movie_format": "{Quality Full}"}); msg == "" {
		t.Error("a file pattern with no title was accepted")
	}
	settingsUnchanged(t, path, refused)
}
