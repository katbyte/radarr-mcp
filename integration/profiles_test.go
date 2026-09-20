//go:build integration

package integration

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/radarr-mcp/lib/client"
	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// The profiles and the rest of the settings kept as records: each is
// created, changed, read back and deleted, and a deleted one reads as 404.

// gone fails the test unless a read of something deleted answers 404.
func gone[T any](t *testing.T, what string, _ T, err error) {
	t.Helper()

	if !client.IsNotFound(err) {
		t.Errorf("%s after deleting it: want 404, got %v", what, err)
	}
}

// goneFromCache fails the test unless a read of something deleted answers
// 500 with the KeyNotFoundException Radarr throws when it looks up a custom
// format or an auto tagging rule by id in its cache of them rather than the
// database: it never answers 404 for one.
func goneFromCache[T any](t *testing.T, what string, _ T, err error) {
	t.Helper()

	if client.StatusCode(err) != http.StatusInternalServerError || !strings.Contains(err.Error(), "was not present in the dictionary") {
		t.Errorf("%s after deleting it: want 500 for a key not in the dictionary, got %v", what, err)
	}
}

// removeAfter deletes what a test created when the test ends, if the test
// has not already: a deleted one answering 404 is fine.
func removeAfter[T any](ctx context.Context, t *testing.T, what string, del func(context.Context) (T, error)) {
	t.Helper()

	ctx = context.WithoutCancel(ctx)
	t.Cleanup(func() {
		if _, err := del(ctx); err != nil && !client.IsNotFound(err) {
			t.Errorf("removing %s: %v", what, err)
		}
	})
}

//nolint:paralleltest // the tests share one server
func TestTags(t *testing.T) {
	ctx := skipUnlessUp(t)

	tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: "sdk-tag"})).Model
	removeAfter(ctx, t, "the tag", func(ctx context.Context) (radarr.DeleteTagByIdOperationResponse, error) {
		return sdk.DeleteTagById(ctx, tag.Id)
	})
	if tag.Id == 0 || tag.Label != "sdk-tag" {
		t.Fatalf("created %+v", tag)
	}

	renamed := must(sdk.PutTagById(ctx, tag.Id, radarr.TagResource{Id: tag.Id, Label: "sdk-tag-renamed"})).Model
	if renamed.Label != "sdk-tag-renamed" {
		t.Errorf("renamed to %q", renamed.Label)
	}
	if got := must(sdk.GetTagById(ctx, tag.Id)).Model; got.Label != "sdk-tag-renamed" {
		t.Errorf("a fresh read has %q", got.Label)
	}

	must(sdk.DeleteTagById(ctx, tag.Id))
	res, err := sdk.GetTagById(ctx, tag.Id)
	gone(t, "the tag", res, err)
}

//nolint:paralleltest // the tests share one server
func TestCustomFilters(t *testing.T) {
	ctx := skipUnlessUp(t)

	filter := must(sdk.PostCustomfilter(ctx, radarr.CustomFilterResource{
		Type: "movieIndex", Label: "SDK Filter", Filters: []map[string]any{{"key": "monitored", "value": []any{true}, "type": "equal"}},
	})).Model
	removeAfter(ctx, t, "the filter", func(ctx context.Context) (radarr.DeleteCustomfilterByIdOperationResponse, error) {
		return sdk.DeleteCustomfilterById(ctx, filter.Id)
	})

	edit := *filter
	edit.Label = "SDK Filter Renamed"
	edit.Filters = append(edit.Filters, map[string]any{"key": "hasFile", "value": []any{false}, "type": "equal"})
	got := must(sdk.PutCustomfilterById(ctx, filter.Id, edit)).Model
	if got.Label != edit.Label || len(got.Filters) != 2 {
		t.Errorf("updated to %q with %d filters, want %q with 2", got.Label, len(got.Filters), edit.Label)
	}

	must(sdk.DeleteCustomfilterById(ctx, filter.Id))
	res, err := sdk.GetCustomfilterById(ctx, filter.Id)
	gone(t, "the filter", res, err)
}

