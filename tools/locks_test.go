package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// keeping is a canned Radarr that keeps one record as it is sent back: a read
// answers the record as it stands and a write replaces it whole, as Radarr's
// own PUT does. A read waits a moment before it answers, so that calls made
// at once all read before any of them writes, the way they do against a real
// server over a network.
type keeping struct {
	mu     sync.Mutex
	record map[string]any
}

func (k *keeping) read(w http.ResponseWriter, list bool) {
	k.mu.Lock()
	now := maps.Clone(k.record)
	k.mu.Unlock()
	time.Sleep(20 * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	if list {
		_ = json.NewEncoder(w).Encode([]any{now})

		return
	}
	_ = json.NewEncoder(w).Encode(now)
}

func (k *keeping) write(w http.ResponseWriter, r *http.Request) {
	var sent map[string]any
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &sent); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}
	k.mu.Lock()
	k.record = sent
	k.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(sent)
}

func (k *keeping) now() map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()

	return maps.Clone(k.record)
}

// at once makes the calls of one turn the way an MCP client does: all of
// them together.
func atOnce(t *testing.T, cs *mcp.ClientSession, name string, calls ...map[string]any) {
	t.Helper()

	var wg sync.WaitGroup
	errs := make(chan string, len(calls))
	for _, args := range calls {
		wg.Go(func() {
			if _, msg := run(t, cs, name, args); msg != "" {
				errs <- fmt.Sprintf("%s %v: %s", name, args, msg)
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// holds fails for each setting of a record that is not what the edits made
// at once should between them have left it.
func holds(t *testing.T, what string, got, want map[string]any) {
	t.Helper()

	for setting, value := range want {
		if got[setting] != value {
			t.Errorf("after three edits at once the %s has %s %v, want %v: one edit undid another", what, setting, got[setting], value)
		}
	}
}

// An edit that reads a record and sends it back whole undoes another made
// beside it: each sends the record as it was before the other. Three edits
// of one quality profile in one turn, each changing its own setting, must
// leave all three changed.
func TestEditsOfOneProfileAtOnceAllHold(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library()
	profile := &keeping{record: maps.Clone(fakeProfile)}
	profile.record["minFormatScore"], profile.record["cutoffFormatScore"] = 0, 0
	f.mux.HandleFunc("GET /api/v3/qualityprofile/{id}", func(w http.ResponseWriter, _ *http.Request) { profile.read(w, false) })
	f.mux.HandleFunc("PUT /api/v3/qualityprofile/{id}", profile.write)
	cs := session(t, f, Options{})

	atOnce(t, cs, "qualityprofile_edit", map[string]any{"profile": "HD-1080p", "upgrade_allowed": false}, map[string]any{"profile": "HD-1080p", "min_format_score": 10}, map[string]any{"profile": "HD-1080p", "cutoff_format_score": 50})

	holds(t, "profile", profile.now(), map[string]any{"upgradeAllowed": false, "minFormatScore": float64(10), "cutoffFormatScore": float64(50)})
}

// The same of a quality's size limits, and of the naming settings, which are
// one record each.
func TestEditsOfOneSettingAtOnceAllHold(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library()
	definition := &keeping{record: map[string]any{"id": 3, "title": "WEBDL-1080p", "quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}, "minSize": 0, "maxSize": 100, "preferredSize": 95}}
	f.mux.HandleFunc("GET /api/v3/qualitydefinition", func(w http.ResponseWriter, _ *http.Request) { definition.read(w, true) })
	f.mux.HandleFunc("GET /api/v3/qualitydefinition/{id}", func(w http.ResponseWriter, _ *http.Request) { definition.read(w, false) })
	f.mux.HandleFunc("PUT /api/v3/qualitydefinition/{id}", definition.write)
	naming := &keeping{record: map[string]any{"id": 1, "renameMovies": false, "replaceIllegalCharacters": true, "colonReplacementFormat": "delete", "standardMovieFormat": "{Movie Title} ({Release Year})", "movieFolderFormat": "{Movie Title} ({Release Year})"}}
	f.mux.HandleFunc("GET /api/v3/config/naming", func(w http.ResponseWriter, _ *http.Request) { naming.read(w, false) })
	f.mux.HandleFunc("PUT /api/v3/config/naming/{id}", naming.write)
	f.answer("GET /api/v3/config/naming/examples", http.StatusOK, map[string]any{})
	cs := session(t, f, Options{})

	atOnce(t, cs, "qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p", "min_size": 2}, map[string]any{"quality": "WEBDL-1080p", "preferred_size": 40}, map[string]any{"quality": "WEBDL-1080p", "max_size": 60})
	holds(t, "quality", definition.now(), map[string]any{"minSize": float64(2), "preferredSize": float64(40), "maxSize": float64(60)})

	atOnce(t, cs, "naming_edit", map[string]any{"rename_movies": true}, map[string]any{"colon_replacement": "dash"}, map[string]any{"movie_folder_format": "{Movie Title} [{Release Year}]"})
	holds(t, "naming", naming.now(), map[string]any{"renameMovies": true, "colonReplacementFormat": "dash", "movieFolderFormat": "{Movie Title} [{Release Year}]"})
}
