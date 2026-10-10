package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools that set Radarr up, against a canned Radarr that keeps what it is
// given: each kind of record is added, changed and deleted through the same
// calls a real one takes, and what the tools sent is read back from it.

// kept is a list of records the fake Radarr holds at one path, the way
// Radarr holds indexers, profiles and the rest: listed, added with the next
// id, changed and deleted by id.
type kept struct {
	mu   sync.Mutex
	rows []map[string]any
	next int
}

// keep serves a list of records at path, starting with rows.
func (f *fakeRadarr) keep(t *testing.T, path string, rows ...map[string]any) *kept {
	t.Helper()

	k := &kept{rows: rows, next: 1}
	for _, row := range rows {
		if id, ok := row["id"].(int); ok && id >= k.next {
			k.next = id + 1
		}
	}
	// Radarr answers what it made with 201 and what it changed with 202
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	}
	find := func(id string) int {
		return slices.IndexFunc(k.rows, func(row map[string]any) bool { return fmt.Sprint(row["id"]) == id })
	}
	f.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		write(w, http.StatusOK, k.rows)
	})
	f.mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		row := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		row["id"] = k.next
		k.next++
		k.rows = append(k.rows, row)
		write(w, http.StatusCreated, row)
	})
	f.mux.HandleFunc("PUT "+path+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		i := find(r.PathValue("id"))
		if i < 0 {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		row := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		row["id"] = k.rows[i]["id"]
		k.rows[i] = row
		write(w, http.StatusAccepted, row)
	})
	f.mux.HandleFunc("DELETE "+path+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		i := find(r.PathValue("id"))
		if i < 0 {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		k.rows = slices.Delete(k.rows, i, i+1)
		write(w, http.StatusOK, map[string]any{})
	})

	return k
}

// held is the records the fake holds now.
func (k *kept) held() []map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()

	return slices.Clone(k.rows)
}

// settingsFor are the tags and profiles every setup tool's answer reads.
func (f *fakeRadarr) settingsFor(t *testing.T) {
	t.Helper()

	f.answer("GET /api/v3/qualityprofile", http.StatusOK, []any{fakeProfile})
	f.answer("GET /api/v3/rootfolder", http.StatusOK, []any{})
	f.keep(t, "/api/v3/tag", map[string]any{"id": 1, "label": "4k"})
}

// setupSession is a session with every tool, deletes and all, whose write
// log is kept for the test to read.
func setupSession(t *testing.T, f *fakeRadarr) (cs *mcp.ClientSession, logged func() []string) {
	t.Helper()

	c, err := radarr.New(f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	r := &registry{client: c, writeLog: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}}
	queueTools(r)

	return hostRegistry(t, r), func() []string {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(lines)
	}
}

// webhookKind is the Webhook notification as Radarr describes the kind.
func webhookKind() map[string]any {
	return map[string]any{
		"implementation": "Webhook", "implementationName": "Webhook", "configContract": "WebhookSettings", "onGrab": false, "onDownload": false, "supportsOnGrab": true, "tags": []any{},
		"fields": []any{
			map[string]any{"name": "url", "label": "URL", "type": "url"},
			map[string]any{"name": "method", "label": "Method", "type": "select", "value": 1, "selectOptions": []any{map[string]any{"value": 1, "name": "POST"}, map[string]any{"value": 2, "name": "PUT"}}},
			map[string]any{"name": "password", "label": "Password", "type": "password", "privacy": "password"},
			map[string]any{"name": "username", "label": "Username", "type": "textbox", "privacy": "userName"},
			map[string]any{"name": "devices", "label": "Devices", "type": "select", "selectOptionsProviderAction": "getDevices"},
		},
		"presets": []any{map[string]any{"name": "a preset"}},
	}
}

