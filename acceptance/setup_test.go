//go:build integration

package acceptance

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	acc "github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/arrserver"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/radarr-mcp/lib/testenv"
	"github.com/katbyte/radarr-mcp/tools"
)

// The tools that set Radarr up and run it, each through its whole life
// against the real one: a thing is added, read back as Radarr holds it,
// changed, and deleted - first without confirm, which must change nothing.

// named finds the row whose value under key is name, or nil.
func named(rows []map[string]any, key, name string) map[string]any {
	i := slices.IndexFunc(rows, func(r map[string]any) bool { return acc.Str(r[key]) == name })
	if i < 0 {
		return nil
	}

	return rows[i]
}

// rowsOf calls a tool that lists and answers its rows.
func rowsOf(t *testing.T, tool, key string) []map[string]any {
	t.Helper()

	return acc.Rows(t, suite.Call(t, tool, nil)[key], key)
}

// deleted deletes with a tool that asks first: without confirm nothing goes
// and the answer names it with what goes with it, and with confirm it is gone.
func deleted(t *testing.T, tool string, args map[string]any, want string, still func() bool) {
	t.Helper()

	would := suite.Call(t, tool, args)
	if acc.Str(would["would_delete"]) != want || would["deleted"] != nil || len(acc.Strs(t, would["loses"], "loses")) == 0 {
		t.Errorf("%s without confirm = %v, want would_delete %q and what goes with it", tool, would, want)
	}
	if !still() {
		t.Fatalf("%s took %q without confirm", tool, want)
	}

	confirmed := maps.Clone(args)
	confirmed["confirm"] = true
	if gone := suite.Call(t, tool, confirmed); acc.Str(gone["deleted"]) != want || gone["would_delete"] != nil {
		t.Errorf("%s with confirm = %v, want deleted %q", tool, gone, want)
	}
	if still() {
		t.Errorf("%s answered that %q was deleted, and it is still there", tool, want)
	}
}

// lookedAt are the settings of text the tools show, each read against what its kind keeps there and found to be no credential: an address, a folder,
// a name, who a thing signs in as. One Radarr adds later is in none of the tools' rules for a credential and not here either, so the test below
// fails on it until someone has looked.
var lookedAt = []string{"accountId", "additionalParameters", "apiPath", "apiUrl", "aPIUser", "appId", "arguments", "author", "authorizeNotification", "authUser", "authUsername", "avatar", "baseUrl", "bcc", "category", "cc", "certification", "channel", "channelTags", "chatId", "clickUrl", "companyId", "completedDirectory", "destination", "destinationDirectory", "deviceIds", "deviceNames", "devices", "directory", "displayTime", "downloadDirectory", "event", "expire", "expires", "filterCriteria.certification", "filterCriteria.excludeCompanyIds", "filterCriteria.excludeGenreIds", "filterCriteria.includeCompanyIds", "filterCriteria.includeGenreIds", "filterCriteria.minVoteAverage", "filterCriteria.minVotes", "from", "genres", "host", "icon", "iconColor", "keywordId", "limit", "link", "listId", "listname", "magnetFileExtension", "mapFrom", "mapTo", "mention", "minimumSeeders", "movieCategory", "movieDirectory", "movieImportedCategory", "notificationName", "nzbFolder", "path", "personId", "port", "postImportTags", "rating", "receiverId", "recipients", "retry", "rpcPath", "senderDomain", "senderId", "server", "serverUrl", "signIn", "sound", "strmFolder", "tags", "to", "topicId", "topics", "torrentFolder", "traktAdditionalParameters", "ttl", "tvCategory", "tvDirectory", "url", "urlBase", "user", "userName", "username", "vibration", "watchFolder", "years"}

// Every setting of every kind of indexer, download client, import list, notification and metadata consumer this Radarr has is a credential, which is
// never shown, or a switch, a number or a choice, or text that has been looked at. A webhook's headers and the addresses Apprise sends to were both
// shown until this asked.
func TestEverySettingShownHasBeenLookedAt(t *testing.T) {
	skipUnlessReady(t)

	kinds, settings := 0, 0
	for _, provider := range []string{"indexer", "downloadclient", "importlist", "notification", "metadata"} {
		for _, kind := range acc.Strs(t, suite.Call(t, "provider_kinds", map[string]any{"provider": provider})["kinds"], "kinds") {
			kinds++

			for _, setting := range acc.Rows(t, suite.Call(t, "provider_kinds", map[string]any{"provider": provider, "kind": kind})["settings"], "settings") {
				settings++

				name, holds := acc.Str(setting["name"]), acc.Str(setting["type"])
				if setting["secret"] != true && !slices.Contains([]string{"checkbox", "number", "select"}, holds) && !slices.Contains(lookedAt, name) {
					t.Errorf("the %s %s shows its setting %s (%s), which nobody has looked at: make it a credential in the tools, or add it to lookedAt", provider, kind, name, holds)
				}
			}
		}
	}
	if kinds < 70 || settings < 400 {
		t.Errorf("%d kinds with %d settings were looked through, want every kind this Radarr has", kinds, settings)
	}

	for kind, name := range map[string]string{"Webhook": "headers", "Apprise": "statelessUrls"} {
		if setting := named(acc.Rows(t, suite.Call(t, "provider_kinds", map[string]any{"provider": "notification", "kind": kind})["settings"], "settings"), "name", name); setting == nil || setting["secret"] != true {
			t.Errorf("a %s notification's %s = %v, want it a credential", kind, name, setting)
		}
	}
}

// provider_kinds says what there is to add and what each kind takes, for
// every family that is added by kind.
func TestProviderKinds(t *testing.T) {
	skipUnlessReady(t)

	for provider, kind := range map[string]string{"indexer": "Newznab", "downloadclient": "UsenetBlackhole", "importlist": "RadarrImport", "notification": "Webhook", "metadata": "XbmcMetadata", "customformat": "ReleaseTitleSpecification", "autotagging": "GenreSpecification"} {
		kinds := acc.Strs(t, suite.Call(t, "provider_kinds", map[string]any{"provider": provider})["kinds"], "kinds")
		if !slices.Contains(kinds, kind) {
			t.Errorf("the kinds of %s = %v, want %s among them", provider, kinds, kind)
		}
		one := suite.Call(t, "provider_kinds", map[string]any{"provider": provider, "kind": kind})
		if settings := acc.Rows(t, one["settings"], "settings"); acc.Str(one["kind"]) != kind || len(settings) == 0 || acc.Str(settings[0]["name"]) == "" {
			t.Errorf("what a %s %s takes = %v", provider, kind, one)
		}
	}

	settings := acc.Rows(t, suite.Call(t, "provider_kinds", map[string]any{"provider": "indexer", "kind": "newznab"})["settings"], "settings")
	if key := named(settings, "name", "apiKey"); key == nil || key["secret"] != true || named(settings, "name", "baseUrl") == nil || named(settings, "name", "baseUrl")["secret"] == true {
		t.Errorf("a Newznab indexer's settings = %v, want baseUrl, and apiKey as a credential", settings)
	}
	// a select says what it takes by name
	hook := acc.Rows(t, suite.Call(t, "provider_kinds", map[string]any{"provider": "notification", "kind": "Webhook"})["settings"], "settings")
	if method := named(hook, "name", "method"); method == nil || !slices.Contains(acc.Strs(t, method["options"], "options"), "POST") {
		t.Errorf("a webhook's method = %v, want what it can be set to", named(hook, "name", "method"))
	}
	// a setting whose choices the service has is marked, and the service is asked for them: the fake indexer's own categories
	if categories := named(settings, "name", "categories"); categories == nil || acc.Str(categories["options_from"]) == "" {
		t.Errorf("a Newznab indexer's categories = %v, want where its choices come from", categories)
	}
	offered := acc.Rows(t, suite.Call(t, "provider_options", map[string]any{"provider": "indexer", "name": "Fake Indexer", "setting": "categories"})["options"], "options")
	if !slices.ContainsFunc(offered, func(o map[string]any) bool { return acc.NumOr0(o["value"]) == 2000 && acc.Str(o["name"]) != "" }) {
		t.Errorf("the categories the indexer offers = %v, want Movies (2000) among them", offered)
	}
	if msg := suite.CallErr(t, "provider_options", map[string]any{"provider": "indexer", "name": "Fake Indexer", "setting": "baseUrl"}); !strings.Contains(msg, "not a setting whose choices come from the service") || !strings.Contains(msg, "categories") {
		t.Errorf("the choices of a setting that has none = %q, want the ones that do", msg)
	}
	if msg := suite.CallErr(t, "provider_kinds", map[string]any{"provider": "indexer", "kind": "Carrier Pigeon"}); !strings.Contains(msg, "no indexer kind") || !strings.Contains(msg, "Newznab") {
		t.Errorf("an unknown kind = %q, want the ones there are", msg)
	}
	if msg := suite.CallErr(t, "provider_kinds", map[string]any{"provider": "pigeons"}); !strings.Contains(msg, "no provider") || !strings.Contains(msg, "notification") {
		t.Errorf("an unknown provider = %q, want the ones there are", msg)
	}
}

