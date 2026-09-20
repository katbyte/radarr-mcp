//go:build integration

package integration

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/katbyte/radarr-mcp/lib/client"
	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// The films the write tests add of their own, from the catalogue
// scripts/testenv.sh lays out, so the fixtures the other tests read are left
// alone.
var (
	// arrival is added, edited and deleted, with a copy of the catalogue's
	// file in a folder of its own: deleting its file deletes the copy
	arrival = filmFixture{"Arrival", 2016, 329865, messyRoot + "/Arrival (2016)", "Arrival (2016) Bluray-1080p.mkv"}
	// mononoke and thirteenth are added in bulk, the way an import of
	// existing folders and an import list add them, and deleted in bulk
	mononoke   = filmFixture{"Princess Mononoke", 1997, 128, moviesRoot + "/Princess Mononoke (1997)", "Princess Mononoke (1997) Bluray-1080p.mkv"}
	thirteenth = filmFixture{"The Thirteenth Floor", 1999, 1090, moviesRoot + "/The Thirteenth Floor (1999)", "The Thirteenth Floor (1999) Bluray-1080p.mkv"}
)

// newMovie is a film as an add sends it.
func newMovie(f filmFixture) radarr.MovieResource {
	return radarr.MovieResource{
		Title: f.Title, Year: f.Year, TmdbId: f.TmdbID, Path: f.Path, QualityProfileId: hd1080,
		Monitored: new(true), MinimumAvailability: radarr.MovieStatusTypeReleased,
		AddOptions: &radarr.AddMovieOptions{SearchForMovie: new(false), Monitor: radarr.MonitorTypesMovieOnly},
	}
}

// withFile waits for Radarr to find a film's file, which it scans for in
// the background after an add or a rescan.
func withFile(ctx context.Context, t *testing.T, id int) *radarr.MovieResource {
	t.Helper()

	var m *radarr.MovieResource
	if !poll(2*time.Minute, func() bool {
		m = must(sdk.GetMovieById(ctx, id)).Model
		return isTrue(m.HasFile) && m.MovieFile != nil
	}) {
		t.Fatalf("%s never found its file", m.Title)
	}

	return m
}

