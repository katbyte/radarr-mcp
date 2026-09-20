package tools

// The audits of the library as a whole: films whose metadata came back
// empty, collections held in part, and downloads that will never finish on
// their own.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// metadataFields are what audit_missing_metadata can look for, and how to
// tell each is missing.
var metadataFields = map[string]func(m *radarr.MovieResource) bool{
	"overview":      func(m *radarr.MovieResource) bool { return strings.TrimSpace(m.Overview) == "" },
	"imdb":          func(m *radarr.MovieResource) bool { return m.ImdbId == "" },
	"poster":        func(m *radarr.MovieResource) bool { return !slices.ContainsFunc(m.Images, isPoster) },
	"genres":        func(m *radarr.MovieResource) bool { return len(m.Genres) == 0 },
	"runtime":       func(m *radarr.MovieResource) bool { return m.Runtime == 0 },
	"year":          func(m *radarr.MovieResource) bool { return m.Year == 0 },
	"certification": func(m *radarr.MovieResource) bool { return m.Certification == "" },
	"studio":        func(m *radarr.MovieResource) bool { return m.Studio == "" },
}

// defaultMetadataFields are what "all" checks: what a film cannot do without.
// Certification and studio are often missing for foreign and old films, and
// only checked when asked for.
var defaultMetadataFields = []string{"overview", "imdb", "poster", "genres", "runtime", "year"}

func isPoster(i radarr.MediaCover) bool { return i.CoverType == radarr.MediaCoverTypesPoster }

// metadataCheck flags films missing any of the fields.
func metadataCheck(fields []string) filmCheck {
	return func(_ context.Context, _ *auditEnv, m *radarr.MovieResource) (string, bool, bool, error) {
		var missing []string
		for _, f := range fields {
			if metadataFields[f](m) {
				missing = append(missing, "no "+f)
			}
		}
		if len(missing) == 0 {
			return "", false, true, nil
		}

		return strings.Join(missing, ", "), true, true, nil
	}
}

// metadataFieldsFor turns audit_missing_metadata's field into the fields to
// check.
func metadataFieldsFor(field string) ([]string, error) {
	field = strings.ToLower(strings.TrimSpace(field))
	if field == "" || field == "all" {
		return defaultMetadataFields, nil
	}
	if _, ok := metadataFields[field]; !ok {
		names := make([]string, 0, len(metadataFields))
		for n := range metadataFields {
			names = append(names, n)
		}
		slices.Sort(names)
		return nil, fmt.Errorf("field %q: want all or one of %s", field, strings.Join(names, ", "))
	}

	return []string{field}, nil
}

type gapsIn struct {
	IncludeUnreleased bool `json:"include_unreleased,omitempty" jsonschema:"count announced films as missing too"`
	Limit             int  `json:"limit,omitempty"              jsonschema:"findings to return, default 100"`
}

// gapsAudit lists the collections the library holds some but not all of.
func gapsAudit(ctx context.Context, e *auditEnv, in gapsIn) (auditOut, error) {
	res, err := e.c.GetCollection(ctx, radarr.GetCollectionOperationOptions{})
	if err != nil {
		return auditOut{}, err
	}
	cols := res.Model
	slices.SortFunc(cols, func(a, b radarr.CollectionResource) int { return strings.Compare(a.Title, b.Title) })
	out := auditOut{}
	for i := range cols {
		out.Scanned++
		row := collectionOf(&cols[i], lookups{}, in.IncludeUnreleased)
		var missing []collectionMember
		for _, m := range row.Missing {
			if !m.Excluded {
				missing = append(missing, m)
			}
		}
		if len(row.Held) == 0 || len(missing) == 0 {
			continue
		}
		names := make([]string, 0, len(missing))
		for _, m := range missing {
			names = append(names, titleYear(m.Title, m.Year))
		}
		out.collect(auditFinding{
			Title:   row.Title,
			Detail:  fmt.Sprintf("holds %d of %d: missing %s", len(row.Held), len(row.Held)+len(missing), strings.Join(names, ", ")),
			Missing: missing,
		}, in.Limit)
	}

	return out, nil
}

type queueAuditIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"findings to return, default 100"`
}

// stuck reports whether a download in the queue needs someone: an import
// blocked or failed, a failed download, or anything Radarr flags.
func stuck(q *radarr.QueueResource) bool {
	switch q.TrackedDownloadState {
	case radarr.TrackedDownloadStateImportBlocked, radarr.TrackedDownloadStateFailedPending, radarr.TrackedDownloadStateFailed:
		return true
	default:
	}
	switch q.Status {
	case radarr.QueueStatusFailed, radarr.QueueStatusWarning:
		return true
	default:
	}

	return q.TrackedDownloadStatus == radarr.TrackedDownloadStatusWarning || q.TrackedDownloadStatus == radarr.TrackedDownloadStatusError
}

// queueAudit lists the downloads that will not finish on their own.
func queueAudit(ctx context.Context, e *auditEnv, in queueAuditIn) (auditOut, error) {
	items, err := queueItems(ctx, e.c, nil, true)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{Scanned: len(items)}
	for i := range items {
		q := &items[i]
		if !stuck(q) {
			continue
		}
		row := queueOf(q)
		detail := row.State
		if detail == "" {
			detail = row.Status
		}
		if len(row.Messages) > 0 {
			detail += ": " + strings.Join(row.Messages, "; ")
		} else if row.ErrorMessage != "" {
			detail += ": " + row.ErrorMessage
		}
		f := auditFinding{ID: row.MovieID, Title: q.Title, Path: row.OutputPath, Detail: detail, QueueID: q.Id}
		if q.Movie != nil {
			f.Year = q.Movie.Year
			f.Detail = titleYear(q.Movie.Title, q.Movie.Year) + ": " + f.Detail
		}
		out.collect(f, in.Limit)
	}

	return out, nil
}

func registerLibraryAudits(r *registry) {
	client := r.client

	type metadataIn struct {
		auditIn
		Field string `json:"field,omitempty" jsonschema:"what to look for: overview, imdb, poster, genres, runtime, year, certification or studio; default all, which is every one but certification and studio"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_missing_metadata",
		Description: "Sweep the library for films whose TMDB metadata came back without something a film should have - an overview, an IMDb id, a poster, genres, a runtime, a year - usually a stub TMDB entry or a failed refresh. " +
			"movie_refresh fetches it again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in metadataIn) (*mcp.CallToolResult, auditOut, error) {
		fields, err := metadataFieldsFor(in.Field)
		if err != nil {
			return nil, auditOut{}, err
		}
		out, err := sweep(ctx, newAuditEnv(client), in.auditIn, metadataCheck(fields))
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_collection_gaps",
		Description: "List the TMDB collections the library holds some but not all of - two films of a trilogy, most of a franchise - each with the released films it is missing, leaving out ones on the import list exclusions. " +
			"movie_add fills a gap; collection_edit monitored true has Radarr fill them all.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gapsIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := gapsAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_queue",
		Description: "List the downloads that will not finish on their own: imports Radarr blocked (no video file, not an upgrade, the wrong film), failed downloads, and anything else it flags, each with the reasons it gives. " +
			"import_scan and import_apply import one by hand; queue_remove clears one, optionally blocklisting the release.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueAuditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := queueAudit(ctx, newAuditEnv(client), in)
		return nil, out, err
	})
}

// auditRun is one line of audit_all: an audit run with its defaults.
type auditRun struct {
	name string
	// deep audits ask Radarr about every film in turn, and only run when
	// audit_all is asked to go deep
	deep bool
	run  func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error)
}

// auditRuns is every audit, in the order audit_all reports them.
func auditRuns() []auditRun {
	var runs []auditRun
	for _, a := range filmAudits {
		runs = append(runs, auditRun{name: a.name, run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, a.check)
		}})
	}

	return append(runs,
		auditRun{name: "audit_runtime", run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, runtimeCheck(0))
		}},
		auditRun{name: "audit_resolution_mismatch", run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, resolutionCheck)
		}},
		auditRun{name: "audit_quality", run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, qualityCheck(0, 0))
		}},
		auditRun{name: "audit_size", run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, sizeCheck)
		}},
		auditRun{name: "audit_language", run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, languageCheck(nil))
		}},
		auditRun{name: "audit_missing_metadata", run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return sweep(ctx, e, in, metadataCheck(defaultMetadataFields))
		}},
		auditRun{name: "audit_unmapped_folders", run: unmappedAudit},
		auditRun{name: "audit_missing_folders", run: missingFoldersAudit},
		auditRun{name: "audit_collection_gaps", run: func(ctx context.Context, e *auditEnv, _ auditIn) (auditOut, error) {
			return gapsAudit(ctx, e, gapsIn{})
		}},
		auditRun{name: "audit_queue", run: func(ctx context.Context, e *auditEnv, _ auditIn) (auditOut, error) {
			return queueAudit(ctx, e, queueAuditIn{})
		}},
		auditRun{name: "audit_naming", deep: true, run: func(ctx context.Context, e *auditEnv, in auditIn) (auditOut, error) {
			return namingAudit(ctx, e, namingIn{auditIn: in})
		}},
		auditRun{name: "audit_untracked_files", deep: true, run: untrackedAudit},
	)
}

func registerAuditAll(r *registry) {
	client := r.client

	type allIn struct {
		RootFolder string `json:"root_folder,omitempty" jsonschema:"only films in this root folder (path or id)"`
		Tag        string `json:"tag,omitempty"         jsonschema:"only films with this tag"`
		Deep       bool   `json:"deep,omitempty"        jsonschema:"also run the audits that ask Radarr about every film in turn (naming and untracked files), which are slower"`
	}
	type allRow struct {
		Audit   string `json:"audit"`
		Scanned int    `json:"scanned"`
		Found   int    `json:"total_findings"`
	}
	type allOut struct {
		Found   int      `json:"total_findings"`
		Audits  []allRow `json:"audits"            jsonschema:"every audit run, with how many findings it has; run the audit itself for the worklist"`
		Skipped []string `json:"skipped,omitempty" jsonschema:"the slow audits left out, which deep runs"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_all",
		Description: "Run every audit at once and report how many findings each has, counts only, so one call says where the library needs work - start here. " +
			"The two that ask Radarr about every film in turn run only with deep.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in allIn) (*mcp.CallToolResult, allOut, error) {
		env := newAuditEnv(client)
		scope := auditIn{RootFolder: in.RootFolder, Tag: in.Tag, Limit: 1}
		out := allOut{}
		for _, a := range auditRuns() {
			if a.deep && !in.Deep {
				out.Skipped = append(out.Skipped, a.name)
				continue
			}
			res, err := a.run(ctx, env, scope)
			if err != nil {
				return nil, out, fmt.Errorf("%s: %w", a.name, err)
			}
			out.Audits = append(out.Audits, allRow{Audit: a.name, Scanned: res.Scanned, Found: res.Found})
			out.Found += res.Found
		}
		if len(out.Audits) == 0 {
			return nil, out, errors.New("no audit ran")
		}

		return nil, out, nil
	})
}