// An indexer is added by kind with its settings by name, tested by Radarr as
// it is saved, shown without its API key, changed and deleted.
func TestIndexerLifecycle(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() { _, _ = suite.Invoke("tag_delete", map[string]any{"tag": "second-indexer"}) })
	t.Cleanup(func() {
		_, _ = suite.Invoke("indexer_delete", map[string]any{"name": "Second Indexer", "confirm": true})
	})
	listed := func() map[string]any { return named(rowsOf(t, "indexer_list", "indexers"), "name", "Second Indexer") }

	// left alone, a new indexer is switched on, as in Radarr's own form; Radarr tests it before saving it, and an API key the indexer turns away fails that test
	wrong := map[string]any{"kind": "Newznab", "name": "Wrong Key", "settings": map[string]any{"baseUrl": indexer.URL(), "apiPath": "/api", "apiKey": suite.Secret("a-wrong-key-nobody-should-be-shown"), "categories": []any{2000, 2040}}}
	t.Cleanup(func() { _, _ = suite.Invoke("indexer_delete", map[string]any{"name": "Wrong Key", "confirm": true}) })
	if msg := suite.CallErr(t, "indexer_add", wrong); !strings.Contains(msg, "force=true") || named(rowsOf(t, "indexer_list", "indexers"), "name", "Wrong Key") != nil {
		t.Errorf("an indexer whose key is wrong = %q, want it refused with the way round it", msg)
	}
	// forced, it is saved untested and switched on all the same, and its own test then says what is wrong with it
	wrong["force"] = true
	if forced := acc.Object(t, suite.Call(t, "indexer_add", wrong)["options"], "options"); forced["enableRss"] != true || forced["enableAutomaticSearch"] != true || forced["enableInteractiveSearch"] != true {
		t.Errorf("an indexer added by force = %v, want it switched on", forced)
	}
	if results := acc.Rows(t, suite.Call(t, "indexer_test", map[string]any{"name": "Wrong Key"})["results"], "results"); len(results) != 1 || results[0]["valid"] != false || len(acc.Strs(t, results[0]["failures"], "failures")) == 0 {
		t.Errorf("a test of an indexer whose key is wrong = %v, want it failed with why", results)
	}
	suite.Call(t, "indexer_delete", map[string]any{"name": "Wrong Key", "confirm": true})

	added := suite.Call(t, "indexer_add", map[string]any{
		"kind": "Newznab", "name": "Second Indexer", "options": map[string]any{"enableRss": false, "enableAutomaticSearch": false, "enableInteractiveSearch": true, "priority": 40},
		"settings": map[string]any{"baseUrl": indexer.URL(), "apiPath": "/api", "apiKey": indexerKey, "categories": []any{2000, 2040}},
	})
	if settings := acc.Object(t, added["settings"], "settings"); acc.Str(added["kind"]) != "Newznab" || acc.Str(settings["baseUrl"]) != indexer.URL() || settings["apiKey"] != nil || !slices.Contains(acc.Strs(t, added["credentials"], "credentials"), "apiKey") {
		t.Errorf("the indexer added = %v, want its address shown and its key only named", added)
	}
	if options := acc.Object(t, added["options"], "options"); options["enableRss"] != false || options["enableInteractiveSearch"] != true || acc.Num(t, options["priority"], "priority") != 40 {
		t.Errorf("its switches = %v", added["options"])
	}

	// the list shows it too, with the same settings and no key
	if row := listed(); row == nil || acc.Num(t, row["priority"], "priority") != 40 || row["enable_rss"] != false || acc.Object(t, row["settings"], "settings")["apiKey"] != nil || !slices.Contains(acc.Strs(t, row["credentials"], "credentials"), "apiKey") {
		t.Errorf("indexer_list shows it as %v", row)
	}

	edited := suite.Call(t, "indexer_edit", map[string]any{"name": "second indexer", "options": map[string]any{"priority": 45}, "settings": map[string]any{"categories": []any{2000, 2040, 2045}}, "add_tags": []any{"second-indexer"}})
	if acc.Num(t, acc.Object(t, edited["options"], "options")["priority"], "priority") != 45 || len(acc.RowsOfAny(acc.Object(t, edited["settings"], "settings")["categories"])) != 3 || !slices.Equal(acc.Strs(t, edited["tags"], "tags"), []string{"second-indexer"}) {
		t.Errorf("the indexer edited = %v", edited)
	}
	// its key was not named in the edit, and is still the one Radarr tests it with
	if results := acc.Rows(t, suite.Call(t, "indexer_test", map[string]any{"name": "Second Indexer"})["results"], "results"); len(results) != 1 || results[0]["valid"] != true {
		t.Errorf("the indexer tested after the edit = %v, want it still valid with the key it had", results)
	}
	if msg := suite.CallErr(t, "indexer_edit", map[string]any{"name": "Second Indexer", "settings": map[string]any{"baseURL2": "x"}}); !strings.Contains(msg, `no setting "baseURL2"`) || !strings.Contains(msg, "baseUrl") {
		t.Errorf("a setting it does not have = %q, want the ones it has", msg)
	}
	if msg := suite.CallErr(t, "indexer_edit", map[string]any{"name": "Second Indexer", "options": map[string]any{"enabled": true}}); !strings.Contains(msg, `no option "enabled"`) || !strings.Contains(msg, "enableRss") {
		t.Errorf("a switch it does not have = %q, want the ones it has", msg)
	}

	deleted(t, "indexer_delete", map[string]any{"name": "Second Indexer"}, "Second Indexer", func() bool { return listed() != nil })
}

// An indexer that answers an error is one Radarr reports with the address it
// asked, the indexer's stored API key on the end. That reaches a caller in a
// test that fails, which is a row, and in a change Radarr refuses, which is
// an error: both say what went wrong, and neither shows the key.
func TestAFailingIndexerDoesNotGiveItsKeyAway(t *testing.T) {
	skipUnlessReady(t)

	// an indexer of its own, so the one every other test searches is never the one that is down
	key := suite.Secret("a-key-nobody-should-be-shown")
	down, err := indexers.Add(newznab.Site{Name: "down", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := down.Offer(background); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = suite.Invoke("indexer_delete", map[string]any{"name": "Down Indexer", "confirm": true}) })
	suite.Call(t, "indexer_add", map[string]any{"kind": "Newznab", "name": "Down Indexer", "settings": map[string]any{"baseUrl": down.URL(), "apiPath": "/api", "apiKey": key, "categories": []any{2000, 2040}}, "options": map[string]any{"enableRss": false, "enableAutomaticSearch": false}})

	down.SetFailure(newznab.Failure{Status: 500})
	results := suite.Call(t, "indexer_test", map[string]any{"name": "Down Indexer"})
	rows := acc.Rows(t, results["results"], "results")
	if len(rows) != 1 || rows[0]["valid"] != false {
		t.Fatalf("a test of an indexer that is down = %v, want it failed", results)
	}
	if said := strings.Join(acc.Strs(t, rows[0]["failures"], "failures"), "\n"); strings.Contains(said, key) || !strings.Contains(said, "apikey=REDACTED") || !strings.Contains(said, down.URL()) {
		t.Errorf("what the test said = %q, want the address Radarr asked, without the key it asked with", said)
	}
	// a change to it is tested too, and refused in the same words
	if msg := suite.CallErr(t, "indexer_edit", map[string]any{"name": "Down Indexer", "options": map[string]any{"priority": 30}}); strings.Contains(msg, key) || !strings.Contains(msg, "apikey=REDACTED") || !strings.Contains(msg, "force=true") {
		t.Errorf("a change Radarr's test refused = %q, want what Radarr said without the key, and the way round it", msg)
	}
	// and so is every indexer at once, where the one that is down is one row among them
	every := acc.Rows(t, suite.Call(t, "indexer_test", nil)["results"], "results")
	if failed := named(every, "name", "Down Indexer"); failed == nil || failed["valid"] != false || strings.Contains(acc.Str(failed["failures"]), key) || named(every, "name", "Fake Indexer") == nil || named(every, "name", "Fake Indexer")["valid"] != true {
		t.Errorf("a test of every indexer = %v, want the one that is down failed without its key and the other valid", every)
	}

	down.SetFailure(newznab.Failure{})
	suite.Call(t, "indexer_delete", map[string]any{"name": "Down Indexer", "confirm": true})
}

// A download client is added switched off, renamed, changed and deleted.
func TestDownloadClientLifecycle(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() {
		for _, name := range []string{"Second Blackhole", "Spare Blackhole"} {
			_, _ = suite.Invoke("downloadclient_delete", map[string]any{"name": name, "confirm": true})
		}
	})
	listed := func(name string) map[string]any {
		return named(rowsOf(t, "downloadclient_list", "download_clients"), "name", name)
	}

	added := suite.Call(t, "downloadclient_add", map[string]any{"kind": "UsenetBlackhole", "name": "Second Blackhole", "options": map[string]any{"enable": false, "priority": 9}, "settings": map[string]any{"nzbFolder": nzbFolder, "watchFolder": watchFolder}})
	if acc.Str(added["kind"]) != "UsenetBlackhole" || acc.Str(acc.Object(t, added["settings"], "settings")["nzbFolder"]) != nzbFolder || acc.Object(t, added["options"], "options")["enable"] != false {
		t.Errorf("the download client added = %v", added)
	}

	edited := suite.Call(t, "downloadclient_edit", map[string]any{"name": "Second Blackhole", "new_name": "Spare Blackhole", "options": map[string]any{"priority": 7, "removeFailedDownloads": false}})
	if options := acc.Object(t, edited["options"], "options"); acc.Str(edited["name"]) != "Spare Blackhole" || acc.Num(t, options["priority"], "priority") != 7 || options["removeFailedDownloads"] != false {
		t.Errorf("the download client edited = %v", edited)
	}
	if row := listed("Spare Blackhole"); row == nil || listed("Second Blackhole") != nil || row["enabled"] != false || acc.Num(t, row["priority"], "priority") != 7 || acc.Str(acc.Object(t, row["settings"], "settings")["watchFolder"]) != watchFolder {
		t.Errorf("downloadclient_list shows it as %v", row)
	}
	// one that is switched off is tested when it is named
	if results := acc.Rows(t, suite.Call(t, "downloadclient_test", map[string]any{"name": "Spare Blackhole"})["results"], "results"); len(results) != 1 || results[0]["valid"] != true {
		t.Errorf("the download client tested = %v", results)
	}

	deleted(t, "downloadclient_delete", map[string]any{"name": "Spare Blackhole"}, "Spare Blackhole", func() bool { return listed("Spare Blackhole") != nil })
}