// A provider is added from the kind Radarr describes, with its settings and
// switches by name; it is shown without its credentials; it is changed and
// deleted by name; and the line logged for each write carries the names of
// what was set and none of the values.
func TestAProviderIsAddedChangedAndDeleted(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.answer("GET /api/v3/notification/schema", http.StatusOK, []any{webhookKind()})
	notifications := f.keep(t, "/api/v3/notification")
	cs, logged := setupSession(t, f)

	kinds := mustCall(t, cs, "provider_kinds", map[string]any{"provider": "notification"})
	if fmt.Sprint(kinds["kinds"]) != "[Webhook]" || !strings.Contains(fmt.Sprint(kinds["options"]), "onGrab") {
		t.Errorf("the kinds of notification = %v", kinds)
	}
	one := mustCall(t, cs, "provider_kinds", map[string]any{"provider": "notification", "kind": "webhook"})
	if settings := rowsOf(one["settings"]); len(settings) != 5 || settings[2]["name"] != "password" || !is(settings[2]["secret"], true) || settings[3]["secret"] != nil || fmt.Sprint(settings[1]["options"]) != "[POST PUT]" || settings[4]["options_from"] != "getDevices" {
		t.Errorf("what a Webhook takes = %v, want the password a credential, the user name not, and where the devices come from", one["settings"])
	}
	mustFail(t, cs, "provider_kinds", map[string]any{"provider": "proxy"}, "no provider")

	// left alone, it starts sent for what its kind can tell of, as in Radarr's own form, where its API describes it sent for nothing
	added := mustCall(t, cs, "notification_add", map[string]any{"kind": "Webhook", "name": "Hook", "settings": map[string]any{"url": "http://hooks.example/x", "method": "PUT", "password": "hunter2", "username": "kt"}, "tags": []any{"4k", "new"}})
	if options := object(added["options"]); added["name"] != "Hook" || added["kind"] != "Webhook" || !is(options["onGrab"], true) || !is(options["onDownload"], false) || !is(options["onHealthIssue"], false) || fmt.Sprint(added["credentials"]) != "[password]" || fmt.Sprint(added["tags"]) != "[4k new]" {
		t.Errorf("the notification added = %v, want it sent on a grab, which its kind supports, and for nothing else", added)
	}
	if settings := object(added["settings"]); settings["url"] != "http://hooks.example/x" || settings["method"] != 2.0 || settings["password"] != nil || settings["username"] != "kt" {
		t.Errorf("its settings as shown = %v, want the url, the method as its number, who it signs in as, and no password", added["settings"])
	}
	// off adds it switched off, whatever its kind supports, which Radarr saves untested
	quiet := mustCall(t, cs, "notification_add", map[string]any{"kind": "Webhook", "name": "Quiet", "off": true})
	if is(object(quiet["options"])["onGrab"], true) || !is(notifications.held()[1]["onGrab"], false) {
		t.Errorf("a notification added off = %v, sent as %v", quiet, notifications.held()[1])
	}
	mustCall(t, cs, "notification_delete", map[string]any{"name": "Quiet", "confirm": true})
	// force saves one whose test would fail. Radarr refuses that on an add whatever it is told, so it is added switched off, which Radarr does not test, and switched on with a change, which Radarr saves untested when told to
	forced := mustCall(t, cs, "notification_add", map[string]any{"kind": "Webhook", "name": "Forced", "force": true, "settings": map[string]any{"password": "hunter3"}})
	posts, puts := f.requests(http.MethodPost, "/api/v3/notification"), f.requests(http.MethodPut, "/api/v3/notification/"+fmt.Sprint(forced["id"]))
	if !is(object(forced["options"])["onGrab"], true) || !strings.Contains(posts[len(posts)-1].Body, `"onGrab":false`) || len(puts) != 1 || !strings.Contains(puts[0].Body, `"onGrab":true`) || !strings.Contains(puts[0].Body, "hunter3") || !strings.Contains(puts[0].Query, "forceSave=true") {
		t.Errorf("a forced add = %v, added as %v and then changed with %v", forced, posts[len(posts)-1], puts)
	}
	mustCall(t, cs, "notification_delete", map[string]any{"name": "Forced", "confirm": true})
	sent := notifications.held()[0]
	if sent["presets"] != nil || !strings.Contains(fmt.Sprint(sent["fields"]), "hunter2") {
		t.Errorf("what Radarr was sent = %v, want the password in its field and no presets", sent)
	}
	mustFail(t, cs, "notification_add", map[string]any{"kind": "Carrier Pigeon"}, "no notification kind")
	mustFail(t, cs, "notification_add", map[string]any{"kind": "Webhook", "settings": map[string]any{"uri": "x"}}, `no setting "uri" (have: url, method, password, username, devices)`)
	mustFail(t, cs, "notification_add", map[string]any{"kind": "Webhook", "options": map[string]any{"onFire": true}}, `no option "onFire"`)

	listed := rowsOf(mustCall(t, cs, "notification_list", nil)["items"])
	if len(listed) != 1 || listed[0]["name"] != "Hook" {
		t.Fatalf("the notifications listed = %v", listed)
	}

	edited := mustCall(t, cs, "notification_edit", map[string]any{"name": "hook", "new_name": "Hook 2", "settings": map[string]any{"url": "http://hooks.example/y"}, "options": map[string]any{"onGrab": false, "onDownload": true}, "remove_tags": []any{"new"}})
	if edited["name"] != "Hook 2" || object(edited["settings"])["url"] != "http://hooks.example/y" || !is(object(edited["options"])["onDownload"], true) || fmt.Sprint(edited["tags"]) != "[4k]" {
		t.Errorf("the notification edited = %v", edited)
	}
	if kept := fmt.Sprint(notifications.held()[0]["fields"]); !strings.Contains(kept, "hunter2") {
		t.Errorf("an edit that did not name the password lost it: %s", kept)
	}
	mustFail(t, cs, "notification_edit", map[string]any{"name": "Hook 2"}, "nothing to change")
	mustFail(t, cs, "notification_edit", map[string]any{"name": "nope", "new_name": "x"}, `no notification "nope" (have: Hook 2)`)

	would := mustCall(t, cs, "notification_delete", map[string]any{"name": "Hook 2"})
	if would["would_delete"] != "Hook 2" || would["deleted"] != nil || len(notifications.held()) != 1 {
		t.Errorf("a delete that was not confirmed = %v, with %d held", would, len(notifications.held()))
	}
	if gone := mustCall(t, cs, "notification_delete", map[string]any{"name": "Hook 2", "confirm": true}); gone["deleted"] != "Hook 2" || len(notifications.held()) != 0 {
		t.Errorf("the delete = %v, with %d held", gone, len(notifications.held()))
	}

	lines := strings.Join(logged(), "\n")
	if strings.Contains(lines, "hunter2") || strings.Contains(lines, "hunter3") || strings.Contains(lines, "hooks.example") {
		t.Errorf("the write log carries what a setting was set to:\n%s", lines)
	}
	if !strings.Contains(lines, "notification_add answered") || !strings.Contains(lines, `"password":"REDACTED"`) || !strings.Contains(lines, `"onDownload":true`) {
		t.Errorf("the write log does not say what was set, by name:\n%s", lines)
	}
}

