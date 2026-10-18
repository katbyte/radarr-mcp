//go:build acceptance

package radarr_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/radarr-mcp/lib/testenv"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
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
func release(title string, f *radarr.MovieResource) newznab.Release {
	return newznab.Release{Category: newznab.CategoryMoviesHD, Title: title, TMDBID: int(f.TmdbId), IMDBID: f.ImdbId, Size: 8 << 30}
}

// nzb waits for the blackhole to have written a release's .nzb, and removes
// it when the test ends.
func nzb(t *testing.T, title string) {
	t.Helper()

	path := testenv.HostPath(nzbFolder + "/" + title + ".nzb")
	t.Cleanup(func() { _ = os.Remove(path) })
	if !poll(30*time.Second, func() bool { _, err := os.Stat(path); return err == nil }) {
		t.Errorf("no .nzb for %s in the blackhole", title)
	}
}

func TestReleases(t *testing.T) {
	ctx := skipUnlessUp(t)
	downloads(t)
	a3 := film(ctx, t, alien3)
	if isTrue(a3.HasFile) {
		t.Fatal("Alien³ has a file: the grabs would be rejected")
	}

	// a failed download is not searched for again while the test runs,
	// which offers each release itself
	dc := must(must(sdk.GetConfigDownloadClient(ctx)).Record())
	noRetry := dc
	noRetry.AutoRedownloadFailed = new(false)
	must(sdk.PutConfigDownloadClientById(ctx, dc.Id, noRetry))
	t.Cleanup(func() { _, _ = sdk.PutConfigDownloadClientById(context.WithoutCancel(ctx), dc.Id, dc) })

	before := len(hooks.received())
	hook := must(sdk.PostNotification(ctx, webhook(t, "SDK Grab Hook"))).Answer
	removeAfter(ctx, t, "the webhook", func(ctx context.Context) (radarr.DeleteNotificationByIdResponse, error) {
		return sdk.DeleteNotificationById(ctx, hook.Id)
	})

	// grabAndFail grabs a release from an interactive search, marks the
	// grab failed, and answers the blocklist entry that makes
	grabAndFail := func(t *testing.T, title string) radarr.BlocklistResource {
		t.Helper()

		if err := indexer.Offer(release(title, &a3)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { indexer.Withdraw(title) })

		// an interactive search answers every release with Radarr's
		// decision on it
		releases := must(sdk.GetRelease(ctx, a3.Id)).Answer
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
		grabbed := must(sdk.PostRelease(ctx, radarr.ReleaseResource{Guid: found.Guid, IndexerId: found.IndexerId})).Answer
		if grabbed.Guid != found.Guid || grabbed.IndexerId != found.IndexerId || grabbed.Title != "" {
			t.Errorf("the grab answered %q from %d, titled %q", grabbed.Guid, grabbed.IndexerId, grabbed.Title)
		}
		nzb(t, title)
		if !slices.Contains(indexer.Grabbed(), title) {
			t.Errorf("Radarr did not fetch the .nzb of %s", title)
		}

		var grab radarr.HistoryResource
		if !poll(30*time.Second, func() bool {
			history := must(sdk.GetHistoryMovie(ctx, radarr.GetHistoryMovieOptions{MovieId: a3.Id, EventType: radarr.MovieHistoryEventTypeGrabbed})).Answer
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
			list := must(sdk.GetBlocklist(ctx, radarr.GetBlocklistOptions{MovieIds: []radarr.MovieID{a3.Id}})).Answer.Records
			i := slices.IndexFunc(list, func(b radarr.BlocklistResource) bool { return b.SourceTitle == title })
			if i >= 0 {
				entry = list[i]
			}
			return i >= 0
		}) {
			t.Fatalf("marking the grab of %s failed did not blocklist it", title)
		}
		// the film's own blocklist, which is not paged, holds the same entry
		if byFilm := must(sdk.GetBlocklistMovie(ctx, a3.Id)).Answer; !slices.ContainsFunc(byFilm, func(b radarr.BlocklistResource) bool { return b.Id == entry.Id && b.SourceTitle == title }) {
			t.Errorf("the blocklist of %s does not hold the grab of %s: %+v", alien3.Title, title, byFilm)
		}

		return entry
	}

	blocklisted := func(id int) bool {
		list := must(sdk.GetBlocklist(ctx, radarr.GetBlocklistOptions{MovieIds: []radarr.MovieID{a3.Id}})).Answer.Records
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
		tag := must(sdk.PostTag(ctx, radarr.TagResource{Label: "sdk-pending"})).Answer
		removeAfter(ctx, t, "the tag", func(ctx context.Context) (radarr.DeleteTagByIdResponse, error) {
			return sdk.DeleteTagById(ctx, tag.Id)
		})
		delay := must(sdk.PostDelayProfile(ctx, radarr.DelayProfileResource{
			EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: radarr.DownloadProtocolUsenet,
			UsenetDelay: 600, Tags: []radarr.TagID{tag.Id}, BypassIfHighestQuality: new(false), BypassIfAboveCustomFormatScore: new(false),
		})).Answer
		removeAfter(ctx, t, "the delay profile", func(ctx context.Context) (radarr.DeleteDelayProfileByIdResponse, error) {
			return sdk.DeleteDelayProfileById(ctx, delay.Id)
		})

		// two missing films, one pending release each
		d2 := must(sdk.PostMovie(ctx, newMovie(dune2))).Answer
		removeAfter(ctx, t, dune2.Title, func(ctx context.Context) (radarr.DeleteMovieByIdResponse, error) {
			return sdk.DeleteMovieById(ctx, d2.Id, radarr.DeleteMovieByIdOptions{DeleteFiles: new(false)})
		})
		ids := []radarr.MovieID{a3.Id, d2.Id}
		must(sdk.PutMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: ids, Tags: []radarr.TagID{tag.Id}, ApplyTags: radarr.ApplyTagsAdd}))
		t.Cleanup(func() {
			_, _ = sdk.PutMovieEditor(context.WithoutCancel(ctx), radarr.MovieEditorResource{MovieIds: ids[:1], Tags: []radarr.TagID{tag.Id}, ApplyTags: radarr.ApplyTagsRemove})
		})
		titles := map[radarr.MovieID]string{a3.Id: "Alien3.1992.1080p.BluRay.x264-PEND", d2.Id: "Dune.Part.Two.2024.1080p.BluRay.x264-PEND"}
		for id, title := range titles {
			r := release(title, must(sdk.GetMovieById(ctx, id)).Answer)
			r.PubDate = time.Now()
			if err := indexer.Offer(r); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { indexer.Withdraw(title) })
		}
		completed(t, must(sdk.RssSyncAndWait(ctx, radarr.RssSyncCommand{}, commandWait)))

		pending := map[radarr.MovieID]radarr.QueueResource{}
		if !poll(30*time.Second, func() bool {
			queue := must(sdk.GetQueue(ctx, radarr.GetQueueOptions{MovieIds: ids})).Answer.Records
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
			t.Cleanup(func() { _ = os.RemoveAll(testenv.HostPath(watchFolder + "/" + name)) })
		}
		completed(t, must(sdk.RefreshMonitoredDownloadsAndWait(ctx, radarr.RefreshMonitoredDownloadsCommand{}, commandWait)))
		items := map[string]radarr.QueueResource{}
		if !poll(30*time.Second, func() bool {
			queue := must(sdk.GetQueue(ctx, radarr.GetQueueOptions{IncludeUnknownMovieItems: new(true)})).Answer.Records
			for _, q := range queue {
				if slices.Contains(names, q.Title) {
					items[q.Title] = q
				}
			}
			return len(items) == len(names)
		}) {
			t.Fatalf("the queue has %d of the watch folder's downloads, want %d", len(items), len(names))
		}
		// the queue read whole and its counts say the same: two downloads, neither a film Radarr knows
		details := must(sdk.GetQueueDetails(ctx)).Answer
		for _, name := range names {
			if !slices.ContainsFunc(details, func(q radarr.QueueResource) bool { return q.Title == name && q.Id == items[name].Id }) {
				t.Errorf("the queue's details do not hold %s: %d items", name, len(details))
			}
		}
		if status := must(sdk.GetQueueStatus(ctx)).Answer; status.UnknownCount < len(names) {
			t.Errorf("the queue's status counts %d downloads for no film Radarr knows, want at least %d: %+v", status.UnknownCount, len(names), status)
		}

		// removing one from the client deletes it from the watch folder
		must(sdk.DeleteQueueById(ctx, items[names[0]].Id, radarr.DeleteQueueByIdOptions{RemoveFromClient: new(true), Blocklist: new(false)}))
		must(sdk.DeleteQueueBulk(ctx, radarr.QueueBulkResource{Ids: []int{items[names[1]].Id}}, radarr.DeleteQueueBulkOptions{RemoveFromClient: new(true), Blocklist: new(false)}))
		for _, name := range names {
			if _, err := os.Stat(testenv.HostPath(watchFolder + "/" + name)); !os.IsNotExist(err) {
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
			DownloadUrl: indexer.URL() + "/download/" + strings.ToLower(title),
			PublishDate: radarr.Time{Time: time.Now().UTC()},
		})).Answer
		if len(pushed) != 1 || !isTrue(pushed[0].Rejected) || len(pushed[0].Rejections) == 0 {
			t.Fatalf("the push answered %+v", pushed)
		}
		if pushed[0].MappedMovieId == nil || *pushed[0].MappedMovieId != film(ctx, t, alien).Id {
			t.Errorf("the push was matched to film %v, want Alien", pushed[0].MappedMovieId)
		}
	})
}
