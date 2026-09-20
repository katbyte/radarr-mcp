package tools

// The audits: each a sweep over the whole library for one thing that goes
// wrong in a real collection, returning a worklist rather than a dump, and
// naming the tool that fixes what it finds. Detection is code, correction is
// judgment: the audits run cheap deterministic checks over everything, so a
// model only has to reason about the anomalies.
//
// Most audits are a check applied to every film Radarr answers in one
// request (films carry their file and the file's media info); the rest read
// the root folders, the queue, the collections, or - the deep ones - ask
// Radarr about each film's folder in turn.

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditIn narrows an audit to part of the library.
type auditIn struct {
	RootFolder string `json:"root_folder,omitempty" jsonschema:"only films in this root folder (path or id)"`
	Tag        string `json:"tag,omitempty"         jsonschema:"only films with this tag"`
	Limit      int    `json:"limit,omitempty"       jsonschema:"findings to return, default 100"`
}

// auditFinding is one thing an audit found: a film, a folder, a download or
// a collection, and what is wrong with it.
type auditFinding struct {
	ID          int                `json:"id,omitempty"           jsonschema:"the film's id; absent for a folder no film owns"`
	Title       string             `json:"title"`
	Year        int                `json:"year,omitempty"`
	Path        string             `json:"path,omitempty"`
	File        string             `json:"file,omitempty"         jsonschema:"the file the finding is about"`
	Detail      string             `json:"detail"`
	DuplicateOf int                `json:"duplicate_of,omitempty" jsonschema:"the id of the film in the library this is a second copy of"`
	Folder      string             `json:"folder,omitempty"       jsonschema:"a folder on disk no film is in that reads as this film"`
	QueueID     int                `json:"queue_id,omitempty"`
	Missing     []collectionMember `json:"missing,omitempty"      jsonschema:"the collection's released films the library does not hold"`
}

type auditOut struct {
	Scanned  int            `json:"scanned"        jsonschema:"what the audit looked at: films, or folders, downloads or collections"`
	Found    int            `json:"total_findings"`
	Findings []auditFinding `json:"findings"       jsonschema:"capped at limit; total_findings is the real count"`
	Note     string         `json:"note,omitempty" jsonschema:"why the audit could not check everything, when it could not"`
}

// finding is a film's finding.
func finding(m *radarr.MovieResource, detail string) auditFinding {
	return auditFinding{ID: m.Id, Title: m.Title, Year: m.Year, Path: m.Path, Detail: detail}
}

// collect adds a finding, counting it and keeping it when there is room.
func (o *auditOut) collect(f auditFinding, limit int) {
	o.Found++
	if len(o.Findings) < limitOr(limit, 100) {
		o.Findings = append(o.Findings, f)
	}
}

// auditEnv is what one audit call - or one audit_all - reads from Radarr,
// each list read once and shared.
type auditEnv struct {
	c *radarr.Client

	movies      []radarr.MovieResource
	profiles    map[int]*radarr.QualityProfileResource
	definitions map[string]definitionRow
}

func newAuditEnv(c *radarr.Client) *auditEnv { return &auditEnv{c: c} }

func (e *auditEnv) allMovies(ctx context.Context) ([]radarr.MovieResource, error) {
	if e.movies == nil {
		movies, err := allMovies(ctx, e.c)
		if err != nil {
			return nil, err
		}
		e.movies = movies
	}

	return e.movies, nil
}

func (e *auditEnv) profile(ctx context.Context, id int) (*radarr.QualityProfileResource, error) {
	if e.profiles == nil {
		res, err := e.c.GetQualityprofile(ctx)
		if err != nil {
			return nil, err
		}
		e.profiles = map[int]*radarr.QualityProfileResource{}
		for i := range res.Model {
			e.profiles[res.Model[i].Id] = &res.Model[i]
		}
	}

	return e.profiles[id], nil
}

func (e *auditEnv) definition(ctx context.Context, quality string) (definitionRow, bool, error) {
	if e.definitions == nil {
		rows, err := definitions(ctx, e.c)
		if err != nil {
			return definitionRow{}, false, err
		}
		e.definitions = map[string]definitionRow{}
		for _, d := range rows {
			e.definitions[d.Quality] = d
		}
	}
	d, ok := e.definitions[quality]

	return d, ok, nil
}