// A test of one provider that fails is the answer, whichever way Radarr
// refuses it: a 400 with what is wrong from most kinds, a 500 from a
// notification that could not be sent.
func TestAFailedProviderTestIsARow(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	hook := webhookKind()
	hook["id"], hook["name"] = 5, "Hook"
	f.keep(t, "/api/v3/notification", hook)
	f.answer("POST /api/v3/notification/test", http.StatusInternalServerError, map[string]any{"message": "Unable to post to webhook: connection refused"})
	f.answer("POST /api/v3/notification/testall", http.StatusBadRequest, []any{map[string]any{"id": 5, "isValid": false, "validationFailures": []any{map[string]any{"errorMessage": "connection refused"}}}})
	cs, _ := setupSession(t, f)

	one := rowsOf(mustCall(t, cs, "notification_test", map[string]any{"name": "Hook"})["results"])
	if len(one) != 1 || one[0]["name"] != "Hook" || !is(one[0]["valid"], false) || !strings.Contains(fmt.Sprint(one[0]["failures"]), "connection refused") {
		t.Errorf("a test that could not be sent = %v", one)
	}
	all := rowsOf(mustCall(t, cs, "notification_test", nil)["results"])
	if len(all) != 1 || !is(all[0]["valid"], false) || fmt.Sprint(all[0]["failures"]) != "[connection refused]" {
		t.Errorf("a test of every one = %v", all)
	}
}

