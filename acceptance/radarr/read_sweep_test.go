//go:build acceptance

package radarr_test

// The read sweep: every GET operation in the definitions, called against the
// running Radarr with arguments resolved from the fixtures, and its answer
// decoded (or its file read). The bespoke tests prove the shapes the tools
// rely on field by field; the sweep proves the rest of the read surface
// answers in its documented status with something in it, that every field
// the server sends is one its model declares, and that it keeps doing so as
// the definitions change: a GET the importer adds is swept on the next run
// with nothing to write, and one that fails must be classified here.
//
// The sweep itself is pandorest's (github.com/katbyte/pandorest/sweep); what
// is here is this suite's fixtures and its cases. Every operation either
// answers with something, or has a case that says why not. A case whose
// operation starts answering fails the sweep, so a stale case is noticed and
// removed, the way a stale importer workaround is.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/test/services/arrserver"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/sweep"
	"github.com/katbyte/radarr-mcp/lib/testenv"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// TestReadSweep calls every GET against the fixtures.
// One of every resource a GET can address is created first - a tag, a
// custom filter, custom format, release profile, remote path mapping, auto
// tagging rule, exclusion, notification and import list - so each by-id GET
// has something real to read.
func TestReadSweep(t *testing.T) {
	ctx := skipUnlessUp(t)
	l := fixtures(t)
	d := downloads(t)
	a := film(ctx, t, alien)

	// a subtitle beside Alien's file, which the rescan further down finds and Radarr then keeps track of as an extra file. It is laid out first: the container sees a new file on this machine's disk a moment after it is written
	subtitle := testenv.HostPath(alien.Path + "/" + strings.TrimSuffix(alien.File, ".mkv") + ".en.srt")
	if err := os.WriteFile(subtitle, []byte("1\n00:00:01,000 --> 00:00:03,000\nIn space no one can hear you scream.\n"), 0o644); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(subtitle) })

	withFile := a.MovieFile
	if withFile == nil {
		t.Fatal("Alien has no file")
	}
	var altTitle int
	for _, f := range []filmFixture{alien, aliens, bladeRunner, dune1984} {
		if m := film(ctx, t, f); len(m.AlternateTitles) > 0 {
			altTitle = m.AlternateTitles[0].Id
			break
		}
	}
	if altTitle == 0 {
		t.Fatal("no fixture film has an alternate title to read")
	}
	credits := must(sdk.GetCredit(ctx, radarr.GetCreditOptions{MovieId: a.Id})).Answer
	collections := must(sdk.GetCollection(ctx)).Answer
	logs := must(sdk.GetLogFile(ctx)).Answer
	tasks := must(sdk.GetSystemTask(ctx)).Answer
	languages := must(sdk.GetLanguage(ctx)).Answer
	definitions := must(sdk.GetQualityDefinition(ctx)).Answer
	profiles := must(sdk.GetQualityProfile(ctx)).Answer
	delays := must(sdk.GetDelayProfile(ctx)).Answer
	metadata := must(sdk.GetMetadata(ctx)).Answer
	if len(credits) == 0 || len(collections) == 0 || len(logs) == 0 || len(tasks) == 0 || len(languages) == 0 ||
		len(definitions) == 0 || len(profiles) == 0 || len(delays) == 0 || len(metadata) == 0 {
		t.Fatalf("the fixtures are incomplete: %d credits, %d collections, %d log files, %d tasks, %d languages, %d definitions, %d profiles, %d delay profiles, %d metadata",
			len(credits), len(collections), len(logs), len(tasks), len(languages), len(definitions), len(profiles), len(delays), len(metadata))
	}

	// one of each resource a GET by id reads
	tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: "sdk-sweep"})).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteTagById(context.WithoutCancel(ctx), tag.Id) })
	filter := must(sdk.PostCustomFilter(ctx, radarr.CustomFilterResource{
		Type: "movieIndex", Label: "SDK Sweep", Filters: []map[string]any{{"key": "monitored", "value": []any{true}, "type": "equal"}},
	})).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteCustomFilterById(context.WithoutCancel(ctx), filter.Id) })
	format := must(sdk.PostCustomFormat(ctx, customFormat("SDK Sweep"))).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteCustomFormatById(context.WithoutCancel(ctx), format.Id) })
	release := must(sdk.PostReleaseProfile(ctx, radarr.ReleaseProfileResource{Name: "SDK Sweep", Enabled: new(true), Required: []string{"x264"}})).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteReleaseProfileById(context.WithoutCancel(ctx), release.Id) })
	mapping := must(sdk.PostRemotePathMapping(ctx, radarr.RemotePathMappingResource{Host: "sweep", RemotePath: "/downloads/", LocalPath: "/media/downloads/"})).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteRemotePathMappingById(context.WithoutCancel(ctx), mapping.Id) })
	auto := must(sdk.PostAutoTagging(ctx, autoTagging("SDK Sweep", tag.Id))).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteAutoTaggingById(context.WithoutCancel(ctx), auto.Id) })
	exclusion := must(sdk.PostExclusions(ctx, radarr.ImportListExclusionResource{TmdbId: 8078, MovieTitle: "Alien Resurrection", MovieYear: 1997})).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteExclusionsById(context.WithoutCancel(ctx), exclusion.Id) })
	notification := must(sdk.PostNotification(ctx, webhook(t, "SDK Sweep"))).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteNotificationById(context.WithoutCancel(ctx), notification.Id) })
	// the list is switched on and read once, so that there are films a list offers to read: this Radarr's own, every one of them in the library already
	offering := selfList("SDK Sweep")
	offering.Enabled = new(true)
	list := must(sdk.PostImportList(ctx, offering)).Answer
	t.Cleanup(func() { _, _ = sdk.DeleteImportListById(context.WithoutCancel(ctx), list.Id) })
	completed(t, must(sdk.ImportListSyncAndWait(ctx, radarr.ImportListSyncCommand{}, commandWait)))

	cmd := completed(t, must(sdk.RescanMovieAndWait(ctx, radarr.RescanMovieCommand{MovieId: a.Id}, commandWait)))

	// a newer build on offer, for the list of updates to have one in
	testenv.Services.Offer(arrserver.Update{Version: "99.1.0.1", New: []string{"A setting the suite made up"}, Fixed: []string{"A fault the suite made up"}})
	t.Cleanup(func() { testenv.Services.Offer() })

	// the log Radarr's updater leaves, which a container never runs: laid out where it would write one
	const updateLog = "radarr.update.txt"
	if err := os.MkdirAll(testenv.ConfigPath("UpdateLogs"), 0o755); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	if err := os.WriteFile(testenv.ConfigPath("UpdateLogs", updateLog), []byte("2026-01-01 00:00:00.0|Info|UpdateApp|Starting Radarr Update Client\n"), 0o644); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testenv.ConfigPath("UpdateLogs")) })
	// a film with a history: the manual import, which runs first, leaves the only history a seeded Radarr has
	recent := must(sdk.GetHistory(ctx, radarr.GetHistoryOptions{PageSize: new(1)})).Answer.Records
	if len(recent) == 0 {
		t.Fatal("the fixtures are incomplete: Radarr has no history; TestManualImport makes it, so run the whole suite")
	}
	// a download no film has yet, for the manual import to find in its folder
	const loose = "/media/downloads/sweep/Alien.1979.1080p.BluRay.x264-SWEEP"
	copyFile(t, alien.Path+"/"+alien.File, loose+"/Alien.1979.1080p.BluRay.x264-SWEEP.mkv")
	t.Cleanup(func() { _ = os.RemoveAll(testenv.HostPath("/media/downloads/sweep")) })

	id := strconv.Itoa
	resolve := sweep.Fixtures{
		Path: map[string]string{
			"movie/id":             id(int(a.Id)),
			"moviefile/id":         id(int(withFile.Id)),
			"alttitle/id":          id(altTitle),
			"autotagging/id":       id(auto.Id),
			"collection/id":        id(collections[0].Id),
			"command/id":           id(cmd.Id),
			"credit/id":            id(credits[0].Id),
			"customfilter/id":      id(filter.Id),
			"customformat/id":      id(format.Id),
			"delayprofile/id":      id(delays[0].Id),
			"downloadclient/id":    id(int(d.client)),
			"exclusions/id":        id(exclusion.Id),
			"importlist/id":        id(list.Id),
			"indexer/id":           id(int(d.indexer)),
			"language/id":          id(languages[1].Id),
			"metadata/id":          id(metadata[0].Id),
			"notification/id":      id(notification.Id),
			"qualitydefinition/id": id(definitions[0].Id),
			"qualityprofile/id":    id(int(profiles[0].Id)),
			"releaseprofile/id":    id(release.Id),
			"remotepathmapping/id": id(mapping.Id),
			"rootfolder/id":        id(int(l.roots[moviesRoot])),
			"tag/id":               id(int(tag.Id)),
			"detail/id":            id(int(tag.Id)),
			"task/id":              id(tasks[0].Id),
			"file/filename":        logs[0].Filename,
			"update/filename":      updateLog,
			"mediacover/movieId":   id(int(a.Id)),
			"filename":             "poster.jpg",
			// a file at the top of /Content: the SDK escapes a path
			// argument, so a slash in it would reach the catch-all route as
			// %2F and name nothing
			"content/path": "styles.css",
			"path":         "movie/" + id(int(a.TmdbId)),
		},
		Options: map[string]string{
			"movieId": id(int(a.Id)),
			"tmdbId":  id(int(a.TmdbId)),
			"imdbId":  a.ImdbId,
			"term":    "Alien",
			"title":   "Alien.1979.1080p.BluRay.x264-GRP",
			"date":    "2000-01-01T00:00:00Z",
			"folder":  alien.Path,
			"path":    moviesRoot,
		},
	}
	// each group of settings is one record, 1
	for _, name := range []string{"host", "ui", "naming", "mediamanagement", "indexer", "downloadclient", "importlist", "metadata"} {
		resolve.Path["config/"+name+"/id"] = "1"
	}

	cases := map[string]sweep.Case{
		// both answer for a film or a folder, and need one or the other: Radarr
		// answers 400 or 500 with neither, which one parameter cannot declare
		"GetMovieFile": {Options: map[string]any{"MovieId": a.Id}},
		// extra files are a film's, and the document does not say the film has to be named: asked for none, Radarr answers none
		"GetExtraFile":    {Options: map[string]any{"MovieId": a.Id}},
		"GetManualImport": {Options: map[string]any{"Folder": loose}},
		// the calendar is the days around today unless told, and no fixture film comes out now: Alien opened in 1979 and Aliens in 1986
		"GetCalendar":       {Options: map[string]any{"Start": "1979-01-01T00:00:00Z", "End": "1986-12-31T00:00:00Z", "Unmonitored": true}},
		"GetHistoryMovie":   {Options: map[string]any{"MovieId": recent[0].MovieId}},
		"GetSystemBackup":   {Empty: "there is no backup until one is made; TestBackups makes one and reads the list with it in"},
		"GetQueueDetails":   {Empty: "nothing is downloading when the sweep runs; TestReleases reads it with two downloads in the queue"},
		"GetQueueStatus":    {Empty: "nothing is downloading when the sweep runs, so every count is zero; TestReleases reads it with two downloads in the queue"},
		"GetBlocklistMovie": {Empty: "nothing is blocklisted when the sweep runs; TestReleases marks a grab failed and reads the film's blocklist with it in"},
		"GetRename":         {Empty: "Radarr's rename preview is empty while renaming is off, which is how it is seeded; TestSettings turns it on and reads Dune's"},
	}

	svc, err := apiDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	// Strict: a field Radarr sends that its document does not declare is a failure, not a value the client drops without a word
	sweep.Sweep{Definitions: svc, Client: sdk, StatusCode: client.StatusCode, Fixtures: resolve, Cases: cases, Strict: true, Timeout: 90 * time.Second}.Run(t)
}

