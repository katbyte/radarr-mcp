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

type exclusionRow struct {
	ID     int    `json:"id"`
	TmdbID int    `json:"tmdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
}

func exclusionOf(e *radarr.ImportListExclusionResource) exclusionRow {
	return exclusionRow{ID: e.Id, TmdbID: e.TmdbId, Title: e.MovieTitle, Year: e.MovieYear}
}

// exclusions reads every import list exclusion, a page at a time (the
// unpaged list is deprecated).
func exclusions(ctx context.Context, c *radarr.Client) ([]radarr.ImportListExclusionResource, error) {
	res, err := c.GetExclusionsPagedComplete(ctx, radarr.GetExclusionsPagedOperationOptions{})
	if err != nil {
		return nil, err
	}

	return res.Items, nil
}

func registerExclusionTools(r *registry) {
	client := r.client

	type exclusionListOut struct {
		Exclusions []exclusionRow `json:"exclusions"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "exclusion_list",
		Description: "List the films on Radarr's import list exclusions: ones an import list or a monitored collection will never add, because someone removed them and said so.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, exclusionListOut, error) {
		all, err := exclusions(ctx, client)
		if err != nil {
			return nil, exclusionListOut{}, err
		}
		out := exclusionListOut{}
		for i := range all {
			out.Exclusions = append(out.Exclusions, exclusionOf(&all[i]))
		}
		slices.SortFunc(out.Exclusions, func(a, b exclusionRow) int { return strings.Compare(a.Title, b.Title) })

		return nil, out, nil
	})

	type exclusionAddIn struct {
		Movie string `json:"movie" jsonschema:"the film to exclude: tmdb:<id>, imdb:<tt id>, a film in the library by id or title, or a title and year movie_lookup finds once"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "exclusion_add",
		Description: "Add a film to the import list exclusions, so no import list or monitored collection adds it. The film does not have to be in the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exclusionAddIn) (*mcp.CallToolResult, exclusionRow, error) {
		var film *radarr.MovieResource
		if m, err := resolveMovie(ctx, client, in.Movie); err == nil {
			film = m
		} else {
			found, lookErr := addCandidate(ctx, client, in.Movie)
			if lookErr != nil {
				return nil, exclusionRow{}, lookErr
			}
			film = found
		}
		existing, err := exclusions(ctx, client)
		if err != nil {
			return nil, exclusionRow{}, err
		}
		for i := range existing {
			if existing[i].TmdbId == film.TmdbId {
				return nil, exclusionRow{}, fmt.Errorf("%s is already excluded (id %d)", titleYear(film.Title, film.Year), existing[i].Id)
			}
		}
		res, err := client.PostExclusions(ctx, radarr.ImportListExclusionResource{TmdbId: film.TmdbId, MovieTitle: film.Title, MovieYear: film.Year})
		if err != nil {
			return nil, exclusionRow{}, err
		}

		return nil, exclusionOf(res.Model), nil
	})

	type exclusionRemoveIn struct {
		IDs []int `json:"ids" jsonschema:"the exclusion ids, as exclusion_list gives them"`
	}
	type exclusionRemoveOut struct {
		Removed []exclusionRow `json:"removed" jsonschema:"the films no longer excluded"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "exclusion_remove",
		Description: "Take films off the import list exclusions, so an import list or a monitored collection may add them again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exclusionRemoveIn) (*mcp.CallToolResult, exclusionRemoveOut, error) {
		if len(in.IDs) == 0 {
			return nil, exclusionRemoveOut{}, errors.New("no exclusion ids: exclusion_list gives them")
		}
		// Radarr answers 200 to deleting an exclusion that is not there, so
		// the ids are checked first rather than reported removed
		all, err := exclusions(ctx, client)
		if err != nil {
			return nil, exclusionRemoveOut{}, err
		}
		byID := map[int]*radarr.ImportListExclusionResource{}
		for i := range all {
			byID[all[i].Id] = &all[i]
		}
		var missing []string
		for _, id := range in.IDs {
			if byID[id] == nil {
				missing = append(missing, strconv.Itoa(id))
			}
		}
		if len(missing) > 0 {
			return nil, exclusionRemoveOut{}, fmt.Errorf("no exclusion %s; exclusion_list lists them", strings.Join(missing, ", "))
		}
		out := exclusionRemoveOut{}
		for _, id := range in.IDs {
			if _, err := client.DeleteExclusionsById(ctx, id); err != nil {
				return nil, out, fmt.Errorf("removing %d: %w", id, err)
			}
			out.Removed = append(out.Removed, exclusionOf(byID[id]))
		}

		return nil, out, nil
	})
}
