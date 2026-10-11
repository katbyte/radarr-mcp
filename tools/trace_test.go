package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// traced keeps every line of a trace of what is sent to Radarr and what it answers, for a test to read.
type traced struct {
	mu    sync.Mutex
	lines []string
}

func (*traced) Debugf(string, ...any) {}

func (l *traced) Tracef(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *traced) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// A trace of what the tools send Radarr, and of what it answers, carries no credential: not one of Radarr's own, whatever it is called, and not an
// indexer's key, which Radarr's API carries as the value of a setting rather than under a name of its own.
func TestATraceCarriesNoCredential(t *testing.T) {
	t.Parallel()

	newznab := map[string]any{
		"implementation": "Newznab", "implementationName": "Newznab", "configContract": "NewznabSettings", "protocol": "usenet", "supportsRss": true, "supportsSearch": true, "priority": 25, "tags": []any{},
		"fields": []any{map[string]any{"name": "baseUrl", "type": "textbox"}, map[string]any{"name": "apiKey", "type": "textbox", "privacy": "apiKey"}},
	}

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.answer("GET /api/v3/indexer/schema", http.StatusOK, []any{newznab})
	f.keep(t, "/api/v3/indexer")
	f.mux.HandleFunc("GET /api/v3/config/host", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "port": 7878})
	})
	f.mux.HandleFunc("PUT /api/v3/config/host/{id}", func(w http.ResponseWriter, r *http.Request) {
		sent := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(sent)
	})

	trace := &traced{}
	c, err := radarr.New(f.srv.URL, "k", client.WithLog(trace))
	if err != nil {
		t.Fatal(err)
	}
	r := &registry{client: c}
	queueTools(r)
	cs := hostRegistry(t, r)

	mustCall(t, cs, "indexer_add", map[string]any{"kind": "Newznab", "name": "New Indexer", "off": true, "settings": map[string]any{"baseUrl": "http://nzb.example", "apiKey": "s3cret-of-indexer"}})
	// and as answered: Radarr masks a key it marks as one, and not every key is marked
	mustCall(t, cs, "indexer_list", nil)

	set := 0
	for _, section := range r.settingsSections() {
		for _, name := range section.names {
			if !secretSetting(name) {
				continue
			}
			set++

			mustCall(t, cs, "server_settings_edit", map[string]any{"section": section.name, "settings": map[string]any{name: "s3cret-of-" + name}, "confirm": true})
		}
	}
	if set < 5 {
		t.Fatalf("%d credentials among Radarr's own settings, want the host's five at least", set)
	}

	text := trace.text()
	if !strings.Contains(text, "POST /api/v3/indexer") || !strings.Contains(text, "GET /api/v3/indexer HTTP") || !strings.Contains(text, "PUT /api/v3/config/host/1") || !strings.Contains(text, "New Indexer") || !strings.Contains(text, "7878") {
		t.Fatalf("the trace does not show what was sent:\n%s", text)
	}
	if at := strings.Index(text, "s3cret-of-"); at >= 0 {
		t.Errorf("the trace carries a credential, %q", text[at:min(at+40, len(text))])
	}
}