// scope is the films an audit covers: every film, or those in a root folder
// or with a tag.
func (e *auditEnv) scope(ctx context.Context, in auditIn) ([]radarr.MovieResource, error) {
	movies, err := e.allMovies(ctx)
	if err != nil {
		return nil, err
	}
	root := ""
	if in.RootFolder != "" {
		f, err := resolveRootFolder(ctx, e.c, in.RootFolder)
		if err != nil {
			return nil, err
		}
		root = f.Path
	}
	tag := -1
	if in.Tag != "" {
		ids, err := resolveTags(ctx, e.c, []string{in.Tag}, false)
		if err != nil {
			return nil, err
		}
		tag = ids[0]
	}
	out := make([]radarr.MovieResource, 0, len(movies))
	for i := range movies {
		m := &movies[i]
		if root != "" && !inFolder(m.Path, root) || tag >= 0 && !slices.Contains(m.Tags, tag) {
			continue
		}
		out = append(out, *m)
	}
	slices.SortStableFunc(out, func(a, b radarr.MovieResource) int { return strings.Compare(sortTitle(&a), sortTitle(&b)) })

	return out, nil
}

// filmCheck is one film's audit: a finding's detail when the film is
// suspect, and whether the film was one the check could look at (a check of
// files cannot look at a film with none).
type filmCheck func(ctx context.Context, e *auditEnv, m *radarr.MovieResource) (detail string, suspect, examined bool, err error)

// sweep applies a check to every film in scope.
func sweep(ctx context.Context, e *auditEnv, in auditIn, check filmCheck) (auditOut, error) {
	movies, err := e.scope(ctx, in)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{}
	for i := range movies {
		detail, suspect, examined, err := check(ctx, e, &movies[i])
		if err != nil {
			return out, err
		}
		if examined {
			out.Scanned++
		}
		if suspect {
			f := finding(&movies[i], detail)
			if movies[i].MovieFile != nil {
				f.File = movies[i].MovieFile.RelativePath
			}
			out.collect(f, in.Limit)
		}
	}

	return out, nil
}

// withFile adapts a check of a film's file into a filmCheck that skips the
// films with none.
func withFile(check func(ctx context.Context, e *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error)) filmCheck {
	return func(ctx context.Context, e *auditEnv, m *radarr.MovieResource) (string, bool, bool, error) {
		if !isTrue(m.HasFile) || m.MovieFile == nil {
			return "", false, false, nil
		}
		detail, suspect, err := check(ctx, e, m, m.MovieFile)

		return detail, suspect, true, err
	}
}

// filmAudit is an audit that is a check on each film, with no inputs of its
// own: the table drives both its tool and its line in audit_all.
type filmAudit struct {
	name        string
	description string
	check       filmCheck
}

