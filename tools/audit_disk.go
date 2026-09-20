package tools

// The audits of what is on disk against what Radarr believes: names that do
// not follow its naming scheme, folders it does not know about, folders it
// believes in that are not there, and files in a film's folder it does not
// track.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// renameBatch is how many films one rename preview asks about: the ids go in
// the query string, which a server caps.
const renameBatch = 100

type namingIn struct {
	auditIn
	Folders *bool `json:"folders,omitempty" jsonschema:"also check each film's folder name, one request a film; default true"`
}

// namingAudit compares every file's name with the one Radarr's naming scheme
// gives it (Radarr's own rename preview, which it gives only with renaming
// switched on) and, with folders, every folder's name with the one its
// folder format gives.
func namingAudit(ctx context.Context, e *auditEnv, in namingIn) (auditOut, error) {
	movies, err := e.scope(ctx, in.auditIn)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{Scanned: len(movies)}

	naming, err := e.c.GetConfigNaming(ctx)
	if err != nil {
		return out, err
	}
	byID := map[int]*radarr.MovieResource{}
	var withFiles []int
	for i := range movies {
		byID[movies[i].Id] = &movies[i]
		if isTrue(movies[i].HasFile) {
			withFiles = append(withFiles, movies[i].Id)
		}
	}
	if isTrue(naming.Model.RenameMovies) {
		for start := 0; start < len(withFiles); start += renameBatch {
			ids := withFiles[start:min(start+renameBatch, len(withFiles))]
			res, err := e.c.GetRename(ctx, radarr.GetRenameOperationOptions{MovieId: ids})
			if err != nil {
				return out, err
			}
			for _, p := range res.Model {
				m := byID[p.MovieId]
				if m == nil {
					continue
				}
				f := finding(m, fmt.Sprintf("file %q would be named %q", p.ExistingPath, p.NewPath))
				f.File = p.ExistingPath
				out.collect(f, in.Limit)
			}
		}
	} else {
		out.Note = "renaming is switched off in Radarr, so it gives no file names to compare with; naming_edit rename_movies true switches it on (folders are still checked)"
	}

	if in.Folders != nil && !*in.Folders {
		return out, nil
	}
	for i := range movies {
		m := &movies[i]
		res, err := e.c.GetMovieByIdFolder(ctx, m.Id)
		if err != nil {
			return out, err
		}
		var folder struct {
			Folder string `json:"folder"`
		}
		if err := decodeBody(res.HttpResponse, &folder); err != nil {
			return out, fmt.Errorf("reading %s's folder: %w", titleYear(m.Title, m.Year), err)
		}
		have := baseName(m.Path)
		if folder.Folder != "" && have != folder.Folder {
			out.collect(finding(m, fmt.Sprintf("folder %q would be named %q", have, folder.Folder)), in.Limit)
		}
	}

	return out, nil
}

// baseName is the last element of a path.
func baseName(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}

	return path
}

// parseFolder reads "Alien (1979) Directors Cut" as the title, year and
// what follows the year.
func parseFolder(name string) (title string, year int, rest string) {
	loc := folderYear.FindStringSubmatchIndex(name)
	if loc == nil {
		return strings.TrimSpace(name), 0, ""
	}
	y, _ := fmt.Sscanf(name[loc[2]:loc[3]], "%d", &year)
	if y != 1 {
		year = 0
	}

	return strings.TrimSpace(name[:loc[0]]), year, strings.TrimSpace(name[loc[1]:])
}

