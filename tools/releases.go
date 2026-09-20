package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// releaseRow is one release an indexer offers for a film.
type releaseRow struct {
	GUID              string   `json:"guid"                          jsonschema:"what release_grab takes, with indexer_id"`
	IndexerID         int      `json:"indexer_id"`
	Indexer           string   `json:"indexer"`
	Title             string   `json:"title"`
	Quality           string   `json:"quality,omitempty"`
	Size              int64    `json:"size"                          jsonschema:"bytes"`
	AgeDays           int      `json:"age_days"`
	Protocol          string   `json:"protocol"                      jsonschema:"usenet or torrent"`
	Seeders           int      `json:"seeders,omitempty"`
	Leechers          int      `json:"leechers,omitempty"`
	Languages         []string `json:"languages,omitempty"`
	ReleaseGroup      string   `json:"release_group,omitempty"`
	CustomFormats     []string `json:"custom_formats,omitempty"`
	CustomFormatScore int      `json:"custom_format_score,omitempty"`
	Approved          bool     `json:"approved"                      jsonschema:"Radarr would grab it on its own"`
	Rejections        []string `json:"rejections,omitempty"          jsonschema:"why it would not"`
}

func releaseOf(r *radarr.ReleaseResource) releaseRow {
	row := releaseRow{
		GUID:              r.Guid,
		IndexerID:         r.IndexerId,
		Indexer:           r.Indexer,
		Title:             r.Title,
		Quality:           qualityName(r.Quality),
		Size:              r.Size,
		AgeDays:           r.Age,
		Protocol:          string(r.Protocol),
		Seeders:           val(r.Seeders),
		Leechers:          val(r.Leechers),
		Languages:         languageNames(r.Languages),
		ReleaseGroup:      r.ReleaseGroup,
		CustomFormatScore: r.CustomFormatScore,
		Approved:          isTrue(r.Approved),
		Rejections:        r.Rejections,
	}
	for _, cf := range r.CustomFormats {
		row.CustomFormats = append(row.CustomFormats, cf.Name)
	}

	return row
}

// parsedRow is what Radarr makes of a release or file name.
type parsedRow struct {
	Title             string    `json:"title"                         jsonschema:"the name parsed"`
	MovieTitles       []string  `json:"movie_titles,omitempty"        jsonschema:"the titles Radarr read out of it"`
	Year              int       `json:"year,omitempty"`
	Quality           string    `json:"quality,omitempty"`
	Edition           string    `json:"edition,omitempty"`
	ReleaseGroup      string    `json:"release_group,omitempty"`
	Languages         []string  `json:"languages,omitempty"`
	TmdbID            int       `json:"tmdb_id,omitempty"`
	ImdbID            string    `json:"imdb_id,omitempty"`
	CustomFormats     []string  `json:"custom_formats,omitempty"`
	CustomFormatScore int       `json:"custom_format_score,omitempty"`
	Movie             *movieRow `json:"movie,omitempty"               jsonschema:"the film in the library it matches, when it matches one"`
}

