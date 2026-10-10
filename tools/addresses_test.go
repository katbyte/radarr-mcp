package tools

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
)

// what Radarr says of an indexer that answered an error: the request it
// made, with the indexer's stored API key on the end
const indexerDown = "Unable to connect to indexer, indexer's server is unavailable. Try again later. HTTP request failed: [500:InternalServerError] [GET] at [http://nzb.example/api?t=movie&cat=2000,2040&apikey=s3cret]"

func TestAnAddressIsShownWithoutWhatItCarries(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ in, want string }{
		"an indexer that answered an error":        {indexerDown, "Unable to connect to indexer, indexer's server is unavailable. Try again later. HTTP request failed: [500:InternalServerError] [GET] at [http://nzb.example/api?t=REDACTED&cat=REDACTED&apikey=REDACTED]"},
		"who it signs in as":                       {"could not reach https://kt:hunter2@tracker.example:8443/announce today", "could not reach https://REDACTED@tracker.example:8443/announce today"},
		"what follows its #":                       {"see http://host/callback#access_token=abc.", "see http://host/callback#REDACTED."},
		"a parameter with no value keeps its name": {"http://host/x?debug&key=abc", "http://host/x?debug&key=REDACTED"},
		"two addresses":                            {"tried http://a.example/?k=1 then http://b.example/?k=2", "tried http://a.example/?k=REDACTED then http://b.example/?k=REDACTED"},
		"an address with nothing to hide":          {"moved to http://host:7878/radarr/, as Radarr says", "moved to http://host:7878/radarr/, as Radarr says"},
		"no address":                               {"Path: Folder '/nowhere' does not exist", "Path: Folder '/nowhere' does not exist"},
	} {
		if got := addressesHidden(tc.in); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
	}

	// a failure keeps what it was underneath, so what asks what kind it is still gets its answer
	cause := errors.New("refused at http://host/api?apikey=s3cret")
	if shown := failureShown(fmt.Errorf("saving: %w", cause)); strings.Contains(shown.Error(), "s3cret") || !errors.Is(shown, cause) {
		t.Errorf("a failure shown = %q, want its key gone and the failure itself kept", shown)
	}
	if plain := errors.New("no address here"); failureShown(plain) != plain { //nolint:errorlint // the same error, not one like it, is what is checked
		t.Error("a failure with nothing to hide was wrapped")
	}
}

// Radarr quotes an indexer's address back with its stored API key when the
// indexer answers an error. That reaches a caller three ways - a test that
// fails, which is a row and not an error; an add that Radarr's test refuses,
// which is an error; and a setting that is itself an address - and none of
// them shows the key.
func TestWhatRadarrSaysNeverCarriesAKey(t *testing.T) {
	t.Parallel()

	newznab := map[string]any{
		"implementation": "Newznab", "implementationName": "Newznab", "configContract": "NewznabSettings", "protocol": "usenet", "supportsRss": true, "supportsSearch": true, "priority": 25, "tags": []any{},
		"fields": []any{map[string]any{"name": "baseUrl", "type": "textbox"}, map[string]any{"name": "apiKey", "type": "textbox", "privacy": "apiKey"}},
	}
	held := maps.Clone(newznab)
	held["id"], held["name"], held["enableRss"] = 3, "Down", true
	//nolint:gosec // a made-up sign-in and key, for the test to see neither shown
	held["fields"] = []any{map[string]any{"name": "baseUrl", "type": "textbox", "value": "http://kt:hunter2@nzb.example/?passkey=s3cret"}, map[string]any{"name": "apiKey", "type": "textbox", "privacy": "apiKey", "value": "s3cret"}}
	refused := []any{map[string]any{"propertyName": "", "errorMessage": indexerDown}}

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.answer("GET /api/v3/indexer/schema", http.StatusOK, []any{newznab})
	f.answer("GET /api/v3/indexer", http.StatusOK, []any{held})
	f.answer("POST /api/v3/indexer/test", http.StatusBadRequest, refused)
	f.answer("POST /api/v3/indexer", http.StatusBadRequest, refused)
	cs, _ := setupSession(t, f)

	tested := rowsOf(mustCall(t, cs, "indexer_test", map[string]any{"name": "Down"})["results"])
	if len(tested) != 1 || !is(tested[0]["valid"], false) || strings.Contains(fmt.Sprint(tested[0]["failures"]), "s3cret") || !strings.Contains(fmt.Sprint(tested[0]["failures"]), "http://nzb.example/api?t=REDACTED&cat=REDACTED&apikey=REDACTED") {
		t.Errorf("a test that failed = %v, want why, with the address and without its key", tested)
	}

	_, msg := run(t, cs, "indexer_add", map[string]any{"kind": "Newznab", "name": "New", "settings": map[string]any{"baseUrl": "http://nzb.example", "apiKey": "s3cret"}})
	if strings.Contains(msg, "s3cret") || !strings.Contains(msg, "apikey=REDACTED") || !strings.Contains(msg, "force=true") {
		t.Errorf("an add Radarr's test refused = %q, want what Radarr said, without the key, and the way round it", msg)
	}

	listed := rowsOf(mustCall(t, cs, "indexer_list", nil)["indexers"])
	if len(listed) != 1 || object(listed[0]["settings"])["baseUrl"] != "http://REDACTED@nzb.example/?passkey=REDACTED" || strings.Contains(fmt.Sprint(listed[0]), "s3cret") || strings.Contains(fmt.Sprint(listed[0]), "hunter2") {
		t.Errorf("an indexer whose address carries a sign-in and a key is listed as %v", listed)
	}
}