// customFormat is a custom format matching x265 in a release title.
func customFormat(name string) radarr.CustomFormatResource {
	return radarr.CustomFormatResource{
		Name: name, IncludeCustomFormatWhenRenaming: new(false),
		Specifications: []radarr.CustomFormatSpecificationSchema{{
			Name: "x265", Implementation: "ReleaseTitleSpecification", Negate: new(false), Required: new(false),
			Fields: []radarr.Field{{Name: "value", Value: "x265"}},
		}},
	}
}

// autoTagging tags every horror film with a tag.
func autoTagging(name string, tag radarr.TagID) radarr.AutoTaggingResource {
	return radarr.AutoTaggingResource{
		Name: name, RemoveTagsAutomatically: new(false), Tags: []radarr.TagID{tag},
		Specifications: []radarr.AutoTaggingSpecificationSchema{{
			Name: "horror", Implementation: "GenreSpecification", Negate: new(false), Required: new(false),
			Fields: []radarr.Field{{Name: "value", Value: []string{"Horror"}}},
		}},
	}
}

// webhook is a Webhook notification pointed at the suite's receiver.
func webhook(t *testing.T, name string) radarr.NotificationResource {
	t.Helper()

	return radarr.NotificationResource{
		Name: name, Implementation: "Webhook", ConfigContract: "WebhookSettings", OnGrab: new(true), OnDownload: new(true), OnMovieAdded: new(true),
		Fields: []radarr.Field{{Name: "url", Value: webhookURL(t)}, {Name: "method", Value: 1}},
	}
}

// selfList is Radarr's "another Radarr" import list, pointed at this Radarr:
// the one list that needs nothing outside the container.
func selfList(name string) radarr.ImportListResource {
	return radarr.ImportListResource{
		Name: name, Implementation: "RadarrImport", ConfigContract: "RadarrSettings", ListType: radarr.ImportListTypeProgram,
		Enabled: new(false), EnableAuto: new(false), Monitor: radarr.MonitorTypesMovieOnly, QualityProfileId: hd1080,
		RootFolderPath: moviesRoot, SearchOnAdd: new(false), MinimumAvailability: radarr.MovieStatusTypeReleased,
		Fields: []radarr.Field{
			{Name: "baseUrl", Value: "http://localhost:7878"},
			{Name: "apiKey", Value: sdkKey()},
			{Name: "profileIds", Value: []int{}},
			{Name: "tagIds", Value: []int{}},
			{Name: "rootFolderPaths", Value: []string{}},
		},
	}
}