// An import list pointed at this Radarr offers the library's own films, every
// one of them held already; it is tested, changed and deleted.
func TestImportListLifecycle(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() { _, _ = suite.Invoke("importlist_delete", map[string]any{"name": "Itself", "confirm": true}) })
	listed := func() map[string]any { return named(rowsOf(t, "importlist_list", "items"), "name", "Itself") }

	hd := named(rowsOf(t, "qualityprofile_list", "profiles"), "name", profileHD)
	if hd == nil {
		t.Fatalf("no %s profile to give the list", profileHD)
	}
	added := suite.Call(t, "importlist_add", map[string]any{
		"kind": "RadarrImport", "name": "Itself", "settings": map[string]any{"baseUrl": "http://localhost:7878", "apiKey": testenv.Token()},
		"options": map[string]any{"enabled": true, "enableAuto": false, "searchOnAdd": false, "qualityProfileId": hd["id"], "rootFolderPath": moviesRoot, "minimumAvailability": "released", "monitor": "movieOnly"},
	})
	if options := acc.Object(t, added["options"], "options"); acc.Str(added["kind"]) != "RadarrImport" || options["enabled"] != true || options["enableAuto"] != false || acc.Str(options["rootFolderPath"]) != moviesRoot {
		t.Errorf("the import list added = %v", added)
	}
	if row := listed(); row == nil || !slices.Contains(acc.Strs(t, row["credentials"], "credentials"), "apiKey") || acc.Object(t, row["settings"], "settings")["apiKey"] != nil {
		t.Errorf("importlist_list shows it as %v, want its key only named", row)
	}
	for _, args := range []map[string]any{{"name": "Itself"}, nil} {
		if results := acc.Rows(t, suite.Call(t, "importlist_test", args)["results"], "results"); len(results) != 1 || acc.Str(results[0]["name"]) != "Itself" || results[0]["valid"] != true {
			t.Errorf("the import list tested with %v = %v", args, results)
		}
	}

	// once Radarr has read the list, it offers the library's own films, all held already
	if out := suite.Call(t, "task_run", map[string]any{"task": "ImportListSync"}); acc.Str(acc.Object(t, out["command"], "command")["status"]) != "completed" {
		t.Fatalf("the import list sync = %v", out)
	}
	offered := suite.Call(t, "importlist_movies", nil)
	alien := named(acc.Rows(t, offered["movies"], "movies"), "title", "Alien")
	if alien == nil || alien["in_library"] != true || alien["excluded"] != false || acc.Num(t, alien["tmdb_id"], "tmdb_id") != 348 || !slices.Equal(acc.Strs(t, alien["lists"], "lists"), []string{"Itself"}) || acc.Num(t, offered["total"], "total") < len(fixtures()) {
		t.Errorf("what the list offers = %v films, Alien %v", offered["total"], alien)
	}
	if fresh := suite.Call(t, "importlist_movies", map[string]any{"new_only": true}); acc.Num(t, fresh["total"], "total") != 0 {
		t.Errorf("a list of this library offers %v films it does not hold", fresh["total"])
	}
	// what the list can be limited to is asked of the Radarr it reads: with the key the list holds, which no answer shows, and for a kind not added yet with the key given
	folders := acc.Rows(t, suite.Call(t, "provider_options", map[string]any{"provider": "importlist", "name": "Itself", "setting": "rootFolderPaths"})["options"], "options")
	if named(folders, "name", moviesRoot) == nil || named(folders, "name", messyRoot) == nil {
		t.Errorf("the root folders the other Radarr offers = %v, want this one's two", folders)
	}
	profiles := acc.Rows(t, suite.Call(t, "provider_options", map[string]any{"provider": "importlist", "kind": "RadarrImport", "setting": "profileIds", "settings": map[string]any{"baseUrl": "http://localhost:7878", "apiKey": testenv.Token()}})["options"], "options")
	if offered := named(profiles, "name", profileHD); offered == nil || acc.NumOr0(offered["value"]) != acc.NumOr0(hd["id"]) {
		t.Errorf("the profiles the other Radarr offers = %v, want %s by its id", profiles, profileHD)
	}
	if msg := suite.CallErr(t, "provider_options", map[string]any{"provider": "importlist", "kind": "RadarrImport", "setting": "profileIds", "settings": map[string]any{"baseUrl": "http://localhost:7878", "apiKey": "not-the-key"}}); msg == "" {
		t.Error("the profiles of a Radarr asked with the wrong key were answered")
	}

	edited := suite.Call(t, "importlist_edit", map[string]any{"name": "Itself", "options": map[string]any{"enabled": false}})
	if acc.Object(t, edited["options"], "options")["enabled"] != false || !slices.Contains(acc.Strs(t, edited["credentials"], "credentials"), "apiKey") {
		t.Errorf("the import list edited = %v, want it off and its key kept", edited)
	}
	// a list that is off offers nothing, and is not one of every list tested
	if after := suite.Call(t, "importlist_movies", nil); acc.Num(t, after["total"], "total") != 0 {
		t.Errorf("with the list off, the lists offer %v films", after["total"])
	}
	if results := acc.Rows(t, acc.OrEmptyList(suite.Call(t, "importlist_test", nil)["results"]), "results"); len(results) != 0 {
		t.Errorf("a test of every list, with the only one off = %v", results)
	}

	deleted(t, "importlist_delete", map[string]any{"name": "Itself"}, "Itself", func() bool { return listed() != nil })
}

// A notification that is sent for nothing is saved untested, where the same
// one sent for what its kind can tell of is refused: its test says what is
// wrong with where it points. It is changed with its password kept, switched
// on by force, and deleted.
func TestNotificationLifecycle(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() { _, _ = suite.Invoke("notification_delete", map[string]any{"name": "Nowhere", "confirm": true}) })
	listed := func() map[string]any { return named(rowsOf(t, "notification_list", "items"), "name", "Nowhere") }
	// a webhook's own headers are where a token goes, and Radarr marks them as nothing in particular
	headers := []any{map[string]any{"key": "Authorization", "value": "Bearer " + suite.Secret("a-token-nobody-should-be-shown")}}
	nowhere := map[string]any{"url": "http://127.0.0.1:9/hook", "method": "POST", "username": "radarr", "password": suite.Secret("a-password-nobody-should-be-shown"), "headers": headers}

	// left alone, a new notification is sent for what its kind can tell of, so Radarr tests it before saving, and nothing answers there
	msg := suite.CallErr(t, "notification_add", map[string]any{"kind": "Webhook", "name": "Nowhere", "settings": nowhere})
	if !strings.Contains(msg, "force=true") || !strings.Contains(msg, "off=true") || listed() != nil {
		t.Errorf("a notification that cannot be sent = %q, want it refused with the ways round it", msg)
	}

	// added off, it is saved untested
	added := suite.Call(t, "notification_add", map[string]any{"kind": "Webhook", "name": "Nowhere", "settings": nowhere, "off": true})
	if settings := acc.Object(t, added["settings"], "settings"); acc.Str(settings["url"]) != "http://127.0.0.1:9/hook" || settings["password"] != nil || acc.Str(settings["username"]) != "radarr" || settings["headers"] != nil || !slices.Equal(acc.Strs(t, added["credentials"], "credentials"), []string{"headers", "password"}) {
		t.Errorf("the notification added = %v, want its address and who it signs in as shown, and its password and its headers only named", added)
	}
	if options := acc.Object(t, added["options"], "options"); options["onGrab"] != false || options["onDownload"] != false || options["onHealthIssue"] != false {
		t.Errorf("a notification added off = %v, want it sent for nothing", options)
	}
	results := acc.Rows(t, suite.Call(t, "notification_test", map[string]any{"name": "Nowhere"})["results"], "results")
	if len(results) != 1 || acc.Str(results[0]["name"]) != "Nowhere" || results[0]["valid"] != false || len(acc.Strs(t, results[0]["failures"], "failures")) == 0 {
		t.Errorf("a test of a notification that points nowhere = %v, want it failed with why", results)
	}

	edited := suite.Call(t, "notification_edit", map[string]any{"name": "Nowhere", "settings": map[string]any{"url": "http://127.0.0.1:9/other"}, "options": map[string]any{"includeHealthWarnings": true}})
	if acc.Str(acc.Object(t, edited["settings"], "settings")["url"]) != "http://127.0.0.1:9/other" || acc.Object(t, edited["options"], "options")["includeHealthWarnings"] != true || !slices.Contains(acc.Strs(t, edited["credentials"], "credentials"), "password") {
		t.Errorf("the notification edited = %v, want the address changed and the password kept", edited)
	}
	// switching it on is a change Radarr tests, and refuses; force saves the change untested
	if msg := suite.CallErr(t, "notification_edit", map[string]any{"name": "Nowhere", "options": map[string]any{"onGrab": true}}); !strings.Contains(msg, "force=true") {
		t.Errorf("switching on a notification that cannot be sent = %q", msg)
	}
	forced := suite.Call(t, "notification_edit", map[string]any{"name": "Nowhere", "options": map[string]any{"onHealthIssue": true}, "force": true})
	if acc.Object(t, forced["options"], "options")["onHealthIssue"] != true || acc.Object(t, forced["options"], "options")["onGrab"] != false {
		t.Errorf("the notification switched on untested = %v", forced)
	}
	// switched on, it is one of every notification tested. Radarr's test of them all fails whole on one it cannot reach, and this one still gets its answer
	if all := acc.Rows(t, suite.Call(t, "notification_test", nil)["results"], "results"); len(all) != 1 || acc.Str(all[0]["name"]) != "Nowhere" || all[0]["valid"] != false {
		t.Errorf("a test of every notification = %v", all)
	}
	if row := listed(); row == nil || acc.Str(row["kind"]) != "Webhook" || acc.Object(t, row["options"], "options")["onHealthIssue"] != true {
		t.Errorf("notification_list shows it as %v", row)
	}

	deleted(t, "notification_delete", map[string]any{"name": "Nowhere"}, "Nowhere", func() bool { return listed() != nil })
}