//nolint:paralleltest // the tests share one server
func TestCustomFormats(t *testing.T) {
	ctx := skipUnlessUp(t)

	a := must(sdk.PostCustomformat(ctx, customFormat("SDK Format A"))).Model
	removeAfter(ctx, t, "format A", func(ctx context.Context) (radarr.DeleteCustomformatByIdOperationResponse, error) {
		return sdk.DeleteCustomformatById(ctx, a.Id)
	})
	b := must(sdk.PostCustomformat(ctx, customFormat("SDK Format B"))).Model
	removeAfter(ctx, t, "format B", func(ctx context.Context) (radarr.DeleteCustomformatByIdOperationResponse, error) {
		return sdk.DeleteCustomformatById(ctx, b.Id)
	})

	edit := *a
	edit.Name = "SDK Format A Renamed"
	if got := must(sdk.PutCustomformatById(ctx, a.Id, edit)).Model; got.Name != edit.Name {
		t.Errorf("renamed to %q", got.Name)
	}

	// a bulk edit answers every format it changed
	changed := must(sdk.PutCustomformatBulk(ctx, radarr.CustomFormatBulkResource{Ids: []int{a.Id, b.Id}, IncludeCustomFormatWhenRenaming: new(true)})).Model
	if len(changed) != 2 {
		t.Fatalf("the bulk edit answered %d formats, want 2", len(changed))
	}
	for _, f := range changed {
		if !isTrue(f.IncludeCustomFormatWhenRenaming) {
			t.Errorf("%s: not included when renaming after the bulk edit", f.Name)
		}
	}

	must(sdk.DeleteCustomformatBulk(ctx, radarr.CustomFormatBulkResource{Ids: []int{b.Id}}))
	res, err := sdk.GetCustomformatById(ctx, b.Id)
	goneFromCache(t, "format B", res, err)
	must(sdk.DeleteCustomformatById(ctx, a.Id))
	res, err = sdk.GetCustomformatById(ctx, a.Id)
	goneFromCache(t, "format A", res, err)
}

//nolint:paralleltest // the tests share one server
func TestDelayProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	// a delay profile other than the default applies to films with its
	// tags, and no two share a tag
	var profiles [2]*radarr.DelayProfileResource
	for i, label := range []string{"sdk-delay-a", "sdk-delay-b"} {
		tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: label})).Model
		removeAfter(ctx, t, label, func(ctx context.Context) (radarr.DeleteTagByIdOperationResponse, error) {
			return sdk.DeleteTagById(ctx, tag.Id)
		})
		p := must(sdk.PostDelayprofile(ctx, radarr.DelayProfileResource{
			EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: radarr.DownloadProtocolUsenet,
			UsenetDelay: 60, Tags: []int{tag.Id}, BypassIfHighestQuality: new(false), BypassIfAboveCustomFormatScore: new(false),
		})).Model
		removeAfter(ctx, t, label, func(ctx context.Context) (radarr.DeleteDelayprofileByIdOperationResponse, error) {
			return sdk.DeleteDelayprofileById(ctx, p.Id)
		})
		profiles[i] = p
	}
	a, b := profiles[0], profiles[1]

	edit := *a
	edit.UsenetDelay = 120
	if got := must(sdk.PutDelayprofileById(ctx, a.Id, edit)).Model; got.UsenetDelay != 120 {
		t.Errorf("the delay is %d after the update, want 120", got.UsenetDelay)
	}

	// the newer profile is ordered after the older; moving the older after
	// the newer swaps them, and the answer is every profile in its new order
	order := must(sdk.PutDelayprofileReorderById(ctx, a.Id, radarr.PutDelayprofileReorderByIdOperationOptions{After: b.Id})).Model
	ids := make([]int, 0, len(order))
	for _, p := range slices.SortedFunc(slices.Values(order), func(x, y radarr.DelayProfileResource) int { return x.Order - y.Order }) {
		ids = append(ids, p.Id)
	}
	if ia, ib := slices.Index(ids, a.Id), slices.Index(ids, b.Id); ia < 0 || ib < 0 || ia < ib {
		t.Errorf("after moving %d after %d the order is %v", a.Id, b.Id, ids)
	}

	for _, p := range profiles {
		must(sdk.DeleteDelayprofileById(ctx, p.Id))
		res, err := sdk.GetDelayprofileById(ctx, p.Id)
		gone(t, "the delay profile", res, err)
	}
}

