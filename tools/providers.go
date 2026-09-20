package tools

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/client"
	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type indexerRow struct {
	ID                      int      `json:"id"`
	Name                    string   `json:"name"`
	Implementation          string   `json:"implementation"            jsonschema:"Newznab, Torznab, ..."`
	Protocol                string   `json:"protocol"                  jsonschema:"usenet or torrent"`
	EnableRSS               bool     `json:"enable_rss"                jsonschema:"read its feed on the RSS sync"`
	EnableAutomaticSearch   bool     `json:"enable_automatic_search"`
	EnableInteractiveSearch bool     `json:"enable_interactive_search"`
	Priority                int      `json:"priority"                  jsonschema:"1 is tried first"`
	Tags                    []string `json:"tags,omitempty"            jsonschema:"only films with these tags use it"`
}

type downloadClientRow struct {
	ID                       int      `json:"id"`
	Name                     string   `json:"name"`
	Implementation           string   `json:"implementation"             jsonschema:"SABnzbd, qBittorrent, Usenet Blackhole, ..."`
	Protocol                 string   `json:"protocol"`
	Enabled                  bool     `json:"enabled"`
	Priority                 int      `json:"priority"`
	RemoveCompletedDownloads bool     `json:"remove_completed_downloads"`
	RemoveFailedDownloads    bool     `json:"remove_failed_downloads"`
	Tags                     []string `json:"tags,omitempty"`
}

// testResult is one indexer's or download client's answer to a test.
type testResult struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Valid    bool     `json:"valid"`
	Failures []string `json:"failures,omitempty" jsonschema:"what failed, as Radarr says it; a warning says so"`
}

// testResults reads a test of every provider: a result each, and 400 Bad
// Request, with the same list, when any fails.
func testResults(model []radarr.ProviderTestAllResult, resp *http.Response, err error) ([]radarr.ProviderTestAllResult, error) {
	if err == nil {
		return model, nil
	}
	if client.StatusCode(err) != http.StatusBadRequest {
		return nil, err
	}
	var out []radarr.ProviderTestAllResult
	if decodeBody(resp, &out) != nil {
		return nil, err
	}

	return out, nil
}

// failuresOf reads the validation failures a test of one provider answers
// 400 with, or the error itself when the answer has none.
func failuresOf(resp *http.Response, err error) []radarr.ValidationFailure {
	var failures []radarr.ValidationFailure
	if decodeBody(resp, &failures) != nil || len(failures) == 0 {
		failures = append(failures, radarr.ValidationFailure{ErrorMessage: err.Error()})
	}

	return failures
}

// isWarning reports whether a failure is only a warning. A test of one
// provider says so with isWarning, a test of every provider with severity.
func isWarning(f *radarr.ValidationFailure) bool {
	return isTrue(f.IsWarning) || strings.EqualFold(f.Severity, "warning")
}

func toResults(tests []radarr.ProviderTestAllResult, names map[int]string) []testResult {
	out := make([]testResult, 0, len(tests))
	for _, t := range tests {
		row := testResult{ID: t.Id, Name: names[t.Id], Valid: isTrue(t.IsValid)}
		for i := range t.ValidationFailures {
			msg := t.ValidationFailures[i].ErrorMessage
			if isWarning(&t.ValidationFailures[i]) {
				msg = "warning: " + msg
			}
			row.Failures = append(row.Failures, msg)
		}
		out = append(out, row)
	}

	return out
}

