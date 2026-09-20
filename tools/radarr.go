package tools

// What every tool reads Radarr through: resolving a film, a quality profile,
// a root folder or a tag from what a person would call it, the trimmed
// projections the tools answer with, and running a command and waiting for
// it to finish.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/katbyte/radarr-mcp/lib/client"
	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// allMovies reads every film in the library. Radarr answers the whole list in
// one call, each film with its file and the file's media info, which is what
// makes a sweep over the library one request rather than one per film.
func allMovies(ctx context.Context, c *radarr.Client) ([]radarr.MovieResource, error) {
	res, err := c.GetMovie(ctx, radarr.GetMovieOperationOptions{})
	if err != nil {
		return nil, err
	}

	return res.Model, nil
}

// cleanTitle folds a title for comparison: case, punctuation and spacing
// never decide whether two titles name the same film.
func cleanTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}

	return b.String()
}

// titleYear renders a film the way its folder usually names it.
func titleYear(title string, year int) string {
	if year <= 0 {
		return title
	}

	return title + " (" + strconv.Itoa(year) + ")"
}

// trailingYear is the "(1979)" a folder or a person puts after a title.
var trailingYear = regexp.MustCompile(`\s*\((\d{4})\)\s*$`)

// movieRef is how a tool names a film: its Radarr id, tmdb:ID, imdb:ttID, or
// its title, optionally with the year as a folder spells it.
const movieRef = `the film: its Radarr id, tmdb:<id>, imdb:<tt id>, or its title ("Alien", or "Alien (1979)" when two share a title)`

// matchMovie finds one film in a list by reference. A title that matches
// more than one film is an error naming each, so the caller can pick.
func matchMovie(movies []radarr.MovieResource, ref string) (*radarr.MovieResource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("no film named: pass " + movieRef)
	}

	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "tmdb:"):
		id, err := strconv.Atoi(strings.TrimSpace(ref[5:]))
		if err != nil {
			return nil, fmt.Errorf("%q: a tmdb id is a number", ref)
		}
		for i := range movies {
			if movies[i].TmdbId == id {
				return &movies[i], nil
			}
		}
		return nil, fmt.Errorf("no film in the library has tmdb id %d (movie_lookup finds it to add)", id)
	case strings.HasPrefix(lower, "imdb:"):
		id := strings.TrimSpace(ref[5:])
		for i := range movies {
			if strings.EqualFold(movies[i].ImdbId, id) {
				return &movies[i], nil
			}
		}
		return nil, fmt.Errorf("no film in the library has imdb id %s (movie_lookup finds it to add)", id)
	}
	if id, err := strconv.Atoi(ref); err == nil {
		for i := range movies {
			if movies[i].Id == id {
				return &movies[i], nil
			}
		}
		return nil, fmt.Errorf("no film in the library has id %d", id)
	}

	// a title, with the year when the reference carries one
	title, year := ref, 0
	if m := trailingYear.FindStringSubmatch(ref); m != nil {
		title = strings.TrimSpace(ref[:len(ref)-len(m[0])])
		year, _ = strconv.Atoi(m[1])
	}
	want := cleanTitle(title)
	var found []*radarr.MovieResource
	for i := range movies {
		m := &movies[i]
		if year != 0 && m.Year != year {
			continue
		}
		if cleanTitle(m.Title) == want || cleanTitle(m.OriginalTitle) == want {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no film in the library is titled %q (movie_list searches by part of a title; movie_lookup finds a film to add)", ref)
	}
	slices.SortFunc(found, func(a, b *radarr.MovieResource) int { return a.Year - b.Year })
	names := make([]string, 0, len(found))
	for _, m := range found {
		names = append(names, fmt.Sprintf("%s id %d", titleYear(m.Title, m.Year), m.Id))
	}

	return nil, fmt.Errorf("%q matches %d films: %s; name one by id, tmdb:<id> or \"Title (Year)\"", ref, len(found), strings.Join(names, ", "))
}

// resolveMovie reads the library and finds one film in it.
func resolveMovie(ctx context.Context, c *radarr.Client, ref string) (*radarr.MovieResource, error) {
	// an id is one request, not the whole library
	if id, err := strconv.Atoi(strings.TrimSpace(ref)); err == nil {
		res, err := c.GetMovieById(ctx, id)
		if err != nil {
			if res.HttpResponse != nil && res.HttpResponse.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("no film in the library has id %d", id)
			}
			return nil, err
		}
		return res.Model, nil
	}
	movies, err := allMovies(ctx, c)
	if err != nil {
		return nil, err
	}

	return matchMovie(movies, ref)
}

