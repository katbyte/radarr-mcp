package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type movieListIn struct {
	Query          string `json:"query,omitempty"           jsonschema:"only films whose title or original title contains this"`
	Monitored      *bool  `json:"monitored,omitempty"`
	HasFile        *bool  `json:"has_file,omitempty"`
	Status         string `json:"status,omitempty"          jsonschema:"announced, inCinemas, released, or deleted (gone from TMDB)"`
	QualityProfile string `json:"quality_profile,omitempty" jsonschema:"a quality profile's name or id"`
	RootFolder     string `json:"root_folder,omitempty"     jsonschema:"a root folder's path or id"`
	Tag            string `json:"tag,omitempty"             jsonschema:"a tag's label"`
	Genre          string `json:"genre,omitempty"`
	YearFrom       int    `json:"year_from,omitempty"`
	YearTo         int    `json:"year_to,omitempty"`
	Sort           string `json:"sort,omitempty"            jsonschema:"title (default), year, added, size or rating"`
	Descending     bool   `json:"descending,omitempty"`
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"           jsonschema:"films to return, default 50"`
}

type movieListOut struct {
	Total  int        `json:"total"  jsonschema:"films matching the filters; movies holds at most limit of them"`
	Offset int        `json:"offset"`
	Movies []movieRow `json:"movies"`
}

type movieIn struct {
	Movie string `json:"movie" jsonschema:"the film: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title (\"Alien\", or \"Alien (1979)\" when two share a title)"`
}

// movieDetail is one film in full: its metadata, its file and what the probe
// found in it, and where it stands in the download queue and history.
type movieDetail struct {
	movieRow

	ImdbID              string          `json:"imdb_id,omitempty"`
	OriginalTitle       string          `json:"original_title,omitempty"`
	OriginalLanguage    string          `json:"original_language,omitempty"`
	Overview            string          `json:"overview,omitempty"`
	Runtime             int             `json:"runtime,omitempty"              jsonschema:"minutes, from TMDB"`
	Genres              []string        `json:"genres,omitempty"`
	Certification       string          `json:"certification,omitempty"`
	Studio              string          `json:"studio,omitempty"`
	Collection          *collectionRef  `json:"collection,omitempty"`
	MinimumAvailability string          `json:"minimum_availability,omitempty" jsonschema:"when Radarr starts looking: announced, inCinemas or released"`
	IsAvailable         bool            `json:"is_available"                   jsonschema:"whether the minimum availability has been reached"`
	InCinemas           string          `json:"in_cinemas,omitempty"`
	DigitalRelease      string          `json:"digital_release,omitempty"`
	PhysicalRelease     string          `json:"physical_release,omitempty"`
	RootFolder          string          `json:"root_folder,omitempty"`
	Added               string          `json:"added,omitempty"`
	Ratings             map[string]any  `json:"ratings,omitempty"              jsonschema:"imdb, tmdb, metacritic and rotten tomatoes, as Radarr has them"`
	AlternateTitles     []string        `json:"alternate_titles,omitempty"     jsonschema:"up to ten"`
	File                *fileInfo       `json:"file,omitempty"`
	Queue               []queueRow      `json:"queue,omitempty"                jsonschema:"downloads for this film in the queue"`
	History             []historyRow    `json:"history,omitempty"              jsonschema:"the last five events: grabs, imports, failures, renames, deletions"`
	Statistics          *movieStatistic `json:"statistics,omitempty"`
}

type collectionRef struct {
	Title  string `json:"title"`
	TmdbID int    `json:"tmdb_id"`
}

type movieStatistic struct {
	FileCount     int      `json:"file_count"`
	ReleaseGroups []string `json:"release_groups,omitempty"`
}

type lookupIn struct {
	Term  string `json:"term"            jsonschema:"a title (with the year to narrow it: \"Dune 1984\"), tmdb:<id> or imdb:<tt id>"`
	Limit int    `json:"limit,omitempty" jsonschema:"results to return, default 10"`
}

