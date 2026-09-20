package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// queueRow is one download as Radarr tracks it.
type queueRow struct {
	ID                  int      `json:"id"`
	Title               string   `json:"title"                          jsonschema:"the release name"`
	Movie               string   `json:"movie,omitempty"                jsonschema:"the film it is for, as Title (Year); empty when Radarr could not tell"`
	MovieID             int      `json:"movie_id,omitempty"`
	Status              string   `json:"status"                         jsonschema:"the download client's word for it: queued, paused, downloading, completed, failed, warning, delay"`
	State               string   `json:"state,omitempty"                jsonschema:"where Radarr has it: downloading, importPending, importBlocked, importing, imported, failedPending, failed or ignored"`
	Health              string   `json:"health,omitempty"               jsonschema:"ok, warning or error"`
	Messages            []string `json:"messages,omitempty"             jsonschema:"why it is stuck, as Radarr explains it"`
	ErrorMessage        string   `json:"error_message,omitempty"`
	Quality             string   `json:"quality,omitempty"`
	Size                int64    `json:"size"                           jsonschema:"bytes"`
	SizeLeft            int64    `json:"size_left"                      jsonschema:"bytes"`
	TimeLeft            string   `json:"time_left,omitempty"`
	EstimatedCompletion string   `json:"estimated_completion,omitempty"`
	Protocol            string   `json:"protocol,omitempty"             jsonschema:"usenet or torrent"`
	DownloadClient      string   `json:"download_client,omitempty"`
	Indexer             string   `json:"indexer,omitempty"`
	OutputPath          string   `json:"output_path,omitempty"          jsonschema:"where the download client put it"`
	DownloadID          string   `json:"download_id,omitempty"`
	Added               string   `json:"added,omitempty"`
}

func queueOf(q *radarr.QueueResource) queueRow {
	row := queueRow{
		ID:                  q.Id,
		Title:               q.Title,
		MovieID:             val(q.MovieId),
		Status:              string(q.Status),
		State:               string(q.TrackedDownloadState),
		Health:              string(q.TrackedDownloadStatus),
		ErrorMessage:        q.ErrorMessage,
		Quality:             qualityName(q.Quality),
		Size:                int64(q.Size),
		SizeLeft:            int64(q.Sizeleft),
		TimeLeft:            q.Timeleft,
		EstimatedCompletion: q.EstimatedCompletionTime,
		Protocol:            string(q.Protocol),
		DownloadClient:      q.DownloadClient,
		Indexer:             q.Indexer,
		OutputPath:          q.OutputPath,
		DownloadID:          q.DownloadId,
		Added:               q.Added,
	}
	if q.Movie != nil {
		row.Movie = titleYear(q.Movie.Title, q.Movie.Year)
	}
	for _, sm := range q.StatusMessages {
		for _, msg := range sm.Messages {
			line := msg
			if sm.Title != "" && sm.Title != q.Title {
				line = sm.Title + ": " + msg
			}
			if !slices.Contains(row.Messages, line) {
				row.Messages = append(row.Messages, line)
			}
		}
	}

	return row
}

// queueItems reads the whole queue, each item with its film, optionally for
// some films only, and optionally with the downloads Radarr could not match
// to a film.
func queueItems(ctx context.Context, c *radarr.Client, movieIDs []int, unknown bool) ([]radarr.QueueResource, error) {
	res, err := c.GetQueueComplete(ctx, radarr.GetQueueOperationOptions{
		IncludeMovie:             new(true),
		IncludeUnknownMovieItems: new(unknown),
		MovieIds:                 movieIDs,
	})
	if err != nil {
		return nil, err
	}

	return res.Items, nil
}

// queueFor is the queue for some films, as rows.
func queueFor(ctx context.Context, c *radarr.Client, movieIDs []int) ([]queueRow, error) {
	items, err := queueItems(ctx, c, movieIDs, false)
	if err != nil {
		return nil, err
	}
	out := make([]queueRow, 0, len(items))
	for i := range items {
		out = append(out, queueOf(&items[i]))
	}

	return out, nil
}