// resolveMovies finds each of several films, reading the library once.
func resolveMovies(ctx context.Context, c *radarr.Client, refs []string) ([]radarr.MovieResource, error) {
	if len(refs) == 0 {
		return nil, errors.New("no films named: pass one or more of " + movieRef)
	}
	movies, err := allMovies(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]radarr.MovieResource, 0, len(refs))
	seen := map[int]bool{}
	for _, ref := range refs {
		m, err := matchMovie(movies, ref)
		if err != nil {
			return nil, err
		}
		if !seen[m.Id] {
			seen[m.Id] = true
			out = append(out, *m)
		}
	}

	return out, nil
}

// lookups are the names a projection renders ids as: quality profiles and
// tags.
type lookups struct {
	profiles map[int]string
	tags     map[int]string
}

func loadLookups(ctx context.Context, c *radarr.Client) (lookups, error) {
	lk := lookups{profiles: map[int]string{}, tags: map[int]string{}}
	profiles, err := c.GetQualityprofile(ctx)
	if err != nil {
		return lk, err
	}
	for _, p := range profiles.Model {
		lk.profiles[p.Id] = p.Name
	}
	tags, err := c.GetTag(ctx)
	if err != nil {
		return lk, err
	}
	for _, t := range tags.Model {
		lk.tags[t.Id] = t.Label
	}

	return lk, nil
}

func (lk lookups) tagLabels(ids []int) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if l, ok := lk.tags[id]; ok {
			out = append(out, l)
		} else {
			out = append(out, strconv.Itoa(id))
		}
	}
	slices.Sort(out)

	return out
}

// resolveProfile finds a quality profile by name or id; an unknown one is an
// error listing the profiles there are.
func resolveProfile(ctx context.Context, c *radarr.Client, ref string) (*radarr.QualityProfileResource, error) {
	res, err := c.GetQualityprofile(ctx)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	id, idErr := strconv.Atoi(ref)
	names := make([]string, 0, len(res.Model))
	for i := range res.Model {
		p := &res.Model[i]
		if idErr == nil && p.Id == id || strings.EqualFold(p.Name, ref) {
			return p, nil
		}
		names = append(names, fmt.Sprintf("%s (%d)", p.Name, p.Id))
	}

	return nil, fmt.Errorf("no quality profile %q; the profiles are %s", ref, strings.Join(names, ", "))
}

// resolveRootFolder finds a root folder by path or id; with no reference and
// exactly one root folder, that one.
func resolveRootFolder(ctx context.Context, c *radarr.Client, ref string) (*radarr.RootFolderResource, error) {
	res, err := c.GetRootfolder(ctx)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" && len(res.Model) == 1 {
		return &res.Model[0], nil
	}
	id, idErr := strconv.Atoi(ref)
	paths := make([]string, 0, len(res.Model))
	for i := range res.Model {
		f := &res.Model[i]
		if idErr == nil && f.Id == id || strings.TrimRight(f.Path, "/") == strings.TrimRight(ref, "/") {
			return f, nil
		}
		paths = append(paths, fmt.Sprintf("%s (%d)", f.Path, f.Id))
	}
	if len(paths) == 0 {
		return nil, errors.New("Radarr has no root folders; rootfolder_add creates one") //nolint:staticcheck // Radarr is a name
	}
	if ref == "" {
		return nil, fmt.Errorf("Radarr has %d root folders, so name one: %s", len(paths), strings.Join(paths, ", ")) //nolint:staticcheck // Radarr is a name
	}

	return nil, fmt.Errorf("no root folder %q; the root folders are %s", ref, strings.Join(paths, ", "))
}

// resolveTags turns tag labels (or ids) into ids, creating the labels that do
// not exist yet when create is set.
func resolveTags(ctx context.Context, c *radarr.Client, refs []string, create bool) ([]int, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	res, err := c.GetTag(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		found := false
		for _, t := range res.Model {
			if strings.EqualFold(t.Label, ref) || strconv.Itoa(t.Id) == ref {
				ids = append(ids, t.Id)
				found = true
				break
			}
		}
		if found {
			continue
		}
		if !create {
			labels := make([]string, 0, len(res.Model))
			for _, t := range res.Model {
				labels = append(labels, t.Label)
			}
			return nil, fmt.Errorf("no tag %q; the tags are %s", ref, strings.Join(labels, ", "))
		}
		label, err := tagLabel(ref)
		if err != nil {
			return nil, err
		}
		made, err := c.PostTag(ctx, radarr.TagResource{Label: label})
		if client.StatusCode(err) == http.StatusConflict {
			// another call made it between our read and our create: use it
			if id, found := tagID(ctx, c, label); found {
				ids = append(ids, id)
				continue
			}
		}
		if err != nil {
			return nil, fmt.Errorf("creating tag %q: %w", ref, err)
		}
		ids = append(ids, made.Model.Id)
	}

	return ids, nil
}