type lookupRow struct {
	Title            string         `json:"title"`
	Year             int            `json:"year,omitempty"`
	TmdbID           int            `json:"tmdb_id"`
	ImdbID           string         `json:"imdb_id,omitempty"`
	Runtime          int            `json:"runtime,omitempty"           jsonschema:"minutes"`
	OriginalLanguage string         `json:"original_language,omitempty"`
	Status           string         `json:"status,omitempty"`
	Overview         string         `json:"overview,omitempty"          jsonschema:"the first 300 characters"`
	Collection       *collectionRef `json:"collection,omitempty"`
	InLibrary        bool           `json:"in_library"`
	LibraryID        int            `json:"library_id,omitempty"        jsonschema:"its id in the library, when it is there"`
}

type creditsIn struct {
	Movie string `json:"movie"           jsonschema:"the film: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title"`
	Type  string `json:"type,omitempty"  jsonschema:"cast, crew or all (default)"`
	Limit int    `json:"limit,omitempty" jsonschema:"people of each kind to return, default 20"`
}

type creditRow struct {
	Name       string `json:"name"`
	Character  string `json:"character,omitempty"`
	Job        string `json:"job,omitempty"`
	Department string `json:"department,omitempty"`
	TmdbID     int    `json:"tmdb_id,omitempty"`
}