// The metadata consumers are a set Radarr comes with: each is listed, one is
// set up and put back as it was, and tested.
func TestMetadataConsumers(t *testing.T) {
	skipUnlessReady(t)

	consumers := rowsOf(t, "metadata_list", "items")
	kodi := named(consumers, "kind", "XbmcMetadata")
	if len(consumers) < 3 || kodi == nil || acc.Object(t, kodi["options"], "options")["enable"] != false {
		t.Fatalf("the metadata consumers = %v, want Kodi's among them, off", consumers)
	}
	name, before := acc.Str(kodi["name"]), acc.Object(t, kodi["settings"], "settings")["useMovieNfo"] == true
	t.Cleanup(func() {
		_, _ = suite.Invoke("metadata_edit", map[string]any{"name": name, "settings": map[string]any{"useMovieNfo": before}})
	})

	edited := suite.Call(t, "metadata_edit", map[string]any{"name": name, "settings": map[string]any{"useMovieNfo": !before}})
	if after := acc.Object(t, edited["settings"], "settings")["useMovieNfo"] == true; after == before || acc.Object(t, edited["options"], "options")["enable"] != false {
		t.Errorf("the consumer edited = %v, want useMovieNfo changed from %v and the consumer still off", edited, before)
	}
	if again := named(rowsOf(t, "metadata_list", "items"), "kind", "XbmcMetadata"); (acc.Object(t, again["settings"], "settings")["useMovieNfo"] == true) == before {
		t.Errorf("read back, the consumer = %v", again)
	}
	if results := acc.Rows(t, suite.Call(t, "metadata_test", map[string]any{"name": name})["results"], "results"); len(results) != 1 || acc.Str(results[0]["name"]) != name || results[0]["valid"] != true {
		t.Errorf("the consumer tested = %v", results)
	}
}

