package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// importRow is a file manual import could take in, and what stands in its
// way.
type importRow struct {
	Path         string   `json:"path"`
	RelativePath string   `json:"relative_path,omitempty"`
	Size         int64    `json:"size"                    jsonschema:"bytes"`
	Quality      string   `json:"quality,omitempty"       jsonschema:"the quality Radarr parses from its name and contents"`
	Languages    []string `json:"languages,omitempty"`
	ReleaseGroup string   `json:"release_group,omitempty"`
	Movie        string   `json:"movie,omitempty"         jsonschema:"the film Radarr matched it to, as Title (Year); empty when it matched none"`
	MovieID      int      `json:"movie_id,omitempty"`
	DownloadID   string   `json:"download_id,omitempty"`
	Rejections   []string `json:"rejections,omitempty"    jsonschema:"why Radarr would not import it on its own"`
	Importable   bool     `json:"importable"              jsonschema:"true when nothing but a choice of film stands in the way: import_apply can take it"`
}

func importOf(f *radarr.ManualImportResource) importRow {
	row := importRow{
		Path:         f.Path,
		RelativePath: f.RelativePath,
		Size:         f.Size,
		Quality:      qualityName(f.Quality),
		Languages:    languageNames(f.Languages),
		ReleaseGroup: f.ReleaseGroup,
		DownloadID:   f.DownloadId,
		Importable:   true,
	}
	if f.Movie != nil {
		row.Movie = titleYear(f.Movie.Title, f.Movie.Year)
		row.MovieID = f.Movie.Id
	}
	for _, rej := range f.Rejections {
		row.Rejections = append(row.Rejections, rej.Reason)
		// "Unknown Movie" is what a film chosen by hand answers; every other
		// permanent rejection is Radarr refusing the file itself
		if rej.Type == radarr.RejectionTypePermanent && rej.Reason != "Unknown Movie" {
			row.Importable = false
		}
	}

	return row
}

type importScanIn struct {
	Folder     string `json:"folder,omitempty"      jsonschema:"a folder as Radarr sees it (the path inside its container, when it runs in one)"`
	DownloadID string `json:"download_id,omitempty" jsonschema:"or a download in the queue, by its download id"`
	Movie      string `json:"movie,omitempty"       jsonschema:"or a film, whose own folder is scanned for files it does not track"`
}

