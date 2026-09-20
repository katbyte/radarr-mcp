package tools

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type collectionMember struct {
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	TmdbID   int    `json:"tmdb_id"`
	Status   string `json:"status,omitempty"   jsonschema:"announced, inCinemas or released"`
	Excluded bool   `json:"excluded,omitempty" jsonschema:"on the import list exclusions, so a monitored collection will not add it"`
}

type collectionRow struct {
	ID             int                `json:"id"`
	Title          string             `json:"title"`
	TmdbID         int                `json:"tmdb_id"`
	Monitored      bool               `json:"monitored"                 jsonschema:"Radarr adds the collection's films it does not have"`
	SearchOnAdd    bool               `json:"search_on_add"`
	QualityProfile string             `json:"quality_profile,omitempty"`
	RootFolder     string             `json:"root_folder,omitempty"`
	Held           []string           `json:"held"                      jsonschema:"the films the library has, as Title (Year)"`
	Missing        []collectionMember `json:"missing"                   jsonschema:"the films it does not"`
}

func collectionOf(c *radarr.CollectionResource, lk lookups, includeUnreleased bool) collectionRow {
	row := collectionRow{
		ID: c.Id, Title: c.Title, TmdbID: c.TmdbId, Monitored: isTrue(c.Monitored), SearchOnAdd: isTrue(c.SearchOnAdd),
		QualityProfile: lk.profiles[c.QualityProfileId], RootFolder: c.RootFolderPath,
	}
	members := slices.Clone(c.Movies)
	slices.SortStableFunc(members, func(a, b radarr.CollectionMovieResource) int { return releaseOrder(a) - releaseOrder(b) })
	for _, m := range members {
		switch {
		case isTrue(m.IsExisting):
			row.Held = append(row.Held, titleYear(m.Title, m.Year))
		case includeUnreleased || released(m.Status):
			row.Missing = append(row.Missing, collectionMember{Title: m.Title, Year: m.Year, TmdbID: m.TmdbId, Status: string(m.Status), Excluded: isTrue(m.IsExcluded)})
		}
	}

	return row
}

// releaseOrder puts a collection's films in release order, the unannounced
// ones (year 0) last.
func releaseOrder(m radarr.CollectionMovieResource) int {
	if m.Year == 0 {
		return 9999
	}

	return m.Year
}

// released reports whether a film is out: in cinemas or later.
func released(s radarr.MovieStatusType) bool {
	return s == radarr.MovieStatusTypeReleased || s == radarr.MovieStatusTypeInCinemas
}

// resolveCollection finds a collection by id, tmdb:<id> or title.
func resolveCollection(ctx context.Context, c *radarr.Client, ref string) (*radarr.CollectionResource, error) {
	res, err := c.GetCollection(ctx, radarr.GetCollectionOperationOptions{})
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	lower := strings.ToLower(ref)
	var titles []string
	for i := range res.Model {
		col := &res.Model[i]
		switch {
		case strconv.Itoa(col.Id) == ref,
			strings.HasPrefix(lower, "tmdb:") && strings.TrimSpace(ref[5:]) == strconv.Itoa(col.TmdbId),
			cleanTitle(col.Title) == cleanTitle(ref),
			cleanTitle(col.Title) == cleanTitle(ref+" collection"):
			return col, nil
		}
		titles = append(titles, col.Title)
	}
	slices.Sort(titles)

	return nil, fmt.Errorf("no collection %q; the collections are %s", ref, strings.Join(titles, ", "))
}

func registerCollectionTools(r *registry) {
	client := r.client

	type collectionListIn struct {
		MissingOnly       bool `json:"missing_only,omitempty"       jsonschema:"only collections the library holds some but not all of"`
		IncludeUnreleased bool `json:"include_unreleased,omitempty" jsonschema:"count announced films as missing too"`
	}
	type collectionListOut struct {
		Collections []collectionRow `json:"collections"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "collection_list",
		Description: "List the TMDB collections the library's films belong to (a franchise, a trilogy), each with the films the library holds and the released ones it does not. " +
			"A monitored collection has Radarr add its missing films on its own.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in collectionListIn) (*mcp.CallToolResult, collectionListOut, error) {
		res, err := client.GetCollection(ctx, radarr.GetCollectionOperationOptions{})
		if err != nil {
			return nil, collectionListOut{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, collectionListOut{}, err
		}
		out := collectionListOut{}
		for i := range res.Model {
			row := collectionOf(&res.Model[i], lk, in.IncludeUnreleased)
			if in.MissingOnly && len(row.Missing) == 0 {
				continue
			}
			out.Collections = append(out.Collections, row)
		}
		slices.SortFunc(out.Collections, func(a, b collectionRow) int { return strings.Compare(a.Title, b.Title) })

		return nil, out, nil
	})

	type collectionEditIn struct {
		Collection          string `json:"collection"                     jsonschema:"the collection: its id, tmdb:<id> or title"`
		Monitored           *bool  `json:"monitored,omitempty"            jsonschema:"have Radarr add the collection's films the library does not hold"`
		SearchOnAdd         *bool  `json:"search_on_add,omitempty"        jsonschema:"search for each film as it is added"`
		QualityProfile      string `json:"quality_profile,omitempty"      jsonschema:"the profile films it adds get"`
		RootFolder          string `json:"root_folder,omitempty"          jsonschema:"the root folder films it adds go in"`
		MinimumAvailability string `json:"minimum_availability,omitempty" jsonschema:"announced, inCinemas or released"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "collection_edit",
		Description: "Change a collection: monitor it, so Radarr adds the films of it the library does not hold (and searches for them with search_on_add), and choose the quality profile, root folder and minimum availability those films get. " +
			"Only what is given changes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in collectionEditIn) (*mcp.CallToolResult, collectionRow, error) {
		col, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, collectionRow{}, err
		}
		edit := radarr.CollectionUpdateResource{CollectionIds: []int{col.Id}, Monitored: in.Monitored, SearchOnAdd: in.SearchOnAdd}
		if in.QualityProfile != "" {
			var p *radarr.QualityProfileResource
			if p, err = resolveProfile(ctx, client, in.QualityProfile); err != nil {
				return nil, collectionRow{}, err
			}
			edit.QualityProfileId = new(p.Id)
		}
		if in.RootFolder != "" {
			var f *radarr.RootFolderResource
			if f, err = resolveRootFolder(ctx, client, in.RootFolder); err != nil {
				return nil, collectionRow{}, err
			}
			edit.RootFolderPath = f.Path
		}
		if in.MinimumAvailability != "" {
			if edit.MinimumAvailability, err = minimumAvailability(in.MinimumAvailability); err != nil {
				return nil, collectionRow{}, err
			}
		}
		if _, err := client.PutCollection(ctx, edit); err != nil {
			return nil, collectionRow{}, err
		}
		after, err := client.GetCollectionById(ctx, col.Id)
		if err != nil {
			return nil, collectionRow{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, collectionRow{}, err
		}

		return nil, collectionOf(after.Model, lk, false), nil
	})
}
