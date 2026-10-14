package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two more ways a release reaches the download client: one Radarr is holding
// back in its queue, grabbed now, and one handed to Radarr from elsewhere to
// decide on.

// waiting reports whether a queue item is a release Radarr has decided on and
// not yet sent: held back by a delay profile, or with no download client to
// send it to.
func waiting(status radarr.QueueStatus) bool {
	return status == radarr.QueueStatusDelay || status == radarr.QueueStatusDownloadClientUnavailable
}

func registerAcquireMoreTools(r *registry) {
	c := r.client

	type grabIn struct {
		IDs []int `json:"ids" jsonschema:"the queue ids of the releases to grab now, as queue_list gives them; each must be one Radarr is holding back (status delay or downloadClientUnavailable)"`
	}
	type grabOut struct {
		Grabbed []queueRow `json:"grabbed" jsonschema:"the releases sent to the download client, as they stood in the queue"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "queue_grab",
		Description: "Send a release Radarr is holding back to the download client now: one waiting out a delay profile's delay, or one it had no download client for. It is the queue's own Grab button; a release that is already downloading is not one of these.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in grabIn) (*mcp.CallToolResult, grabOut, error) {
		if len(in.IDs) == 0 {
			return nil, grabOut{}, errors.New("no queue ids: queue_list gives them")
		}
		items, err := queueItems(ctx, c, nil, true)
		if err != nil {
			return nil, grabOut{}, err
		}
		byID := map[int]*radarr.QueueResource{}
		for i := range items {
			byID[items[i].Id] = &items[i]
		}
		for _, id := range in.IDs {
			switch item := byID[id]; {
			case item == nil:
				return nil, grabOut{}, fmt.Errorf("no queue item %d in the queue", id)
			case !waiting(item.Status):
				return nil, grabOut{}, fmt.Errorf("queue item %d is %s, not held back: only a release waiting out a delay, or for a download client, can be grabbed", id, item.Status)
			}
		}

		out := grabOut{}
		for _, id := range in.IDs {
			if _, err := c.PostQueueGrabById(ctx, id); err != nil {
				return nil, out, fmt.Errorf("grabbing %d: %w", id, err)
			}
			out.Grabbed = append(out.Grabbed, queueOf(byID[id]))
		}

		return nil, out, nil
	})

	type pushIn struct {
		Title       string `json:"title"                  jsonschema:"the release's name, which Radarr reads the film, quality and group from"`
		DownloadURL string `json:"download_url"           jsonschema:"where its .nzb or .torrent is fetched from, or a magnet link"`
		Protocol    string `json:"protocol"               jsonschema:"usenet or torrent"`
		PublishDate string `json:"publish_date,omitempty" jsonschema:"when it was posted, RFC 3339; default now"`
		Size        int64  `json:"size,omitempty"         jsonschema:"bytes, when known: Radarr holds it to the quality's size limits"`
		Indexer     string `json:"indexer,omitempty"      jsonschema:"the indexer it came from, by name, when Radarr has it"`
	}
	type pushOut struct {
		Releases []releaseRow `json:"releases" jsonschema:"what Radarr made of it: approved and sent to the download client, or rejected with why"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "release_push",
		Description: "Hand Radarr a release found somewhere else - by name and where to fetch it - for it to decide on as it would one of its own searches: matched to a film, held to that film's profile, and sent to the download client if it qualifies. The answer says whether it was approved, and why not when it was not. release_search and release_grab are for what Radarr's own indexers offer.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pushIn) (*mcp.CallToolResult, pushOut, error) {
		title, link := strings.TrimSpace(in.Title), strings.TrimSpace(in.DownloadURL)
		if title == "" || link == "" {
			return nil, pushOut{}, errors.New("title and download_url are both required")
		}
		protocol := radarr.DownloadProtocol(strings.ToLower(strings.TrimSpace(in.Protocol)))
		if protocol != radarr.DownloadProtocolUsenet && protocol != radarr.DownloadProtocolTorrent {
			return nil, pushOut{}, fmt.Errorf("protocol %q: want usenet or torrent", in.Protocol)
		}
		published := radarr.Time{Time: time.Now().UTC()}
		if text := strings.TrimSpace(in.PublishDate); text != "" {
			when, err := time.Parse(time.RFC3339, text)
			if err != nil {
				return nil, pushOut{}, fmt.Errorf("publish_date %q: want a date such as 2024-03-01T12:00:00Z", text)
			}
			published.Time = when
		}
		release := radarr.ReleaseResource{Title: title, DownloadUrl: link, Protocol: protocol, PublishDate: published, Size: in.Size, Indexer: strings.TrimSpace(in.Indexer)}
		if strings.HasPrefix(strings.ToLower(link), "magnet:") {
			release.DownloadUrl, release.MagnetUrl = "", link
		}

		res, err := c.PostReleasePush(ctx, release)
		if err != nil {
			return nil, pushOut{}, err
		}
		out := pushOut{Releases: make([]releaseRow, 0, len(res.Answer))}
		for i := range res.Answer {
			out.Releases = append(out.Releases, releaseOf(&res.Answer[i]))
		}
		if len(out.Releases) == 0 {
			return nil, out, errors.New("Radarr took the release and answered nothing about it: " + strconv.Quote(title))
		}

		return nil, out, nil
	})
}