//nolint:paralleltest // the tests share one server
func TestMovies(t *testing.T) {
	ctx := skipUnlessUp(t)
	fixtures(t)

	copyFile(t, moviesRoot+"/Arrival (2016)/"+arrival.File, arrival.Path+"/"+arrival.File)
	t.Cleanup(func() { _ = os.RemoveAll(hostPath(arrival.Path)) })
	added := must(sdk.PostMovie(ctx, newMovie(arrival))).Model
	removeAfter(ctx, t, arrival.Title, func(ctx context.Context) (radarr.DeleteMovieByIdOperationResponse, error) {
		return sdk.DeleteMovieById(ctx, added.Id, radarr.DeleteMovieByIdOperationOptions{DeleteFiles: new(false)})
	})
	if added.Id == 0 || added.TmdbId != arrival.TmdbID || added.Title != arrival.Title {
		t.Fatalf("added %d %q (tmdb %d)", added.Id, added.Title, added.TmdbId)
	}
	m := withFile(ctx, t, added.Id)

	t.Run("update", func(t *testing.T) {
		edit := *m
		edit.Monitored = new(false)
		edit.MinimumAvailability = radarr.MovieStatusTypeInCinemas
		got := must(sdk.PutMovieById(ctx, m.Id, edit, radarr.PutMovieByIdOperationOptions{MoveFiles: new(false)})).Model
		if isTrue(got.Monitored) || got.MinimumAvailability != radarr.MovieStatusTypeInCinemas {
			t.Errorf("monitored %v, available when %s after the update", isTrue(got.Monitored), got.MinimumAvailability)
		}
	})

	t.Run("files", func(t *testing.T) {
		file := *must(sdk.GetMovieById(ctx, m.Id)).Model.MovieFile

		edit := file
		edit.ReleaseGroup = "SDK"
		if got := must(sdk.PutMoviefileById(ctx, file.Id, edit)).Model; got.ReleaseGroup != "SDK" {
			t.Errorf("release group %q after the update", got.ReleaseGroup)
		}

		// the bulk edit takes whole files, applying the fields it reads
		// (edition, release group, quality, languages, indexer flags), and
		// answers every file it changed
		bulk := must(sdk.PutMoviefileBulk(ctx, []radarr.MovieFileResource{{Id: file.Id, Edition: "SDK Cut", ReleaseGroup: "SDK"}})).Model
		if len(bulk) != 1 || bulk[0].Edition != "SDK Cut" {
			t.Errorf("the bulk edit answered %+v", bulk)
		}

		// the editor sets fields on a list of files, and answers them
		//nolint:staticcheck // deprecated in the document, and still served: exercised while it is
		edited := must(sdk.PutMoviefileEditor(ctx, radarr.MovieFileListResource{MovieFileIds: []int{file.Id}, ReleaseGroup: "SDK2"})).Model
		if len(edited) != 1 || edited[0].ReleaseGroup != "SDK2" {
			t.Errorf("the editor answered %+v", edited)
		}

		// deleting a file deletes it from disk, and the film is missing
		must(sdk.DeleteMoviefileById(ctx, file.Id))
		if _, err := os.Stat(hostPath(arrival.Path + "/" + arrival.File)); !os.IsNotExist(err) {
			t.Errorf("the file is still on disk after deleting it: %v", err)
		}
		if isTrue(must(sdk.GetMovieById(ctx, m.Id)).Model.HasFile) {
			t.Error("the film still has a file")
		}

		// and a film's files can be deleted as a list
		copyFile(t, moviesRoot+"/Arrival (2016)/"+arrival.File, arrival.Path+"/"+arrival.File)
		command(ctx, t, `{"name":"RescanMovie","movieId":`+strconv.Itoa(m.Id)+`}`)
		again := withFile(ctx, t, m.Id).MovieFile
		must(sdk.DeleteMoviefileBulk(ctx, radarr.MovieFileListResource{MovieFileIds: []int{again.Id}}))
		res, err := sdk.GetMoviefileById(ctx, again.Id)
		gone(t, "the bulk-deleted file", res, err)
	})

	t.Run("editor", func(t *testing.T) {
		changed := must(sdk.PutMovieEditor(ctx, radarr.MovieEditorResource{
			MovieIds: []int{m.Id}, Monitored: new(true), MinimumAvailability: radarr.MovieStatusTypeReleased,
		})).Model
		if len(changed) != 1 || !isTrue(changed[0].Monitored) || changed[0].MinimumAvailability != radarr.MovieStatusTypeReleased {
			t.Errorf("the editor answered %+v", changed)
		}
	})

	must(sdk.DeleteMovieById(ctx, m.Id, radarr.DeleteMovieByIdOperationOptions{DeleteFiles: new(true), AddImportExclusion: new(false)}))
	res, err := sdk.GetMovieById(ctx, m.Id)
	gone(t, arrival.Title, res, err)
	// the folder goes in the background, after the delete has answered
	if !poll(30*time.Second, func() bool { _, err := os.Stat(hostPath(arrival.Path)); return os.IsNotExist(err) }) {
		t.Error("the film's folder is still on disk after deleting it with its files")
	}
}