func registerProviderTools(r *registry) {
	c := r.client

	type indexerListOut struct {
		Indexers []indexerRow `json:"indexers"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "indexer_list",
		Description: "List the indexers Radarr searches for releases: what kind each is, whether it is used for the RSS sync, automatic and interactive searches, its priority, and the tags that limit it to some films.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, indexerListOut, error) {
		res, err := c.GetIndexer(ctx)
		if err != nil {
			return nil, indexerListOut{}, err
		}
		lk, err := loadLookups(ctx, c)
		if err != nil {
			return nil, indexerListOut{}, err
		}
		out := indexerListOut{}
		for _, i := range res.Model {
			out.Indexers = append(out.Indexers, indexerRow{
				ID: i.Id, Name: i.Name, Implementation: i.ImplementationName, Protocol: string(i.Protocol),
				EnableRSS: isTrue(i.EnableRss), EnableAutomaticSearch: isTrue(i.EnableAutomaticSearch), EnableInteractiveSearch: isTrue(i.EnableInteractiveSearch),
				Priority: i.Priority, Tags: lk.tagLabels(i.Tags),
			})
		}
		slices.SortFunc(out.Indexers, func(a, b indexerRow) int { return a.Priority - b.Priority })

		return nil, out, nil
	})

	type testIn struct {
		Name string `json:"name,omitempty" jsonschema:"test only this one, by name or id; default tests them all"`
	}
	type testOut struct {
		Results []testResult `json:"results"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "indexer_test",
		Description: "Test the indexers - can Radarr reach each, is its API key accepted, does it answer a search - and say what failed for each one that did.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in testIn) (*mcp.CallToolResult, testOut, error) {
		res, err := c.GetIndexer(ctx)
		if err != nil {
			return nil, testOut{}, err
		}
		names := map[int]string{}
		var pick *radarr.IndexerResource
		for i := range res.Model {
			names[res.Model[i].Id] = res.Model[i].Name
			if in.Name != "" && (strings.EqualFold(res.Model[i].Name, in.Name) || strconv.Itoa(res.Model[i].Id) == in.Name) {
				pick = &res.Model[i]
			}
		}
		var results []radarr.ProviderTestAllResult
		switch {
		case in.Name != "" && pick == nil:
			return nil, testOut{}, fmt.Errorf("no indexer %q; indexer_list names them", in.Name)
		case pick != nil:
			one, err := c.PostIndexerTest(ctx, *pick, radarr.PostIndexerTestOperationOptions{ForceTest: new(true)})
			results = []radarr.ProviderTestAllResult{{Id: pick.Id, IsValid: new(err == nil)}}
			if err != nil {
				results[0].ValidationFailures = failuresOf(one.HttpResponse, err)
			}
		default:
			all, err := c.PostIndexerTestall(ctx)
			if results, err = testResults(all.Model, all.HttpResponse, err); err != nil {
				return nil, testOut{}, err
			}
		}

		return nil, testOut{Results: toResults(results, names)}, nil
	})

	type downloadClientListOut struct {
		DownloadClients []downloadClientRow `json:"download_clients"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "downloadclient_list",
		Description: "List the download clients Radarr sends grabs to: what kind each is, whether it is enabled, its priority, whether Radarr removes finished and failed downloads from it, and the tags that limit it to some films.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, downloadClientListOut, error) {
		res, err := c.GetDownloadclient(ctx)
		if err != nil {
			return nil, downloadClientListOut{}, err
		}
		lk, err := loadLookups(ctx, c)
		if err != nil {
			return nil, downloadClientListOut{}, err
		}
		out := downloadClientListOut{}
		for _, d := range res.Model {
			out.DownloadClients = append(out.DownloadClients, downloadClientRow{
				ID: d.Id, Name: d.Name, Implementation: d.ImplementationName, Protocol: string(d.Protocol), Enabled: isTrue(d.Enable),
				Priority: d.Priority, RemoveCompletedDownloads: isTrue(d.RemoveCompletedDownloads), RemoveFailedDownloads: isTrue(d.RemoveFailedDownloads),
				Tags: lk.tagLabels(d.Tags),
			})
		}
		slices.SortFunc(out.DownloadClients, func(a, b downloadClientRow) int { return a.Priority - b.Priority })

		return nil, out, nil
	})

	add(r, readTool, &mcp.Tool{
		Name:        "downloadclient_test",
		Description: "Test the download clients - can Radarr reach each, log in, and see its folders - and say what failed for each one that did.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in testIn) (*mcp.CallToolResult, testOut, error) {
		res, err := c.GetDownloadclient(ctx)
		if err != nil {
			return nil, testOut{}, err
		}
		names := map[int]string{}
		var pick *radarr.DownloadClientResource
		for i := range res.Model {
			names[res.Model[i].Id] = res.Model[i].Name
			if in.Name != "" && (strings.EqualFold(res.Model[i].Name, in.Name) || strconv.Itoa(res.Model[i].Id) == in.Name) {
				pick = &res.Model[i]
			}
		}
		var results []radarr.ProviderTestAllResult
		switch {
		case in.Name != "" && pick == nil:
			return nil, testOut{}, fmt.Errorf("no download client %q; downloadclient_list names them", in.Name)
		case pick != nil:
			one, err := c.PostDownloadclientTest(ctx, *pick, radarr.PostDownloadclientTestOperationOptions{ForceTest: new(true)})
			results = []radarr.ProviderTestAllResult{{Id: pick.Id, IsValid: new(err == nil)}}
			if err != nil {
				results[0].ValidationFailures = failuresOf(one.HttpResponse, err)
			}
		default:
			all, err := c.PostDownloadclientTestall(ctx)
			if results, err = testResults(all.Model, all.HttpResponse, err); err != nil {
				return nil, testOut{}, err
			}
		}

		return nil, testOut{Results: toResults(results, names)}, nil
	})
}
