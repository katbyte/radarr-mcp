package tools

// The audits of what is on disk against what Radarr believes: names that do
// not follow its naming scheme, folders it does not know about, folders it
// believes in that are not there, and files in a film's folder it does not
// track.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
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
	byID := map[radarr.MovieID]*radarr.MovieResource{}
	var withFiles []radarr.MovieID
	for i := range movies {
		byID[movies[i].Id] = &movies[i]
		if isTrue(movies[i].HasFile) {
			withFiles = append(withFiles, movies[i].Id)
		}
	}
	if isTrue(naming.Answer.RenameMovies) {
		for start := 0; start < len(withFiles); start += renameBatch {
			ids := withFiles[start:min(start+renameBatch, len(withFiles))]
			res, err := e.c.GetRename(ctx, ids)
			if err != nil {
				return out, err
			}
			for _, p := range res.Answer {
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
// library is in, and says which of them are a film the library already has:
// a second copy beside its file, or where a film with no file probably is.
func unmappedAudit(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
	folders, err := e.c.GetRootFolder(ctx)
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
	byTitle := titleIndex(movies)

	out := auditOut{}
	for _, rf := range folders.Answer {
		if root != "" && rf.Path != root {
			continue
		}
		for _, u := range rf.UnmappedFolders {
			out.Scanned++
			title, year, rest := parseFolder(u.Name)
			f := auditFinding{Title: title, Year: year, Path: u.Path, Detail: "not in the library: movie_add with this folder takes it in"}
			if m := sameFilm(byTitle[cleanTitle(title)], year); m != nil {
				f.DuplicateOf = int(m.Id)
				if isTrue(m.HasFile) {
					f.Detail = fmt.Sprintf("a second copy of %s, which the library keeps at %s", titleYear(m.Title, m.Year), m.Path)
				} else {
					f.Detail = fmt.Sprintf("probably %s, which the library has with no file, at %s: movie_edit with this folder points the film here", titleYear(m.Title, m.Year), m.Path)
				}
				if rest != "" {
					f.Detail += " (this one says " + rest + ")"
				}
			}
			out.collect(f, in.Limit)
		}
	}

	return out, nil
}

// titleIndex is the library's films by every title each goes by, cleaned: its own, its original and its alternative titles, so a folder
// named for a film's title in another country still finds it.
func titleIndex(movies []radarr.MovieResource) map[string][]*radarr.MovieResource {
	out := map[string][]*radarr.MovieResource{}
	for i := range movies {
		m := &movies[i]
		seen := map[string]bool{}
		add := func(title string) {
			if key := cleanTitle(title); key != "" && !seen[key] {
				seen[key] = true
				out[key] = append(out[key], m)
			}
		}

		add(m.Title)
		add(m.OriginalTitle)
		for _, alt := range m.AlternateTitles {
			add(alt.Title)
		}
	}

	return out
}

// sameFilm is the film among those of a title that a folder of that year is: the one of the year, or failing that one a year either side or
// of its second year, since TMDB and a release often disagree by one. A folder with no year is the first of them.
func sameFilm(films []*radarr.MovieResource, year int) *radarr.MovieResource {
	if len(films) == 0 {
		return nil
	}
	if year == 0 {
		return films[0]
	}

	for _, m := range films {
		if m.Year == year {
			return m
		}
	}
	for _, m := range films {
		if m.Year-1 <= year && year <= m.Year+1 || val(m.SecondaryYear) == year {
			return m
		}
	}

	return nil
}

type diskSpaceIn struct {
	RootFolder     string `json:"root_folder,omitempty"      jsonschema:"only this root folder (path or id)"`
	MinFreePercent int    `json:"min_free_percent,omitempty" jsonschema:"flag a root folder whose disk has less than this share free, default 5"`
	MinFreeGB      int    `json:"min_free_gb,omitempty"      jsonschema:"also flag a root folder with less than this many GB free; off by default"`
}

// gb is a size in bytes as a person reads a disk's: whole GB, or MB under one.
func gb(bytes int64) string {
	if bytes < 1<<30 {
		return fmt.Sprintf("%d MB", bytes>>20)
	}

	return fmt.Sprintf("%d GB", bytes>>30)
}

// diskSpaceAudit finds the root folders running out of room, or out of reach: each is checked against Radarr's own floor for imports, a
// share of the disk it is on, and a number of GB when one is given.
func diskSpaceAudit(ctx context.Context, e *auditEnv, in diskSpaceIn) (auditOut, error) {
	roots, err := e.c.GetRootFolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	disks, err := e.c.GetDiskSpace(ctx)
	if err != nil {
		return auditOut{}, err
	}
	media, err := e.c.GetConfigMediaManagement(ctx)
	if err != nil {
		return auditOut{}, err
	}
	floor := int64(0)
	if media.Answer != nil {
		floor = int64(media.Answer.MinimumFreeSpaceWhenImporting) << 20
	}
	pct := in.MinFreePercent
	if pct <= 0 {
		pct = 5
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
	for _, rf := range roots.Answer {
		if root != "" && rf.Path != root {
			continue
		}
		out.Scanned++
		f := auditFinding{ID: int(rf.Id), Title: rf.Path, Path: rf.Path}
		if !isTrue(rf.Accessible) {
			f.Detail = "Radarr cannot reach it, so nothing in it can be imported, renamed or checked: server_health says why"
			out.collect(f, 0)
			continue
		}

		free := val(rf.FreeSpace)
		// the disk is the one whose mount the root folder sits deepest in
		var total int64
		mount := ""
		for _, d := range disks.Answer {
			if inFolder(rf.Path, d.Path) && len(d.Path) > len(mount) {
				mount, total = d.Path, d.TotalSpace
			}
		}

		switch {
		case floor > 0 && free < floor:
			f.Detail = fmt.Sprintf("%s free, under the %d MB Radarr keeps free when importing: it refuses every import here until room is made", gb(free), floor>>20)
		case total > 0 && free*100 < int64(pct)*total:
			f.Detail = fmt.Sprintf("%s free of %s (%d%%), under %d%%", gb(free), gb(total), free*100/total, pct)
		case in.MinFreeGB > 0 && free < int64(in.MinFreeGB)<<30:
			f.Detail = fmt.Sprintf("%s free, under the %d GB asked for", gb(free), in.MinFreeGB)
		default:
			continue
		}
		out.collect(f, 0)
	}

	return out, nil
}

// folderNames lists the folders directly inside a path, as Radarr sees them
// from where it runs.
func folderNames(ctx context.Context, c *radarr.Client, path string) (map[string]bool, error) {
	res, err := c.GetFileSystem(ctx, radarr.GetFileSystemOptions{
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
	if err := json.Unmarshal(res.Answer, &body); err != nil {
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
	roots, err := e.c.GetRootFolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	// one listing a root folder, and the folders in it no film is in: where
	// a renamed film now lives
	onDisk := map[string]map[string]bool{}
	unmapped := map[string][]radarr.UnmappedFolder{}
	for i := range roots.Answer {
		rf := &roots.Answer[i]
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

// looksLike picks the folder no film is in that reads as this film, by any of its titles.
func looksLike(m *radarr.MovieResource, folders []radarr.UnmappedFolder) string {
	titles := titleIndex([]radarr.MovieResource{*m})
	for _, u := range folders {
		title, year, _ := parseFolder(u.Name)
		if sameFilm(titles[cleanTitle(title)], year) != nil {
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
	extras := 0
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
		files, err := scanImport(ctx, e.c, radarr.GetManualImportOptions{Folder: m.Path, FilterExistingFiles: new(true)})
		if err != nil {
			return out, fmt.Errorf("scanning %s: %w", m.Path, err)
		}
		for j := range files {
			if tracked[files[j].Path] {
				continue
			}
			row := importOf(&files[j])
			if inExtrasFolder(row.RelativePath) {
				extras++
				continue
			}
			f := finding(m, "an untracked video file")
			f.File = row.RelativePath
			if len(row.Rejections) > 0 {
				f.Detail += ": " + strings.Join(row.Rejections, "; ")
			}
			out.collect(f, in.Limit)
		}
	}

	if extras > 0 {
		out.Note = fmt.Sprintf("%d videos in extras folders (%s) are left out: a theme or a trailer is not a copy of the film", extras, strings.Join(extrasFolders, ", "))
	}

	return out, nil
}

// extrasFolders are the folders a media server keeps a film's extras in, by the names Emby, Jellyfin, Plex and Kodi read.
var extrasFolders = []string{"backdrops", "behind the scenes", "clips", "deleted scenes", "extras", "featurettes", "interviews", "other", "samples", "scenes", "shorts", "theme-music", "trailers"}

// inExtrasFolder reports whether a file, by its path from the film's folder, sits in a folder of extras.
func inExtrasFolder(relative string) bool {
	folders := strings.Split(strings.ReplaceAll(relative, `\`, "/"), "/")

	return slices.ContainsFunc(folders[:len(folders)-1], func(folder string) bool { return slices.Contains(extrasFolders, strings.ToLower(folder)) })
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
		Description: "List the folders in the root folders that no film in the library is in: films on disk Radarr has never heard of, second copies of films it keeps elsewhere, and where a film it has with no file probably is. " +
			"A folder is matched to a film by any of the film's titles, and a year either side. movie_add with the folder takes a new one in; movie_edit with folder points a film with no file at its folder.",
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
		Name: "audit_disk_space",
		Description: "Check the root folders for space: one Radarr cannot reach, one with less free than Radarr keeps free when importing (it refuses every import there), and one under a share of its disk free (default 5%) or, when given, under a number of GB. " +
			"movie_batch_edit with root_folder moves films to another; audit_quality and audit_size find copies worth replacing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in diskSpaceIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := diskSpaceAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_untracked_files",
		Description: "Sweep the films' folders for video files Radarr does not track - a second copy beside the one it does, a failed upgrade, a file dropped in by hand - each with why Radarr would not take it. A video in a folder of extras (backdrops, trailers, featurettes and the like) is left out and counted in note. " +
			"One request a film, so slower than the other audits; import_apply imports a file chosen over the tracked one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := untrackedAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})
}