func registerQueueTools(r *registry) {
	client := r.client

	type queueListIn struct {
		Movie          string `json:"movie,omitempty"           jsonschema:"only this film's downloads: its id, tmdb:<id>, imdb:<tt id> or title"`
		IncludeUnknown bool   `json:"include_unknown,omitempty" jsonschema:"also list downloads Radarr could not match to a film in the library"`
	}
	type queueListOut struct {
		Total int        `json:"total"`
		Items []queueRow `json:"items" jsonschema:"stuck ones (health warning or error) first"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "queue_list",
		Description: "List the downloads Radarr is tracking: what each is for, how far along it is, and where Radarr has it - downloading, waiting to import, blocked from importing - with the reasons it gives for anything stuck. " +
			"Stuck ones come first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueListIn) (*mcp.CallToolResult, queueListOut, error) {
		var ids []int
		if in.Movie != "" {
			m, err := resolveMovie(ctx, client, in.Movie)
			if err != nil {
				return nil, queueListOut{}, err
			}
			ids = []int{m.Id}
		}
		items, err := queueItems(ctx, client, ids, in.IncludeUnknown)
		if err != nil {
			return nil, queueListOut{}, err
		}
		out := queueListOut{Total: len(items)}
		for i := range items {
			out.Items = append(out.Items, queueOf(&items[i]))
		}
		slices.SortStableFunc(out.Items, func(a, b queueRow) int { return healthRank(b.Health) - healthRank(a.Health) })

		return nil, out, nil
	})

	type queueRemoveIn struct {
		IDs              []int `json:"ids"                          jsonschema:"the queue ids to remove, as audit_queue gives them"`
		RemoveFromClient bool  `json:"remove_from_client,omitempty" jsonschema:"also delete the download, and what it downloaded, from the download client; default false leaves it there"`
		Blocklist        bool  `json:"blocklist,omitempty"          jsonschema:"blocklist the release so Radarr never grabs it again, and let it search for another"`
		SkipRedownload   bool  `json:"skip_redownload,omitempty"    jsonschema:"with blocklist: do not search for a replacement"`
	}
	type queueRemoveOut struct {
		Removed []queueRow `json:"removed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "queue_remove",
		Description: "Stop tracking downloads Radarr has in its queue: a stuck import, a stalled or unwanted download. Optionally delete the download from the client too, and optionally blocklist the release so Radarr looks for another. " +
			"Files already imported into the library are never touched.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueRemoveIn) (*mcp.CallToolResult, queueRemoveOut, error) {
		if len(in.IDs) == 0 {
			return nil, queueRemoveOut{}, errors.New("no queue ids: audit_queue gives them")
		}
		items, err := queueItems(ctx, client, nil, true)
		if err != nil {
			return nil, queueRemoveOut{}, err
		}
		byID := map[int]*radarr.QueueResource{}
		for i := range items {
			byID[items[i].Id] = &items[i]
		}
		var missing []string
		for _, id := range in.IDs {
			if byID[id] == nil {
				missing = append(missing, strconv.Itoa(id))
			}
		}
		if len(missing) > 0 {
			return nil, queueRemoveOut{}, fmt.Errorf("no queue item %s in the queue", strings.Join(missing, ", "))
		}

		out := queueRemoveOut{}
		for _, id := range in.IDs {
			if _, err := client.DeleteQueueById(ctx, id, radarr.DeleteQueueByIdOperationOptions{
				RemoveFromClient: new(in.RemoveFromClient),
				Blocklist:        new(in.Blocklist),
				SkipRedownload:   new(in.SkipRedownload),
			}); err != nil {
				return nil, out, fmt.Errorf("removing %d: %w", id, err)
			}
			out.Removed = append(out.Removed, queueOf(byID[id]))
		}

		return nil, out, nil
	})
}

// healthRank orders a queue item's health for sorting.
func healthRank(h string) int {
	switch h {
	case string(radarr.TrackedDownloadStatusError):
		return 2
	case string(radarr.TrackedDownloadStatusWarning):
		return 1
	default:
		return 0
	}
}