func registerMovieTools(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "movie_list",
		Description: "List the films in the library, filtered by part of a title, monitored or not, with or without a file, status, quality profile, root folder, tag, genre and year range, sorted by title, year, date added, size or rating. " +
			"Each row says whether the film is monitored and has a file, the file's quality and size, and its profile, tags and path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieListIn) (*mcp.CallToolResult, movieListOut, error) {
		movies, err := allMovies(ctx, client)
		if err != nil {
			return nil, movieListOut{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, movieListOut{}, err
		}
		keep, err := movieFilter(ctx, client, in)
		if err != nil {
			return nil, movieListOut{}, err
		}
		matched := slices.DeleteFunc(movies, func(m radarr.MovieResource) bool { return !keep(&m) })
		if err := sortMovies(matched, in.Sort, in.Descending); err != nil {
			return nil, movieListOut{}, err
		}

		out := movieListOut{Total: len(matched), Offset: in.Offset}
		limit := limitOr(in.Limit, 50)
		for i := in.Offset; i < len(matched) && len(out.Movies) < limit; i++ {
			out.Movies = append(out.Movies, rowOf(&matched[i], lk))
		}

		return nil, out, nil
	})

	add(r, readTool, &mcp.Tool{
		Name: "movie_get",
		Description: "Read one film in full: its TMDB metadata, its file with the quality Radarr graded it and what the probe found inside (resolution, codecs, audio and subtitle languages, runtime), its downloads in the queue, and its last five history events. " +
			"Name it by id, tmdb:<id>, imdb:<tt id> or title.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieIn) (*mcp.CallToolResult, movieDetail, error) {
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, movieDetail{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, movieDetail{}, err
		}
		out := detailOf(m, lk)

		queue, err := queueFor(ctx, client, []int{m.Id})
		if err != nil {
			return nil, out, err
		}
		out.Queue = queue

		history, err := client.GetHistoryMovie(ctx, radarr.GetHistoryMovieOperationOptions{MovieId: m.Id})
		if err != nil {
			return nil, out, err
		}
		events := history.Model
		slices.SortStableFunc(events, func(a, b radarr.HistoryResource) int { return strings.Compare(b.Date, a.Date) })
		for i := range events {
			if len(out.History) == 5 {
				break
			}
			out.History = append(out.History, historyOf(&events[i]))
		}

		return nil, out, nil
	})

	type lookupOut struct {
		Results []lookupRow `json:"results"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "movie_lookup",
		Description: "Look a film up on TMDB through Radarr, by title, tmdb:<id> or imdb:<tt id>, to find the one to add: each result has its ids, runtime, original language and collection, and says whether the library already holds it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
		found, err := lookupMovies(ctx, client, in.Term)
		if err != nil {
			return nil, lookupOut{}, err
		}
		library, err := allMovies(ctx, client)
		if err != nil {
			return nil, lookupOut{}, err
		}
		have := map[int]int{}
		for i := range library {
			m := &library[i]
			have[m.TmdbId] = m.Id
		}
		out := lookupOut{}
		for i := range found {
			if len(out.Results) == limitOr(in.Limit, 10) {
				break
			}
			out.Results = append(out.Results, lookupOf(&found[i], have))
		}

		return nil, out, nil
	})

	type creditsOut struct {
		Movie string      `json:"movie"`
		Cast  []creditRow `json:"cast,omitempty" jsonschema:"in billing order"`
		Crew  []creditRow `json:"crew,omitempty" jsonschema:"directors and writers first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "movie_credits",
		Description: "List a film's cast in billing order and its crew with directors and writers first, as TMDB has them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in creditsIn) (*mcp.CallToolResult, creditsOut, error) {
		kind := strings.ToLower(strings.TrimSpace(in.Type))
		if kind == "" {
			kind = "all"
		}
		if kind != "all" && kind != "cast" && kind != "crew" {
			return nil, creditsOut{}, fmt.Errorf("type %q: want cast, crew or all", in.Type)
		}
		m, err := resolveMovie(ctx, client, in.Movie)
		if err != nil {
			return nil, creditsOut{}, err
		}
		res, err := client.GetCredit(ctx, radarr.GetCreditOperationOptions{MovieId: m.Id})
		if err != nil {
			return nil, creditsOut{}, err
		}
		credits := res.Model
		slices.SortStableFunc(credits, func(a, b radarr.CreditResource) int {
			if a.Type == radarr.CreditTypeCrew && b.Type == radarr.CreditTypeCrew {
				return crewRank(a) - crewRank(b)
			}
			return a.Order - b.Order
		})
		out := creditsOut{Movie: titleYear(m.Title, m.Year)}
		limit := limitOr(in.Limit, 20)
		for _, c := range credits {
			switch {
			case c.Type == radarr.CreditTypeCast && kind != "crew" && len(out.Cast) < limit:
				out.Cast = append(out.Cast, creditRow{Name: c.PersonName, Character: c.Character, TmdbID: c.PersonTmdbId})
			case c.Type == radarr.CreditTypeCrew && kind != "cast" && len(out.Crew) < limit:
				out.Crew = append(out.Crew, creditRow{Name: c.PersonName, Job: c.Job, Department: c.Department, TmdbID: c.PersonTmdbId})
			}
		}

		return nil, out, nil
	})
}

// crewRank puts the director first, then the writers, then everyone else.
func crewRank(c radarr.CreditResource) int {
	switch {
	case c.Job == "Director":
		return 0
	case c.Department == "Writing":
		return 1
	default:
		return 2
	}
}

