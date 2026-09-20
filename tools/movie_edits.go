package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type movieAddIn struct {
	Movie               string   `json:"movie"                          jsonschema:"the film to add: tmdb:<id>, imdb:<tt id>, or a title with its year (\"Heat (1995)\") that movie_lookup finds exactly once"`
	QualityProfile      string   `json:"quality_profile"                jsonschema:"the quality profile's name or id"`
	RootFolder          string   `json:"root_folder,omitempty"          jsonschema:"the root folder's path or id; may be left out when there is only one"`
	Folder              string   `json:"folder,omitempty"               jsonschema:"the film's folder inside the root folder, when it is already on disk under another name than the one Radarr would give it"`
	Monitored           *bool    `json:"monitored,omitempty"            jsonschema:"whether Radarr should look for it and upgrade it; default true"`
	MinimumAvailability string   `json:"minimum_availability,omitempty" jsonschema:"when Radarr starts looking: announced, inCinemas or released (default)"`
	Tags                []string `json:"tags,omitempty"                 jsonschema:"tag labels, created when they do not exist"`
	Search              bool     `json:"search,omitempty"               jsonschema:"search the indexers for it straight away"`
}

// editFields are what movie_edit and movie_batch_edit change; every one left
// out is left alone.
type editFields struct {
	Monitored           *bool    `json:"monitored,omitempty"`
	QualityProfile      string   `json:"quality_profile,omitempty"      jsonschema:"a quality profile's name or id"`
	MinimumAvailability string   `json:"minimum_availability,omitempty" jsonschema:"announced, inCinemas or released"`
	RootFolder          string   `json:"root_folder,omitempty"          jsonschema:"move the films to this root folder (path or id), each folder named by the naming scheme; the root folder a film is already in renames just its folder"`
	MoveFiles           *bool    `json:"move_files,omitempty"           jsonschema:"with root_folder: move the folders on disk too (default true); false only points Radarr at folders already moved"`
	Tags                []string `json:"tags,omitempty"                 jsonschema:"replace the films' tags with these labels"`
	AddTags             []string `json:"add_tags,omitempty"             jsonschema:"labels to add, created when they do not exist"`
	RemoveTags          []string `json:"remove_tags,omitempty"          jsonschema:"labels to take off"`
}

func (e editFields) empty() bool {
	return e.Monitored == nil && e.QualityProfile == "" && e.MinimumAvailability == "" && e.RootFolder == "" &&
		e.Tags == nil && len(e.AddTags) == 0 && len(e.RemoveTags) == 0
}

