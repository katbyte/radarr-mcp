package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type movieFileOut struct {
	Movie string    `json:"movie"`
	File  *fileInfo `json:"file"`
}

func registerMovieFileTools(r *registry) {
	client := r.client

	type fileEditIn struct {
		Movie        string   `json:"movie"                   jsonschema:"the film whose file to edit: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
		Quality      string   `json:"quality,omitempty"       jsonschema:"the quality the file really is, as qualitydefinition_list names them (Bluray-720p, WEBDL-1080p, DVD...)"`
		Languages    []string `json:"languages,omitempty"     jsonschema:"the languages it is in, by name (English, Japanese...)"`
		ReleaseGroup string   `json:"release_group,omitempty"`
		Edition      string   `json:"edition,omitempty"       jsonschema:"Director's Cut, Extended, IMAX..."`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "moviefile_edit",
		Description: "Correct what Radarr believes about a film's file: its quality (a 720p file sold as 4K, a remux graded as a web download), its languages, its release group or its edition. " +
			"Radarr decides upgrades from these, so a wrong grade means it never replaces a bad file, or replaces a good one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in fileEditIn) (*mcp.CallToolResult, movieFileOut, error) {
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, movieFileOut{}, err
		}
		if m.MovieFile == nil {
			return nil, movieFileOut{}, fmt.Errorf("%s has no file", titleYear(m.Title, m.Year))
		}
		if in.Quality == "" && in.Languages == nil && in.ReleaseGroup == "" && in.Edition == "" {
			return nil, movieFileOut{}, errors.New("nothing to change: give quality, languages, release_group or edition")
		}
		// the bulk editor changes only the fields it is sent, and the
		// generated model leaves out what is empty
		edit := radarr.MovieFileResource{Id: m.MovieFile.Id, ReleaseGroup: in.ReleaseGroup, Edition: in.Edition}
		if in.Quality != "" {
			var q *radarr.Quality
			if q, err = qualityByName(ctx, client, in.Quality); err != nil {
				return nil, movieFileOut{}, err
			}
			edit.Quality = &radarr.QualityModel{Quality: q, Revision: &radarr.Revision{Version: 1}}
		}
		if in.Languages != nil {
			if edit.Languages, err = languagesByName(ctx, client, in.Languages); err != nil {
				return nil, movieFileOut{}, err
			}
		}
		if _, err := client.PutMoviefileBulk(ctx, []radarr.MovieFileResource{edit}); err != nil {
			return nil, movieFileOut{}, err
		}
		file, err := client.GetMoviefileById(ctx, m.MovieFile.Id)
		if err != nil {
			return nil, movieFileOut{}, err
		}

		return nil, movieFileOut{Movie: titleYear(m.Title, m.Year), File: fileOf(file.Model)}, nil
	})

	type fileDeleteIn struct {
		Movie string `json:"movie" jsonschema:"the film whose file to delete: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "moviefile_delete",
		Description: "Delete a film's file from disk (to the recycle bin, when Radarr has one set up), leaving the film in the library to be downloaded again: for a fake, a wrong film or a bad encode. " +
			"A monitored film then counts as missing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in fileDeleteIn) (*mcp.CallToolResult, movieFileOut, error) {
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, movieFileOut{}, err
		}
		if m.MovieFile == nil {
			return nil, movieFileOut{}, fmt.Errorf("%s has no file", titleYear(m.Title, m.Year))
		}
		if _, err := client.DeleteMoviefileById(ctx, m.MovieFile.Id); err != nil {
			return nil, movieFileOut{}, err
		}

		return nil, movieFileOut{Movie: titleYear(m.Title, m.Year), File: fileOf(m.MovieFile)}, nil
	})
}

// qualityByName finds a quality by name among the ones Radarr defines.
func qualityByName(ctx context.Context, c *radarr.Client, name string) (*radarr.Quality, error) {
	res, err := c.GetQualitydefinition(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range res.Model {
		if d.Quality == nil {
			continue
		}
		if strings.EqualFold(d.Quality.Name, strings.TrimSpace(name)) {
			return d.Quality, nil
		}
		names = append(names, d.Quality.Name)
	}

	return nil, fmt.Errorf("no quality %q; the qualities are %s", name, strings.Join(names, ", "))
}

// languagesByName finds languages by name.
func languagesByName(ctx context.Context, c *radarr.Client, names []string) ([]radarr.Language, error) {
	res, err := c.GetLanguage(ctx)
	if err != nil {
		return nil, err
	}
	out := []radarr.Language{}
	for _, name := range names {
		i := slices.IndexFunc(res.Model, func(l radarr.LanguageResource) bool { return strings.EqualFold(l.Name, strings.TrimSpace(name)) })
		if i < 0 {
			return nil, fmt.Errorf("no language %q", name)
		}
		out = append(out, radarr.Language{Id: res.Model[i].Id, Name: res.Model[i].Name})
	}

	return out, nil
}