// tagID finds a tag's id by label.
func tagID(ctx context.Context, c *radarr.Client, label string) (int, bool) {
	res, err := c.GetTag(ctx)
	if err != nil {
		return 0, false
	}
	for _, t := range res.Model {
		if strings.EqualFold(t.Label, label) {
			return t.Id, true
		}
	}

	return 0, false
}

// movieRow is a film as a list shows it: enough to tell films apart and see
// what state each is in.
type movieRow struct {
	ID             int      `json:"id"`
	Title          string   `json:"title"`
	Year           int      `json:"year,omitempty"`
	TmdbID         int      `json:"tmdb_id,omitempty"`
	Monitored      bool     `json:"monitored"`
	HasFile        bool     `json:"has_file"`
	Quality        string   `json:"quality,omitempty"         jsonschema:"the file's quality, when there is a file"`
	SizeOnDisk     int64    `json:"size_on_disk,omitempty"    jsonschema:"bytes"`
	Status         string   `json:"status,omitempty"          jsonschema:"announced, inCinemas, released, or deleted (gone from TMDB)"`
	QualityProfile string   `json:"quality_profile,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Path           string   `json:"path,omitempty"`
}

func rowOf(m *radarr.MovieResource, lk lookups) movieRow {
	r := movieRow{
		ID:             m.Id,
		Title:          m.Title,
		Year:           m.Year,
		TmdbID:         m.TmdbId,
		Monitored:      isTrue(m.Monitored),
		HasFile:        isTrue(m.HasFile),
		SizeOnDisk:     val(m.SizeOnDisk),
		Status:         string(m.Status),
		QualityProfile: lk.profiles[m.QualityProfileId],
		Tags:           lk.tagLabels(m.Tags),
		Path:           m.Path,
	}
	if m.MovieFile != nil {
		r.Quality = qualityName(m.MovieFile.Quality)
	}

	return r
}

// fileInfo is a film's file: where it is, how Radarr graded it, and what the
// probe found inside it.
type fileInfo struct {
	ID                int          `json:"id"`
	RelativePath      string       `json:"relative_path"`
	Path              string       `json:"path,omitempty"`
	Size              int64        `json:"size"                          jsonschema:"bytes"`
	Quality           string       `json:"quality,omitempty"`
	CutoffNotMet      bool         `json:"cutoff_not_met"                jsonschema:"the profile would still upgrade this file"`
	Languages         []string     `json:"languages,omitempty"`
	ReleaseGroup      string       `json:"release_group,omitempty"`
	Edition           string       `json:"edition,omitempty"`
	CustomFormats     []string     `json:"custom_formats,omitempty"`
	CustomFormatScore int          `json:"custom_format_score,omitempty"`
	DateAdded         string       `json:"date_added,omitempty"`
	Media             *mediaDetail `json:"media,omitempty"`
}

// mediaDetail is what Radarr's probe read from the file.
type mediaDetail struct {
	Resolution     string   `json:"resolution,omitempty"      jsonschema:"width x height"`
	VideoCodec     string   `json:"video_codec,omitempty"`
	VideoBitrate   int64    `json:"video_bitrate,omitempty"   jsonschema:"bits per second, when the file records one"`
	VideoFPS       float64  `json:"video_fps,omitempty"`
	DynamicRange   string   `json:"dynamic_range,omitempty"`
	AudioCodec     string   `json:"audio_codec,omitempty"`
	AudioChannels  float64  `json:"audio_channels,omitempty"`
	AudioLanguages []string `json:"audio_languages,omitempty"`
	Subtitles      []string `json:"subtitles,omitempty"`
	Runtime        string   `json:"runtime,omitempty"         jsonschema:"h:mm:ss"`
	RuntimeMinutes float64  `json:"runtime_minutes,omitempty"`
}

func fileOf(f *radarr.MovieFileResource) *fileInfo {
	if f == nil {
		return nil
	}
	out := &fileInfo{
		ID:                f.Id,
		RelativePath:      f.RelativePath,
		Path:              f.Path,
		Size:              f.Size,
		Quality:           qualityName(f.Quality),
		CutoffNotMet:      isTrue(f.QualityCutoffNotMet),
		Languages:         languageNames(f.Languages),
		ReleaseGroup:      f.ReleaseGroup,
		Edition:           f.Edition,
		CustomFormatScore: val(f.CustomFormatScore),
		DateAdded:         f.DateAdded,
	}
	for _, cf := range f.CustomFormats {
		out.CustomFormats = append(out.CustomFormats, cf.Name)
	}
	if mi := f.MediaInfo; mi != nil {
		out.Media = &mediaDetail{
			Resolution:     mi.Resolution,
			VideoCodec:     mi.VideoCodec,
			VideoBitrate:   mi.VideoBitrate,
			VideoFPS:       mi.VideoFps,
			DynamicRange:   strings.TrimSpace(mi.VideoDynamicRange + " " + mi.VideoDynamicRangeType),
			AudioCodec:     mi.AudioCodec,
			AudioChannels:  mi.AudioChannels,
			AudioLanguages: splitList(mi.AudioLanguages),
			Subtitles:      splitList(mi.Subtitles),
			Runtime:        mi.RunTime,
			RuntimeMinutes: runtimeMinutes(mi.RunTime),
		}
	}

	return out
}

func isTrue(b *bool) bool { return b != nil && *b }

// val is what a nullable field holds, its zero value when it is null.
func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}

// qualityName is the name of a graded quality, "" when there is none.
func qualityName(q *radarr.QualityModel) string {
	if q == nil || q.Quality == nil {
		return ""
	}

	return q.Quality.Name
}

func languageNames(ls []radarr.Language) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}

	return out
}

// splitList splits the "/"-separated lists Radarr's media info renders
// languages and subtitles as ("eng/jpn"), dropping empties.
func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, "/") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}

// runtimeMinutes reads Radarr's h:mm:ss (or mm:ss) runtime as minutes, 0
// when it cannot.
func runtimeMinutes(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	total := 0.0
	for _, p := range parts {
		n, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}

	return total / 60
}

// commandOut is a Radarr command as it stood when the tool stopped waiting.
type commandOut struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"             jsonschema:"queued, started, completed, failed, aborted or cancelled"`
	Message  string `json:"message,omitempty"`
	Queued   string `json:"queued,omitempty"`
	Started  string `json:"started,omitempty"`
	Ended    string `json:"ended,omitempty"`
	Duration string `json:"duration,omitempty"`
}