type movieEditIn struct {
	Movie  string `json:"movie"            jsonschema:"the film: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
	Folder string `json:"folder,omitempty" jsonschema:"point the film at this folder, which is already on disk, without moving anything: the fix for a folder renamed or moved behind Radarr's back (audit_missing_folders finds them). A path, or a folder name inside the film's root folder"`
	editFields
}

type movieBatchEditIn struct {
	Movies []string `json:"movies" jsonschema:"the films: ids, tmdb:<id>, imdb:<tt id> or titles"`
	editFields
}

type moviesIn struct {
	Movies []string `json:"movies"         jsonschema:"the films: ids, tmdb:<id>, imdb:<tt id> or titles"`
	Wait   int      `json:"wait,omitempty" jsonschema:"seconds to wait for Radarr to finish, default 120; -1 starts it and returns at once"`
}

type editOut struct {
	Movies []movieRow `json:"movies" jsonschema:"the films as they stand after the edit"`
}

func registerMovieEditTools(r *registry) {
	client := r.client

	add(r, writeTool, &mcp.Tool{
		Name: "movie_add",
		Description: "Add a film to the library by tmdb:<id>, imdb:<tt id> or an unambiguous title and year, into a root folder with a quality profile. " +
			"When its folder is already on disk (an unmapped folder), name it with folder and Radarr takes the file that is there. Answers with the film as added, with its file once Radarr has scanned the folder.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieAddIn) (*mcp.CallToolResult, movieDetail, error) {
		found, err := addCandidate(ctx, client, in.Movie)
		if err != nil {
			return nil, movieDetail{}, err
		}
		library, err := allMovies(ctx, client)
		if err != nil {
			return nil, movieDetail{}, err
		}
		for i := range library {
			m := &library[i]
			if m.TmdbId == found.TmdbId {
				return nil, movieDetail{}, fmt.Errorf("%s is already in the library as id %d, at %s", titleYear(m.Title, m.Year), m.Id, m.Path)
			}
		}
		if in.QualityProfile == "" {
			return nil, movieDetail{}, errors.New("quality_profile is required: qualityprofile_list names them")
		}
		profile, err := resolveProfile(ctx, client, in.QualityProfile)
		if err != nil {
			return nil, movieDetail{}, err
		}
		root, err := resolveRootFolder(ctx, client, in.RootFolder)
		if err != nil {
			return nil, movieDetail{}, err
		}
		availability := radarr.MovieStatusTypeReleased
		if in.MinimumAvailability != "" {
			if availability, err = minimumAvailability(in.MinimumAvailability); err != nil {
				return nil, movieDetail{}, err
			}
		}
		tags, err := resolveTags(ctx, client, in.Tags, true)
		if err != nil {
			return nil, movieDetail{}, err
		}

		// the lookup result is what Radarr's own client posts back, with the
		// choices made here filled in
		body := *found
		body.Id = 0
		body.QualityProfileId = profile.Id
		body.Monitored = new(in.Monitored == nil || *in.Monitored)
		body.MinimumAvailability = availability
		body.Tags = tags
		body.AddOptions = &radarr.AddMovieOptions{SearchForMovie: new(in.Search), Monitor: radarr.MonitorTypesMovieOnly}
		if folder := strings.Trim(in.Folder, "/"); folder != "" {
			body.Path = strings.TrimRight(root.Path, "/") + "/" + folder
			body.RootFolderPath = ""
		} else {
			body.Path = ""
			body.RootFolderPath = root.Path
		}
		added, err := client.PostMovie(ctx, body)
		if err != nil {
			return nil, movieDetail{}, err
		}

		// Radarr refreshes the new film and scans its folder in a command of
		// its own; waiting for that one, rather than starting a scan beside
		// it, is what makes the file (if the folder has one) part of the
		// answer - two scans at once import the same file twice
		if err := waitForNewMovie(ctx, client, added.Model.Id); err != nil {
			return nil, movieDetail{}, err
		}
		m, err := resolveMovie(ctx, client, strconv.Itoa(added.Model.Id))
		if err != nil {
			return nil, movieDetail{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, movieDetail{}, err
		}

		return nil, detailOf(m, lk), nil
	})

	add(r, writeTool, &mcp.Tool{
		Name: "movie_edit",
		Description: "Change a film: whether it is monitored, its quality profile, its minimum availability, its tags (replace, add or remove), or its root folder - which moves its folder on disk unless move_files is false. " +
			"folder instead points the film at a folder already on disk, moving nothing, and rescans it. Only what is given changes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieEditIn) (*mcp.CallToolResult, editOut, error) {
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, editOut{}, err
		}
		if in.Folder != "" {
			if in.RootFolder != "" {
				return nil, editOut{}, errors.New("folder and root_folder do not go together: root_folder moves the film's folder, folder points the film at one already there")
			}
			row, pointErr := pointAt(ctx, client, m, in.Folder)
			if pointErr != nil {
				return nil, editOut{}, pointErr
			}
			if in.empty() {
				return nil, editOut{Movies: []movieRow{row}}, nil
			}
		}
		rows, err := applyEdit(ctx, client, []int{m.Id}, in.editFields)

		return nil, editOut{Movies: rows}, err
	})

	add(r, writeTool, &mcp.Tool{
		Name: "movie_batch_edit",
		Description: "Make the same change to many films at once: monitored, quality profile, minimum availability, root folder (moving their folders unless move_files is false), and tags, which add_tags and remove_tags change on each film without touching its other tags. " +
			"Only what is given changes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieBatchEditIn) (*mcp.CallToolResult, editOut, error) {
		movies, err := resolveMovies(ctx, client, in.Movies)
		if err != nil {
			return nil, editOut{}, err
		}
		ids := make([]int, 0, len(movies))
		for i := range movies {
			m := &movies[i]
			ids = append(ids, m.Id)
		}
		rows, err := applyEdit(ctx, client, ids, in.editFields)

		return nil, editOut{Movies: rows}, err
	})

	type movieDeleteIn struct {
		Movie        string `json:"movie"                   jsonschema:"the film: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
		DeleteFiles  bool   `json:"delete_files,omitempty"  jsonschema:"also delete its folder and files from disk; default false leaves them there"`
		AddExclusion bool   `json:"add_exclusion,omitempty" jsonschema:"also add it to the import list exclusions, so a list does not add it back"`
	}
	type movieDeleteOut struct {
		Deleted      movieRow `json:"deleted"`
		FilesDeleted bool     `json:"files_deleted" jsonschema:"its folder and files are being deleted: Radarr does it in the background, so they can still be on disk for a moment after this answers"`
		Excluded     bool     `json:"excluded"      jsonschema:"it is being added to the exclusions, likewise in the background"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "movie_delete",
		Description: "Remove a film from the library; with delete_files, delete its folder and files from disk as well (to the recycle bin, when Radarr has one set up). " +
			"add_exclusion stops an import list adding it back.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieDeleteIn) (*mcp.CallToolResult, movieDeleteOut, error) {
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, movieDeleteOut{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, movieDeleteOut{}, err
		}
		if _, err := client.DeleteMovieById(ctx, m.Id, radarr.DeleteMovieByIdOperationOptions{
			DeleteFiles: new(in.DeleteFiles), AddImportExclusion: new(in.AddExclusion),
		}); err != nil {
			return nil, movieDeleteOut{}, err
		}

		return nil, movieDeleteOut{Deleted: rowOf(m, lk), FilesDeleted: in.DeleteFiles, Excluded: in.AddExclusion}, nil
	})

	type commandsOut struct {
		Commands []commandOut `json:"commands"`
		Movies   []movieRow   `json:"movies"   jsonschema:"the films as they stand once Radarr finished (or the wait ran out)"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "movie_refresh",
		Description: "Refresh films from TMDB - title, year, overview, runtime, artwork, collection - and rescan their folders, the way Radarr does on its schedule. " +
			"After a wrong match is fixed on TMDB, or to pick up a film's new release dates.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moviesIn) (*mcp.CallToolResult, commandsOut, error) {
		movies, err := resolveMovies(ctx, client, in.Movies)
		if err != nil {
			return nil, commandsOut{}, err
		}
		cmd, err := runCommand(ctx, client, map[string]any{"name": "RefreshMovie", "movieIds": movieIDs(movies)}, waitFor(in.Wait))
		out := commandsOut{Commands: []commandOut{cmd}}
		if err != nil {
			return nil, out, err
		}
		out.Movies, err = rowsFor(ctx, client, movieIDs(movies))

		return nil, out, err
	})

	add(r, writeTool, &mcp.Tool{
		Name: "movie_rescan",
		Description: "Rescan films' folders on disk: Radarr picks up a file put there by hand, drops one that is gone, and re-reads what is inside each file. " +
			"Nothing is fetched from TMDB (movie_refresh does that).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moviesIn) (*mcp.CallToolResult, commandsOut, error) {
		movies, err := resolveMovies(ctx, client, in.Movies)
		if err != nil {
			return nil, commandsOut{}, err
		}
		out := commandsOut{}
		for i := range movies {
			var cmd commandOut
			cmd, err = runCommand(ctx, client, map[string]any{"name": "RescanMovie", "movieId": movies[i].Id}, waitFor(in.Wait))
			out.Commands = append(out.Commands, cmd)
			if err != nil {
				return nil, out, err
			}
		}
		out.Movies, err = rowsFor(ctx, client, movieIDs(movies))

		return nil, out, err
	})

	type renameIn struct {
		Movies  []string `json:"movies"            jsonschema:"the films: ids, tmdb:<id>, imdb:<tt id> or titles"`
		Preview *bool    `json:"preview,omitempty" jsonschema:"only show what would be renamed (default true); false renames"`
	}
	type renameRow struct {
		Movie    string `json:"movie"`
		MovieID  int    `json:"movie_id"`
		FileID   int    `json:"file_id"`
		Existing string `json:"existing" jsonschema:"the file's path in its folder now"`
		New      string `json:"new"      jsonschema:"the path the naming scheme gives it"`
	}
	type renameOut struct {
		Renames  []renameRow  `json:"renames"            jsonschema:"what is (or would be) renamed"`
		Renamed  bool         `json:"renamed"`
		Commands []commandOut `json:"commands,omitempty"`
		Note     string       `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "movie_rename",
		Description: "Rename films' files to Radarr's naming scheme: by default only preview what would change, with preview false rename them. " +
			"Radarr gives new names only when renaming is switched on in its settings (naming_get says whether; naming_edit switches it on).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, renameOut, error) {
		movies, err := resolveMovies(ctx, client, in.Movies)
		if err != nil {
			return nil, renameOut{}, err
		}
		naming, err := client.GetConfigNaming(ctx)
		if err != nil {
			return nil, renameOut{}, err
		}
		out := renameOut{}
		if !isTrue(naming.Model.RenameMovies) {
			out.Note = "renaming is switched off in Radarr, so it gives no new names: naming_edit rename_movies true switches it on"
		}
		byFile := map[int][]int{}
		for i := range movies {
			m := &movies[i]
			res, err := client.GetRename(ctx, radarr.GetRenameOperationOptions{MovieId: []int{m.Id}})
			if err != nil {
				return nil, out, err
			}
			for _, p := range res.Model {
				out.Renames = append(out.Renames, renameRow{Movie: titleYear(m.Title, m.Year), MovieID: m.Id, FileID: p.MovieFileId, Existing: p.ExistingPath, New: p.NewPath})
				byFile[m.Id] = append(byFile[m.Id], p.MovieFileId)
			}
		}
		if in.Preview != nil && !*in.Preview {
			for i := range movies {
				m := &movies[i]
				if len(byFile[m.Id]) == 0 {
					continue
				}
				cmd, err := runCommand(ctx, client, map[string]any{"name": "RenameFiles", "movieId": m.Id, "files": byFile[m.Id]}, defaultWait)
				out.Commands = append(out.Commands, cmd)
				if err != nil {
					return nil, out, err
				}
			}
			out.Renamed = len(out.Commands) > 0
		}

		return nil, out, nil
	})

	type downloadOut struct {
		Command commandOut `json:"command"`
		Queue   []queueRow `json:"queue"   jsonschema:"what the films have in the download queue once the search finished: what it grabbed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "movie_download",
		Description: "Have Radarr search its indexers for films and grab the best release its quality profile allows (its automatic search): this starts downloads. " +
			"Answers with what the films then have in the queue; release_search lists the releases without grabbing anything.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moviesIn) (*mcp.CallToolResult, downloadOut, error) {
		movies, err := resolveMovies(ctx, client, in.Movies)
		if err != nil {
			return nil, downloadOut{}, err
		}
		cmd, err := runCommand(ctx, client, map[string]any{"name": "MoviesSearch", "movieIds": movieIDs(movies)}, waitFor(in.Wait))
		out := downloadOut{Command: cmd}
		if err != nil {
			return nil, out, err
		}
		out.Queue, err = queueFor(ctx, client, movieIDs(movies))

		return nil, out, err
	})
}