// Each plain record Radarr is set up with is added by its values, read back,
// changed and deleted: a delay profile, a release profile, an auto tagging
// rule, a remote path mapping, a saved filter and a custom format.
func TestRecordLifecycles(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() {
		for _, label := range []string{"held-back", "held-longer", "auto-horror"} {
			_, _ = suite.Invoke("tag_delete", map[string]any{"tag": label})
		}
	})
	withID := func(rows []map[string]any, id int) map[string]any {
		i := slices.IndexFunc(rows, func(r map[string]any) bool { return acc.NumOr0(r["id"]) == id })
		if i < 0 {
			return nil
		}

		return rows[i]
	}

	t.Run("a delay profile, and the order they are tried in", func(t *testing.T) {
		delay := func(minutes int, tag string) map[string]any {
			return map[string]any{"values": map[string]any{"enableUsenet": true, "enableTorrent": true, "preferredProtocol": "usenet", "usenetDelay": minutes, "bypassIfHighestQuality": false, "bypassIfAboveCustomFormatScore": false}, "tags": []any{tag}}
		}
		first := acc.Object(t, suite.Call(t, "delayprofile_add", delay(30, "held-back"))["saved"], "saved")
		a := acc.Num(t, first["id"], "id")
		t.Cleanup(func() { _, _ = suite.Invoke("delayprofile_delete", map[string]any{"id": itoa(a), "confirm": true}) })
		if acc.Num(t, first["usenetDelay"], "usenetDelay") != 30 || acc.Str(first["preferredProtocol"]) != "usenet" || !slices.Equal(acc.Strs(t, first["tags"], "tags"), []string{"held-back"}) {
			t.Fatalf("the delay profile added = %v", first)
		}
		// Radarr refuses one with no tags: only the default holds for every film
		if msg := suite.CallErr(t, "delayprofile_add", map[string]any{"values": map[string]any{"enableUsenet": true, "enableTorrent": true, "preferredProtocol": "usenet", "usenetDelay": 5}}); msg == "" {
			t.Error("a second delay profile with no tags was taken")
		}

		edited := acc.Object(t, suite.Call(t, "delayprofile_edit", map[string]any{"id": itoa(a), "values": map[string]any{"usenetDelay": 45}})["saved"], "saved")
		if acc.Num(t, edited["usenetDelay"], "usenetDelay") != 45 || !slices.Equal(acc.Strs(t, edited["tags"], "tags"), []string{"held-back"}) {
			t.Errorf("the delay profile edited = %v, want the delay changed and its tag kept", edited)
		}
		if row := withID(rowsOf(t, "delayprofile_list", "items"), a); row == nil || acc.Num(t, row["usenetDelay"], "usenetDelay") != 45 {
			t.Errorf("delayprofile_list shows it as %v", row)
		}
		if msg := suite.CallErr(t, "delayprofile_edit", map[string]any{"id": itoa(a), "values": map[string]any{"usenetDelays": 1}}); !strings.Contains(msg, `no value "usenetDelays"`) || !strings.Contains(msg, "usenetDelay") {
			t.Errorf("a value it does not have = %q, want the ones it has", msg)
		}

		second := acc.Object(t, suite.Call(t, "delayprofile_add", delay(90, "held-longer"))["saved"], "saved")
		b := acc.Num(t, second["id"], "id")
		t.Cleanup(func() { _, _ = suite.Invoke("delayprofile_delete", map[string]any{"id": itoa(b), "confirm": true}) })
		order := func(rows []map[string]any, id int) int { return acc.Num(t, withID(rows, id)["order"], "order") }
		// the newer one goes first, then back after the older; the default stays last throughout
		moved := acc.Rows(t, suite.Call(t, "delayprofile_reorder", map[string]any{"id": b})["items"], "items")
		if len(moved) != 3 || order(moved, b) >= order(moved, a) || order(moved, a) >= order(moved, 1) {
			t.Errorf("with the newer profile moved first, the order = %v", moved)
		}
		back := acc.Rows(t, suite.Call(t, "delayprofile_reorder", map[string]any{"id": b, "after": a})["items"], "items")
		if order(back, a) >= order(back, b) || order(back, b) >= order(back, 1) {
			t.Errorf("with it moved after the older one, the order = %v", back)
		}

		if msg := suite.CallErr(t, "delayprofile_delete", map[string]any{"id": "1", "confirm": true}); !strings.Contains(msg, "is the default") {
			t.Errorf("deleting the default delay profile = %q", msg)
		}
		for _, id := range []int{b, a} {
			deleted(t, "delayprofile_delete", map[string]any{"id": itoa(id)}, "delay profile "+itoa(id), func() bool { return withID(rowsOf(t, "delayprofile_list", "items"), id) != nil })
		}
	})

	t.Run("a release profile", func(t *testing.T) {
		t.Cleanup(func() { _, _ = suite.Invoke("releaseprofile_delete", map[string]any{"id": "No Cams", "confirm": true}) })
		saved := acc.Object(t, suite.Call(t, "releaseprofile_add", map[string]any{"values": map[string]any{"name": "No Cams", "enabled": true, "ignored": []any{"CAM", "TS"}, "required": []any{}}})["saved"], "saved")
		if acc.Str(saved["name"]) != "No Cams" || !slices.Equal(acc.Strs(t, saved["ignored"], "ignored"), []string{"CAM", "TS"}) || acc.NumOr0(saved["id"]) == 0 {
			t.Fatalf("the release profile added = %v", saved)
		}
		edited := acc.Object(t, suite.Call(t, "releaseprofile_edit", map[string]any{"id": "no cams", "values": map[string]any{"enabled": false}})["saved"], "saved")
		if edited["enabled"] != false || !slices.Equal(acc.Strs(t, edited["ignored"], "ignored"), []string{"CAM", "TS"}) {
			t.Errorf("the release profile edited = %v, want it off and its words kept", edited)
		}
		// a release profile is not made of conditions, so its tools do not take any
		if msg := suite.CallErr(t, "releaseprofile_edit", map[string]any{"id": "No Cams", "conditions": []any{map[string]any{"kind": "GenreSpecification"}}}); !strings.Contains(msg, "releaseprofile_edit takes id, values and tags") {
			t.Errorf("conditions for a record that has none = %q", msg)
		}
		deleted(t, "releaseprofile_delete", map[string]any{"id": "No Cams"}, "No Cams", func() bool { return named(rowsOf(t, "releaseprofile_list", "items"), "name", "No Cams") != nil })
	})

	t.Run("an auto tagging rule", func(t *testing.T) {
		t.Cleanup(func() { _, _ = suite.Invoke("autotagging_delete", map[string]any{"id": "Horror", "confirm": true}) })
		saved := acc.Object(t, suite.Call(t, "autotagging_add", map[string]any{"values": map[string]any{"name": "Horror", "removeTagsAutomatically": false}, "tags": []any{"auto-horror"}, "conditions": []any{map[string]any{"kind": "GenreSpecification", "name": "horror", "settings": map[string]any{"value": []any{"Horror"}}}}})["saved"], "saved")
		conditions := acc.Rows(t, saved["conditions"], "conditions")
		if acc.Str(saved["name"]) != "Horror" || len(conditions) != 1 || acc.Str(conditions[0]["kind"]) != "GenreSpecification" || !slices.Equal(acc.Strs(t, acc.Object(t, conditions[0]["settings"], "settings")["value"], "value"), []string{"Horror"}) || !slices.Equal(acc.Strs(t, saved["tags"], "tags"), []string{"auto-horror"}) {
			t.Fatalf("the rule added = %v", saved)
		}
		edited := acc.Object(t, suite.Call(t, "autotagging_edit", map[string]any{"id": "Horror", "values": map[string]any{"removeTagsAutomatically": true}})["saved"], "saved")
		if edited["removeTagsAutomatically"] != true || len(acc.Rows(t, edited["conditions"], "conditions")) != 1 {
			t.Errorf("the rule edited = %v, want the switch changed and its condition kept", edited)
		}
		if msg := suite.CallErr(t, "autotagging_edit", map[string]any{"id": "Horror", "conditions": []any{map[string]any{"kind": "MoodSpecification"}}}); !strings.Contains(msg, "no kind of condition") || !strings.Contains(msg, "GenreSpecification") {
			t.Errorf("a kind of condition there is not = %q, want the ones there are", msg)
		}
		deleted(t, "autotagging_delete", map[string]any{"id": "Horror"}, "Horror", func() bool { return named(rowsOf(t, "autotagging_list", "items"), "name", "Horror") != nil })
	})

	t.Run("a remote path mapping", func(t *testing.T) {
		saved := acc.Object(t, suite.Call(t, "remotepathmapping_add", map[string]any{"values": map[string]any{"host": "sab.example", "remotePath": "/remote/downloads/", "localPath": "/media/downloads/"}})["saved"], "saved")
		id := acc.Num(t, saved["id"], "id")
		t.Cleanup(func() {
			_, _ = suite.Invoke("remotepathmapping_delete", map[string]any{"id": itoa(id), "confirm": true})
		})
		if acc.Str(saved["host"]) != "sab.example" || acc.Str(saved["localPath"]) != "/media/downloads/" {
			t.Fatalf("the mapping added = %v", saved)
		}
		edited := acc.Object(t, suite.Call(t, "remotepathmapping_edit", map[string]any{"id": itoa(id), "values": map[string]any{"remotePath": "/remote/other/"}})["saved"], "saved")
		if acc.Str(edited["remotePath"]) != "/remote/other/" || acc.Str(edited["host"]) != "sab.example" {
			t.Errorf("the mapping edited = %v", edited)
		}
		deleted(t, "remotepathmapping_delete", map[string]any{"id": itoa(id)}, "remote path mapping "+itoa(id), func() bool { return withID(rowsOf(t, "remotepathmapping_list", "items"), id) != nil })
	})

	t.Run("a saved filter", func(t *testing.T) {
		t.Cleanup(func() {
			for _, label := range []string{"Monitored Only", "Monitored Films"} {
				_, _ = suite.Invoke("customfilter_delete", map[string]any{"id": label, "confirm": true})
			}
		})
		saved := acc.Object(t, suite.Call(t, "customfilter_add", map[string]any{"values": map[string]any{"label": "Monitored Only", "type": "movieIndex", "filters": []any{map[string]any{"key": "monitored", "value": []any{true}, "type": "equal"}}}})["saved"], "saved")
		if acc.Str(saved["label"]) != "Monitored Only" || len(acc.Rows(t, saved["filters"], "filters")) != 1 {
			t.Fatalf("the filter added = %v", saved)
		}
		if edited := acc.Object(t, suite.Call(t, "customfilter_edit", map[string]any{"id": "Monitored Only", "values": map[string]any{"label": "Monitored Films"}})["saved"], "saved"); acc.Str(edited["label"]) != "Monitored Films" || acc.Str(edited["type"]) != "movieIndex" || len(acc.Rows(t, edited["filters"], "filters")) != 1 {
			t.Errorf("the filter edited = %v", edited)
		}
		deleted(t, "customfilter_delete", map[string]any{"id": "Monitored Films"}, "Monitored Films", func() bool { return named(rowsOf(t, "customfilter_list", "items"), "label", "Monitored Films") != nil })
	})

	t.Run("a custom format, and a profile's score for it", func(t *testing.T) {
		t.Cleanup(func() {
			_, _ = suite.Invoke("customformat_delete", map[string]any{"id": "Scratch x265", "confirm": true})
		})
		saved := acc.Object(t, suite.Call(t, "customformat_add", map[string]any{"values": map[string]any{"name": "Scratch x265", "includeCustomFormatWhenRenaming": false}, "conditions": []any{map[string]any{"kind": "ReleaseTitleSpecification", "name": "x265", "settings": map[string]any{"value": `\bx265\b`}}}})["saved"], "saved")
		if conditions := acc.Rows(t, saved["conditions"], "conditions"); acc.Str(saved["name"]) != "Scratch x265" || len(conditions) != 1 || acc.Str(acc.Object(t, conditions[0]["settings"], "settings")["value"]) != `\bx265\b` {
			t.Fatalf("the custom format added = %v", saved)
		}
		// Radarr reads a release's name with it at once, and a profile can score it
		if parsed := suite.Call(t, "release_parse", map[string]any{"title": "Alien.1979.1080p.BluRay.x265-GRP"}); !slices.Contains(acc.Strs(t, acc.OrEmptyList(parsed["custom_formats"]), "custom_formats"), "Scratch x265") {
			t.Errorf("a release named x265 parsed = %v, want the new format on it", parsed)
		}
		scored := suite.Call(t, "qualityprofile_edit", map[string]any{"profile": profileUHD, "format_scores": map[string]any{"Scratch x265": 25}})
		if acc.Num(t, acc.Object(t, scored["format_scores"], "format_scores")["Scratch x265"], "score") != 25 {
			t.Errorf("the profile's score for it = %v", scored["format_scores"])
		}

		edited := acc.Object(t, suite.Call(t, "customformat_edit", map[string]any{"id": "Scratch x265", "conditions": []any{map[string]any{"kind": "ReleaseTitleSpecification", "name": "x265 or HEVC", "required": true, "settings": map[string]any{"value": `\b(x265|HEVC)\b`}}}})["saved"], "saved")
		if conditions := acc.Rows(t, edited["conditions"], "conditions"); len(conditions) != 1 || acc.Str(conditions[0]["name"]) != "x265 or HEVC" || conditions[0]["required"] != true {
			t.Errorf("the custom format edited = %v", edited)
		}
		if parsed := suite.Call(t, "release_parse", map[string]any{"title": "Alien.1979.1080p.BluRay.HEVC-GRP"}); !slices.Contains(acc.Strs(t, acc.OrEmptyList(parsed["custom_formats"]), "custom_formats"), "Scratch x265") {
			t.Errorf("a release named HEVC parsed = %v, want the edited format on it", parsed)
		}

		deleted(t, "customformat_delete", map[string]any{"id": "Scratch x265"}, "Scratch x265", func() bool { return named(rowsOf(t, "customformat_list", "formats"), "name", "Scratch x265") != nil })
		// and the profile's score for it went with it
		uhd := named(rowsOf(t, "qualityprofile_list", "profiles"), "name", profileUHD)
		if scores, _ := uhd["format_scores"].(map[string]any); uhd == nil || scores["Scratch x265"] != nil {
			t.Errorf("after the format was deleted, the profile still scores it: %v", uhd)
		}
	})
}