// Radarr's test of every provider fails whole when one cannot be reached at
// all: each one that is switched on is then tested by itself, so the others
// still get their answer, and one that is off is left out as Radarr leaves it.
func TestATestOfEveryProviderSurvivesOneThatCannotBeReached(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	kind := func(id int, name string, onGrab bool) map[string]any {
		hook := webhookKind()
		hook["id"], hook["name"], hook["onGrab"] = id, name, onGrab
		return hook
	}
	f.keep(t, "/api/v3/notification", kind(5, "Down", true), kind(6, "Off", false), kind(7, "Up", true))
	f.answer("POST /api/v3/notification/testall", http.StatusInternalServerError, map[string]any{"message": "Connection refused (127.0.0.1:9)"})
	f.mux.HandleFunc("POST /api/v3/notification/test", func(w http.ResponseWriter, r *http.Request) {
		sent := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		if sent["name"] == "Down" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Connection refused (127.0.0.1:9)"})
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	cs, _ := setupSession(t, f)

	all := rowsOf(mustCall(t, cs, "notification_test", nil)["results"])
	if len(all) != 2 || all[0]["name"] != "Down" || !is(all[0]["valid"], false) || !strings.Contains(fmt.Sprint(all[0]["failures"]), "Connection refused") || all[1]["name"] != "Up" || !is(all[1]["valid"], true) {
		t.Errorf("a test of every one, one of them unreachable = %v, want the two that are on, each with its own answer", all)
	}
}

// What a setting can be set to is asked of the service the provider talks
// to, with the settings given, for one Radarr has or a kind not added yet.
func TestProviderOptions(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.answer("GET /api/v3/notification/schema", http.StatusOK, []any{webhookKind()})
	hook := webhookKind()
	hook["id"], hook["name"] = 5, "Hook"
	f.keep(t, "/api/v3/notification", hook)
	f.answer("POST /api/v3/notification/action/getDevices", http.StatusOK, map[string]any{"options": []any{map[string]any{"value": "abc", "name": "Pho\u200bne", "hint": "android"}, map[string]any{"value": 2, "name": "Tablet"}}})
	cs, _ := setupSession(t, f)

	fresh := mustCall(t, cs, "provider_options", map[string]any{"provider": "notification", "kind": "Webhook", "setting": "devices", "settings": map[string]any{"url": "http://hooks.example/x"}})
	if options := rowsOf(fresh["options"]); fresh["setting"] != "devices" || len(options) != 2 || options[0]["value"] != "abc" || options[0]["name"] != "Phone" || options[0]["hint"] != "android" || options[1]["value"] != 2.0 {
		t.Errorf("the choices for a kind not added yet = %v", fresh)
	}
	asked := f.requests(http.MethodPost, "/api/v3/notification/action/getDevices")
	if len(asked) != 1 || !strings.Contains(asked[0].Body, "http://hooks.example/x") || !strings.Contains(asked[0].Body, `"implementation":"Webhook"`) {
		t.Errorf("Radarr was asked with %v, want the kind and the settings given", asked)
	}
	if held := mustCall(t, cs, "provider_options", map[string]any{"provider": "Notification", "name": "hook", "setting": "Devices"}); len(rowsOf(held["options"])) != 2 || !strings.Contains(f.requests(http.MethodPost, "/api/v3/notification/action/getDevices")[1].Body, `"id":5`) {
		t.Errorf("the choices for one Radarr has = %v, want it asked as the one it holds", held)
	}

	mustFail(t, cs, "provider_options", map[string]any{"provider": "notification", "kind": "Webhook", "setting": "method"}, "the choices of method are fixed")
	mustFail(t, cs, "provider_options", map[string]any{"provider": "notification", "kind": "Webhook", "setting": "url"}, `"url" is not a setting whose choices come from the service (those are: devices)`)
	mustFail(t, cs, "provider_options", map[string]any{"provider": "notification", "setting": "devices"}, "name the notification to ask for, or the kind")
	mustFail(t, cs, "provider_options", map[string]any{"provider": "pigeons", "kind": "Webhook", "setting": "devices"}, "no provider")
}

// A setting is a credential by its name as well as by Radarr's mark, which
// not every one that holds a credential carries.
func TestASettingIsACredentialByItsName(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{"apiKey": true, "rssPasskey": true, "userKey": true, "accessTokenSecret": true, "botToken": true, "Password": true, "passwordConfirmation": true, "cookie": true, "webHookUrl": true, "passphrase": true, "baseUrl": false, "username": false, "categories": false, "host": false, "keywords": false} {
		if got := secret(&radarr.Field{Name: name}); got != want {
			t.Errorf("%s is a credential: %v, want %v", name, got, want)
		}
	}
	// and by Radarr's mark whatever it is called, but for a user name
	if !secret(&radarr.Field{Name: "x", Privacy: radarr.PrivacyLevelApiKey}) || !secret(&radarr.Field{Name: "x", Type: "password"}) || secret(&radarr.Field{Name: "x", Privacy: radarr.PrivacyLevelUserName}) {
		t.Error("a setting Radarr marks as a key or a password is a credential, and one it marks as a user name is not")
	}
}

// A test that only warns is of a provider Radarr would still save: its row is
// valid, and carries the warning.
func TestAProviderTestThatOnlyWarnsIsValid(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.keep(t, "/api/v3/importlist", map[string]any{"id": 4, "name": "Watchlist", "implementation": "TMDbListImport", "fields": []any{}, "tags": []any{}})
	f.answer("POST /api/v3/importlist/test", http.StatusBadRequest, []any{map[string]any{"errorMessage": "the list is empty", "isWarning": true}})
	cs, _ := setupSession(t, f)

	one := rowsOf(mustCall(t, cs, "importlist_test", map[string]any{"name": "Watchlist"})["results"])
	if len(one) != 1 || one[0]["name"] != "Watchlist" || !is(one[0]["valid"], true) || fmt.Sprint(one[0]["failures"]) != "[warning: the list is empty]" {
		t.Errorf("a test that only warned = %v, want it valid with the warning", one)
	}
}

// The films the import lists offer are shown with the lists they are on by
// name; what Radarr is asked to add to them is what was asked for; and
// new_only leaves the ones the library does not hold and nothing keeps out.
func TestImportListMovies(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	f.keep(t, "/api/v3/importlist", map[string]any{"id": 4, "name": "Watchlist", "implementation": "TMDbListImport", "fields": []any{}, "tags": []any{}})
	f.answer("GET /api/v3/importlist/movie", http.StatusOK, []any{
		map[string]any{"title": "Solaris", "year": 1972, "tmdbId": 593, "imdbId": "tt0069293", "isExisting": false, "isExcluded": false, "lists": []any{4}},
		map[string]any{"title": "Alien", "year": 1979, "tmdbId": 348, "isExisting": true, "lists": []any{4}},
		map[string]any{"title": "Cats", "year": 2019, "tmdbId": 536869, "isExcluded": true, "isTrending": true, "isPopular": true, "isRecommendation": true, "lists": []any{}},
	})
	cs, _ := setupSession(t, f)

	all := mustCall(t, cs, "importlist_movies", map[string]any{"trending": true, "popular": true})
	rows := rowsOf(all["movies"])
	if len(rows) != 3 || all["total"] != 3.0 || rows[0]["title"] != "Alien" || !is(rows[0]["in_library"], true) || fmt.Sprint(rows[0]["lists"]) != "[Watchlist]" {
		t.Fatalf("what the lists offer = %v, want three films by title, each with its lists by name", all)
	}
	if cats := rows[1]; !is(cats["excluded"], true) || !is(cats["trending"], true) || !is(cats["popular"], true) || !is(cats["recommendation"], true) || fmt.Sprint(cats["lists"]) != "[]" {
		t.Errorf("a film TMDB offers and an exclusion keeps out = %v", cats)
	}
	if asked := f.requests(http.MethodGet, "/api/v3/importlist/movie"); len(asked) != 1 || !strings.Contains(asked[0].Query, "includeTrending=true") || !strings.Contains(asked[0].Query, "includePopular=true") || !strings.Contains(asked[0].Query, "includeRecommendations=false") {
		t.Errorf("Radarr was asked %v, want the trending and the popular films and not the recommendations", asked)
	}

	fresh := rowsOf(mustCall(t, cs, "importlist_movies", map[string]any{"new_only": true})["movies"])
	if len(fresh) != 1 || fresh[0]["title"] != "Solaris" || fresh[0]["tmdb_id"] != 593.0 || fresh[0]["imdb_id"] != "tt0069293" {
		t.Errorf("what the lists would add = %v, want Solaris alone", fresh)
	}
}

// A record is added by its values as Radarr names them, with its tags by
// label; a name it does not have is refused with the ones it does; the
// default delay profile cannot be deleted; and a custom format is made of
// conditions built from the kinds Radarr describes.
func TestARecordIsAddedChangedAndDeleted(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.settingsFor(t)
	delays := f.keep(t, "/api/v3/delayprofile", map[string]any{"id": 1, "enableUsenet": true, "enableTorrent": true, "preferredProtocol": "usenet", "tags": []any{}})
	formats := f.keep(t, "/api/v3/customformat")
	f.answer("GET /api/v3/customformat/schema", http.StatusOK, []any{map[string]any{
		"implementation": "ReleaseTitleSpecification", "implementationName": "Release Title", "infoLink": "https://wiki.example",
		"fields": []any{map[string]any{"name": "value", "label": "Regular Expression", "type": "textbox"}},
	}})
	cs, _ := setupSession(t, f)

	added := object(mustCall(t, cs, "delayprofile_add", map[string]any{"values": map[string]any{"usenetDelay": 60, "preferredProtocol": "usenet", "enableUsenet": true}, "tags": []any{"4k"}})["saved"])
	if added["id"] != 2.0 || added["usenetDelay"] != 60.0 || fmt.Sprint(added["tags"]) != "[4k]" {
		t.Errorf("the delay profile added = %v", added)
	}
	if sent := delays.held()[1]; fmt.Sprint(sent["tags"]) != "[1]" {
		t.Errorf("Radarr was sent the tags as %v, want the tag's id", sent["tags"])
	}
	mustFail(t, cs, "delayprofile_add", map[string]any{"values": map[string]any{"usenetDelayMinutes": 60}}, `no value "usenetDelayMinutes" (it has:`)
	mustFail(t, cs, "delayprofile_add", map[string]any{"values": map[string]any{"usenetDelay": "an hour"}}, "the delay profile:")
	mustFail(t, cs, "delayprofile_add", map[string]any{"values": map[string]any{"tags": []any{1}}}, "give tags with the tags argument")

	edited := object(mustCall(t, cs, "delayprofile_edit", map[string]any{"id": "2", "values": map[string]any{"usenetDelay": 120}})["saved"])
	if edited["usenetDelay"] != 120.0 || fmt.Sprint(edited["tags"]) != "[4k]" || edited["preferredProtocol"] != "usenet" {
		t.Errorf("the delay profile edited = %v, want the delay changed and the rest kept", edited)
	}
	if listed := rowsOf(mustCall(t, cs, "delayprofile_list", nil)["items"]); len(listed) != 2 {
		t.Errorf("the delay profiles listed = %v", listed)
	}

	mustFail(t, cs, "delayprofile_delete", map[string]any{"id": "1", "confirm": true}, "delay profile 1 is the default")
	if would := mustCall(t, cs, "delayprofile_delete", map[string]any{"id": "2"}); would["would_delete"] != "delay profile 2" || len(delays.held()) != 2 {
		t.Errorf("a delete that was not confirmed = %v", would)
	}
	if gone := mustCall(t, cs, "delayprofile_delete", map[string]any{"id": "2", "confirm": true}); gone["deleted"] != "delay profile 2" || len(delays.held()) != 1 {
		t.Errorf("the delete = %v", gone)
	}

	kinds := mustCall(t, cs, "provider_kinds", map[string]any{"provider": "customformat"})
	if fmt.Sprint(kinds["kinds"]) != "[ReleaseTitleSpecification]" {
		t.Errorf("the kinds of condition = %v", kinds)
	}
	format := object(mustCall(t, cs, "customformat_add", map[string]any{"values": map[string]any{"name": "x265"}, "conditions": []any{map[string]any{"kind": "ReleaseTitleSpecification", "name": "x265", "required": true, "settings": map[string]any{"value": `\bx265\b`}}}})["saved"])
	if conditions := rowsOf(format["conditions"]); format["name"] != "x265" || len(conditions) != 1 || conditions[0]["kind"] != "ReleaseTitleSpecification" || !is(conditions[0]["required"], true) || object(conditions[0]["settings"])["value"] != `\bx265\b` {
		t.Errorf("the custom format added = %v", format)
	}
	if sent := fmt.Sprint(formats.held()[0]["specifications"]); !strings.Contains(sent, `\bx265\b`) || strings.Contains(sent, "wiki.example") {
		t.Errorf("Radarr was sent the conditions as %s", sent)
	}
	mustFail(t, cs, "customformat_add", map[string]any{"values": map[string]any{"name": "y"}, "conditions": []any{map[string]any{"kind": "Smell"}}}, "no kind of condition")
	mustFail(t, cs, "delayprofile_add", map[string]any{"values": map[string]any{"usenetDelay": 1}, "conditions": []any{map[string]any{"kind": "x"}}}, "delayprofile_add takes values and tags")
	if gone := mustCall(t, cs, "customformat_delete", map[string]any{"id": "x265", "confirm": true}); gone["deleted"] != "x265" {
		t.Errorf("the custom format deleted by name = %v", gone)
	}
}

// Radarr's own settings are shown without their credentials, changed by
// name, and read back: one that decides how Radarr is reached waits for
// confirm, and one Radarr took and did not keep is said to be not kept.
func TestServerSettings(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	host := map[string]any{"id": 1, "port": 7878, "apiKey": "secret-key", "logLevel": "info", "backupRetention": 28, "username": ""}
	var mu sync.Mutex
	f.mux.HandleFunc("GET /api/v3/config/host", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(host)
	})
	f.mux.HandleFunc("PUT /api/v3/config/host/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		sent := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		// a user name is taken and not kept, as Radarr does while nothing asks anyone to sign in
		sent["username"], sent["id"] = "", 1
		host = sent
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(host)
	})
	cs, logged := setupSession(t, f)

	shown := rowsOf(mustCall(t, cs, "server_settings", map[string]any{"section": "host"})["sections"])
	if len(shown) != 1 || object(shown[0]["settings"])["apiKey"] != nil || object(shown[0]["settings"])["logLevel"] != "info" || fmt.Sprint(shown[0]["credentials"]) != "[apiKey]" {
		t.Errorf("the host settings as shown = %v", shown)
	}
	mustFail(t, cs, "server_settings", map[string]any{"section": "plumbing"}, "no settings section")

	edited := mustCall(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"loglevel": "debug", "backupRetention": 7, "username": "kt"}, "confirm": true})
	if changed, lost := rowsOf(edited["changed"]), rowsOf(edited["not_kept"]); len(changed) != 2 || changed[0]["setting"] != "backupRetention" || changed[0]["to"] != 7.0 || len(lost) != 1 || lost[0]["setting"] != "username" {
		t.Errorf("the edit = changed %v, not kept %v", edited["changed"], edited["not_kept"])
	}
	mustFail(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"logLevle": "debug"}}, `no setting "logLevle" (have:`)
	mustFail(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"logLevel": "debug"}}, "already has that value")

	would := mustCall(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"port": 9000, "apiKey": "new-key"}})
	if w := rowsOf(would["would_change"]); len(w) != 2 || w[0]["setting"] != "apiKey" || w[0]["to"] != "(not shown)" || w[1]["to"] != 9000.0 || would["changed"] != nil && len(rowsOf(would["changed"])) != 0 {
		t.Errorf("a change to how Radarr is reached, not confirmed = %v", would)
	}
	mu.Lock()
	port := host["port"]
	mu.Unlock()
	if fmt.Sprint(port) != "7878" {
		t.Errorf("the port was changed to %v without confirm", port)
	}
	done := mustCall(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"port": 9000}, "confirm": true})
	if changed := rowsOf(done["changed"]); len(changed) != 1 || changed[0]["to"] != 9000.0 || !strings.Contains(fmt.Sprint(done["note"]), "restarted") {
		t.Errorf("the confirmed change = %v", done)
	}

	// a setting that is off or at 0 is shown, and one set to 0 is a change Radarr kept: the client leaves a value at rest out of what it reads
	if settings := object(done["settings"]); !is(settings["enableSsl"], false) || settings["backupInterval"] != 0.0 {
		t.Errorf("the settings shown = %v, want the ones that are off and at 0 among them", settings)
	}
	zeroed := mustCall(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"backupRetention": 0}})
	if changed := rowsOf(zeroed["changed"]); len(changed) != 1 || changed[0]["from"] != 7.0 || changed[0]["to"] != 0.0 || zeroed["not_kept"] != nil {
		t.Errorf("a setting set to 0 = %v, want it changed and kept", zeroed)
	}
	mustFail(t, cs, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"backupRetention": 0, "enableSsl": false}}, "already has that value")

	lines := strings.Join(logged(), "\n")
	if strings.Contains(lines, "new-key") || !strings.Contains(lines, `"port":9000`) || !strings.Contains(lines, `"apiKey":"REDACTED"`) {
		t.Errorf("the write log of Radarr's own settings shows a credential, or hides what a port was changed to:\n%s", lines)
	}
}