type importFile struct {
	Path    string `json:"path"              jsonschema:"the file, as import_scan lists it"`
	Movie   string `json:"movie"             jsonschema:"the film it is: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
	Quality string `json:"quality,omitempty" jsonschema:"override the quality Radarr parsed (Bluray-1080p, WEBDL-720p...)"`
}

type identifyIn struct {
	Folders    []string `json:"folders,omitempty"     jsonschema:"the folders to identify, as Radarr sees them; default the folders in the root folders no film is in, which audit_unmapped_folders lists"`
	RootFolder string   `json:"root_folder,omitempty" jsonschema:"only folders in this root folder (path or id)"`
	Limit      int      `json:"limit,omitempty"       jsonschema:"folders to identify, default 10: each one is a lookup and a scan"`
	Candidates int      `json:"candidates,omitempty"  jsonschema:"candidate films a folder, default 3"`
}

// identifyRow is one folder and what film it holds.
type identifyRow struct {
	Folder     string      `json:"folder"`
	Name       string      `json:"name"                 jsonschema:"the folder's own name, which the candidates were looked up from"`
	Files      []importRow `json:"files,omitempty"      jsonschema:"the video files in it, with the quality Radarr reads and the film it matched each to"`
	Candidates []lookupRow `json:"candidates,omitempty" jsonschema:"what TMDB offers for the name, best first: movie_add takes the right one with this folder, and Radarr imports the files in it"`
	Note       string      `json:"note,omitempty"`
}

type importApplyIn struct {
	Files []importFile `json:"files"`
	Mode  string       `json:"mode,omitempty" jsonschema:"move or copy; default leaves it to Radarr, which moves a file and copies (or hardlinks) a download still seeding"`
}

func registerImportTools(r *registry) {
	client := r.client

	type importScanOut struct {
		Files []importRow `json:"files"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "import_scan",
		Description: "Scan a folder, a download in the queue, or a film's own folder for video files Radarr could import: each with the film and quality Radarr makes of it, and why it would refuse it (an unknown film, not an upgrade, a sample). " +
			"import_apply imports the ones chosen. A film's folder lists only the files Radarr does not already track there.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in importScanIn) (*mcp.CallToolResult, importScanOut, error) {
		opts := radarr.GetManualimportOperationOptions{Folder: in.Folder, DownloadId: in.DownloadID, FilterExistingFiles: new(true)}
		switch {
		case in.Movie != "":
			m, err := resolveMovie(ctx, client, in.Movie)
			if err != nil {
				return nil, importScanOut{}, err
			}
			// the folder alone: given the film's id as well, Radarr ignores
			// the folder and the filter and answers the film's own files
			opts.Folder = m.Path
		case in.Folder == "" && in.DownloadID == "":
			return nil, importScanOut{}, errors.New("name a folder, a download_id or a movie to scan")
		}
		files, err := scanImport(ctx, client, opts)
		if err != nil {
			return nil, importScanOut{}, err
		}
		// Radarr's filter of the files it has only works for a file whose
		// name parses as its own film, so the ones the library tracks are
		// taken out here: there is nothing to import in them
		library, err := allMovies(ctx, client)
		if err != nil {
			return nil, importScanOut{}, err
		}
		tracked := trackedPaths(library)
		out := importScanOut{}
		for i := range files {
			if !tracked[files[i].Path] {
				out.Files = append(out.Files, importOf(&files[i]))
			}
		}

		return nil, out, nil
	})

	type identifyOut struct {
		Folders []identifyRow `json:"folders"`
		Total   int           `json:"total"          jsonschema:"folders there are to identify, of which this many were"`
		Note    string        `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "import_identify",
		Description: "Work out which film each folder on disk holds, for folders no film in the library is in: what is in the folder, and the films TMDB offers for its name, best first. " +
			"Then movie_add with the folder and the chosen film takes it into the library, and Radarr imports what is in it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in identifyIn) (*mcp.CallToolResult, identifyOut, error) {
		folders := in.Folders
		out := identifyOut{}
		if len(folders) == 0 {
			var err error
			if folders, err = unmappedPaths(ctx, client, in.RootFolder); err != nil {
				return nil, out, err
			}
		}
		out.Total = len(folders)
		library, err := allMovies(ctx, client)
		if err != nil {
			return nil, out, err
		}
		have := map[int]int{}
		for i := range library {
			have[library[i].TmdbId] = library[i].Id
		}
		limit := limitOr(in.Limit, 10)
		for _, folder := range folders {
			if len(out.Folders) == limit {
				out.Note = fmt.Sprintf("%d folders of %d, the ones asked for; raise limit or name folders for the rest", limit, out.Total)
				break
			}
			row, err := identifyFolder(ctx, client, folder, have, limitOr(in.Candidates, 3))
			if err != nil {
				return nil, out, err
			}
			out.Folders = append(out.Folders, row)
		}

		return nil, out, nil
	})

	type importApplyOut struct {
		Command commandOut `json:"command"`
		Movies  []movieRow `json:"movies"  jsonschema:"the films the files were imported into, as they stand afterwards"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "import_apply",
		Description: "Import video files into films by hand - a download Radarr could not match, a copy left in a film's folder, a file dropped anywhere Radarr can see - naming the film each one is, and optionally its quality. " +
			"The file takes the film's place in its folder, named by the naming scheme; a file the film already has is replaced.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in importApplyIn) (*mcp.CallToolResult, importApplyOut, error) {
		if len(in.Files) == 0 {
			return nil, importApplyOut{}, errors.New("no files: import_scan lists what can be imported")
		}
		mode := strings.ToLower(strings.TrimSpace(in.Mode))
		if mode != "" && mode != "move" && mode != "copy" {
			return nil, importApplyOut{}, fmt.Errorf("mode %q: want move or copy", in.Mode)
		}
		files, ids, err := importFiles(ctx, client, in.Files)
		if err != nil {
			return nil, importApplyOut{}, err
		}
		body := map[string]any{"name": "ManualImport", "files": files}
		if mode != "" {
			body["importMode"] = mode
		}
		cmd, err := runCommand(ctx, client, body, defaultWait)
		out := importApplyOut{Command: cmd}
		if err != nil {
			return nil, out, err
		}
		out.Movies, err = rowsFor(ctx, client, ids)

		return nil, out, err
	})
}