// movieFilter compiles movie_list's filters into one predicate, resolving
// the profile, root folder and tag names first so an unknown one is an
// error rather than an empty list.
func movieFilter(ctx context.Context, c *radarr.Client, in movieListIn) (func(*radarr.MovieResource) bool, error) {
	profile := 0
	if in.QualityProfile != "" {
		p, err := resolveProfile(ctx, c, in.QualityProfile)
		if err != nil {
			return nil, err
		}
		profile = p.Id
	}
	root := ""
	if in.RootFolder != "" {
		f, err := resolveRootFolder(ctx, c, in.RootFolder)
		if err != nil {
			return nil, err
		}
		root = strings.TrimRight(f.Path, "/")
	}
	tag := -1
	if in.Tag != "" {
		ids, err := resolveTags(ctx, c, []string{in.Tag}, false)
		if err != nil {
			return nil, err
		}
		tag = ids[0]
	}
	status := strings.TrimSpace(in.Status)
	if status != "" && !slices.Contains(radarr.PossibleValuesForMovieStatusType(), status) {
		return nil, fmt.Errorf("status %q: want one of %s", in.Status, strings.Join(radarr.PossibleValuesForMovieStatusType(), ", "))
	}
	query := cleanTitle(in.Query)

	return func(m *radarr.MovieResource) bool {
		switch {
		case query != "" && !strings.Contains(cleanTitle(m.Title), query) && !strings.Contains(cleanTitle(m.OriginalTitle), query):
		case in.Monitored != nil && isTrue(m.Monitored) != *in.Monitored:
		case in.HasFile != nil && isTrue(m.HasFile) != *in.HasFile:
		case status != "" && string(m.Status) != status:
		case profile != 0 && m.QualityProfileId != profile:
		case root != "" && strings.TrimRight(m.RootFolderPath, "/") != root:
		case tag >= 0 && !slices.Contains(m.Tags, tag):
		case in.Genre != "" && !slices.ContainsFunc(m.Genres, func(g string) bool { return strings.EqualFold(g, in.Genre) }):
		case in.YearFrom != 0 && m.Year < in.YearFrom:
		case in.YearTo != 0 && m.Year > in.YearTo:
		default:
			return true
		}
		return false
	}, nil
}

// sortMovies orders a film list by one of movie_list's keys; title order is
// Radarr's own sort title, which files "The Matrix" under M.
func sortMovies(movies []radarr.MovieResource, key string, descending bool) error {
	var cmpFn func(a, b *radarr.MovieResource) int
	switch strings.ToLower(key) {
	case "", "title":
		cmpFn = func(a, b *radarr.MovieResource) int { return cmp.Compare(sortTitle(a), sortTitle(b)) }
	case "year":
		cmpFn = func(a, b *radarr.MovieResource) int { return cmp.Compare(a.Year, b.Year) }
	case "added":
		cmpFn = func(a, b *radarr.MovieResource) int { return cmp.Compare(a.Added, b.Added) }
	case "size":
		cmpFn = func(a, b *radarr.MovieResource) int { return cmp.Compare(val(a.SizeOnDisk), val(b.SizeOnDisk)) }
	case "rating":
		cmpFn = func(a, b *radarr.MovieResource) int { return cmp.Compare(rating(a), rating(b)) }
	default:
		return fmt.Errorf("sort %q: want title, year, added, size or rating", key)
	}
	slices.SortStableFunc(movies, func(a, b radarr.MovieResource) int {
		c := cmpFn(&a, &b)
		if c == 0 {
			c = cmp.Compare(sortTitle(&a), sortTitle(&b))
		}
		if descending {
			return -c
		}
		return c
	})

	return nil
}

func sortTitle(m *radarr.MovieResource) string {
	if m.SortTitle != "" {
		return m.SortTitle
	}

	return strings.ToLower(m.Title)
}

// rating is a film's TMDB rating, the one every film has.
func rating(m *radarr.MovieResource) float64 {
	if m.Ratings == nil || m.Ratings.Tmdb == nil {
		return 0
	}

	return m.Ratings.Tmdb.Value
}

