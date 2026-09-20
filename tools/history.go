package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// historyRow is one event in a film's life in Radarr.
type historyRow struct {
	ID          int               `json:"id"`
	Date        string            `json:"date"`
	Event       string            `json:"event"                 jsonschema:"grabbed, downloadFolderImported, downloadFailed, movieFileDeleted, movieFileRenamed, downloadIgnored"`
	Movie       string            `json:"movie,omitempty"       jsonschema:"Title (Year)"`
	MovieID     int               `json:"movie_id"`
	SourceTitle string            `json:"source_title"          jsonschema:"the release name, or the file's"`
	Quality     string            `json:"quality,omitempty"`
	DownloadID  string            `json:"download_id,omitempty"`
	Details     map[string]string `json:"details,omitempty"     jsonschema:"what Radarr recorded: the indexer and download client of a grab, the path an import went to, the reason for a failure or a deletion"`
}

// historyDetail are the keys of an event's data worth showing: the rest is
// bookkeeping (guids, flags, hashes).
var historyDetail = []string{
	"indexer", "downloadClient", "downloadClientName", "releaseGroup", "size", "reason", "message",
	"importedPath", "droppedPath", "sourcePath", "path", "sourceRelativePath", "relativePath", "publishedDate",
}

func historyOf(h *radarr.HistoryResource) historyRow {
	row := historyRow{
		ID:          h.Id,
		Date:        h.Date,
		Event:       string(h.EventType),
		MovieID:     h.MovieId,
		SourceTitle: h.SourceTitle,
		Quality:     qualityName(h.Quality),
		DownloadID:  h.DownloadId,
	}
	if h.Movie != nil {
		row.Movie = titleYear(h.Movie.Title, h.Movie.Year)
	}
	for _, k := range historyDetail {
		if v := strings.TrimSpace(h.Data[k]); v != "" {
			if row.Details == nil {
				row.Details = map[string]string{}
			}
			row.Details[k] = v
		}
	}

	return row
}

