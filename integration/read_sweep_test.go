//go:build integration

package integration

import (
	"context"
	"maps"
	"strconv"
	"testing"

	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// TestReadSweep calls every GET against the fixtures (see sweep_test.go).
// One of every resource a GET can address is created first - a tag, a
// custom filter, custom format, release profile, remote path mapping, auto
// tagging rule, exclusion, notification and import list - so each by-id GET
// has something real to read.
//
//nolint:paralleltest // the tests share one server and its fixtures
func TestReadSweep(t *testing.T) {
	ctx := skipUnlessUp(t)
	l := fixtures(t)
	d := downloads(t)
	a := film(ctx, t, alien)
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
	credits := must(sdk.GetCredit(ctx, radarr.GetCreditOperationOptions{MovieId: a.Id})).Model
	collections := must(sdk.GetCollection(ctx, radarr.GetCollectionOperationOptions{})).Model
	logs := must(sdk.GetLogFile(ctx)).Model
	tasks := must(sdk.GetSystemTask(ctx)).Model
	languages := must(sdk.GetLanguage(ctx)).Model
	definitions := must(sdk.GetQualitydefinition(ctx)).Model
	profiles := must(sdk.GetQualityprofile(ctx)).Model
	delays := must(sdk.GetDelayprofile(ctx)).Model
	metadata := must(sdk.GetMetadata(ctx)).Model
	if len(credits) == 0 || len(collections) == 0 || len(logs) == 0 || len(tasks) == 0 || len(languages) == 0 ||
		len(definitions) == 0 || len(profiles) == 0 || len(delays) == 0 || len(metadata) == 0 {
		t.Fatalf("the fixtures are incomplete: %d credits, %d collections, %d log files, %d tasks, %d languages, %d definitions, %d profiles, %d delay profiles, %d metadata",
			len(credits), len(collections), len(logs), len(tasks), len(languages), len(definitions), len(profiles), len(delays), len(metadata))
	}

	// one of each resource a GET by id reads
	tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: "sdk-sweep"})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteTagById(context.WithoutCancel(ctx), tag.Id) })
	filter := must(sdk.PostCustomfilter(ctx, radarr.CustomFilterResource{
		Type: "movieIndex", Label: "SDK Sweep", Filters: []map[string]any{{"key": "monitored", "value": []any{true}, "type": "equal"}},
	})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteCustomfilterById(context.WithoutCancel(ctx), filter.Id) })
	format := must(sdk.PostCustomformat(ctx, customFormat("SDK Sweep"))).Model
	t.Cleanup(func() { _, _ = sdk.DeleteCustomformatById(context.WithoutCancel(ctx), format.Id) })
	release := must(sdk.PostReleaseprofile(ctx, radarr.ReleaseProfileResource{Name: "SDK Sweep", Enabled: new(true), Required: []string{"x264"}})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteReleaseprofileById(context.WithoutCancel(ctx), release.Id) })
	mapping := must(sdk.PostRemotepathmapping(ctx, radarr.RemotePathMappingResource{Host: "sweep", RemotePath: "/downloads/", LocalPath: "/media/downloads/"})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteRemotepathmappingById(context.WithoutCancel(ctx), mapping.Id) })
	auto := must(sdk.PostAutotagging(ctx, autoTagging("SDK Sweep", tag.Id))).Model
	t.Cleanup(func() { _, _ = sdk.DeleteAutotaggingById(context.WithoutCancel(ctx), auto.Id) })
	exclusion := must(sdk.PostExclusions(ctx, radarr.ImportListExclusionResource{TmdbId: 8078, MovieTitle: "Alien Resurrection", MovieYear: 1997})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteExclusionsById(context.WithoutCancel(ctx), exclusion.Id) })
	notification := must(sdk.PostNotification(ctx, webhook(t, "SDK Sweep"), radarr.PostNotificationOperationOptions{})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteNotificationById(context.WithoutCancel(ctx), notification.Id) })
	list := must(sdk.PostImportlist(ctx, selfList("SDK Sweep"), radarr.PostImportlistOperationOptions{})).Model
	t.Cleanup(func() { _, _ = sdk.DeleteImportlistById(context.WithoutCancel(ctx), list.Id) })
	cmd := command(ctx, t, `{"name":"RescanMovie","movieId":`+strconv.Itoa(a.Id)+`}`)

	id := strconv.Itoa
	resolve := sweepFixtures{
		path: map[string]string{
			"movie/id":             id(a.Id),
			"moviefile/id":         id(withFile.Id),
			"alttitle/id":          id(altTitle),
			"autotagging/id":       id(auto.Id),
			"collection/id":        id(collections[0].Id),
			"command/id":           id(cmd.Id),
			"credit/id":            id(credits[0].Id),
			"customfilter/id":      id(filter.Id),
			"customformat/id":      id(format.Id),
			"delayprofile/id":      id(delays[0].Id),
			"downloadclient/id":    id(d.client),
			"exclusions/id":        id(exclusion.Id),
			"importlist/id":        id(list.Id),
			"indexer/id":           id(d.indexer),
			"language/id":          id(languages[1].Id),
			"metadata/id":          id(metadata[0].Id),
			"notification/id":      id(notification.Id),
			"qualitydefinition/id": id(definitions[0].Id),
			"qualityprofile/id":    id(profiles[0].Id),
			"releaseprofile/id":    id(release.Id),
			"remotepathmapping/id": id(mapping.Id),
			"rootfolder/id":        id(l.roots[moviesRoot]),
			"tag/id":               id(tag.Id),
			"detail/id":            id(tag.Id),
			"task/id":              id(tasks[0].Id),
			"file/filename":        logs[0].Filename,
			"mediacover/movieId":   id(a.Id),
			"filename":             "poster.jpg",
			// a file at the top of /Content: the SDK escapes a path
			// argument, so a slash in it would reach the catch-all route as
			// %2F and name nothing
			"content/path": "styles.css",
			"path":         "movie/" + id(a.TmdbId),
		},
		options: map[string]string{
			"movieId": id(a.Id),
			"tmdbId":  id(a.TmdbId),
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
		resolve.path["config/"+name+"/id"] = "1"
	}

	cases := maps.Clone(sweepCases)
	// both answer for a film or a folder, and need one or the other: Radarr
	// answers 400 or 500 with neither, which one parameter cannot declare
	cases["GetMoviefile"] = sweepCase{Options: map[string]any{"MovieId": a.Id}}
	cases["GetManualimport"] = sweepCase{Options: map[string]any{"Folder": alien.Path}}

	sweep(t, resolve, cases)
}

// sweepCases classifies the GETs that do not simply answer.
var sweepCases = map[string]sweepCase{
	"GetUpdate": {Status: 500, Why: "Radarr lists its updates from radarr.servarr.com, which the proxy answers empty " +
		"(its answers carry Radarr's version and the machine's architecture, so no recording would replay elsewhere): with no list to sort, Radarr answers 500"},
	"GetLogFileUpdateByFilename": {Skip: "Radarr writes an update log only when it installs an update, which the suite never does, so there is none to read"},
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
func autoTagging(name string, tag int) radarr.AutoTaggingResource {
	return radarr.AutoTaggingResource{
		Name: name, RemoveTagsAutomatically: new(false), Tags: []int{tag},
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