// A quality profile is made as a copy of another with one quality fewer, and
// deleted once no film is on it; one films are on is refused.
func TestQualityProfileAddAndDelete(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() {
		for _, name := range []string{"Scratch Profile", "From Nothing"} {
			_, _ = suite.Invoke("qualityprofile_delete", map[string]any{"profile": name, "confirm": true})
		}
	})
	profile := func(name string) map[string]any {
		return named(rowsOf(t, "qualityprofile_list", "profiles"), "name", name)
	}

	hd := profile(profileHD)
	accepted := acc.Strs(t, hd["allowed"], "allowed")
	worst := accepted[len(accepted)-1]
	made := suite.Call(t, "qualityprofile_add", map[string]any{"name": "Scratch Profile", "copy_of": profileHD, "disallow": []any{worst}, "upgrade_allowed": true, "min_upgrade_format_score": 5})
	if allowed := acc.Strs(t, made["allowed"], "allowed"); acc.Str(made["name"]) != "Scratch Profile" || made["upgrade_allowed"] != true || len(allowed) != len(accepted)-1 || slices.Contains(allowed, worst) || acc.Str(made["cutoff"]) != acc.Str(hd["cutoff"]) || acc.Num(t, made["min_upgrade_format_score"], "min_upgrade_format_score") != 5 {
		t.Errorf("the profile added = %v, want %s without %s", made, profileHD, worst)
	}
	if msg := suite.CallErr(t, "qualityprofile_add", map[string]any{"name": "Scratch Profile", "copy_of": profileHD}); !strings.Contains(msg, "already a quality profile") {
		t.Errorf("a second profile of the name = %q", msg)
	}
	if msg := suite.CallErr(t, "qualityprofile_add", map[string]any{"name": "Empty Profile"}); !strings.Contains(msg, "accepts no quality") {
		t.Errorf("a profile that accepts nothing = %q", msg)
	}

	// made from nothing, it accepts what it is told to, and stops at the best of them
	scratch := suite.Call(t, "qualityprofile_add", map[string]any{"name": "From Nothing", "allow": []any{"Bluray-1080p", "Bluray-720p"}})
	if allowed := acc.Strs(t, scratch["allowed"], "allowed"); !slices.Equal(allowed, []string{"Bluray-1080p", "Bluray-720p"}) || acc.Str(scratch["cutoff"]) != "Bluray-1080p" {
		t.Errorf("a profile made from nothing = %v, want the two Blurays and a cutoff of the better", scratch)
	}
	again := suite.Call(t, "qualityprofile_edit", map[string]any{"profile": "Scratch Profile", "allow": []any{worst}})
	if len(acc.Strs(t, again["allowed"], "allowed")) != len(accepted) {
		t.Errorf("with %s allowed again, the profile accepts %v", worst, again["allowed"])
	}

	if msg := suite.CallErr(t, "qualityprofile_delete", map[string]any{"profile": profileHD, "confirm": true}); !strings.Contains(msg, "on "+profileHD) || !strings.Contains(msg, "movie_batch_edit") {
		t.Errorf("deleting a profile films are on = %q", msg)
	}
	for _, name := range []string{"From Nothing", "Scratch Profile"} {
		deleted(t, "qualityprofile_delete", map[string]any{"profile": name}, name, func() bool { return profile(name) != nil })
	}
}

// Radarr's own settings are shown without its API key and changed by name;
// one that decides how it is reached waits for confirm.
func TestServerSettings(t *testing.T) {
	skipUnlessReady(t)
	section := func(name string) map[string]any {
		one := acc.Rows(t, suite.Call(t, "server_settings", map[string]any{"section": name})["sections"], "sections")
		if len(one) != 1 || acc.Str(one[0]["section"]) != name {
			t.Fatalf("the %s section = %v", name, one)
		}

		return acc.Object(t, one[0]["settings"], "settings")
	}

	sections := rowsOf(t, "server_settings", "sections")
	host := named(sections, "section", "host")
	if len(sections) != 7 || host == nil || acc.Object(t, host["settings"], "settings")["apiKey"] != nil || !slices.Contains(acc.Strs(t, host["credentials"], "credentials"), "apiKey") || acc.Num(t, acc.Object(t, host["settings"], "settings")["port"], "port") != 7878 {
		t.Fatalf("Radarr's settings = %d sections, host %v", len(sections), host)
	}

	days := acc.Num(t, section("mediamanagement")["recycleBinCleanupDays"], "recycleBinCleanupDays")
	t.Cleanup(func() {
		_, _ = suite.Invoke("server_settings_edit", map[string]any{"section": "mediamanagement", "settings": map[string]any{"recycleBinCleanupDays": days}})
	})
	edited := suite.Call(t, "server_settings_edit", map[string]any{"section": "mediamanagement", "settings": map[string]any{"recyclebincleanupdays": days + 3}})
	if changed := acc.Rows(t, edited["changed"], "changed"); len(changed) != 1 || acc.Str(changed[0]["setting"]) != "recycleBinCleanupDays" || acc.Num(t, changed[0]["from"], "from") != days || acc.Num(t, changed[0]["to"], "to") != days+3 || acc.Num(t, acc.Object(t, edited["settings"], "settings")["recycleBinCleanupDays"], "now") != days+3 {
		t.Errorf("the edit = %v", edited)
	}
	if now := acc.Num(t, section("mediamanagement")["recycleBinCleanupDays"], "read back"); now != days+3 {
		t.Errorf("read back, the recycling bin is emptied after %d days, want %d", now, days+3)
	}
	if msg := suite.CallErr(t, "server_settings_edit", map[string]any{"section": "mediamanagement", "settings": map[string]any{"recycleBinCleanupDays": days + 3}}); !strings.Contains(msg, "already has that value") {
		t.Errorf("a change to what it already is = %q", msg)
	}

	// each of the other sections takes a change, keeps it, and takes the old value back
	for name, change := range map[string]map[string]any{"ui": {"firstDayOfWeek": 1}, "indexer": {"retention": 900}, "downloadclient": {"checkForFinishedDownloadInterval": 2}, "importlist": {"listSyncLevel": "logOnly"}, "metadata": {"certificationCountry": "gb"}} {
		for setting, to := range change {
			was := section(name)[setting]
			if out := suite.Call(t, "server_settings_edit", map[string]any{"section": name, "settings": map[string]any{setting: to}}); len(acc.Rows(t, out["changed"], "changed")) != 1 || len(acc.Rows(t, acc.OrEmptyList(out["not_kept"]), "not_kept")) != 0 {
				t.Errorf("%s.%s set to %v = %v", name, setting, to, out)
			}
			if back := suite.Call(t, "server_settings_edit", map[string]any{"section": name, "settings": map[string]any{setting: was}}); len(acc.Rows(t, back["changed"], "changed")) != 1 {
				t.Errorf("%s.%s put back to %v = %v", name, setting, was, back)
			}
		}
	}

	would := suite.Call(t, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"port": 9999}})
	if w := acc.Rows(t, would["would_change"], "would_change"); len(w) != 1 || acc.Num(t, w[0]["from"], "from") != 7878 || acc.Num(t, w[0]["to"], "to") != 9999 || len(acc.Rows(t, acc.OrEmptyList(would["changed"]), "changed")) != 0 || acc.Str(would["note"]) == "" {
		t.Errorf("a change of port, not confirmed = %v", would)
	}
	if port := acc.Num(t, section("host")["port"], "port"); port != 7878 {
		t.Errorf("the port after a change that was not confirmed = %d", port)
	}
	// a host setting that does not decide how Radarr is reached is sent without confirm. This Radarr lets anyone on its own network in without signing in and names no host it answers to, and Radarr saves nothing in the section until one of the two is put right: the refusal is its own, with its reason
	keep := acc.Num(t, section("host")["backupRetention"], "backupRetention")
	if msg := suite.CallErr(t, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"backupRetention": keep + 1}}); !strings.Contains(msg, "Allowed Hosts is required") {
		t.Errorf("a change to how many backups are kept = %q, want what Radarr said of its host settings", msg)
	}
	if now := acc.Num(t, section("host")["backupRetention"], "backupRetention"); now != keep {
		t.Errorf("after a change Radarr refused, it keeps %d backups, want %d", now, keep)
	}
	if msg := suite.CallErr(t, "server_settings_edit", map[string]any{"section": "host", "settings": map[string]any{"prot": 1}}); !strings.Contains(msg, `no setting "prot"`) || !strings.Contains(msg, "port") {
		t.Errorf("a setting the section does not have = %q, want the ones it has", msg)
	}
	if msg := suite.CallErr(t, "server_settings", map[string]any{"section": "naming2"}); !strings.Contains(msg, "no settings section") || !strings.Contains(msg, "mediamanagement") {
		t.Errorf("a section there is not = %q, want the ones there are", msg)
	}
}