//nolint:paralleltest // the tests share one server
func TestReleaseProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	p := must(sdk.PostReleaseprofile(ctx, radarr.ReleaseProfileResource{Name: "SDK Release Profile", Enabled: new(true), Required: []string{"x264"}})).Model
	removeAfter(ctx, t, "the release profile", func(ctx context.Context) (radarr.DeleteReleaseprofileByIdOperationResponse, error) {
		return sdk.DeleteReleaseprofileById(ctx, p.Id)
	})

	edit := *p
	edit.Ignored = []string{"cam", "telesync"}
	got := must(sdk.PutReleaseprofileById(ctx, p.Id, edit)).Model
	if ignored, ok := got.Ignored.([]any); !ok || len(ignored) != 2 {
		t.Errorf("ignores %v after the update, want cam and telesync", got.Ignored)
	}

	must(sdk.DeleteReleaseprofileById(ctx, p.Id))
	res, err := sdk.GetReleaseprofileById(ctx, p.Id)
	gone(t, "the release profile", res, err)
}

//nolint:paralleltest // the tests share one server
func TestAutoTagging(t *testing.T) {
	ctx := skipUnlessUp(t)

	tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: "sdk-auto"})).Model
	removeAfter(ctx, t, "the tag", func(ctx context.Context) (radarr.DeleteTagByIdOperationResponse, error) {
		return sdk.DeleteTagById(ctx, tag.Id)
	})
	rule := must(sdk.PostAutotagging(ctx, autoTagging("SDK Auto Tag", tag.Id))).Model
	removeAfter(ctx, t, "the rule", func(ctx context.Context) (radarr.DeleteAutotaggingByIdOperationResponse, error) {
		return sdk.DeleteAutotaggingById(ctx, rule.Id)
	})

	edit := *rule
	edit.Name = "SDK Auto Tag Renamed"
	edit.RemoveTagsAutomatically = new(true)
	got := must(sdk.PutAutotaggingById(ctx, rule.Id, edit)).Model
	if got.Name != edit.Name || !isTrue(got.RemoveTagsAutomatically) {
		t.Errorf("updated to %q, removing tags %v", got.Name, isTrue(got.RemoveTagsAutomatically))
	}

	must(sdk.DeleteAutotaggingById(ctx, rule.Id))
	res, err := sdk.GetAutotaggingById(ctx, rule.Id)
	goneFromCache(t, "the rule", res, err)
}

//nolint:paralleltest // the tests share one server
func TestRemotePathMappings(t *testing.T) {
	ctx := skipUnlessUp(t)

	m := must(sdk.PostRemotepathmapping(ctx, radarr.RemotePathMappingResource{Host: "sdk-host", RemotePath: "/remote/", LocalPath: "/media/downloads/"})).Model
	removeAfter(ctx, t, "the mapping", func(ctx context.Context) (radarr.DeleteRemotepathmappingByIdOperationResponse, error) {
		return sdk.DeleteRemotepathmappingById(ctx, m.Id)
	})

	edit := *m
	edit.RemotePath = "/elsewhere/"
	if got := must(sdk.PutRemotepathmappingById(ctx, m.Id, edit)).Model; got.RemotePath != "/elsewhere/" {
		t.Errorf("maps %q after the update", got.RemotePath)
	}

	must(sdk.DeleteRemotepathmappingById(ctx, m.Id))
	res, err := sdk.GetRemotepathmappingById(ctx, m.Id)
	gone(t, "the mapping", res, err)
}

//nolint:paralleltest // the tests share one server
func TestExclusions(t *testing.T) {
	ctx := skipUnlessUp(t)

	one := must(sdk.PostExclusions(ctx, radarr.ImportListExclusionResource{TmdbId: 126889, MovieTitle: "Alien: Covenant", MovieYear: 2017})).Model
	removeAfter(ctx, t, "the exclusion", func(ctx context.Context) (radarr.DeleteExclusionsByIdOperationResponse, error) {
		return sdk.DeleteExclusionsById(ctx, one.Id)
	})

	edit := *one
	edit.MovieTitle = "Alien Covenant"
	if got := must(sdk.PutExclusionsById(ctx, one.Id, edit)).Model; got.MovieTitle != edit.MovieTitle {
		t.Errorf("titled %q after the update", got.MovieTitle)
	}

	// a bulk add answers what it added
	added := must(sdk.PostExclusionsBulk(ctx, []radarr.ImportListExclusionResource{
		{TmdbId: 70981, MovieTitle: "Prometheus", MovieYear: 2012},
		{TmdbId: 395, MovieTitle: "AVP: Alien vs. Predator", MovieYear: 2004},
	})).Model
	if len(added) != 2 {
		t.Fatalf("the bulk add answered %d exclusions, want 2", len(added))
	}
	ids := []int{added[0].Id, added[1].Id}
	removeAfter(ctx, t, "the bulk exclusions", func(ctx context.Context) (radarr.DeleteExclusionsBulkOperationResponse, error) {
		return sdk.DeleteExclusionsBulk(ctx, radarr.ImportListExclusionBulkResource{Ids: ids})
	})

	must(sdk.DeleteExclusionsBulk(ctx, radarr.ImportListExclusionBulkResource{Ids: ids}))
	for _, id := range ids {
		res, err := sdk.GetExclusionsById(ctx, id)
		gone(t, "a bulk exclusion", res, err)
	}
	must(sdk.DeleteExclusionsById(ctx, one.Id))
	res, err := sdk.GetExclusionsById(ctx, one.Id)
	gone(t, "the exclusion", res, err)
}