type blocklistRow struct {
	ID          int    `json:"id"`
	Date        string `json:"date"`
	Movie       string `json:"movie,omitempty"`
	MovieID     int    `json:"movie_id"`
	SourceTitle string `json:"source_title"`
	Quality     string `json:"quality,omitempty"`
	Indexer     string `json:"indexer,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Message     string `json:"message,omitempty"  jsonschema:"why it was blocklisted"`
}

func registerHistoryTools(r *registry) {
	client := r.client

	type historyListIn struct {
		Movie string `json:"movie,omitempty" jsonschema:"only this film's events: its id, tmdb:<id>, imdb:<tt id> or title"`
		Event string `json:"event,omitempty" jsonschema:"only events of this kind: grabbed, downloadFolderImported, downloadFailed, movieFileDeleted, movieFileRenamed or downloadIgnored"`
		Days  int    `json:"days,omitempty"  jsonschema:"only events from the last this many days"`
		Limit int    `json:"limit,omitempty" jsonschema:"events to return, newest first, default 50"`
	}
	type historyListOut struct {
		Events []historyRow `json:"events" jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "history_list",
		Description: "Read Radarr's history, newest first: what it grabbed and from which indexer, what it imported and where to, what failed and why, and what was renamed or deleted. " +
			"Filter by film, kind of event and how many days back.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyListIn) (*mcp.CallToolResult, historyListOut, error) {
		event := strings.TrimSpace(in.Event)
		if event != "" && !slices.Contains(radarr.PossibleValuesForMovieHistoryEventType(), event) {
			return nil, historyListOut{}, fmt.Errorf("event %q: want one of %s", in.Event, strings.Join(radarr.PossibleValuesForMovieHistoryEventType(), ", "))
		}
		limit := limitOr(in.Limit, 50)
		var since string
		if in.Days > 0 {
			since = time.Now().UTC().AddDate(0, 0, -in.Days).Format(time.RFC3339)
		}
		keep := func(h *radarr.HistoryResource) bool {
			return (event == "" || string(h.EventType) == event) && (since == "" || h.Date >= since)
		}

		out := historyListOut{}
		if in.Movie != "" {
			m, err := resolveMovie(ctx, client, in.Movie)
			if err != nil {
				return nil, out, err
			}
			res, err := client.GetHistoryMovie(ctx, radarr.GetHistoryMovieOperationOptions{MovieId: m.Id, IncludeMovie: new(true)})
			if err != nil {
				return nil, out, err
			}
			events := res.Model
			slices.SortStableFunc(events, func(a, b radarr.HistoryResource) int { return strings.Compare(b.Date, a.Date) })
			for i := range events {
				if keep(&events[i]) && len(out.Events) < limit {
					out.Events = append(out.Events, historyOf(&events[i]))
				}
			}
			return nil, out, nil
		}

		// newest first, a page at a time, until enough match or the pages
		// are older than asked for
		for page := 1; len(out.Events) < limit; page++ {
			res, err := client.GetHistory(ctx, radarr.GetHistoryOperationOptions{
				Page: page, PageSize: 250, SortKey: "date", SortDirection: radarr.SortDirectionDescending, IncludeMovie: new(true),
			})
			if err != nil {
				return nil, out, err
			}
			records := res.Model.Records
			for i := range records {
				if keep(&records[i]) && len(out.Events) < limit {
					out.Events = append(out.Events, historyOf(&records[i]))
				}
			}
			if len(records) < 250 || since != "" && records[len(records)-1].Date < since {
				break
			}
		}

		return nil, out, nil
	})

	type markFailedIn struct {
		ID    int    `json:"id,omitempty"    jsonschema:"the history id of the grab"`
		Movie string `json:"movie,omitempty" jsonschema:"or the film, whose latest grab is marked"`
	}
	type markFailedOut struct {
		Marked historyRow `json:"marked" jsonschema:"the grab now marked failed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "history_mark_failed",
		Description: "Mark a grab as failed: Radarr blocklists that release so it is never grabbed again and, as its settings say, searches for another. " +
			"This is how to reject a download that turned out wrong - a fake, the wrong film, a bad encode - after it was imported. Name the grab by its history id, or name the film to mark its latest grab.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in markFailedIn) (*mcp.CallToolResult, markFailedOut, error) {
		grab, err := findGrab(ctx, client, in.ID, in.Movie)
		if err != nil {
			return nil, markFailedOut{}, err
		}
		if _, err := client.PostHistoryFailedById(ctx, grab.Id); err != nil {
			return nil, markFailedOut{}, err
		}

		return nil, markFailedOut{Marked: historyOf(grab)}, nil
	})

	type blocklistListIn struct {
		Movie string `json:"movie,omitempty" jsonschema:"only this film's blocklisted releases"`
		Limit int    `json:"limit,omitempty" jsonschema:"entries to return, newest first, default 50"`
	}
	type blocklistListOut struct {
		Total   int            `json:"total"`
		Entries []blocklistRow `json:"entries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "blocklist_list",
		Description: "List the releases Radarr will never grab again - ones that failed to download or import, or that were rejected by hand - newest first, each with why.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in blocklistListIn) (*mcp.CallToolResult, blocklistListOut, error) {
		var entries []radarr.BlocklistResource
		if in.Movie != "" {
			m, err := resolveMovie(ctx, client, in.Movie)
			if err != nil {
				return nil, blocklistListOut{}, err
			}
			res, err := client.GetBlocklistMovie(ctx, radarr.GetBlocklistMovieOperationOptions{MovieId: m.Id})
			if err != nil {
				return nil, blocklistListOut{}, err
			}
			entries = res.Model
			for i := range entries {
				entries[i].Movie = m
			}
		} else {
			res, err := client.GetBlocklistComplete(ctx, radarr.GetBlocklistOperationOptions{SortKey: "date", SortDirection: radarr.SortDirectionDescending})
			if err != nil {
				return nil, blocklistListOut{}, err
			}
			entries = res.Items
		}
		slices.SortStableFunc(entries, func(a, b radarr.BlocklistResource) int { return strings.Compare(b.Date, a.Date) })

		out := blocklistListOut{Total: len(entries)}
		for i := range entries {
			if len(out.Entries) == limitOr(in.Limit, 50) {
				break
			}
			out.Entries = append(out.Entries, blocklistOf(&entries[i]))
		}

		return nil, out, nil
	})

	type blocklistRemoveIn struct {
		IDs []int `json:"ids" jsonschema:"the blocklist ids to remove, as blocklist_list gives them"`
	}
	type blocklistRemoveOut struct {
		Removed []int `json:"removed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "blocklist_remove",
		Description: "Take releases off the blocklist, so Radarr may grab them again: for a release blocklisted by mistake, or when an indexer re-uploaded a fixed copy under the same name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in blocklistRemoveIn) (*mcp.CallToolResult, blocklistRemoveOut, error) {
		if len(in.IDs) == 0 {
			return nil, blocklistRemoveOut{}, errors.New("no blocklist ids: blocklist_list gives them")
		}
		out := blocklistRemoveOut{}
		for _, id := range in.IDs {
			if _, err := client.DeleteBlocklistById(ctx, id); err != nil {
				return nil, out, fmt.Errorf("removing %d: %w", id, err)
			}
			out.Removed = append(out.Removed, id)
		}

		return nil, out, nil
	})
}

// findGrab finds a grab event by history id, or a film's latest grab.
func findGrab(ctx context.Context, c *radarr.Client, id int, movie string) (*radarr.HistoryResource, error) {
	if id == 0 && movie == "" {
		return nil, errors.New("name the grab by id, or the film whose latest grab to mark")
	}
	var movieID int
	if movie != "" {
		m, err := resolveMovie(ctx, c, movie)
		if err != nil {
			return nil, err
		}
		movieID = m.Id
	}
	if movieID != 0 {
		res, err := c.GetHistoryMovie(ctx, radarr.GetHistoryMovieOperationOptions{MovieId: movieID, IncludeMovie: new(true)})
		if err != nil {
			return nil, err
		}
		events := res.Model
		slices.SortStableFunc(events, func(a, b radarr.HistoryResource) int { return strings.Compare(b.Date, a.Date) })
		for i := range events {
			if events[i].EventType == radarr.MovieHistoryEventTypeGrabbed && (id == 0 || events[i].Id == id) {
				return &events[i], nil
			}
		}
		return nil, fmt.Errorf("no grab in the history of film %d", movieID)
	}
	res, err := c.GetHistoryComplete(ctx, radarr.GetHistoryOperationOptions{IncludeMovie: new(true)})
	if err != nil {
		return nil, err
	}
	for i := range res.Items {
		if res.Items[i].Id != id {
			continue
		}
		if res.Items[i].EventType != radarr.MovieHistoryEventTypeGrabbed {
			return nil, fmt.Errorf("history %d is a %s, not a grab: only a grab can be marked failed", id, res.Items[i].EventType)
		}
		return &res.Items[i], nil
	}

	return nil, fmt.Errorf("no history event %d", id)
}

func blocklistOf(b *radarr.BlocklistResource) blocklistRow {
	row := blocklistRow{
		ID: b.Id, Date: b.Date, MovieID: b.MovieId, SourceTitle: b.SourceTitle,
		Quality: qualityName(b.Quality), Indexer: b.Indexer, Protocol: string(b.Protocol), Message: b.Message,
	}
	if b.Movie != nil {
		row.Movie = titleYear(b.Movie.Title, b.Movie.Year)
	}

	return row
}