// A quality profile is added as a copy of another, changed by the names of
// its qualities and custom formats, and deleted only when no film is on it.
func TestQualityProfileAddAndDelete(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Alien", 1979, "", "", ""))
	profile := map[string]any{}
	maps.Copy(profile, fakeProfile)
	profile["formatItems"] = []any{map[string]any{"format": 3, "name": "x265", "score": 0}}
	profiles := f.keep(t, "/api/v3/qualityprofile2", profile)
	f.mux.HandleFunc("POST /api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		row := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&row)
		row["id"] = 9
		profiles.mu.Lock()
		profiles.rows = append(profiles.rows, row)
		profiles.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(row)
	})
	f.mux.HandleFunc("DELETE /api/v3/qualityprofile/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.answer("GET /api/v3/qualityprofile/schema", http.StatusOK, profile)
	cs, _ := setupSession(t, f)

	mustFail(t, cs, "qualityprofile_add", map[string]any{"name": "HD-1080p"}, "there is already a quality profile")
	made := mustCall(t, cs, "qualityprofile_add", map[string]any{"name": "Lean", "copy_of": "HD-1080p", "disallow": []any{"HDTV-720p"}, "upgrade_allowed": true})
	if made["name"] != "Lean" || !is(made["upgrade_allowed"], true) || strings.Contains(fmt.Sprint(made["allowed"]), "HDTV-720p") || !strings.Contains(fmt.Sprint(made["allowed"]), "Bluray-1080p") {
		t.Errorf("the profile added = %v", made)
	}
	if sent := profiles.held()[1]; sent["name"] != "Lean" || fmt.Sprint(sent["id"]) != "9" {
		t.Errorf("Radarr was sent %v", sent)
	}
	mustFail(t, cs, "qualityprofile_add", map[string]any{"name": "Odd", "copy_of": "HD-1080p", "allow": []any{"Betamax"}}, `no quality "Betamax"`)
	mustFail(t, cs, "qualityprofile_add", map[string]any{"name": "Odd", "copy_of": "HD-1080p", "format_scores": map[string]any{"HDR": 5}}, `no custom format "HDR"`)

	mustFail(t, cs, "qualityprofile_delete", map[string]any{"profile": "HD-1080p", "confirm": true}, "1 film is on HD-1080p")
}

