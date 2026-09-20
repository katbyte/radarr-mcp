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

type namingOut struct {
	RenameMovies             bool   `json:"rename_movies"              jsonschema:"whether Radarr renames files it imports; off, it keeps their names and gives no rename previews"`
	ReplaceIllegalCharacters bool   `json:"replace_illegal_characters"`
	ColonReplacement         string `json:"colon_replacement"          jsonschema:"what a colon in a title becomes: delete, dash, spaceDash, spaceDashSpace or smart"`
	StandardMovieFormat      string `json:"standard_movie_format"      jsonschema:"the file name pattern"`
	MovieFolderFormat        string `json:"movie_folder_format"        jsonschema:"the folder name pattern"`
	ExampleFile              string `json:"example_file,omitempty"     jsonschema:"a file named by the pattern"`
	ExampleFolder            string `json:"example_folder,omitempty"   jsonschema:"a folder named by the pattern"`
}

// namingOf renders the naming settings with Radarr's own examples of them.
func namingOf(ctx context.Context, c *radarr.Client, n *radarr.NamingConfigResource) (namingOut, error) {
	out := namingOut{
		RenameMovies:             isTrue(n.RenameMovies),
		ReplaceIllegalCharacters: isTrue(n.ReplaceIllegalCharacters),
		ColonReplacement:         string(n.ColonReplacementFormat),
		StandardMovieFormat:      n.StandardMovieFormat,
		MovieFolderFormat:        n.MovieFolderFormat,
	}
	ex, err := c.GetConfigNamingExamples(ctx, radarr.GetConfigNamingExamplesOperationOptions{
		RenameMovies:             new(true),
		ReplaceIllegalCharacters: n.ReplaceIllegalCharacters,
		ColonReplacementFormat:   n.ColonReplacementFormat,
		StandardMovieFormat:      n.StandardMovieFormat,
		MovieFolderFormat:        n.MovieFolderFormat,
		Id:                       n.Id,
	})
	if err != nil {
		return out, err
	}
	var examples struct {
		MovieExample       string `json:"movieExample"`
		MovieFolderExample string `json:"movieFolderExample"`
	}
	if err := decodeBody(ex.HttpResponse, &examples); err != nil {
		return out, fmt.Errorf("reading the naming examples: %w", err)
	}
	out.ExampleFile, out.ExampleFolder = examples.MovieExample, examples.MovieFolderExample

	return out, nil
}

func registerNamingTools(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "naming_get",
		Description: "Read how Radarr names films' files and folders: whether it renames at all, the file and folder patterns, what a colon becomes, and an example of each pattern applied. " +
			"With renaming off, Radarr keeps whatever names files arrive with.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, namingOut, error) {
		res, err := client.GetConfigNaming(ctx)
		if err != nil {
			return nil, namingOut{}, err
		}
		out, err := namingOf(ctx, client, res.Model)

		return nil, out, err
	})

	type namingEditIn struct {
		RenameMovies             *bool  `json:"rename_movies,omitempty"`
		ReplaceIllegalCharacters *bool  `json:"replace_illegal_characters,omitempty"`
		ColonReplacement         string `json:"colon_replacement,omitempty"          jsonschema:"delete, dash, spaceDash, spaceDashSpace or smart"`
		StandardMovieFormat      string `json:"standard_movie_format,omitempty"      jsonschema:"e.g. {Movie Title} ({Release Year}) {Quality Full}"`
		MovieFolderFormat        string `json:"movie_folder_format,omitempty"        jsonschema:"e.g. {Movie Title} ({Release Year})"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "naming_edit",
		Description: "Change how Radarr names films' files and folders: switch renaming on or off, set the file and folder patterns, and what a colon in a title becomes. " +
			"Files already in the library keep their names until renamed (movie_rename); only what is given changes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in namingEditIn) (*mcp.CallToolResult, namingOut, error) {
		if in.RenameMovies == nil && in.ReplaceIllegalCharacters == nil && in.ColonReplacement == "" && in.StandardMovieFormat == "" && in.MovieFolderFormat == "" {
			return nil, namingOut{}, errors.New("nothing to change: give rename_movies, replace_illegal_characters, colon_replacement, standard_movie_format or movie_folder_format")
		}
		res, err := client.GetConfigNaming(ctx)
		if err != nil {
			return nil, namingOut{}, err
		}
		edit := *res.Model
		if in.RenameMovies != nil {
			edit.RenameMovies = in.RenameMovies
		}
		if in.ReplaceIllegalCharacters != nil {
			edit.ReplaceIllegalCharacters = in.ReplaceIllegalCharacters
		}
		if in.ColonReplacement != "" {
			values := radarr.PossibleValuesForColonReplacementFormat()
			i := slices.IndexFunc(values, func(v string) bool { return strings.EqualFold(v, in.ColonReplacement) })
			if i < 0 {
				return nil, namingOut{}, fmt.Errorf("colon_replacement %q: want one of %s", in.ColonReplacement, strings.Join(values, ", "))
			}
			edit.ColonReplacementFormat = radarr.ColonReplacementFormat(values[i])
		}
		if in.StandardMovieFormat != "" {
			edit.StandardMovieFormat = in.StandardMovieFormat
		}
		if in.MovieFolderFormat != "" {
			edit.MovieFolderFormat = in.MovieFolderFormat
		}
		after, err := client.PutConfigNamingById(ctx, edit.Id, edit)
		if err != nil {
			return nil, namingOut{}, err
		}
		out, err := namingOf(ctx, client, after.Model)

		return nil, out, err
	})
}
