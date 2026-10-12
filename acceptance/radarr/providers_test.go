//go:build acceptance

package radarr_test

import (
	"context"
	"encoding/json"
	"io"
	"iter"
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

// The providers - indexers, download clients, import lists, notifications
// and metadata consumers - share one controller in Radarr, so each is put
// through the same round: create, update, test one, test all, run an
// action, edit in bulk and delete in bulk where there is a bulk edit, and
// delete. Radarr tests a provider before it saves one, so each is one that
// works here: the fake indexer, a blackhole on folders the test makes, the
// "another Radarr" list pointed at this one, the webhook receiver, and a
// metadata consumer, which only writes files.

// testResult finds a provider's result in a test of every provider.
func testResult(t *testing.T, results []radarr.ProviderTestAllResult, id int) radarr.ProviderTestAllResult {
	t.Helper()

	i := slices.IndexFunc(results, func(r radarr.ProviderTestAllResult) bool { return r.Id == id })
	if i < 0 {
		t.Fatalf("no result for %d among %d", id, len(results))
	}

	return results[i]
}

// decodeAnswer decodes the body of an answer the SDK returned an error for.
func decodeAnswer[T any](t *testing.T, resp *http.Response) T {
	t.Helper()

	var v T
	if resp == nil {
		t.Fatal("no answer")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}

	return v
}

// hookEvents yields the webhook bodies of one event type.
func hookEvents(eventType string, bodies []string) iter.Seq[map[string]any] {
	return func(yield func(map[string]any) bool) {
		for _, body := range bodies {
			var event map[string]any
			if json.Unmarshal([]byte(body), &event) != nil || event["eventType"] != eventType {
				continue
			}
			if !yield(event) {
				return
			}
		}
	}
}

// actionOptions reads the options a provider action answers with, the
// choices the settings form offers for one of its fields.
func actionOptions(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()

	var answer struct {
		Options []map[string]any `json:"options"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("the action answered %s: %v", raw, err)
	}

	return answer.Options
}

//nolint:paralleltest // the tests share one server
func TestIndexers(t *testing.T) {
	ctx := skipUnlessUp(t)
	d := downloads(t)

	// two more of the fake indexer: one to delete by id, one in bulk
	ids := make([]int, 0, 2)
	for _, name := range []string{"SDK Indexer B", "SDK Indexer C"} {
		ix := fakeIndexer()
		ix.Name = name
		created := must(sdk.PostIndexer(ctx, ix, radarr.PostIndexerOptions{})).Answer
		removeAfter(ctx, t, name, func(ctx context.Context) (radarr.DeleteIndexerByIdResponse, error) {
			return sdk.DeleteIndexerById(ctx, created.Id)
		})
		ids = append(ids, created.Id)
	}
	b := must(sdk.GetIndexerById(ctx, ids[0])).Answer

	edit := *b
	edit.Priority = 30
	b = must(sdk.PutIndexerById(ctx, b.Id, edit, radarr.PutIndexerByIdOptions{})).Answer
	if b.Priority != 30 {
		t.Errorf("priority %d after the update, want 30", b.Priority)
	}

	must(sdk.PostIndexerTest(ctx, *b, radarr.PostIndexerTestOptions{}))
	all := must(sdk.PostIndexerTestAll(ctx)).Answer
	for _, id := range []int{d.indexer, ids[0], ids[1]} {
		if r := testResult(t, all, id); !r.IsValid {
			t.Errorf("indexer %d failed its test: %+v", id, r.ValidationFailures)
		}
	}

	// the categories a Newznab indexer offers are an action on it, asked of
	// the indexer itself
	categories := actionOptions(t, must(sdk.PostIndexerActionByName(ctx, "newznabCategories", *b)).Answer)
	if len(categories) == 0 {
		t.Error("the indexer offers no categories")
	}

	changed := must(sdk.PutIndexerBulk(ctx, radarr.IndexerBulkResource{Ids: ids, Priority: new(40)})).Answer
	if len(changed) != 2 || changed[0].Priority != 40 || changed[1].Priority != 40 {
		t.Errorf("the bulk edit answered %d indexers, want both at priority 40: %+v", len(changed), changed)
	}

	must(sdk.DeleteIndexerBulk(ctx, radarr.IndexerBulkResource{Ids: ids[1:]}))
	res, err := sdk.GetIndexerById(ctx, ids[1])
	gone(t, "the bulk-deleted indexer", res, err)
	must(sdk.DeleteIndexerById(ctx, ids[0]))
	res, err = sdk.GetIndexerById(ctx, ids[0])
	gone(t, "the indexer", res, err)
}

//nolint:paralleltest // the tests share one server
func TestDownloadClients(t *testing.T) {
	ctx := skipUnlessUp(t)
	downloads(t)

	// two more blackholes, each on folders of its own: two watching one
	// folder would each report every download in it
	ids := make([]int, 0, 2)
	for _, name := range []string{"b", "c"} {
		c := blackhole("SDK Blackhole " + strings.ToUpper(name))
		folders := "/media/downloads/sdk-" + name
		c.Fields = []radarr.Field{{Name: "nzbFolder", Value: folders + "/nzb"}, {Name: "watchFolder", Value: folders + "/watch"}}
		mediaMkdir(testenv.HostPath(folders + "/nzb"))
		mediaMkdir(testenv.HostPath(folders + "/watch"))
		t.Cleanup(func() { _ = os.RemoveAll(testenv.HostPath(folders)) })
		created := must(sdk.PostDownloadClient(ctx, c, radarr.PostDownloadClientOptions{})).Answer
		removeAfter(ctx, t, c.Name, func(ctx context.Context) (radarr.DeleteDownloadClientByIdResponse, error) {
			return sdk.DeleteDownloadClientById(ctx, created.Id)
		})
		ids = append(ids, created.Id)
	}
	b := must(sdk.GetDownloadClientById(ctx, ids[0])).Answer

	edit := *b
	edit.Priority = 5
	b = must(sdk.PutDownloadClientById(ctx, b.Id, edit, radarr.PutDownloadClientByIdOptions{})).Answer
	if b.Priority != 5 {
		t.Errorf("priority %d after the update, want 5", b.Priority)
	}

	must(sdk.PostDownloadClientTest(ctx, *b, radarr.PostDownloadClientTestOptions{}))
	for _, r := range must(sdk.PostDownloadClientTestAll(ctx)).Answer {
		if !r.IsValid {
			t.Errorf("download client %d failed its test: %+v", r.Id, r.ValidationFailures)
		}
	}

	// a test of every client answers 400 when one fails, with every
	// client's result: a blackhole fails once its folders are gone
	if err := os.RemoveAll(testenv.HostPath("/media/downloads/sdk-c")); err != nil {
		t.Fatal(err)
	}
	res, err := sdk.PostDownloadClientTestAll(ctx)
	if err != nil || res.HttpResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("a test of every client with one failing: want 400 and its results, got %v", err)
	}
	results := res.Answer
	if r := testResult(t, results, ids[1]); r.IsValid || len(r.ValidationFailures) == 0 || r.ValidationFailures[0].Severity != "error" {
		t.Errorf("the client without folders: %+v", r)
	}
	if r := testResult(t, results, ids[0]); !r.IsValid {
		t.Errorf("the client with folders failed too: %+v", r.ValidationFailures)
	}
	// and a test of the one client answers 400 with its failures, which
	// say whether each is only a warning
	c := must(sdk.GetDownloadClientById(ctx, ids[1])).Answer
	one, err := sdk.PostDownloadClientTest(ctx, *c, radarr.PostDownloadClientTestOptions{})
	if client.StatusCode(err) != http.StatusBadRequest {
		t.Fatalf("a test of the client without folders: want 400, got %v", err)
	}
	if failures := decodeAnswer[[]radarr.ValidationFailure](t, one.HttpResponse); len(failures) == 0 || failures[0].ErrorMessage == "" {
		t.Errorf("the failed test answered %+v", failures)
	}

	// a blackhole has no actions: an action it does not have answers null
	if act := must(sdk.PostDownloadClientActionByName(ctx, "sdk-no-such-action", *b)).Answer; string(act) != "null" {
		t.Errorf("an action the client does not have answered %s", act)
	}

	changed := must(sdk.PutDownloadClientBulk(ctx, radarr.DownloadClientBulkResource{Ids: ids[:1], Priority: new(10)})).Answer
	if len(changed) != 1 || changed[0].Priority != 10 {
		t.Errorf("the bulk edit answered %+v, want the one client at priority 10", changed)
	}

	must(sdk.DeleteDownloadClientBulk(ctx, radarr.DownloadClientBulkResource{Ids: ids[1:]}))
	gotten, err := sdk.GetDownloadClientById(ctx, ids[1])
	gone(t, "the bulk-deleted client", gotten, err)
	must(sdk.DeleteDownloadClientById(ctx, ids[0]))
	gotten, err = sdk.GetDownloadClientById(ctx, ids[0])
	gone(t, "the client", gotten, err)
}

//nolint:paralleltest // the tests share one server
func TestImportLists(t *testing.T) {
	ctx := skipUnlessUp(t)
	fixtures(t)

	ids := make([]int, 0, 2)
	for _, name := range []string{"SDK List B", "SDK List C"} {
		l := selfList(name)
		// enabled, so a test of every list includes it; nothing is added
		// automatically
		l.Enabled = new(true)
		created := must(sdk.PostImportList(ctx, l, radarr.PostImportListOptions{})).Answer
		removeAfter(ctx, t, name, func(ctx context.Context) (radarr.DeleteImportListByIdResponse, error) {
			return sdk.DeleteImportListById(ctx, created.Id)
		})
		ids = append(ids, created.Id)
	}
	b := must(sdk.GetImportListById(ctx, ids[0])).Answer

	edit := *b
	edit.Name = "SDK List B Renamed"
	b = must(sdk.PutImportListById(ctx, b.Id, edit, radarr.PutImportListByIdOptions{})).Answer
	if b.Name != edit.Name {
		t.Errorf("named %q after the update", b.Name)
	}

	must(sdk.PostImportListTest(ctx, *b, radarr.PostImportListTestOptions{}))
	all := must(sdk.PostImportListTestAll(ctx)).Answer
	for _, id := range ids {
		if r := testResult(t, all, id); !r.IsValid {
			t.Errorf("list %d failed its test: %+v", id, r.ValidationFailures)
		}
	}

	// the profiles of the other Radarr are an action on the list, which
	// asks that Radarr: this one, whose profiles include HD-1080p
	profiles := actionOptions(t, must(sdk.PostImportListActionByName(ctx, "getProfiles", *b)).Answer)
	if !slices.ContainsFunc(profiles, func(o map[string]any) bool { return o["name"] == "HD-1080p" }) {
		t.Errorf("the other Radarr's profiles: %v", profiles)
	}

	changed := must(sdk.PutImportListBulk(ctx, radarr.ImportListBulkResource{Ids: ids, EnableAuto: new(false), Enabled: new(false)})).Answer
	if len(changed) != 2 || isTrue(changed[0].Enabled) || isTrue(changed[1].Enabled) {
		t.Errorf("the bulk edit answered %d lists, want both disabled", len(changed))
	}

	must(sdk.DeleteImportListBulk(ctx, radarr.ImportListBulkResource{Ids: ids[1:]}))
	res, err := sdk.GetImportListById(ctx, ids[1])
	gone(t, "the bulk-deleted list", res, err)
	must(sdk.DeleteImportListById(ctx, ids[0]))
	res, err = sdk.GetImportListById(ctx, ids[0])
	gone(t, "the list", res, err)
}

//nolint:paralleltest // the tests share one server
func TestNotifications(t *testing.T) {
	ctx := skipUnlessUp(t)

	// Radarr tests a notification before saving it, which sends the
	// webhook a test event
	before := len(hooks.received())
	n := must(sdk.PostNotification(ctx, webhook(t, "SDK Hook"), radarr.PostNotificationOptions{})).Answer
	removeAfter(ctx, t, "the webhook", func(ctx context.Context) (radarr.DeleteNotificationByIdResponse, error) {
		return sdk.DeleteNotificationById(ctx, n.Id)
	})
	tests := func() int { return len(slices.Collect(hookEvents("Test", hooks.received()[before:]))) }
	if tests() == 0 {
		t.Errorf("saving the webhook sent it no test event: %v", hooks.received()[before:])
	}

	edit := *n
	edit.Name = "SDK Hook Renamed"
	edit.OnGrab = new(false)
	n = must(sdk.PutNotificationById(ctx, n.Id, edit, radarr.PutNotificationByIdOptions{})).Answer
	if n.Name != edit.Name || isTrue(n.OnGrab) {
		t.Errorf("named %q, on grab %v after the update", n.Name, isTrue(n.OnGrab))
	}

	sent := tests()
	must(sdk.PostNotificationTest(ctx, *n, radarr.PostNotificationTestOptions{}))
	if !poll(10*time.Second, func() bool { return tests() > sent }) {
		t.Error("a test of the webhook sent it nothing")
	}
	if r := testResult(t, must(sdk.PostNotificationTestAll(ctx)).Answer, n.Id); !r.IsValid {
		t.Errorf("the webhook failed its test: %+v", r.ValidationFailures)
	}
	if act := must(sdk.PostNotificationActionByName(ctx, "sdk-no-such-action", *n)).Answer; string(act) != "null" {
		t.Errorf("an action the webhook does not have answered %s", act)
	}

	must(sdk.DeleteNotificationById(ctx, n.Id))
	res, err := sdk.GetNotificationById(ctx, n.Id)
	gone(t, "the webhook", res, err)
}

//nolint:paralleltest // the tests share one server
func TestMetadataConsumers(t *testing.T) {
	ctx := skipUnlessUp(t)

	// Radarr creates one of each consumer on first start, all disabled; a
	// second Kodi one, enabled, is what the test adds
	var kodi *radarr.MetadataResource
	for _, m := range must(sdk.GetMetadata(ctx)).Answer {
		if m.Implementation == "XbmcMetadata" {
			kodi = &m
			break
		}
	}
	if kodi == nil {
		t.Fatal("no Kodi metadata consumer")
	}
	fresh := *kodi
	fresh.Id = 0
	fresh.Name = "SDK Metadata"
	fresh.Enable = new(true)
	m := must(sdk.PostMetadata(ctx, fresh, radarr.PostMetadataOptions{})).Answer
	removeAfter(ctx, t, "the consumer", func(ctx context.Context) (radarr.DeleteMetadataByIdResponse, error) {
		return sdk.DeleteMetadataById(ctx, m.Id)
	})
	if m.Id == kodi.Id {
		t.Fatalf("creating a consumer answered the existing one, %d", m.Id)
	}

	edit := *m
	edit.Name = "SDK Metadata Renamed"
	m = must(sdk.PutMetadataById(ctx, m.Id, edit, radarr.PutMetadataByIdOptions{})).Answer
	if m.Name != edit.Name {
		t.Errorf("named %q after the update", m.Name)
	}

	must(sdk.PostMetadataTest(ctx, *m, radarr.PostMetadataTestOptions{}))
	if r := testResult(t, must(sdk.PostMetadataTestAll(ctx)).Answer, m.Id); !r.IsValid {
		t.Errorf("the consumer failed its test: %+v", r.ValidationFailures)
	}
	if act := must(sdk.PostMetadataActionByName(ctx, "sdk-no-such-action", *m)).Answer; string(act) != "null" {
		t.Errorf("an action the consumer does not have answered %s", act)
	}

	must(sdk.DeleteMetadataById(ctx, m.Id))
	res, err := sdk.GetMetadataById(ctx, m.Id)
	gone(t, "the consumer", res, err)
}