// Putting a backup back, a restart and a shutdown each change nothing until
// confirmed, and say what they would do; a command that has finished is not
// stopped; and the folders Radarr can reach are listed with their paths as
// Radarr gives them.
func TestRunningRadarr(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.answer("GET /api/v3/system/backup", http.StatusOK, []any{map[string]any{"id": 3, "name": "radarr_backup_2026.zip", "type": "manual", "time": "2026-01-02T00:00:00Z", "size": 1024}})
	f.answer("GET /api/v3/command", http.StatusOK, []any{map[string]any{"id": 8, "name": "MoviesSearch", "status": "started"}, map[string]any{"id": 7, "name": "RssSync", "status": "completed"}})
	f.answer("GET /api/v3/command/8", http.StatusOK, map[string]any{"id": 8, "name": "MoviesSearch", "status": "started"})
	f.answer("GET /api/v3/command/7", http.StatusOK, map[string]any{"id": 7, "name": "RssSync", "status": "completed"})
	f.answer("GET /api/v3/command/9", http.StatusOK, map[string]any{"id": 9, "name": "MissingMoviesSearch", "status": "queued"})
	f.answer("GET /api/v3/command/10", http.StatusOK, map[string]any{"id": 10, "name": "RefreshMovie", "status": "queued"})
	// a command that started between being read and being asked for is one Radarr answers it cannot cancel
	f.answer("DELETE /api/v3/command/10", http.StatusConflict, map[string]any{"message": "Unable to cancel task"})
	f.answer("GET /api/v3/system/status", http.StatusOK, map[string]any{"version": "6.4.4.10685", "packageUpdateMechanism": "docker"})
	f.answer("GET /api/v3/update", http.StatusOK, []any{map[string]any{"version": "6.5.0.1", "releaseDate": "2026-02-01T00:00:00Z", "latest": true, "installed": false, "changes": map[string]any{"new": []any{"A new\u200b thing"}, "fixed": []any{}}}})
	f.answer("GET /api/v3/filesystem", http.StatusOK, map[string]any{"parent": "/", "directories": []any{map[string]any{"type": "folder", "name": "mov\u200bies", "path": "/media/mov\u200bies"}}, "files": []any{map[string]any{"type": "file", "name": "a.mkv", "path": "/media/a.mkv", "size": 5}}})
	for _, route := range []string{"POST /api/v3/system/restart", "POST /api/v3/system/shutdown", "POST /api/v3/system/backup/restore/{id}", "DELETE /api/v3/system/backup/{id}", "DELETE /api/v3/command/{id}"} {
		f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	cs, _ := setupSession(t, f)
	posts := func(path string) int { return len(f.requests(http.MethodPost, path)) }

	would := mustCall(t, cs, "server_backup_restore", map[string]any{"backup": "3"})
	if would["would_restore"] != "radarr_backup_2026.zip" || would["restored"] != nil || len(rowsOfStrings(would["overwrites"])) != 3 || posts("/api/v3/system/backup/restore/3") != 0 {
		t.Errorf("a restore that was not confirmed = %v", would)
	}
	if done := mustCall(t, cs, "server_backup_restore", map[string]any{"backup": "radarr_backup_2026.zip", "confirm": true}); done["restored"] != "radarr_backup_2026.zip" || posts("/api/v3/system/backup/restore/3") != 1 {
		t.Errorf("the restore = %v", done)
	}
	mustFail(t, cs, "server_backup_restore", map[string]any{"backup": "last week"}, `no backup "last week" (have: radarr_backup_2026.zip)`)
	if gone := mustCall(t, cs, "server_backup_delete", map[string]any{"backup": "3", "confirm": true}); gone["deleted"] != "radarr_backup_2026.zip" {
		t.Errorf("the backup deleted = %v", gone)
	}

	for tool, path := range map[string]string{"server_restart": "/api/v3/system/restart", "server_shutdown": "/api/v3/system/shutdown"} {
		asked := mustCall(t, cs, tool, nil)
		if asked["would"] == nil || asked["done"] != nil || fmt.Sprint(asked["interrupts"]) != "[MoviesSearch (started)]" || posts(path) != 0 {
			t.Errorf("%s without confirm = %v", tool, asked)
		}
		if done := mustCall(t, cs, tool, map[string]any{"confirm": true}); done["done"] == nil || posts(path) != 1 {
			t.Errorf("%s with confirm = %v", tool, done)
		}
	}

	updates := mustCall(t, cs, "server_updates", nil)
	if rows := rowsOf(updates["updates"]); !is(updates["update_available"], true) || updates["mechanism"] != "docker" || len(rows) != 1 || fmt.Sprint(rows[0]["new"]) != "[A new thing]" {
		t.Errorf("the updates = %v", updates)
	}

	if waiting := mustCall(t, cs, "task_cancel", map[string]any{"command_id": 9}); !is(waiting["cancelled"], true) || waiting["note"] != nil || len(f.requests(http.MethodDelete, "/api/v3/command/9")) != 1 {
		t.Errorf("a waiting command cancelled = %v", waiting)
	}
	if running := mustCall(t, cs, "task_cancel", map[string]any{"command_id": 8}); !is(running["cancelled"], false) || !strings.Contains(fmt.Sprint(running["note"]), "already running") || len(f.requests(http.MethodDelete, "/api/v3/command/8")) != 0 {
		t.Errorf("a running command = %v, want it left to run and Radarr not asked", running)
	}
	if started := mustCall(t, cs, "task_cancel", map[string]any{"command_id": 10}); !is(started["cancelled"], false) || !strings.Contains(fmt.Sprint(started["note"]), "already running") || len(f.requests(http.MethodDelete, "/api/v3/command/10")) != 1 {
		t.Errorf("a command that started as it was asked for = %v", started)
	}
	if left := mustCall(t, cs, "task_cancel", map[string]any{"command_id": 7}); !is(left["cancelled"], false) || !strings.Contains(fmt.Sprint(left["note"]), "already finished") || len(f.requests(http.MethodDelete, "/api/v3/command/7")) != 0 {
		t.Errorf("a finished command = %v", left)
	}

	folder := mustCall(t, cs, "filesystem_browse", map[string]any{"path": "/media", "files": true})
	if entries := rowsOf(folder["entries"]); folder["parent"] != "/" || len(entries) != 2 || entries[0]["name"] != "movies" || entries[0]["path"] != "/media/mov\u200bies" || entries[1]["size"] != 5.0 {
		t.Errorf("the folder = %v, want the name made safe to show and the path as Radarr gave it", folder)
	}
}

// A release held back in the queue is grabbed by its id, and one that is
// already downloading is refused; a release pushed from elsewhere is answered
// with what Radarr made of it.
func TestQueueGrabAndReleasePush(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	f.library(movie(1, "Moon", 2009, "", "", ""))
	f.answer("GET /api/v3/queue", http.StatusOK, map[string]any{"totalRecords": 2, "records": []any{
		map[string]any{"id": 11, "title": "Moon.2009.1080p", "movieId": 1, "status": "delay"},
		map[string]any{"id": 12, "title": "Moon.2009.720p", "movieId": 1, "status": "downloading"},
	}})
	f.mux.HandleFunc("POST /api/v3/queue/grab/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.mux.HandleFunc("POST /api/v3/release/push", func(w http.ResponseWriter, r *http.Request) {
		sent := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		sent["approved"], sent["rejected"], sent["rejections"] = false, true, []any{"Existing file meets cutoff"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{sent})
	})
	cs, _ := setupSession(t, f)

	grabbed := rowsOf(mustCall(t, cs, "queue_grab", map[string]any{"ids": []any{11}})["grabbed"])
	if len(grabbed) != 1 || grabbed[0]["title"] != "Moon.2009.1080p" || len(f.requests(http.MethodPost, "/api/v3/queue/grab/11")) != 1 {
		t.Errorf("the release grabbed = %v", grabbed)
	}
	mustFail(t, cs, "queue_grab", map[string]any{"ids": []any{12}}, "queue item 12 is downloading, not held back")
	mustFail(t, cs, "queue_grab", map[string]any{"ids": []any{99}}, "no queue item 99")

	pushed := rowsOf(mustCall(t, cs, "release_push", map[string]any{"title": "Moon.2009.1080p.BluRay-GRP", "download_url": "magnet:?xt=urn:btih:abc", "protocol": "Torrent"})["releases"])
	if len(pushed) != 1 || !is(pushed[0]["approved"], false) || fmt.Sprint(pushed[0]["rejections"]) != "[Existing file meets cutoff]" {
		t.Errorf("the release pushed = %v", pushed)
	}
	if sent := f.requests(http.MethodPost, "/api/v3/release/push"); len(sent) != 1 || !strings.Contains(sent[0].Body, `"magnetUrl":"magnet:?xt=urn:btih:abc"`) || strings.Contains(sent[0].Body, `"downloadUrl"`) || !strings.Contains(sent[0].Body, `"protocol":"torrent"`) {
		t.Errorf("Radarr was sent %v", sent)
	}
	mustFail(t, cs, "release_push", map[string]any{"title": "x", "download_url": "http://x", "protocol": "carrier"}, "want usenet or torrent")
}

// rowsOfStrings is a decoded JSON list of text.
func rowsOfStrings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, fmt.Sprint(e))
	}

	return out
}

// Every tool that is added by a kind of thing names its kind in the answer
// of provider_kinds, so that an add can be filled in without a guess.
func TestProviderKindsKnowsEveryFamily(t *testing.T) {
	t.Parallel()

	f := newFakeRadarr(t)
	cs, _ := setupSession(t, f)
	out, msg := run(t, cs, "provider_kinds", map[string]any{"provider": "nothing"})
	if out != nil || !strings.Contains(msg, "autotagging, customformat, downloadclient, importlist, indexer, metadata, notification") {
		t.Errorf("the providers provider_kinds knows = %q", msg)
	}
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		family, isAdd := strings.CutSuffix(tool.Name, "_add")
		if !isAdd || !strings.Contains(tool.Description, "provider_kinds") {
			continue
		}
		if !strings.Contains(msg, family) {
			t.Errorf("%s sends a caller to provider_kinds, which does not know %s", tool.Name, strconv.Quote(family))
		}
	}
}
