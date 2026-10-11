package tools

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
)

// what Radarr says of an indexer that answered an error: the request it
// made, with the indexer's stored API key on the end
const indexerDown = "Unable to connect to indexer, indexer's server is unavailable. Try again later. HTTP request failed: [500:InternalServerError] [GET] at [http://nzb.example/api?t=movie&cat=2000,2040&apikey=s3cret]"

// Every name Radarr's words are answered under is one a tool answers under:
// a name that drifts from its field would blank nothing, without a word.
func TestRadarrSaidIsAnswered(t *testing.T) {
	t.Parallel()

	r, err := described(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if unused := r.queued().BlankAddressesUnused(); len(unused) != 0 {
		t.Errorf("radarrSaid names %v, which no tool answers under", unused)
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

// A webhook's own headers are where its token goes, and Radarr marks them as nothing in particular. They are a credential all the same: named when
// set and never shown, in what an add answers, in a list, in the kind's own description, and in the line logged for the add.
func TestAWebhooksHeadersAreNeverShown(t *testing.T) {
	t.Parallel()

	hook := webhookKind()
	fields, ok := hook["fields"].([]any)
	if !ok {
		t.Fatal("the stand-in webhook has no settings")
	}
	hook["fields"] = append(fields, map[string]any{"name": "headers", "label": "Headers", "type": "keyValueList", "advanced": true})

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.answer("GET /api/v3/notification/schema", http.StatusOK, []any{hook})
	notifications := f.keep(t, "/api/v3/notification")
	cs, logged := setupSession(t, f)

	headers := []any{map[string]any{"key": "Authorization", "value": "Bearer s3cret"}}
	added := mustCall(t, cs, "notification_add", map[string]any{"kind": "Webhook", "name": "Hook", "off": true, "settings": map[string]any{"url": "http://hooks.example/in", "headers": headers}})
	if sent := notifications.held(); len(sent) != 1 || !strings.Contains(fmt.Sprint(sent[0]["fields"]), "Bearer s3cret") {
		t.Fatalf("Radarr was sent %v, want the header as it was given", sent)
	}
	if !strings.Contains(fmt.Sprint(added["credentials"]), "headers") {
		t.Errorf("the add = %v, want the headers named among its credentials", added)
	}

	shown := map[string]any{"the add": added, "the list": mustCall(t, cs, "notification_list", nil), "the kind": mustCall(t, cs, "provider_kinds", map[string]any{"provider": "notification", "kind": "Webhook"}), "the write log": logged()}
	for where, answer := range shown {
		if strings.Contains(fmt.Sprint(answer), "s3cret") {
			t.Errorf("%s shows what a header is set to: %v", where, answer)
		}
	}
}
