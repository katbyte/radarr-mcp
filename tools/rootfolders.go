package tools

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rootFolderRow struct {
	ID              int    `json:"id"`
	Path            string `json:"path"`
	Accessible      bool   `json:"accessible"       jsonschema:"whether Radarr can reach it now"`
	FreeSpace       int64  `json:"free_space"       jsonschema:"bytes"`
	Movies          int    `json:"movies"           jsonschema:"films kept in it"`
	UnmappedFolders int    `json:"unmapped_folders" jsonschema:"folders in it no film is in the library for"`
}

// rootFolderRows lists the root folders with how many films each holds.
func rootFolderRows(ctx context.Context, c *radarr.Client) ([]rootFolderRow, error) {
	folders, err := c.GetRootfolder(ctx)
	if err != nil {
		return nil, err
	}
	movies, err := allMovies(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]rootFolderRow, 0, len(folders.Model))
	for _, f := range folders.Model {
		row := rootFolderRow{ID: f.Id, Path: f.Path, Accessible: isTrue(f.Accessible), FreeSpace: val(f.FreeSpace), UnmappedFolders: len(f.UnmappedFolders)}
		for i := range movies {
			if inFolder(movies[i].Path, f.Path) {
				row.Movies++
			}
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b rootFolderRow) int { return strings.Compare(a.Path, b.Path) })

	return out, nil
}

// inFolder reports whether path is inside folder.
func inFolder(path, folder string) bool {
	folder = strings.TrimRight(folder, "/") + "/"

	return strings.HasPrefix(path, folder)
}

func registerRootFolderTools(r *registry) {
	client := r.client

	type rootFolderListOut struct {
		RootFolders []rootFolderRow `json:"root_folders"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "rootfolder_list",
		Description: "List the root folders Radarr keeps films in, as paths Radarr sees: whether each is reachable, its free space, how many films it holds and how many folders in it no film is in the library for.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, rootFolderListOut, error) {
		rows, err := rootFolderRows(ctx, client)

		return nil, rootFolderListOut{RootFolders: rows}, err
	})

	type rootFolderAddIn struct {
		Path string `json:"path" jsonschema:"the folder, as Radarr sees it (the path inside its container, when it runs in one)"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "rootfolder_add",
		Description: "Add a root folder for Radarr to keep films in. Radarr checks it can reach and write it; the folders already in it show up as unmapped until films are added for them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in rootFolderAddIn) (*mcp.CallToolResult, rootFolderRow, error) {
		path := strings.TrimSpace(in.Path)
		if path == "" {
			return nil, rootFolderRow{}, errors.New("no path")
		}
		res, err := client.PostRootfolder(ctx, radarr.RootFolderResource{Path: path})
		if err != nil {
			return nil, rootFolderRow{}, err
		}
		f := res.Model

		return nil, rootFolderRow{ID: f.Id, Path: f.Path, Accessible: isTrue(f.Accessible), FreeSpace: val(f.FreeSpace), UnmappedFolders: len(f.UnmappedFolders)}, nil
	})

	type rootFolderDeleteIn struct {
		RootFolder string `json:"root_folder" jsonschema:"the root folder: its path or id"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "rootfolder_delete",
		Description: "Stop using a root folder: nothing on disk is touched, and films already in it stay in the library with their paths. " +
			"Answers with how many films were still in it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in rootFolderDeleteIn) (*mcp.CallToolResult, rootFolderRow, error) {
		f, err := resolveRootFolder(ctx, client, in.RootFolder)
		if err != nil {
			return nil, rootFolderRow{}, err
		}
		if in.RootFolder == "" {
			return nil, rootFolderRow{}, errors.New("name the root folder to remove")
		}
		rows, err := rootFolderRows(ctx, client)
		if err != nil {
			return nil, rootFolderRow{}, err
		}
		if _, err := client.DeleteRootfolderById(ctx, f.Id); err != nil {
			return nil, rootFolderRow{}, err
		}
		for _, row := range rows {
			if row.ID == f.Id {
				return nil, row, nil
			}
		}

		return nil, rootFolderRow{ID: f.Id, Path: f.Path}, nil
	})
}