// Running Radarr: the updates on offer, a backup deleted, a command taken out
// of the queue, and the folders it can reach. Putting a backup back, a
// restart and a shutdown are asked for and not confirmed: confirmed, each
// takes away the Radarr the rest of the suite is using.
func TestRunningRadarr(t *testing.T) {
	skipUnlessReady(t)

	t.Run("the updates on offer", func(t *testing.T) {
		testenv.Services.Offer(arrserver.Update{Version: "99.1.0.1", New: []string{"Something the suite made up"}, Fixed: []string{"A fault the suite made up"}})
		t.Cleanup(func() { testenv.Services.Offer() })

		out := suite.Call(t, "server_updates", nil)
		newest := named(acc.Rows(t, out["updates"], "updates"), "version", "99.1.0.1")
		if out["update_available"] != true || acc.Str(out["current"]) == "" || acc.Str(out["mechanism"]) != "docker" || newest == nil || newest["latest"] != true || newest["installed"] != false || !slices.Equal(acc.Strs(t, newest["new"], "new"), []string{"Something the suite made up"}) || !slices.Equal(acc.Strs(t, newest["fixed"], "fixed"), []string{"A fault the suite made up"}) {
			t.Errorf("the updates = %v", out)
		}
		testenv.Services.Offer()
		if none := suite.Call(t, "server_updates", nil); none["update_available"] != false || acc.Num(t, none["total"], "total") != 0 {
			t.Errorf("with nothing on offer, the updates = %v", none)
		}
	})

	t.Run("a backup is put back only when confirmed, and deleted", func(t *testing.T) {
		made := acc.Object(t, suite.Call(t, "server_backup_create", nil)["backup"], "backup")
		name := acc.Str(made["name"])
		t.Cleanup(func() { _, _ = suite.Invoke("server_backup_delete", map[string]any{"backup": name, "confirm": true}) })

		would := suite.Call(t, "server_backup_restore", map[string]any{"backup": name})
		if acc.Str(would["would_restore"]) != name || would["restored"] != nil || len(acc.Strs(t, would["overwrites"], "overwrites")) == 0 || acc.Str(would["note"]) == "" {
			t.Errorf("a restore that was not confirmed = %v", would)
		}
		if msg := suite.CallErr(t, "server_backup_restore", map[string]any{"backup": "no such backup", "confirm": true}); !strings.Contains(msg, "no backup") || !strings.Contains(msg, name) {
			t.Errorf("a backup that is not there = %q, want the ones there are", msg)
		}
		deleted(t, "server_backup_delete", map[string]any{"backup": name}, name, func() bool { return named(rowsOf(t, "server_backups", "backups"), "name", name) != nil })
	})

	t.Run("a restart and a shutdown wait to be confirmed", func(t *testing.T) {
		started := acc.Str(suite.Call(t, "server_info", nil)["start_time"])
		for _, tool := range []string{"server_restart", "server_shutdown"} {
			asked := suite.Call(t, tool, nil)
			if acc.Str(asked["would"]) == "" || asked["done"] != nil || acc.Str(asked["note"]) == "" || asked["interrupts"] == nil {
				t.Errorf("%s without confirm = %v", tool, asked)
			}
		}
		// and Radarr is the same one, still up
		if now := acc.Str(suite.Call(t, "server_info", nil)["start_time"]); now == "" || now != started {
			t.Errorf("Radarr started at %q before a restart and a shutdown that were not confirmed, and %q after", started, now)
		}
	})

	t.Run("a command is cancelled only while it waits", func(t *testing.T) {
		// Radarr runs three commands at a time. With the indexer slow to answer, three searches keep it busy and a fourth waits its turn: the one that can be cancelled
		indexer.SetDelay(3 * time.Second)
		t.Cleanup(func() { indexer.SetDelay(0) })
		films := []string{"Alien (1979)", "Aliens (1986)", "Blade Runner (1982)", "Arrival (2016)"}
		last := suite.Call(t, "movie_get", map[string]any{"movie": films[3]})
		before := len(indexer.Requests())
		ids := make([]int, 0, len(films))
		for _, film := range films {
			started := acc.Object(t, suite.Call(t, "movie_download", map[string]any{"movies": []any{film}, "wait": -1})["command"], "command")
			ids = append(ids, acc.Num(t, started["id"], "id"))
		}
		status := func(id int) string {
			one := acc.Rows(t, suite.Call(t, "task_list", map[string]any{"command_id": id})["commands"], "commands")
			if len(one) != 1 {
				t.Fatalf("command %d = %v", id, one)
			}

			return acc.Str(one[0]["status"])
		}

		waiting := suite.Call(t, "task_cancel", map[string]any{"command_id": ids[3]})
		if cmd := acc.Object(t, waiting["command"], "command"); waiting["cancelled"] != true || acc.Str(waiting["note"]) != "" || acc.Num(t, cmd["id"], "id") != ids[3] || acc.Str(cmd["status"]) != "queued" {
			t.Errorf("the search that was waiting, cancelled = %v, want it taken out of the queue", waiting)
		}
		// one that is running is left to run, and the answer says so
		running := suite.Call(t, "task_cancel", map[string]any{"command_id": ids[0]})
		if running["cancelled"] != false || !strings.Contains(acc.Str(running["note"]), "already running") || status(ids[0]) != "started" {
			t.Errorf("a search that was running, asked to stop = %v (now %s), want it left to run", running, status(ids[0]))
		}

		if !acc.EventuallyWithin(time.Minute, func() bool {
			return !slices.ContainsFunc(ids[:3], func(id int) bool { return status(id) != "completed" })
		}) {
			t.Fatalf("the three searches never finished: %s, %s, %s", status(ids[0]), status(ids[1]), status(ids[2]))
		}
		// once it is over, there is nothing to cancel
		if over := suite.Call(t, "task_cancel", map[string]any{"command_id": ids[0]}); over["cancelled"] != false || !strings.Contains(acc.Str(over["note"]), "already finished") {
			t.Errorf("a finished search asked to stop = %v", over)
		}
		// and the one that was cancelled never ran: the indexer was asked about three films, and not about the fourth
		asked := map[string]bool{}
		for _, r := range indexer.Requests()[before:] {
			if r.Function == "movie" {
				asked[r.Query.Get("tmdbid")+"/"+strings.TrimPrefix(r.Query.Get("imdbid"), "tt")] = true
			}
		}
		fourth := []string{itoa(acc.Num(t, last["tmdb_id"], "tmdb_id")), strings.TrimPrefix(acc.Str(last["imdb_id"]), "tt")}
		for film := range asked {
			if id, imdb, _ := strings.Cut(film, "/"); (id != "" && id == fourth[0]) || (imdb != "" && imdb == fourth[1]) {
				t.Errorf("the indexer was asked about %s, the film whose search was cancelled", films[3])
			}
		}
		if len(asked) != 3 {
			t.Errorf("the indexer was asked about %d films (%v), want the three whose searches ran", len(asked), asked)
		}
		if msg := suite.CallErr(t, "task_cancel", map[string]any{"command_id": 987654}); !strings.Contains(msg, "command 987654") {
			t.Errorf("a command that does not exist = %q", msg)
		}
	})

	t.Run("the folders Radarr can reach", func(t *testing.T) {
		media := suite.Call(t, "filesystem_browse", map[string]any{"path": "/media"})
		movies := named(acc.Rows(t, media["entries"], "entries"), "name", "movies")
		if movies == nil || strings.TrimSuffix(acc.Str(movies["path"]), "/") != moviesRoot || acc.Str(movies["type"]) != "folder" || acc.Num(t, media["total"], "total") < 3 {
			t.Fatalf("the folders under /media = %v", media)
		}
		inside := suite.Call(t, "filesystem_browse", map[string]any{"path": moviesRoot + "/Alien (1979)", "files": true})
		files := slices.DeleteFunc(acc.Rows(t, inside["entries"], "entries"), func(e map[string]any) bool { return acc.Str(e["type"]) != "file" })
		if len(files) == 0 || !strings.HasSuffix(acc.Str(files[0]["name"]), ".mkv") || acc.Num(t, files[0]["size"], "size") == 0 || !strings.HasPrefix(acc.Str(files[0]["path"]), moviesRoot+"/Alien (1979)/") {
			t.Errorf("Alien's folder = %v, want its film file with its size", inside)
		}
		// without files, a folder that holds only files lists none of them
		if bare := suite.Call(t, "filesystem_browse", map[string]any{"path": moviesRoot + "/Alien (1979)"}); slices.ContainsFunc(acc.Rows(t, acc.OrEmptyList(bare["entries"]), "entries"), func(e map[string]any) bool { return acc.Str(e["type"]) == "file" }) {
			t.Errorf("Alien's folder without files = %v", bare)
		}
	})
}

// A release handed to Radarr from elsewhere is decided on as one of its own
// searches would be: this one is for a film whose file already meets its
// cutoff, so it is turned down with why and nothing is sent anywhere.
func TestReleasePush(t *testing.T) {
	skipUnlessReady(t)

	const title = "Alien.1979.1080p.BluRay.x264-PUSH"
	out := acc.Rows(t, suite.Call(t, "release_push", map[string]any{"title": title, "download_url": indexer.URL() + "/pushed/" + title + ".nzb", "protocol": "usenet", "size": 8 << 30})["releases"], "releases")
	if len(out) != 1 || acc.Str(out[0]["title"]) != title || out[0]["approved"] != false || acc.Str(out[0]["quality"]) != "Bluray-1080p" || len(acc.Strs(t, acc.OrEmptyList(out[0]["rejections"]), "rejections")) == 0 {
		t.Errorf("the release pushed = %v, want it read as a 1080p BluRay and turned down with why", out)
	}
	if _, err := os.Stat(testenv.HostPath(nzbFolder + "/" + title + ".nzb")); !os.IsNotExist(err) {
		t.Errorf("a release Radarr turned down reached the download client: %v", err)
	}
	if msg := suite.CallErr(t, "release_push", map[string]any{"title": title, "download_url": "http://x.example/a.nzb", "protocol": "pigeon"}); !strings.Contains(msg, "want usenet or torrent") {
		t.Errorf("a protocol there is not = %q", msg)
	}
}