var filmAudits = []filmAudit{
	{
		name: "audit_missing_files",
		description: "Sweep the library for monitored films that are available (their minimum availability reached) but have no file: what Radarr is still looking for. " +
			"Each says whether a download for it is in the queue and when Radarr last searched for it.",
		check: func(_ context.Context, _ *auditEnv, m *radarr.MovieResource) (string, bool, bool, error) {
			if !isTrue(m.Monitored) || isTrue(m.HasFile) || !isTrue(m.IsAvailable) {
				return "", false, true, nil
			}
			detail := "monitored and available (" + string(m.MinimumAvailability) + "), no file"
			if m.LastSearchTime != "" {
				detail += "; last searched " + m.LastSearchTime
			} else {
				detail += "; never searched"
			}
			return detail, true, true, nil
		},
	},
	{
		name:        "audit_unmonitored",
		description: "Sweep the library for films that are unmonitored and have no file: Radarr will never download them. Either monitor them (movie_edit) or take them out of the library.",
		check: func(_ context.Context, _ *auditEnv, m *radarr.MovieResource) (string, bool, bool, error) {
			if isTrue(m.Monitored) || isTrue(m.HasFile) {
				return "", false, true, nil
			}
			return "unmonitored with no file: Radarr will never download it", true, true, nil
		},
	},
	{
		name: "audit_cutoff_unmet",
		description: "Sweep the library for files below their quality profile's cutoff - by quality, or by custom format score - which Radarr replaces when it finds better only if the profile allows upgrades (Radarr's own profiles are made with them off). " +
			"Each names the file's quality, the cutoff it falls short of, and whether its profile upgrades at all.",
		check: withFile(func(ctx context.Context, e *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
			if !isTrue(f.QualityCutoffNotMet) {
				return "", false, nil
			}
			p, err := e.profile(ctx, m.QualityProfileId)
			if err != nil || p == nil {
				return qualityName(f.Quality) + " is below its profile's cutoff", true, err
			}
			_, cutoff := profileQualities(p)
			detail := fmt.Sprintf("%s is below %s's cutoff of %s", qualityName(f.Quality), p.Name, cutoff)
			if p.CutoffFormatScore > 0 && val(f.CustomFormatScore) < p.CutoffFormatScore {
				detail += fmt.Sprintf(", or its custom format score %d is below %d", val(f.CustomFormatScore), p.CutoffFormatScore)
			}
			// Radarr's own profiles are made with upgrades off, and then
			// nothing below the cutoff is ever replaced on its own
			if !isTrue(p.UpgradeAllowed) {
				detail += "; upgrades are switched off on " + p.Name + ", so Radarr will not replace it on its own"
			}
			return detail, true, nil
		}),
	},
	{
		name: "audit_profile_mismatch",
		description: "Sweep the library for files whose quality their film's quality profile does not allow: a 4K file on a 1080p profile, a DVD rip on an HD one. " +
			"Radarr will not upgrade such a file sensibly: move the film to a profile that fits (movie_edit), or correct the file's grade (moviefile_edit) when Radarr got it wrong.",
		check: withFile(func(ctx context.Context, e *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
			p, err := e.profile(ctx, m.QualityProfileId)
			if err != nil || p == nil {
				return "", false, err
			}
			allowed, _ := profileQualities(p)
			q := qualityName(f.Quality)
			if q == "" || slices.Contains(allowed, q) {
				return "", false, nil
			}
			return fmt.Sprintf("%s is not a quality %s allows (%s)", q, p.Name, strings.Join(allowed, ", ")), true, nil
		}),
	},
	{
		name: "audit_year_mismatch",
		description: "Sweep the library for films whose folder names a year two or more away from the film's: the folder says (2021), the match says 1984 - the wrong edition, or the wrong film. " +
			"Check the match with movie_get: a wrong one is fixed by re-adding the right film for the folder, a right one by renaming the folder (movie_edit into the root folder it is already in).",
		check: func(_ context.Context, _ *auditEnv, m *radarr.MovieResource) (string, bool, bool, error) {
			year, ok := pathYear(m.Path)
			if !ok || m.Year == 0 {
				return "", false, m.Path != "", nil
			}
			if diff := year - m.Year; diff >= 2 || diff <= -2 {
				return fmt.Sprintf("the folder says %d, the film is %d", year, m.Year), true, true, nil
			}
			return "", false, true, nil
		},
	},
	{
		name: "audit_removed",
		description: "Sweep the library for films TMDB no longer lists (status deleted): Radarr can no longer refresh them, and they usually mean a duplicate TMDB entry was merged away. " +
			"Find the surviving entry with movie_lookup and add it in the film's place.",
		check: func(_ context.Context, _ *auditEnv, m *radarr.MovieResource) (string, bool, bool, error) {
			if m.Status != radarr.MovieStatusTypeDeleted {
				return "", false, true, nil
			}
			return fmt.Sprintf("TMDB no longer lists tmdb %d", m.TmdbId), true, true, nil
		},
	},
}

// folderYear is a "(1979)" anywhere in a folder or file name.
var folderYear = regexp.MustCompile(`\((\d{4})\)`)

// pathYear is the year a film's folder names, when it names one.
func pathYear(path string) (int, bool) {
	base := path
	if i := strings.LastIndexByte(strings.TrimRight(path, "/"), '/'); i >= 0 {
		base = path[i+1:]
	}
	m := folderYear.FindStringSubmatch(base)
	if m == nil {
		return 0, false
	}
	y, err := strconv.Atoi(m[1])
	if err != nil || y < 1880 || y > 2200 {
		return 0, false
	}

	return y, true
}

func registerAuditTools(r *registry) {
	client := r.client
	for _, a := range filmAudits {
		add(r, readTool, &mcp.Tool{Name: a.name, Description: a.description},
			func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
				out, err := sweep(ctx, newAuditEnv(client), in, a.check)
				return nil, out, err
			})
	}
	registerFileAudits(r)
	registerDiskAudits(r)
	registerLibraryAudits(r)
	registerAuditAll(r)
}