func commandOf(c *radarr.CommandResource) commandOut {
	return commandOut{
		ID:       c.Id,
		Name:     c.Name,
		Status:   string(c.Status),
		Message:  c.Message,
		Queued:   c.Queued,
		Started:  c.Started,
		Ended:    c.Ended,
		Duration: c.Duration,
	}
}

// finished reports whether a command has stopped, for good or ill.
func finished(status radarr.CommandStatus) bool {
	switch status {
	case radarr.CommandStatusCompleted, radarr.CommandStatusFailed, radarr.CommandStatusAborted, radarr.CommandStatusCancelled, radarr.CommandStatusOrphaned:
		return true
	default:
		return false
	}
}

// commandPoll is how often a waiting tool asks after its command.
var commandPoll = 500 * time.Millisecond

// runCommand starts a command (its name and the fields that command reads)
// and, with a wait, polls it until it stops or the wait runs out. A command
// that fails is an error carrying Radarr's message; one still running when
// the wait ends is returned as it stands, so the caller can say so.
func runCommand(ctx context.Context, c *radarr.Client, body map[string]any, wait time.Duration) (commandOut, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return commandOut{}, err
	}
	res, err := c.PostCommand(ctx, raw)
	if err != nil {
		return commandOut{}, fmt.Errorf("starting %v: %w", body["name"], err)
	}
	cmd := res.Model
	deadline := time.Now().Add(wait)
	for wait > 0 && !finished(cmd.Status) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return commandOf(cmd), ctx.Err()
		case <-time.After(commandPoll):
		}
		got, err := c.GetCommandById(ctx, cmd.Id)
		if err != nil {
			return commandOf(cmd), err
		}
		cmd = got.Model
	}
	out := commandOf(cmd)
	if cmd.Status == radarr.CommandStatusFailed {
		msg := strings.TrimSpace(cmd.Message + " " + cmd.Exception)
		return out, fmt.Errorf("%s failed: %s", out.Name, msg)
	}

	return out, nil
}

// defaultWait is how long a tool waits for the command it started: long
// enough for a refresh or a rescan of a handful of films, short enough that
// a stuck command comes back as "still running" rather than a hung call.
const defaultWait = 2 * time.Minute

// waitFor is the wait a tool was asked for, in seconds: 0 means the default,
// and a negative one not to wait at all.
func waitFor(seconds int) time.Duration {
	switch {
	case seconds < 0:
		return 0
	case seconds == 0:
		return defaultWait
	default:
		return time.Duration(seconds) * time.Second
	}
}

// limitOr is the limit asked for, or the default when none was.
func limitOr(limit, dflt int) int {
	if limit <= 0 {
		return dflt
	}

	return limit
}

// decodeBody decodes a response's JSON body into v, for the operations whose
// document declares no answer but whose server sends one. The base client
// buffers bodies, so this can read one a generated method has already read.
func decodeBody(resp *http.Response, v any) error {
	if resp == nil || resp.Body == nil {
		return errors.New("no response body")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) == "" {
		return errors.New("an empty response body")
	}

	return json.Unmarshal(body, v)
}
