//go:build acceptance

package radarr_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/radarr-mcp/lib/testenv"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// TestManualImport imports a file by hand, the way the UI's manual import
// does: it lists what Radarr would import from a folder, has a candidate
// reprocessed with the film chosen for it, and runs the import command,
// which tells the webhook.
//
//nolint:paralleltest // the tests share one server
func TestManualImport(t *testing.T) {
	ctx := skipUnlessUp(t)
	fixtures(t)
	a3 := film(ctx, t, alien3)
	if isTrue(a3.HasFile) {
		t.Fatal("Alien³ has a file already")
	}

	const folder = "/media/downloads/manual/Alien3.1992.1080p.BluRay.x264-MAN"
	const path = folder + "/Alien3.1992.1080p.BluRay.x264-MAN.mkv"
	copyFile(t, alien.Path+"/"+alien.File, path)
	t.Cleanup(func() { _ = os.RemoveAll(testenv.HostPath("/media/downloads/manual")) })

	candidates := must(sdk.GetManualImport(ctx, radarr.GetManualImportOptions{Folder: folder})).Answer
	if len(candidates) != 1 || candidates[0].Path != path {
		t.Fatalf("the folder has %d candidates, want the one file: %+v", len(candidates), candidates)
	}
	c := candidates[0]

	// a candidate reprocessed with a film chosen for it is answered again,
	// with that film and Radarr's decision on the file for it
	reprocessed := must(sdk.PostManualImport(ctx, []radarr.ManualImportReprocessResource{{
		Path: path, MovieId: a3.Id, Quality: c.Quality, Languages: c.Languages, ReleaseGroup: "MAN",
	}})).Answer
	if len(reprocessed) != 1 || reprocessed[0].Movie == nil || reprocessed[0].Movie.Id != a3.Id {
		t.Fatalf("the reprocess answered %+v", reprocessed)
	}
	if len(reprocessed[0].Rejections) > 0 {
		t.Fatalf("the file would be rejected for %s: %+v", alien3.Title, reprocessed[0].Rejections)
	}

	before := len(hooks.received())
	hook := must(sdk.PostNotification(ctx, webhook(t, "SDK Import Hook"))).Answer
	removeAfter(ctx, t, "the webhook", func(ctx context.Context) (radarr.DeleteNotificationByIdResponse, error) {
		return sdk.DeleteNotificationById(ctx, hook.Id)
	})

	// the import itself is a command, which takes the files and the film
	// each goes to
	completed(t, must(sdk.ManualImportAndWait(ctx, radarr.ManualImportCommand{ImportMode: "copy", Files: []radarr.ManualImportCommandFiles{{Path: path, MovieId: a3.Id, Quality: c.Quality, Languages: c.Languages, ReleaseGroup: "MAN"}}}, commandWait)))

	// Alien³ has the file now, a copy: the one in the folder is still there
	m := withFile(ctx, t, a3.Id)
	t.Cleanup(func() { _, _ = sdk.DeleteMovieFileById(context.WithoutCancel(ctx), m.MovieFile.Id) })
	if !strings.HasPrefix(m.MovieFile.Path, alien3.Path+"/") || m.MovieFile.ReleaseGroup != "MAN" {
		t.Errorf("imported to %s from release group %q", m.MovieFile.Path, m.MovieFile.ReleaseGroup)
	}
	if _, err := os.Stat(testenv.HostPath(path)); err != nil {
		t.Errorf("a copy import took the original: %v", err)
	}
	if !poll(10*time.Second, func() bool { return len(slices.Collect(hookEvents("Download", hooks.received()[before:]))) > 0 }) {
		t.Error("the webhook did not hear of the import")
	}
}

// TestCommands cancels a command. Radarr cancels only a command still
// queued, and runs its commands on two threads, each as soon as it can: a
// command waits only while both are busy. A search waits on every indexer,
// so with an indexer that answers only when told, each search holds a
// thread, and the command after two of them waits.
//
//nolint:paralleltest // the tests share one server
func TestCommands(t *testing.T) {
	ctx := skipUnlessUp(t)
	downloads(t)

	// two that stay inside Radarr, each by its own method
	completed(t, must(sdk.HousekeepingAndWait(ctx, radarr.HousekeepingCommand{}, commandWait)))
	completed(t, must(sdk.CleanUpRecycleBinAndWait(ctx, radarr.CleanUpRecycleBinCommand{}, commandWait)))

	// a finished command cannot be cancelled
	done := completed(t, must(sdk.CheckHealthAndWait(ctx, radarr.CheckHealthCommand{}, commandWait)))
	_, err := sdk.DeleteCommandById(ctx, done.Id)
	if client.StatusCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "Unable to cancel task") {
		t.Errorf("cancelling a finished command: want 409 Unable to cancel task, got %v", err)
	}

	ix := must(sdk.PostIndexer(ctx, heldIndexer(t, "SDK Held Indexer"))).Answer
	removeAfter(ctx, t, "the held indexer", func(ctx context.Context) (radarr.DeleteIndexerByIdResponse, error) {
		return sdk.DeleteIndexerById(ctx, ix.Id)
	})
	release := hooks.hold()
	defer release()

	status := func(id int) radarr.CommandStatus { return must(sdk.GetCommandById(ctx, id)).Answer.Status }
	var searches []int
	var waiting *radarr.CommandResource
	for _, f := range []filmFixture{alien, aliens, bladeRunner, dune1984} {
		search := must(sdk.MoviesSearch(ctx, radarr.MoviesSearchCommand{MovieIds: []radarr.MovieID{film(ctx, t, f).Id}})).Answer
		searches = append(searches, search.Id)
		// it searches the held indexer and waits, unless there is no
		// thread for it
		if !poll(30*time.Second, func() bool { return hooks.held() == len(searches) || status(search.Id) == radarr.CommandStatusQueued }) {
			t.Fatalf("the search of %s neither waits on the indexer nor for a thread", f.Title)
		}
		if status(search.Id) == radarr.CommandStatusQueued {
			waiting = search
			break
		}
		probe := must(sdk.PostCommand(ctx, json.RawMessage(`{"name":"CheckHealth"}`))).Answer
		time.Sleep(2 * time.Second)
		if status(probe.Id) == radarr.CommandStatusQueued {
			waiting = probe
			break
		}
	}
	if waiting == nil {
		t.Fatal("no command ever had to wait")
	}
	// cancelled, it is taken off the queue and never runs - but it reads as
	// queued still, then and after: Radarr drops it from the queue without
	// recording that it was cancelled
	must(sdk.DeleteCommandById(ctx, waiting.Id))
	release()
	for _, id := range searches {
		if !poll(time.Minute, func() bool {
			s := status(id)
			return s != radarr.CommandStatusQueued && s != radarr.CommandStatusStarted
		}) {
			t.Errorf("search %d did not finish once the indexer answered", id)
		}
	}
	if got := must(sdk.GetCommandById(ctx, waiting.Id)).Answer; got.Status != radarr.CommandStatusQueued || !got.Started.IsZero() {
		t.Errorf("the cancelled %s is %s, started %q; want it queued and never started", got.Name, got.Status, got.Started)
	}
}
