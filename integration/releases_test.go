//go:build integration

package integration

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/radarr-mcp/internal/fakeindexer"
	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// The download flow, end to end: the fake indexer offers a release, Radarr
// finds it in a search and grabs it into the Usenet Blackhole, the grab
// shows in the history and is announced to the webhook, and marking it
// failed blocklists it. A delay profile holds a search's releases pending in
// the queue until they are grabbed from it, and what appears in the
// blackhole's watch folder is in the queue until it is removed.

// dune2 has no folder on disk: a second missing film, for the pending
// releases.
var dune2 = filmFixture{"Dune: Part Two", 2024, 693134, messyRoot + "/Dune - Part Two (2024)", ""}

// release is a release of a film, as big as a Blu-ray rip.
func release(title string, f *radarr.MovieResource) fakeindexer.Release {
	return fakeindexer.Release{Title: title, TmdbID: f.TmdbId, ImdbID: f.ImdbId, Size: 8 << 30}
}

// nzb waits for the blackhole to have written a release's .nzb, and removes
// it when the test ends.
func nzb(t *testing.T, title string) {
	t.Helper()

	path := hostPath(nzbFolder + "/" + title + ".nzb")
	t.Cleanup(func() { _ = os.Remove(path) })
	if !poll(30*time.Second, func() bool { _, err := os.Stat(path); return err == nil }) {
		t.Errorf("no .nzb for %s in the blackhole", title)
	}
}