// addCandidate finds the one film a movie_add names, on TMDB.
func addCandidate(ctx context.Context, c *radarr.Client, ref string) (*radarr.MovieResource, error) {
	found, err := lookupMovies(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(strings.TrimSpace(ref))
	if strings.HasPrefix(lower, "tmdb:") || strings.HasPrefix(lower, "imdb:") {
		if len(found) == 0 || found[0].TmdbId == 0 {
			return nil, fmt.Errorf("TMDB has no film %s", ref)
		}
		return &found[0], nil
	}

	// a title: exactly one film of that title (and year, when given)
	title, year := strings.TrimSpace(ref), 0
	if m := trailingYear.FindStringSubmatch(title); m != nil {
		title = strings.TrimSpace(title[:len(title)-len(m[0])])
		year, _ = strconv.Atoi(m[1])
	} else if m := bareYear.FindStringSubmatch(title); m != nil {
		title = strings.TrimSpace(title[:len(title)-len(m[0])])
		year, _ = strconv.Atoi(m[1])
	}
	var matches []*radarr.MovieResource
	for i := range found {
		f := &found[i]
		if cleanTitle(f.Title) == cleanTitle(title) && (year == 0 || f.Year == year) {
			matches = append(matches, f)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	var names []string
	for i := range found {
		if len(names) == 8 {
			break
		}
		names = append(names, fmt.Sprintf("%s tmdb:%d", titleYear(found[i].Title, found[i].Year), found[i].TmdbId))
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no film on TMDB is titled %q; the closest are %s", ref, strings.Join(names, ", "))
	}

	return nil, fmt.Errorf("%q names %d films; add one by tmdb:<id>: %s", ref, len(matches), strings.Join(names, ", "))
}

// pointAt gives a film a folder that is already on disk - the fix for one
// renamed or moved behind Radarr's back - and rescans it, so Radarr picks the
// files in it back up. Nothing is moved: Radarr moves a film's folder only
// when its root folder changes.
func pointAt(ctx context.Context, c *radarr.Client, m *radarr.MovieResource, folder string) (movieRow, error) {
	path := strings.TrimRight(strings.TrimSpace(folder), "/")
	if !strings.HasPrefix(path, "/") {
		root := strings.TrimRight(m.RootFolderPath, "/")
		if root == "" {
			f, err := resolveRootFolder(ctx, c, "")
			if err != nil {
				return movieRow{}, fmt.Errorf("%s has no root folder, and folder %q is not a path: %w", titleYear(m.Title, m.Year), folder, err)
			}
			root = strings.TrimRight(f.Path, "/")
		}
		path = root + "/" + path
	}
	if path == strings.TrimRight(m.Path, "/") {
		return movieRow{}, fmt.Errorf("%s is already at %s", titleYear(m.Title, m.Year), path)
	}
	edit := *m
	edit.Path = path
	edit.RootFolderPath = ""
	if _, err := c.PutMovieById(ctx, m.Id, edit, radarr.PutMovieByIdOperationOptions{MoveFiles: new(false)}); err != nil {
		return movieRow{}, fmt.Errorf("pointing %s at %s: %w", titleYear(m.Title, m.Year), path, err)
	}
	if _, err := runCommand(ctx, c, map[string]any{"name": "RescanMovie", "movieId": m.Id}, defaultWait); err != nil {
		return movieRow{}, err
	}
	rows, err := rowsFor(ctx, c, []int{m.Id})
	if err != nil || len(rows) == 0 {
		return movieRow{}, err
	}

	return rows[0], nil
}

// waitForNewMovie waits for the refresh Radarr starts when a film is added.
// The generated command model has no movieIds (every command has fields of
// its own), so the list is read as the JSON Radarr sends. Should Radarr not
// have started one within a few seconds, a rescan of our own stands in.
func waitForNewMovie(ctx context.Context, c *radarr.Client, id int) error {
	type command struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Body   struct {
			MovieIDs []int `json:"movieIds"`
		} `json:"body"`
	}
	seen := false
	for deadline, grace := time.Now().Add(defaultWait), time.Now().Add(5*time.Second); time.Now().Before(deadline); {
		res, err := c.GetCommand(ctx)
		if err != nil {
			return err
		}
		var commands []command
		if err := decodeBody(res.HttpResponse, &commands); err != nil {
			return fmt.Errorf("reading Radarr's commands: %w", err)
		}
		pending := false
		for _, cmd := range commands {
			if cmd.Name != "RefreshMovie" || !slices.Contains(cmd.Body.MovieIDs, id) {
				continue
			}
			seen = true
			if cmd.Status == string(radarr.CommandStatusFailed) {
				return errors.New("Radarr's refresh of the new film failed") //nolint:staticcheck // Radarr is a name
			}
			if !finished(radarr.CommandStatus(cmd.Status)) {
				pending = true
			}
		}
		switch {
		case seen && !pending:
			return nil
		case !seen && time.Now().After(grace):
			_, err := runCommand(ctx, c, map[string]any{"name": "RescanMovie", "movieId": id}, defaultWait)
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(commandPoll):
		}
	}

	return errors.New("the new film is still being refreshed; movie_get will show its file when Radarr is done")
}

// bareYear is the year a person puts after a title without brackets:
// "Heat 1995".
var bareYear = regexp.MustCompile(`\s+(\d{4})$`)

func minimumAvailability(s string) (radarr.MovieStatusType, error) {
	for _, v := range []radarr.MovieStatusType{radarr.MovieStatusTypeAnnounced, radarr.MovieStatusTypeInCinemas, radarr.MovieStatusTypeReleased} {
		if strings.EqualFold(string(v), strings.TrimSpace(s)) {
			return v, nil
		}
	}

	return "", fmt.Errorf("minimum availability %q: want announced, inCinemas or released", s)
}

func movieIDs(movies []radarr.MovieResource) []int {
	ids := make([]int, 0, len(movies))
	for i := range movies {
		m := &movies[i]
		ids = append(ids, m.Id)
	}

	return ids
}

// rowsFor reads films back by id, in the order given.
func rowsFor(ctx context.Context, c *radarr.Client, ids []int) ([]movieRow, error) {
	movies, err := allMovies(ctx, c)
	if err != nil {
		return nil, err
	}
	lk, err := loadLookups(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]movieRow, 0, len(ids))
	for _, id := range ids {
		for i := range movies {
			if movies[i].Id == id {
				out = append(out, rowOf(&movies[i], lk))
			}
		}
	}

	return out, nil
}

// applyEdit makes movie_edit's and movie_batch_edit's change through
// Radarr's movie editor, which changes only the fields it is sent: the
// monitored flag, profile, availability and root folder in one call, then
// each tag operation in one of its own. A move of root folder is a command
// Radarr runs afterwards, and is waited for.
func applyEdit(ctx context.Context, c *radarr.Client, ids []int, e editFields) ([]movieRow, error) {
	if e.empty() {
		return nil, errors.New("nothing to change: give monitored, quality_profile, minimum_availability, root_folder, tags, add_tags or remove_tags")
	}
	edit := radarr.MovieEditorResource{MovieIds: ids, Monitored: e.Monitored}
	if e.QualityProfile != "" {
		p, err := resolveProfile(ctx, c, e.QualityProfile)
		if err != nil {
			return nil, err
		}
		edit.QualityProfileId = new(p.Id)
	}
	if e.MinimumAvailability != "" {
		a, err := minimumAvailability(e.MinimumAvailability)
		if err != nil {
			return nil, err
		}
		edit.MinimumAvailability = a
	}
	moving := false
	if e.RootFolder != "" {
		f, err := resolveRootFolder(ctx, c, e.RootFolder)
		if err != nil {
			return nil, err
		}
		edit.RootFolderPath = f.Path
		edit.MoveFiles = new(e.MoveFiles == nil || *e.MoveFiles)
		moving = *edit.MoveFiles
	}
	started := time.Now()
	if edit.Monitored != nil || edit.QualityProfileId != nil || edit.MinimumAvailability != "" || edit.RootFolderPath != "" {
		if _, err := c.PutMovieEditor(ctx, edit); err != nil {
			return nil, err
		}
	}
	for _, op := range []struct {
		labels []string
		apply  radarr.ApplyTags
		create bool
	}{
		{e.Tags, radarr.ApplyTagsReplace, true},
		{e.AddTags, radarr.ApplyTagsAdd, true},
		{e.RemoveTags, radarr.ApplyTagsRemove, false},
	} {
		if op.labels == nil || op.apply != radarr.ApplyTagsReplace && len(op.labels) == 0 {
			continue
		}
		tags, err := resolveTags(ctx, c, op.labels, op.create)
		if err != nil {
			return nil, err
		}
		if tags == nil {
			tags = []int{}
		}
		if _, err := c.PutMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: ids, Tags: tags, ApplyTags: op.apply}); err != nil {
			return nil, err
		}
	}
	if moving {
		if err := waitForMove(ctx, c, started); err != nil {
			return nil, err
		}
	}

	return rowsFor(ctx, c, ids)
}

// waitForMove waits for the move the movie editor started to finish: Radarr
// updates the films' paths at once and moves the folders in a command of its
// own afterwards.
func waitForMove(ctx context.Context, c *radarr.Client, since time.Time) error {
	deadline := time.Now().Add(defaultWait)
	for time.Now().Before(deadline) {
		res, err := c.GetCommand(ctx)
		if err != nil {
			return err
		}
		pending := false
		for _, cmd := range res.Model {
			if cmd.Name != "BulkMoveMovie" && cmd.Name != "MoveMovie" {
				continue
			}
			queued, err := time.Parse(time.RFC3339, cmd.Queued)
			if err != nil || queued.Before(since.Add(-2*time.Second)) {
				continue
			}
			if cmd.Status == radarr.CommandStatusFailed {
				return fmt.Errorf("moving the films failed: %s", cmd.Message)
			}
			if !finished(cmd.Status) {
				pending = true
			}
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(commandPoll):
		}
	}

	return errors.New("the move is still running; task_list shows it")
}