func registerReleaseTools(r *registry) {
	client := r.client

	type releaseSearchIn struct {
		Movie    string `json:"movie"              jsonschema:"the film: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
		Limit    int    `json:"limit,omitempty"    jsonschema:"releases to return, default 20"`
		Approved bool   `json:"approved,omitempty" jsonschema:"only the releases Radarr would grab on its own"`
	}
	type releaseSearchOut struct {
		Total    int          `json:"total"    jsonschema:"releases the indexers offered"`
		Releases []releaseRow `json:"releases" jsonschema:"in Radarr's order of preference"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "release_search",
		Description: "Search the indexers for a film's releases without grabbing anything (Radarr's interactive search): each with its quality, size, age, seeders, custom formats and score, whether Radarr would take it, and why not. " +
			"release_grab takes the one chosen.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in releaseSearchIn) (*mcp.CallToolResult, releaseSearchOut, error) {
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, releaseSearchOut{}, err
		}
		res, err := client.GetRelease(ctx, radarr.GetReleaseOperationOptions{MovieId: m.Id})
		if err != nil {
			return nil, releaseSearchOut{}, err
		}
		out := releaseSearchOut{Total: len(res.Model)}
		for i := range res.Model {
			if len(out.Releases) == limitOr(in.Limit, 20) {
				break
			}
			if in.Approved && !isTrue(res.Model[i].Approved) {
				continue
			}
			out.Releases = append(out.Releases, releaseOf(&res.Model[i]))
		}

		return nil, out, nil
	})

	type releaseGrabIn struct {
		GUID      string `json:"guid"       jsonschema:"the release's guid, from release_search"`
		IndexerID int    `json:"indexer_id" jsonschema:"the indexer_id release_search gave with it"`
	}
	type releaseGrabOut struct {
		GUID      string      `json:"guid"`
		IndexerID int         `json:"indexer_id"`
		Grabbed   *historyRow `json:"grabbed,omitempty" jsonschema:"the grab as Radarr recorded it in its history: the release's name, the film, the quality, the indexer and download client"`
		Queue     []queueRow  `json:"queue"             jsonschema:"the film's downloads in the queue after the grab; a download client that reports only finished downloads shows nothing yet"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "release_grab",
		Description: "Grab a release release_search found: Radarr sends it to its download client, even one it rejected on its own (that is the point of choosing by hand). " +
			"Answers with the grab as Radarr recorded it, and the film's downloads in the queue afterwards.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in releaseGrabIn) (*mcp.CallToolResult, releaseGrabOut, error) {
		if in.GUID == "" || in.IndexerID == 0 {
			return nil, releaseGrabOut{}, errors.New("guid and indexer_id are both required: release_search gives them")
		}
		if _, err := client.PostRelease(ctx, radarr.ReleaseResource{Guid: in.GUID, IndexerId: in.IndexerID}); err != nil {
			if strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "cache") {
				return nil, releaseGrabOut{}, fmt.Errorf("Radarr no longer has that release from a search; run release_search again first: %w", err) //nolint:staticcheck // Radarr is a name
			}
			return nil, releaseGrabOut{}, err
		}
		// Radarr answers a grab with no more than it was sent; the history it
		// writes before answering has the rest
		out := releaseGrabOut{GUID: in.GUID, IndexerID: in.IndexerID}
		grab, err := grabOf(ctx, client, in.GUID)
		if err != nil || grab == nil {
			return nil, out, err
		}
		row := historyOf(grab)
		out.Grabbed = &row
		out.Queue, err = queueFor(ctx, client, []int{grab.MovieId})

		return nil, out, err
	})

	type parseIn struct {
		Title string `json:"title" jsonschema:"a release or file name, e.g. The.Matrix.1999.1080p.BluRay.x264-GRP"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "release_parse",
		Description: "Parse a release or file name the way Radarr does: the title and year it reads, quality, edition, release group, languages, the custom formats it would match and their score, and the film in the library it maps to. " +
			"How to tell why Radarr grabbed, rejected or mis-matched something.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in parseIn) (*mcp.CallToolResult, parsedRow, error) {
		if strings.TrimSpace(in.Title) == "" {
			return nil, parsedRow{}, errors.New("no title to parse")
		}
		res, err := client.GetParse(ctx, radarr.GetParseOperationOptions{Title: in.Title})
		if err != nil {
			return nil, parsedRow{}, err
		}
		p := res.Model
		out := parsedRow{Title: in.Title, CustomFormatScore: p.CustomFormatScore}
		for _, cf := range p.CustomFormats {
			out.CustomFormats = append(out.CustomFormats, cf.Name)
		}
		if pi := p.ParsedMovieInfo; pi != nil {
			out.MovieTitles = pi.MovieTitles
			out.Year = pi.Year
			out.Quality = qualityName(pi.Quality)
			out.Edition = pi.Edition
			out.ReleaseGroup = pi.ReleaseGroup
			out.Languages = languageNames(pi.Languages)
			out.TmdbID = pi.TmdbId
			out.ImdbID = pi.ImdbId
		}
		if p.Movie != nil && p.Movie.Id != 0 {
			lk, err := loadLookups(ctx, client)
			if err != nil {
				return nil, out, err
			}
			row := rowOf(p.Movie, lk)
			out.Movie = &row
		}

		return nil, out, nil
	})
}

// grabOf finds the history Radarr wrote for the grab of a release, by the
// release's guid: the newest grab carrying it.
func grabOf(ctx context.Context, c *radarr.Client, guid string) (*radarr.HistoryResource, error) {
	res, err := c.GetHistory(ctx, radarr.GetHistoryOperationOptions{
		Page: 1, PageSize: 50, SortKey: "date", SortDirection: radarr.SortDirectionDescending, IncludeMovie: new(true),
	})
	if err != nil {
		return nil, err
	}
	if res.Model == nil {
		return nil, nil
	}
	for i := range res.Model.Records {
		h := &res.Model.Records[i]
		if h.EventType == radarr.MovieHistoryEventTypeGrabbed && h.Data["guid"] == guid {
			return h, nil
		}
	}

	return nil, nil
}