// unmappedAudit lists the folders in the root folders that no film in the
// library is in, and says which of them are a second copy of a film the
// library has elsewhere.
func unmappedAudit(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
	folders, err := e.c.GetRootfolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	movies, err := e.allMovies(ctx)
	if err != nil {
		return auditOut{}, err
	}
	root := ""
	if in.RootFolder != "" {
		f, err := resolveRootFolder(ctx, e.c, in.RootFolder)
		if err != nil {
			return auditOut{}, err
		}
		root = f.Path
	}
	out := auditOut{}
	for _, rf := range folders.Model {
		if root != "" && rf.Path != root {
			continue
		}
		for _, u := range rf.UnmappedFolders {
			out.Scanned++
			title, year, rest := parseFolder(u.Name)
			f := auditFinding{Title: title, Year: year, Path: u.Path, Detail: "not in the library: movie_add with this folder takes it in"}
			for i := range movies {
				m := &movies[i]
				if cleanTitle(m.Title) == cleanTitle(title) && (year == 0 || m.Year == year) {
					f.DuplicateOf = m.Id
					f.Detail = fmt.Sprintf("a second copy of %s, which the library keeps at %s", titleYear(m.Title, m.Year), m.Path)
					if rest != "" {
						f.Detail += " (this one says " + rest + ")"
					}
					break
				}
			}
			out.collect(f, in.Limit)
		}
	}

	return out, nil
}