func detailOf(m *radarr.MovieResource, lk lookups) movieDetail {
	out := movieDetail{
		movieRow:            rowOf(m, lk),
		ImdbID:              m.ImdbId,
		OriginalTitle:       m.OriginalTitle,
		Overview:            m.Overview,
		Runtime:             m.Runtime,
		Genres:              m.Genres,
		Certification:       m.Certification,
		Studio:              m.Studio,
		MinimumAvailability: string(m.MinimumAvailability),
		IsAvailable:         isTrue(m.IsAvailable),
		InCinemas:           m.InCinemas,
		DigitalRelease:      m.DigitalRelease,
		PhysicalRelease:     m.PhysicalRelease,
		RootFolder:          m.RootFolderPath,
		Added:               m.Added,
		File:                fileOf(m.MovieFile),
	}
	if m.OriginalLanguage != nil {
		out.OriginalLanguage = m.OriginalLanguage.Name
	}
	if m.Collection != nil && m.Collection.TmdbId != 0 {
		out.Collection = &collectionRef{Title: m.Collection.Title, TmdbID: m.Collection.TmdbId}
	}
	if r := m.Ratings; r != nil {
		out.Ratings = map[string]any{}
		for name, child := range map[string]*radarr.RatingChild{"imdb": r.Imdb, "tmdb": r.Tmdb, "metacritic": r.Metacritic, "rotten_tomatoes": r.RottenTomatoes} {
			if child != nil && (child.Value != 0 || child.Votes != 0) {
				out.Ratings[name] = child.Value
			}
		}
	}
	for _, t := range m.AlternateTitles {
		if len(out.AlternateTitles) == 10 {
			break
		}
		if !slices.Contains(out.AlternateTitles, t.Title) {
			out.AlternateTitles = append(out.AlternateTitles, t.Title)
		}
	}
	if s := m.Statistics; s != nil {
		out.Statistics = &movieStatistic{FileCount: s.MovieFileCount, ReleaseGroups: s.ReleaseGroups}
	}

	return out
}

// lookupMovies asks Radarr's metadata service about a film by title, tmdb id
// or imdb id.
func lookupMovies(ctx context.Context, c *radarr.Client, term string) ([]radarr.MovieResource, error) {
	term = strings.TrimSpace(term)
	lower := strings.ToLower(term)
	switch {
	case term == "":
		return nil, errors.New("no term: pass a title, tmdb:<id> or imdb:<tt id>")
	case strings.HasPrefix(lower, "tmdb:"):
		id, err := strconv.Atoi(strings.TrimSpace(term[5:]))
		if err != nil {
			return nil, fmt.Errorf("%q: a tmdb id is a number", term)
		}
		res, err := c.GetMovieLookupTmdb(ctx, radarr.GetMovieLookupTmdbOperationOptions{TmdbId: id})
		if err != nil {
			return nil, fmt.Errorf("looking up tmdb:%d: %w", id, err)
		}
		return []radarr.MovieResource{*res.Model}, nil
	case strings.HasPrefix(lower, "imdb:"):
		res, err := c.GetMovieLookupImdb(ctx, radarr.GetMovieLookupImdbOperationOptions{ImdbId: strings.TrimSpace(term[5:])})
		if err != nil {
			return nil, fmt.Errorf("looking up %s: %w", term, err)
		}
		return []radarr.MovieResource{*res.Model}, nil
	default:
		res, err := c.GetMovieLookup(ctx, radarr.GetMovieLookupOperationOptions{Term: term})
		if err != nil {
			return nil, err
		}
		return res.Model, nil
	}
}

func lookupOf(m *radarr.MovieResource, have map[int]int) lookupRow {
	row := lookupRow{
		Title:     m.Title,
		Year:      m.Year,
		TmdbID:    m.TmdbId,
		ImdbID:    m.ImdbId,
		Runtime:   m.Runtime,
		Status:    string(m.Status),
		Overview:  truncate(m.Overview, 300),
		LibraryID: have[m.TmdbId],
	}
	row.InLibrary = row.LibraryID != 0
	if m.OriginalLanguage != nil {
		row.OriginalLanguage = m.OriginalLanguage.Name
	}
	if m.Collection != nil && m.Collection.TmdbId != 0 {
		row.Collection = &collectionRef{Title: m.Collection.Title, TmdbID: m.Collection.TmdbId}
	}

	return row
}

// truncate cuts s to n runes, on a word boundary where there is one.
func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	cut := string(r[:n])
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}

	return cut + "..."
}