// A release Radarr is holding back is sent on its way: a delay profile made
// with the tools holds a release the RSS sync finds for a film with its tag,
// and queue_grab sends it to the download client now.
//
//nolint:paralleltest // the steps share one film and one download client, in order
func TestQueueGrab(t *testing.T) {
	skipUnlessReady(t)

	held := newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Primer.2004.1080p.BluRay.x264-HELD", TMDBID: downloadTmdb, IMDBID: downloadImdb, Size: 8 << 30, PubDate: time.Now()}
	if err := indexer.Offer(held); err != nil {
		t.Fatal(err)
	}
	profile := acc.Object(t, suite.Call(t, "delayprofile_add", map[string]any{"values": map[string]any{"enableUsenet": true, "enableTorrent": true, "preferredProtocol": "usenet", "usenetDelay": 600, "bypassIfHighestQuality": false, "bypassIfAboveCustomFormatScore": false}, "tags": []any{"wait-for-it"}})["saved"], "saved")
	suite.Call(t, "movie_add", map[string]any{"movie": "tmdb:14337", "quality_profile": profileHD, "root_folder": moviesRoot, "tags": []any{"wait-for-it"}})
	queued := func() []map[string]any {
		out, err := suite.Invoke("queue_list", map[string]any{"movie": "tmdb:14337"})
		if err != nil {
			return nil
		}

		return acc.RowsOf(out["items"])
	}
	t.Cleanup(func() {
		indexer.Withdraw(held.Title)
		for _, q := range queued() {
			_, _ = suite.Invoke("queue_remove", map[string]any{"ids": []any{q["id"]}, "remove_from_client": true})
		}
		_, _ = suite.Invoke("movie_delete", map[string]any{"movie": "tmdb:14337", "delete_files": true, "confirm": true})
		_, _ = suite.Invoke("delayprofile_delete", map[string]any{"id": itoa(acc.NumOr0(profile["id"])), "confirm": true})
		_, _ = suite.Invoke("tag_delete", map[string]any{"tag": "wait-for-it"})
		for _, dir := range []string{watchFolder, nzbFolder} {
			entries, _ := os.ReadDir(testenv.HostPath(dir))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "Primer") {
					_ = os.RemoveAll(filepath.Join(testenv.HostPath(dir), e.Name()))
				}
			}
		}
		_ = os.RemoveAll(testenv.HostPath(downloadFolder))
	})

	// a search someone asked for ignores delays, so it is the RSS sync that has to find it
	if out := suite.Call(t, "task_run", map[string]any{"task": "RssSync"}); acc.Str(acc.Object(t, out["command"], "command")["status"]) != "completed" {
		t.Fatalf("the RSS sync = %v", out)
	}
	var waiting map[string]any
	if !acc.EventuallyWithin(time.Minute, func() bool {
		waiting = named(queued(), "title", held.Title)
		return waiting != nil
	}) || acc.Str(waiting["status"]) != "delay" {
		t.Fatalf("the release is not waiting in the queue: %v of %v", waiting, queued())
	}
	if _, err := os.Stat(testenv.HostPath(nzbFolder + "/" + held.Title + ".nzb")); !os.IsNotExist(err) {
		t.Fatalf("a release that is waiting has already reached the download client: %v", err)
	}

	grabbed := acc.Rows(t, suite.Call(t, "queue_grab", map[string]any{"ids": []any{waiting["id"]}})["grabbed"], "grabbed")
	if len(grabbed) != 1 || acc.Str(grabbed[0]["title"]) != held.Title || acc.Str(grabbed[0]["status"]) != "delay" {
		t.Errorf("the release grabbed = %v", grabbed)
	}
	if !acc.EventuallyWithin(time.Minute, func() bool {
		_, err := os.Stat(testenv.HostPath(nzbFolder + "/" + held.Title + ".nzb"))
		return err == nil
	}) {
		t.Errorf("the download client never got %s", held.Title)
	}
	// it is in the film's history as a grab, and no longer waiting
	if !acc.EventuallyWithin(time.Minute, func() bool { return slices.Contains(events(t), "grabbed") }) {
		t.Errorf("the film's history after the grab = %v", events(t))
	}
	if still := named(queued(), "title", held.Title); still != nil && acc.Str(still["status"]) == "delay" {
		t.Errorf("after the grab, the release is still waiting: %v", still)
	}
	if msg := suite.CallErr(t, "queue_grab", map[string]any{"ids": []any{987654}}); !strings.Contains(msg, "no queue item 987654") {
		t.Errorf("a queue item that is not there = %q", msg)
	}
}

// No answer carries a key. Every key the suite gave Radarr, and Radarr's own,
// is looked for in what every tool that reads answers, after an indexer has
// been made to fail so that Radarr has something to say with a key in it, and
// with a notification whose address carries a token. It is the last test, so
// what it leaves Radarr saying of a failing indexer is seen by no other.
//
//nolint:paralleltest // it takes an indexer down and reads what every tool then says
func TestNoAnswerCarriesAKey(t *testing.T) {
	skipUnlessReady(t)

	key, token, password := suite.Secret("a-second-key-nobody-should-be-shown"), suite.Secret("a-token-in-an-address"), suite.Secret("a-password-for-the-hook")
	leaky, err := indexers.Add(newznab.Site{Name: "leaky", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := leaky.Offer(background); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		leaky.SetFailure(newznab.Failure{})
		_, _ = suite.Invoke("indexer_delete", map[string]any{"name": "Leaky Indexer", "confirm": true})
		_, _ = suite.Invoke("notification_delete", map[string]any{"name": "Hook With A Token", "confirm": true})
		_, _ = suite.Invoke("importlist_delete", map[string]any{"name": "Itself Again", "confirm": true})
	})

	// what the adds answer is looked through with the rest
	var said []string
	keep := func(tool string, out map[string]any) {
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		said = append(said, tool+": "+string(raw))
	}
	keep("indexer_add", suite.Call(t, "indexer_add", map[string]any{"kind": "Newznab", "name": "Leaky Indexer", "settings": map[string]any{"baseUrl": leaky.URL(), "apiPath": "/api", "apiKey": key, "categories": []any{2000, 2040}}}))
	keep("notification_add", suite.Call(t, "notification_add", map[string]any{"kind": "Webhook", "name": "Hook With A Token", "off": true, "settings": map[string]any{"url": "http://127.0.0.1:9/hook?token=" + token, "method": "POST", "username": "radarr", "password": password}}))
	hd := named(rowsOf(t, "qualityprofile_list", "profiles"), "name", profileHD)
	keep("importlist_add", suite.Call(t, "importlist_add", map[string]any{"kind": "RadarrImport", "name": "Itself Again", "off": true, "settings": map[string]any{"baseUrl": "http://localhost:7878", "apiKey": testenv.Token()}, "options": map[string]any{"qualityProfileId": hd["id"], "rootFolderPath": moviesRoot}}))

	// the indexer goes down, and Radarr runs into it every way it does: a test of it, a search through it, a read of its feed, and its own check of its health
	leaky.SetFailure(newznab.Failure{Status: 500})
	for tool, args := range map[string]map[string]any{"release_search": {"movie": "Alien (1979)"}, "task_run": {"task": "RssSync"}} {
		if out, err := suite.Invoke(tool, args); err == nil {
			keep(tool, out)
		} else {
			said = append(said, tool+": "+err.Error())
		}
	}
	keep("task_run", suite.Call(t, "task_run", map[string]any{"task": "CheckHealth"}))

	// every tool that only reads, with what the ones that need naming something are given; one that still refuses is looked through as well, since what a tool refuses with is read too
	given := map[string]map[string]any{
		"movie_get": {"movie": "Alien (1979)"}, "release_search": {"movie": "Aliens (1986)"}, "server_logs": {"limit": 500}, "server_log_file": {"name": "radarr.txt", "tail": 500},
		"provider_kinds": {"provider": "indexer", "kind": "Newznab"}, "provider_options": {"provider": "indexer", "name": "Leaky Indexer", "setting": "categories"},
		"queue_list": {"include_unknown": true}, "history_list": {"limit": 500}, "filesystem_browse": {"path": "/config", "files": true},
	}
	reads := toolsFor(t, tools.Options{Toolsets: []string{"all"}, ReadOnly: true})
	for _, tool := range reads {
		out, err := suite.Invoke(tool, given[tool])
		if err != nil {
			said = append(said, tool+": "+err.Error())
			continue
		}
		keep(tool, out)
	}
	if len(reads) < 60 || len(said) < len(reads) {
		t.Fatalf("%d read tools answered %d times: the sweep did not reach them all", len(reads), len(said))
	}

	for name, secret := range map[string]string{"the failing indexer's API key": key, "the other indexer's API key": indexerKey, "Radarr's own API key": testenv.Token(), "the token in the notification's address": token, "the notification's password": password} {
		for _, text := range said {
			if at := strings.Index(text, secret); at >= 0 {
				t.Errorf("%s is shown by %s", name, text[max(0, at-300):min(len(text), at+len(secret)+60)])
			}
		}
	}
	// and the sweep saw the thing it is there for: Radarr's words for the indexer that is down, with the address it asked and the key gone from it
	if !slices.ContainsFunc(said, func(text string) bool {
		return strings.Contains(text, "apikey=REDACTED") && strings.Contains(text, leaky.URL())
	}) {
		t.Error("nothing the tools answered quoted the address Radarr asked the failing indexer at: the sweep proves nothing unless Radarr had a key to give away")
	}
}