//nolint:paralleltest // the tests share one server
func TestReleases(t *testing.T) {
	ctx := skipUnlessUp(t)
	downloads(t)
	a3 := film(ctx, t, alien3)
	if isTrue(a3.HasFile) {
		t.Fatal("Alien³ has a file: the grabs would be rejected")
	}

	// a failed download is not searched for again while the test runs,
	// which offers each release itself
	dc := must(sdk.GetConfigDownloadclient(ctx)).Model
	noRetry := *dc
	noRetry.AutoRedownloadFailed = new(false)
	must(sdk.PutConfigDownloadclientById(ctx, dc.Id, noRetry))
	t.Cleanup(func() { _, _ = sdk.PutConfigDownloadclientById(context.WithoutCancel(ctx), dc.Id, *dc) })

	before := len(hooks.received())
	hook := must(sdk.PostNotification(ctx, webhook(t, "SDK Grab Hook"), radarr.PostNotificationOperationOptions{})).Model
	removeAfter(ctx, t, "the webhook", func(ctx context.Context) (radarr.DeleteNotificationByIdOperationResponse, error) {
		return sdk.DeleteNotificationById(ctx, hook.Id)
	})

	// grabAndFail grabs a release from an interactive search, marks the
	// grab failed, and answers the blocklist entry that makes
	grabAndFail := func(t *testing.T, title string) radarr.BlocklistResource {
		t.Helper()

		indexer.Offer(release(title, &a3))
		t.Cleanup(func() { indexer.Withdraw(title) })

		// an interactive search answers every release with Radarr's
		// decision on it
		releases := must(sdk.GetRelease(ctx, radarr.GetReleaseOperationOptions{MovieId: a3.Id})).Model
		i := slices.IndexFunc(releases, func(r radarr.ReleaseResource) bool { return r.Title == title })
		if i < 0 {
			t.Fatalf("the search did not find %s among %d releases", title, len(releases))
		}
		found := releases[i]
		if !isTrue(found.Approved) {
			t.Fatalf("%s was rejected: %v", title, found.Rejections)
		}

		// a grab names the release by the search's guid and indexer, and
		// answers what it was sent: the guid and the indexer, not the
		// release it grabbed
		grabbed := must(sdk.PostRelease(ctx, radarr.ReleaseResource{Guid: found.Guid, IndexerId: found.IndexerId})).Model
		if grabbed.Guid != found.Guid || grabbed.IndexerId != found.IndexerId || grabbed.Title != "" {
			t.Errorf("the grab answered %q from %d, titled %q", grabbed.Guid, grabbed.IndexerId, grabbed.Title)
		}
		nzb(t, title)
		if !slices.Contains(indexer.Grabbed(), title) {
			t.Errorf("Radarr did not fetch the .nzb of %s", title)
		}

		var grab radarr.HistoryResource
		if !poll(30*time.Second, func() bool {
			history := must(sdk.GetHistoryMovie(ctx, radarr.GetHistoryMovieOperationOptions{MovieId: a3.Id, EventType: radarr.MovieHistoryEventTypeGrabbed})).Model
			i := slices.IndexFunc(history, func(h radarr.HistoryResource) bool { return h.SourceTitle == title })
			if i >= 0 {
				grab = history[i]
			}
			return i >= 0
		}) {
			t.Fatalf("no grab of %s in the history", title)
		}

		must(sdk.PostHistoryFailedById(ctx, grab.Id))
		var entry radarr.BlocklistResource
		if !poll(30*time.Second, func() bool {
			list := must(sdk.GetBlocklist(ctx, radarr.GetBlocklistOperationOptions{MovieIds: []int{a3.Id}})).Model.Records
			i := slices.IndexFunc(list, func(b radarr.BlocklistResource) bool { return b.SourceTitle == title })
			if i >= 0 {
				entry = list[i]
			}
			return i >= 0
		}) {
			t.Fatalf("marking the grab of %s failed did not blocklist it", title)
		}

		return entry
	}

	blocklisted := func(id int) bool {
		list := must(sdk.GetBlocklist(ctx, radarr.GetBlocklistOperationOptions{MovieIds: []int{a3.Id}})).Model.Records
		return slices.ContainsFunc(list, func(b radarr.BlocklistResource) bool { return b.Id == id })
	}

	t.Run("grab and fail", func(t *testing.T) {
		one := grabAndFail(t, "Alien3.1992.1080p.BluRay.x264-SDKA")
		must(sdk.DeleteBlocklistById(ctx, one.Id))
		if blocklisted(one.Id) {
			t.Error("the entry is still on the blocklist")
		}

		two := grabAndFail(t, "Alien3.1992.1080p.BluRay.x264-SDKB")
		must(sdk.DeleteBlocklistBulk(ctx, radarr.BlocklistBulkResource{Ids: []int{two.Id}}))
		if blocklisted(two.Id) {
			t.Error("the bulk-deleted entry is still on the blocklist")
		}

		// each grab was announced to the webhook
		grabs := slices.Collect(hookEvents("Grab", hooks.received()[before:]))
		if len(grabs) < 2 {
			t.Errorf("the webhook heard of %d grabs, want 2", len(grabs))
		}
	})

	t.Run("pending", func(t *testing.T) {
		// a delay profile holds a usenet release for ten hours after it is
		// posted, for the films with its tag - when the RSS sync finds it: a
		// search someone asked for ignores delays
		tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: "sdk-pending"})).Model
		removeAfter(ctx, t, "the tag", func(ctx context.Context) (radarr.DeleteTagByIdOperationResponse, error) {
			return sdk.DeleteTagById(ctx, tag.Id)
		})
		delay := must(sdk.PostDelayprofile(ctx, radarr.DelayProfileResource{
			EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: radarr.DownloadProtocolUsenet,
			UsenetDelay: 600, Tags: []int{tag.Id}, BypassIfHighestQuality: new(false), BypassIfAboveCustomFormatScore: new(false),
		})).Model
		removeAfter(ctx, t, "the delay profile", func(ctx context.Context) (radarr.DeleteDelayprofileByIdOperationResponse, error) {
			return sdk.DeleteDelayprofileById(ctx, delay.Id)
		})

		// two missing films, one pending release each
		d2 := must(sdk.PostMovie(ctx, newMovie(dune2))).Model
		removeAfter(ctx, t, dune2.Title, func(ctx context.Context) (radarr.DeleteMovieByIdOperationResponse, error) {
			return sdk.DeleteMovieById(ctx, d2.Id, radarr.DeleteMovieByIdOperationOptions{DeleteFiles: new(false)})
		})
		ids := []int{a3.Id, d2.Id}
		must(sdk.PutMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: ids, Tags: []int{tag.Id}, ApplyTags: radarr.ApplyTagsAdd}))
		t.Cleanup(func() {
			_, _ = sdk.PutMovieEditor(context.WithoutCancel(ctx), radarr.MovieEditorResource{MovieIds: ids[:1], Tags: []int{tag.Id}, ApplyTags: radarr.ApplyTagsRemove})
		})
		titles := map[int]string{a3.Id: "Alien3.1992.1080p.BluRay.x264-PEND", d2.Id: "Dune.Part.Two.2024.1080p.BluRay.x264-PEND"}
		for id, title := range titles {
			r := release(title, must(sdk.GetMovieById(ctx, id)).Model)
			r.Published = time.Now()
			indexer.Offer(r)
			t.Cleanup(func() { indexer.Withdraw(title) })
		}
		command(ctx, t, `{"name":"RssSync"}`)

		pending := map[int]radarr.QueueResource{}
		if !poll(30*time.Second, func() bool {
			queue := must(sdk.GetQueue(ctx, radarr.GetQueueOperationOptions{MovieIds: ids})).Model.Records
			for _, q := range queue {
				if q.Status == radarr.QueueStatusDelay && q.MovieId != nil {
					pending[*q.MovieId] = q
				}
			}
			return len(pending) == 2
		}) {
			t.Fatalf("the RSS sync left %d releases pending, want 2", len(pending))
		}

		// a pending release is grabbed from the queue, one or several at a
		// time
		must(sdk.PostQueueGrabById(ctx, pending[a3.Id].Id))
		nzb(t, titles[a3.Id])
		must(sdk.PostQueueGrabBulk(ctx, radarr.QueueBulkResource{Ids: []int{pending[d2.Id].Id}}))
		nzb(t, titles[d2.Id])
		for id, title := range titles {
			if !slices.Contains(indexer.Grabbed(), title) {
				t.Errorf("film %d: Radarr did not fetch the .nzb of %s", id, title)
			}
		}
	})

	t.Run("queue", func(t *testing.T) {
		// what appears in the blackhole's watch folder is a download, in the
		// queue whether or not it is a film Radarr knows: these are not
		names := []string{"sdk-queue-one", "sdk-queue-two"}
		for _, name := range names {
			copyFile(t, alien.Path+"/"+alien.File, watchFolder+"/"+name+"/"+name+".mkv")
			t.Cleanup(func() { _ = os.RemoveAll(hostPath(watchFolder + "/" + name)) })
		}
		command(ctx, t, `{"name":"RefreshMonitoredDownloads"}`)
		items := map[string]radarr.QueueResource{}
		if !poll(30*time.Second, func() bool {
			queue := must(sdk.GetQueue(ctx, radarr.GetQueueOperationOptions{IncludeUnknownMovieItems: new(true)})).Model.Records
			for _, q := range queue {
				if slices.Contains(names, q.Title) {
					items[q.Title] = q
				}
			}
			return len(items) == len(names)
		}) {
			t.Fatalf("the queue has %d of the watch folder's downloads, want %d", len(items), len(names))
		}

		// removing one from the client deletes it from the watch folder
		must(sdk.DeleteQueueById(ctx, items[names[0]].Id, radarr.DeleteQueueByIdOperationOptions{RemoveFromClient: new(true), Blocklist: new(false)}))
		must(sdk.DeleteQueueBulk(ctx, radarr.QueueBulkResource{Ids: []int{items[names[1]].Id}}, radarr.DeleteQueueBulkOperationOptions{RemoveFromClient: new(true), Blocklist: new(false)}))
		for _, name := range names {
			if _, err := os.Stat(hostPath(watchFolder + "/" + name)); !os.IsNotExist(err) {
				t.Errorf("%s is still in the watch folder after removing it from the queue: %v", name, err)
			}
		}
	})

	t.Run("push", func(t *testing.T) {
		// a release pushed from elsewhere, the way Prowlarr pushes one,
		// gets the decision a search would give it: Alien has a file that
		// meets its profile's cutoff, so this one is rejected and nothing is
		// grabbed
		const title = "Alien.1979.1080p.BluRay.x264-PUSH"
		pushed := must(sdk.PostReleasePush(ctx, radarr.ReleaseResource{
			Title: title, Protocol: radarr.DownloadProtocolUsenet, Size: 8 << 30,
			DownloadUrl: indexer.BaseURL() + "/download/" + strings.ToLower(title),
			PublishDate: time.Now().UTC().Format(time.RFC3339),
		})).Model
		if len(pushed) != 1 || !isTrue(pushed[0].Rejected) || len(pushed[0].Rejections) == 0 {
			t.Fatalf("the push answered %+v", pushed)
		}
		if pushed[0].MappedMovieId == nil || *pushed[0].MappedMovieId != film(ctx, t, alien).Id {
			t.Errorf("the push was matched to film %v, want Alien", pushed[0].MappedMovieId)
		}
	})
}