// folderNames lists the folders directly inside a path, as Radarr sees them
// from where it runs.
func folderNames(ctx context.Context, c *radarr.Client, path string) (map[string]bool, error) {
	res, err := c.GetFilesystem(ctx, radarr.GetFilesystemOperationOptions{
		Path: strings.TrimSuffix(path, "/") + "/", IncludeFiles: new(false), AllowFoldersWithoutTrailingSlashes: new(true),
	})
	if err != nil {
		return nil, err
	}
	// the filesystem browser has no declared shape: it answers the folders
	// and files of one directory
	var body struct {
		Directories []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"directories"`
	}
	if err := json.Unmarshal(res.Model, &body); err != nil {
		return nil, fmt.Errorf("reading the folders in %s: %w", path, err)
	}
	out := make(map[string]bool, len(body.Directories))
	for _, d := range body.Directories {
		out[strings.TrimSuffix(d.Path, "/")] = true
	}

	return out, nil
}

// missingFoldersAudit finds films whose folder is not on disk: moved or
// renamed behind Radarr's back. A folder that no film is in and reads as the
// film is named as where it probably went. A film Radarr has no file for is
// left alone unless such a folder exists, because Radarr makes a film's
// folder when it imports, not when the film is added.
func missingFoldersAudit(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
	movies, err := e.scope(ctx, in)
	if err != nil {
		return auditOut{}, err
	}
	roots, err := e.c.GetRootfolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	// one listing a root folder, and the folders in it no film is in: where
	// a renamed film now lives
	onDisk := map[string]map[string]bool{}
	unmapped := map[string][]radarr.UnmappedFolder{}
	for i := range roots.Model {
		rf := &roots.Model[i]
		if !isTrue(rf.Accessible) {
			continue
		}
		folders, err := folderNames(ctx, e.c, rf.Path)
		if err != nil {
			return auditOut{}, err
		}
		onDisk[strings.TrimSuffix(rf.Path, "/")] = folders
		unmapped[strings.TrimSuffix(rf.Path, "/")] = rf.UnmappedFolders
	}

	out := auditOut{}
	for i := range movies {
		m := &movies[i]
		if m.Path == "" {
			continue
		}
		// the most specific root folder the film is in: root folders nest
		// (/media/movies and /media/movies/4k), and the film's folder is
		// listed by the innermost one
		root := ""
		for path := range onDisk {
			if inFolder(m.Path, path) && len(path) > len(root) {
				root = path
			}
		}
		if root == "" {
			// an unreachable root folder is one problem, not one a film:
			// server_health and rootfolder_list say which
			continue
		}
		out.Scanned++
		if onDisk[root][strings.TrimSuffix(m.Path, "/")] {
			continue
		}
		folder := looksLike(m, unmapped[root])
		if folder == "" && !isTrue(m.HasFile) {
			// a film Radarr has no file for has no folder yet: it makes one
			// when it imports. Only a folder that reads as the film says
			// otherwise
			continue
		}
		f := finding(m, "its folder is not on disk, though Radarr has a file in it: moved or renamed outside Radarr "+
			"(movie_edit with folder points the film at where it is now; movie_rescan lets go of the file)")
		if folder != "" {
			f.Folder = folder
			f.Detail = "its folder is not on disk; " + folder + " is there and reads as this film (movie_edit with folder points the film at it)"
		}
		out.collect(f, in.Limit)
	}

	return out, nil
}

// looksLike picks the folder no film is in that reads as this film.
func looksLike(m *radarr.MovieResource, folders []radarr.UnmappedFolder) string {
	for _, u := range folders {
		title, year, _ := parseFolder(u.Name)
		if cleanTitle(title) == cleanTitle(m.Title) && (year == 0 || year == m.Year) {
			return u.Path
		}
	}

	return ""
}

// trackedPaths are the files the library's films have.
func trackedPaths(movies []radarr.MovieResource) map[string]bool {
	out := map[string]bool{}
	for i := range movies {
		if f := movies[i].MovieFile; f != nil && f.Path != "" {
			out[f.Path] = true
		}
	}

	return out
}

// untrackedAudit asks Radarr, film by film, what video files in the film's
// folder it does not track, and why it would not import each. Radarr's own
// filter of files it already has only works for a file whose name parses as
// its film ("Blade Runner 2049" reads as a film of 2049), so the files the
// library tracks are taken out here.
func untrackedAudit(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
	movies, err := e.scope(ctx, in)
	if err != nil {
		return auditOut{}, err
	}
	library, err := e.allMovies(ctx)
	if err != nil {
		return auditOut{}, err
	}
	tracked := trackedPaths(library)
	out := auditOut{}
	for i := range movies {
		m := &movies[i]
		if !isTrue(m.HasFile) {
			// a film with no file may have no folder at all, and one it
			// has would be picked up by a rescan on its own
			continue
		}
		out.Scanned++
		// by folder alone: given the film's id as well, Radarr ignores the
		// folder and the filter and answers the film's own tracked files
		files, err := scanImport(ctx, e.c, radarr.GetManualimportOperationOptions{Folder: m.Path, FilterExistingFiles: new(true)})
		if err != nil {
			return out, fmt.Errorf("scanning %s: %w", m.Path, err)
		}
		for j := range files {
			if tracked[files[j].Path] {
				continue
			}
			row := importOf(&files[j])
			f := finding(m, "an untracked video file")
			f.File = row.RelativePath
			if len(row.Rejections) > 0 {
				f.Detail += ": " + strings.Join(row.Rejections, "; ")
			}
			out.collect(f, in.Limit)
		}
	}

	return out, nil
}

func registerDiskAudits(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_naming",
		Description: "Sweep the library for files and folders not named the way Radarr's naming scheme would name them: a scene-named file, a folder named for the wrong film. " +
			"Files are compared only with renaming switched on (naming_edit), which Radarr needs to give new names; movie_rename renames files, and movie_edit into the root folder a film is already in renames its folder.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in namingIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := namingAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_unmapped_folders",
		Description: "List the folders in the root folders that no film in the library is in: films on disk Radarr has never heard of, and second copies of films it keeps elsewhere, each named with the copy it duplicates. " +
			"movie_add with the folder takes one in.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := unmappedAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_missing_folders",
		Description: "Sweep the library for films whose folder is not on disk: moved or renamed behind Radarr's back, so it has lost the film's files and would download them again. " +
			"Each names the folder in the same root folder that reads as the film, when there is one, and movie_edit with folder points the film at it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := missingFoldersAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_untracked_files",
		Description: "Sweep the films' folders for video files Radarr does not track - a second copy beside the one it does, a failed upgrade, a file dropped in by hand - each with why Radarr would not take it. " +
			"One request a film, so slower than the other audits; import_apply imports a file chosen over the tracked one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := untrackedAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})
}