// TestMovieImports adds films the two ways that take a list - an import of
// existing folders, and an add from an import list's discoveries - and
// deletes them together.
//
//nolint:paralleltest // the tests share one server
func TestMovieImports(t *testing.T) {
	ctx := skipUnlessUp(t)
	fixtures(t)

	ids := make([]int, 0, 2)
	t.Cleanup(func() {
		if len(ids) > 0 {
			_, _ = sdk.DeleteMovieEditor(context.WithoutCancel(ctx), radarr.MovieEditorResource{MovieIds: ids, DeleteFiles: new(false)})
		}
	})

	imported := must(sdk.PostMovieImport(ctx, []radarr.MovieResource{newMovie(mononoke)})).Model
	if len(imported) != 1 || imported[0].TmdbId != mononoke.TmdbID || imported[0].Id == 0 {
		t.Fatalf("the import answered %+v", imported)
	}
	ids = append(ids, imported[0].Id)

	listed := must(sdk.PostImportlistMovie(ctx, []radarr.MovieResource{newMovie(thirteenth)})).Model
	if len(listed) != 1 || listed[0].TmdbId != thirteenth.TmdbID || listed[0].Id == 0 {
		t.Fatalf("the add from the discoveries answered %+v", listed)
	}
	ids = append(ids, listed[0].Id)

	// a film Radarr already has fails an import, and is skipped by an add
	// from the discoveries
	if _, err := sdk.PostMovieImport(ctx, []radarr.MovieResource{newMovie(mononoke)}); client.StatusCode(err) != http.StatusBadRequest {
		t.Errorf("importing a film already added: want 400, got %v", err)
	}
	if again := must(sdk.PostImportlistMovie(ctx, []radarr.MovieResource{newMovie(thirteenth)})).Model; len(again) != 0 {
		t.Errorf("adding a film already added answered %+v", again)
	}

	must(sdk.DeleteMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: ids, DeleteFiles: new(false), AddImportExclusion: new(false)}))
	for _, id := range ids {
		res, err := sdk.GetMovieById(ctx, id)
		gone(t, "a film deleted in bulk", res, err)
	}
	// their files were left where they were
	for _, f := range []filmFixture{mononoke, thirteenth} {
		if _, err := os.Stat(hostPath(f.Path + "/" + f.File)); err != nil {
			t.Errorf("%s: %v", f.Title, err)
		}
	}
	ids = nil
}

//nolint:paralleltest // the tests share one server
func TestRootFolders(t *testing.T) {
	ctx := skipUnlessUp(t)

	const path = "/media/sdk-root"
	mediaMkdir(hostPath(path))
	t.Cleanup(func() { _ = os.RemoveAll(hostPath(path)) })
	root := must(sdk.PostRootfolder(ctx, radarr.RootFolderResource{Path: path})).Model
	removeAfter(ctx, t, "the root folder", func(ctx context.Context) (radarr.DeleteRootfolderByIdOperationResponse, error) {
		return sdk.DeleteRootfolderById(ctx, root.Id)
	})
	if root.Path != path || !isTrue(root.Accessible) {
		t.Errorf("added %q, accessible %v", root.Path, isTrue(root.Accessible))
	}

	must(sdk.DeleteRootfolderById(ctx, root.Id))
	res, err := sdk.GetRootfolderById(ctx, root.Id)
	gone(t, "the root folder", res, err)
}

//nolint:paralleltest // the tests share one server
func TestCollections(t *testing.T) {
	ctx := skipUnlessUp(t)
	fixtures(t)

	// the Alien films bring their collection with them
	cols := must(sdk.GetCollection(ctx, radarr.GetCollectionOperationOptions{})).Model
	i := slices.IndexFunc(cols, func(c radarr.CollectionResource) bool { return c.TmdbId == 8091 })
	if i < 0 {
		t.Fatal("no Alien collection")
	}
	col := cols[i]
	t.Cleanup(func() { _, _ = sdk.PutCollectionById(context.WithoutCancel(ctx), col.Id, col) })

	// leaving it unmonitored: a monitored collection adds its missing films
	edit := col
	edit.MinimumAvailability = radarr.MovieStatusTypeInCinemas
	if got := must(sdk.PutCollectionById(ctx, col.Id, edit)).Model; got.MinimumAvailability != radarr.MovieStatusTypeInCinemas {
		t.Errorf("available when %s after the update", got.MinimumAvailability)
	}

	// the bulk edit answers every collection it changed
	changed := must(sdk.PutCollection(ctx, radarr.CollectionUpdateResource{
		CollectionIds: []int{col.Id}, MinimumAvailability: radarr.MovieStatusTypeAnnounced,
	})).Model
	if len(changed) != 1 || changed[0].MinimumAvailability != radarr.MovieStatusTypeAnnounced {
		t.Errorf("the bulk edit answered %+v", changed)
	}
	if again := must(sdk.GetCollectionById(ctx, col.Id)).Model; again.MinimumAvailability != radarr.MovieStatusTypeAnnounced {
		t.Errorf("available when %s after the bulk edit", again.MinimumAvailability)
	}
}