// importFiles builds the ManualImport command's files, and the films they go
// to, from what a tool was given: each file as Radarr's scan of its folder
// reads it, the quality overridden when one was given.
func importFiles(ctx context.Context, c *radarr.Client, in []importFile) (files []map[string]any, ids []int, err error) {
	library, err := allMovies(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	scanned, err := scanFolders(ctx, c, in)
	if err != nil {
		return nil, nil, err
	}

	for _, f := range in {
		m, err := matchMovie(library, f.Movie)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		s, ok := scanned[f.Path]
		if !ok {
			return nil, nil, fmt.Errorf("%s is not a video file Radarr can see; import_scan lists what it can", f.Path)
		}
		quality := s.Quality
		if f.Quality != "" {
			q, err := qualityByName(ctx, c, f.Quality)
			if err != nil {
				return nil, nil, err
			}
			quality = &radarr.QualityModel{Quality: q, Revision: &radarr.Revision{Version: 1}}
		}
		languages := s.Languages
		if len(languages) == 0 {
			languages = []radarr.Language{{Id: 1, Name: "English"}}
		}
		files = append(files, map[string]any{
			"path": f.Path, "movieId": m.Id, "quality": quality, "languages": languages,
			"releaseGroup": s.ReleaseGroup, "downloadId": s.DownloadId, "indexerFlags": s.IndexerFlags,
		})
		if !slices.Contains(ids, m.Id) {
			ids = append(ids, m.Id)
		}
	}

	return files, ids, nil
}

// scanFolders is what Radarr makes of each file, from a scan of its folder:
// the quality and languages an import carries unless overridden. Each
// folder is scanned once, however many of its files are named.
func scanFolders(ctx context.Context, c *radarr.Client, in []importFile) (map[string]radarr.ManualImportResource, error) {
	scanned := map[string]radarr.ManualImportResource{}
	done := map[string]bool{}
	for _, f := range in {
		dir := path.Dir(f.Path)
		if done[dir] {
			continue
		}
		done[dir] = true
		files, err := scanImport(ctx, c, radarr.GetManualimportOperationOptions{Folder: dir, FilterExistingFiles: new(false)})
		if err != nil {
			return nil, err
		}
		for i := range files {
			scanned[files[i].Path] = files[i]
		}
	}

	return scanned, nil
}

// scanImport asks Radarr what it would make of the files in a folder or a
// download.
func scanImport(ctx context.Context, c *radarr.Client, opts radarr.GetManualimportOperationOptions) ([]radarr.ManualImportResource, error) {
	res, err := c.GetManualimport(ctx, opts)
	if err != nil {
		return nil, err
	}

	return res.Model, nil
}

// unmappedPaths are the folders in the root folders that no film is in.
func unmappedPaths(ctx context.Context, c *radarr.Client, rootFolder string) ([]string, error) {
	want := ""
	if rootFolder != "" {
		f, err := resolveRootFolder(ctx, c, rootFolder)
		if err != nil {
			return nil, err
		}
		want = f.Path
	}
	res, err := c.GetRootfolder(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rf := range res.Model {
		if want != "" && rf.Path != want {
			continue
		}
		for _, u := range rf.UnmappedFolders {
			out = append(out, u.Path)
		}
	}
	slices.Sort(out)

	return out, nil
}

// identifyFolder reads one folder: the files in it, and the films TMDB
// offers for its name. Radarr's own lookup does the matching, so a scene
// name, an accent or a year in the wrong place is its problem, not ours.
func identifyFolder(ctx context.Context, c *radarr.Client, folder string, have map[int]int, candidates int) (identifyRow, error) {
	folder = strings.TrimRight(strings.TrimSpace(folder), "/")
	row := identifyRow{Folder: folder, Name: baseName(folder)}
	files, err := scanImport(ctx, c, radarr.GetManualimportOperationOptions{Folder: folder, FilterExistingFiles: new(false)})
	if err != nil {
		return row, fmt.Errorf("scanning %s: %w", folder, err)
	}
	for i := range files {
		row.Files = append(row.Files, importOf(&files[i]))
	}
	if len(row.Files) == 0 {
		row.Note = "no video file Radarr can see in it"
	}
	found, err := lookupMovies(ctx, c, row.Name)
	if err != nil || len(found) == 0 {
		// the folder's name as it stands found nothing: try the title and
		// year read out of it
		title, year, _ := parseFolder(row.Name)
		term := title
		if year != 0 {
			term = fmt.Sprintf("%s %d", title, year)
		}
		if term != "" && term != row.Name {
			found, err = lookupMovies(ctx, c, term)
		}
	}
	if err != nil {
		return row, fmt.Errorf("looking %s up: %w", row.Name, err)
	}
	for i := range found {
		if len(row.Candidates) == candidates {
			break
		}
		row.Candidates = append(row.Candidates, lookupOf(&found[i], have))
	}
	if len(row.Candidates) == 0 && row.Note == "" {
		row.Note = "nothing on TMDB for this name: movie_lookup with a better term finds it"
	}

	return row, nil
}