//nolint:paralleltest // the tests share one server
func TestQualityProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	// a new profile is a copy of HD-1080p under another name
	source := must(sdk.GetQualityprofileById(ctx, hd1080)).Model
	fresh := *source
	fresh.Id = 0
	fresh.Name = "SDK Profile"
	p := must(sdk.PostQualityprofile(ctx, fresh)).Model
	removeAfter(ctx, t, "the profile", func(ctx context.Context) (radarr.DeleteQualityprofileByIdOperationResponse, error) {
		return sdk.DeleteQualityprofileById(ctx, p.Id)
	})
	if p.Id == 0 || p.Id == hd1080 || p.Cutoff != source.Cutoff {
		t.Fatalf("created profile %d with cutoff %d, want a new one with cutoff %d", p.Id, p.Cutoff, source.Cutoff)
	}

	edit := *p
	edit.UpgradeAllowed = new(!isTrue(p.UpgradeAllowed))
	if got := must(sdk.PutQualityprofileById(ctx, p.Id, edit)).Model; isTrue(got.UpgradeAllowed) != isTrue(edit.UpgradeAllowed) {
		t.Errorf("upgrades allowed is %v after the update", isTrue(got.UpgradeAllowed))
	}

	must(sdk.DeleteQualityprofileById(ctx, p.Id))
	res, err := sdk.GetQualityprofileById(ctx, p.Id)
	gone(t, "the profile", res, err)
}

//nolint:paralleltest // the tests share one server
func TestQualityDefinitions(t *testing.T) {
	ctx := skipUnlessUp(t)

	all := must(sdk.GetQualitydefinition(ctx)).Model
	i := slices.IndexFunc(all, func(d radarr.QualityDefinitionResource) bool {
		return d.Quality != nil && d.Quality.Name == "Bluray-1080p"
	})
	if i < 0 {
		t.Fatal("no Bluray-1080p definition")
	}
	def := all[i]
	id := def.Id
	t.Cleanup(func() { _, _ = sdk.PutQualitydefinitionUpdate(context.WithoutCancel(ctx), all) })

	edit := def
	edit.Title = "Blu-ray 1080p"
	if got := must(sdk.PutQualitydefinitionById(ctx, id, edit)).Model; got.Title != edit.Title {
		t.Errorf("titled %q after the update", got.Title)
	}

	// the bulk update answers every definition - as they were: it saves
	// them without clearing Radarr's cache of them, which it answers from,
	// and a read shows the change only once the cache expires, seconds
	// later
	changed := slices.Clone(all)
	changed[i].Title = "Blu-ray 1080p (bulk)"
	got := must(sdk.PutQualitydefinitionUpdate(ctx, changed)).Model
	if len(got) != len(all) {
		t.Errorf("the bulk update answered %d definitions, want %d", len(got), len(all))
	}
	titled(ctx, t, def.Id, changed[i].Title)

	must(sdk.PutQualitydefinitionUpdate(ctx, all))
	titled(ctx, t, def.Id, def.Title)
}

// titled waits for a quality definition to read with a title.
func titled(ctx context.Context, t *testing.T, id int, title string) {
	t.Helper()

	var got string
	if !poll(time.Minute, func() bool {
		got = must(sdk.GetQualitydefinitionById(ctx, id)).Model.Title
		return got == title
	}) {
		t.Errorf("definition %d is still titled %q, want %q", id, got, title)
	}
}
