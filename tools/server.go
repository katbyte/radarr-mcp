package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noArgs struct{}

type rootFolderSpace struct {
	Path       string `json:"path"`
	FreeSpace  int64  `json:"free_space" jsonschema:"bytes"`
	Accessible bool   `json:"accessible"`
}

type serverInfoOut struct {
	RadarrMCP       string            `json:"radarr_mcp_version"      jsonschema:"the build of this server answering: a session keeps the binary it started with"`
	App             string            `json:"app"`
	Version         string            `json:"version"`
	InstanceName    string            `json:"instance_name,omitempty"`
	OS              string            `json:"os"`
	Runtime         string            `json:"runtime"`
	Branch          string            `json:"branch,omitempty"`
	Database        string            `json:"database,omitempty"`
	URLBase         string            `json:"url_base,omitempty"`
	StartTime       string            `json:"start_time,omitempty"`
	Docker          bool              `json:"docker"`
	Movies          int               `json:"movies"                  jsonschema:"films in the library"`
	WithFiles       int               `json:"with_files"              jsonschema:"films with a file on disk"`
	MissingFiles    int               `json:"missing_monitored"       jsonschema:"monitored, released films with no file: what Radarr is still looking for"`
	Unmonitored     int               `json:"unmonitored"`
	SizeOnDisk      int64             `json:"size_on_disk"            jsonschema:"bytes, every film's file together"`
	RootFolders     []rootFolderSpace `json:"root_folders"`
	HealthProblems  int               `json:"health_problems"         jsonschema:"health checks failing"`
	HealthErrorOnly []string          `json:"health_errors,omitempty" jsonschema:"the failing checks at error level, which stop Radarr working"`
}

type healthRow struct {
	Source  string `json:"source"`
	Type    string `json:"type"               jsonschema:"notice, warning or error"`
	Message string `json:"message"`
	WikiURL string `json:"wiki_url,omitempty"`
}

type diskRow struct {
	Path        string  `json:"path"`
	Label       string  `json:"label,omitempty"`
	FreeSpace   int64   `json:"free_space"      jsonschema:"bytes"`
	TotalSpace  int64   `json:"total_space"     jsonschema:"bytes"`
	PercentFree float64 `json:"percent_free"`
}

type logsIn struct {
	Level string `json:"level,omitempty" jsonschema:"only entries at this level or worse: trace, debug, info, warn, error or fatal; default info"`
	Limit int    `json:"limit,omitempty" jsonschema:"entries to return, newest first, default 50"`
	Page  int    `json:"page,omitempty"  jsonschema:"page of limit entries, from 1"`
}

type logRow struct {
	Time      string `json:"time"`
	Level     string `json:"level"`
	Logger    string `json:"logger"`
	Message   string `json:"message"`
	Exception string `json:"exception,omitempty" jsonschema:"the first lines of the exception, when there was one"`
}

type logFileIn struct {
	Name string `json:"name,omitempty" jsonschema:"the log file to read, as the list names it; empty lists the files"`
	Tail int    `json:"tail,omitempty" jsonschema:"lines from the end of the file to return, default 100"`
}

type logFileRow struct {
	Name          string `json:"name"`
	LastWriteTime string `json:"last_write_time"`
}

type backupRow struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type" jsonschema:"scheduled, manual or update"`
	Time string `json:"time"`
	Size int64  `json:"size" jsonschema:"bytes"`
	Path string `json:"path"`
}

// logLevels ranks Radarr's levels, least severe first.
var logLevels = []string{"trace", "debug", "info", "warn", "error", "fatal"}

func registerServerTools(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name:        "server_info",
		Description: "Check connectivity and summarise the Radarr instance: version, platform, how many films it holds, how many have files, how many monitored films it is still missing, its root folders with free space, and how many health checks are failing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, serverInfoOut, error) {
		status, err := client.GetSystemStatus(ctx)
		if err != nil {
			return nil, serverInfoOut{}, err
		}
		s := status.Model
		out := serverInfoOut{
			RadarrMCP:    version.Version,
			App:          s.AppName,
			Version:      s.Version,
			InstanceName: s.InstanceName,
			OS:           strings.TrimSpace(s.OsName + " " + s.OsVersion),
			Runtime:      strings.TrimSpace(s.RuntimeName + " " + s.RuntimeVersion),
			Branch:       s.Branch,
			Database:     strings.TrimSpace(string(s.DatabaseType) + " " + s.DatabaseVersion),
			URLBase:      s.UrlBase,
			StartTime:    s.StartTime,
			Docker:       isTrue(s.IsDocker),
		}

		movies, err := allMovies(ctx, client)
		if err != nil {
			return nil, out, err
		}
		for i := range movies {
			m := &movies[i]
			out.Movies++
			out.SizeOnDisk += val(m.SizeOnDisk)
			switch {
			case isTrue(m.HasFile):
				out.WithFiles++
			case isTrue(m.Monitored) && isTrue(m.IsAvailable):
				out.MissingFiles++
			}
			if !isTrue(m.Monitored) {
				out.Unmonitored++
			}
		}

		folders, err := client.GetRootfolder(ctx)
		if err != nil {
			return nil, out, err
		}
		for _, f := range folders.Model {
			out.RootFolders = append(out.RootFolders, rootFolderSpace{Path: f.Path, FreeSpace: val(f.FreeSpace), Accessible: isTrue(f.Accessible)})
		}

		health, err := client.GetHealth(ctx)
		if err != nil {
			return nil, out, err
		}
		for _, h := range health.Model {
			out.HealthProblems++
			if h.Type == radarr.HealthCheckResultError {
				out.HealthErrorOnly = append(out.HealthErrorOnly, h.Source+": "+h.Message)
			}
		}

		return nil, out, nil
	})

	type healthOut struct {
		Checks []healthRow `json:"checks" jsonschema:"the failing health checks; none is a healthy Radarr"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_health",
		Description: "List Radarr's failing health checks - no indexers, no download client, a root folder it cannot reach, an import list or indexer failing, too little disk - each with its level (notice, warning, error) and the wiki page that explains the fix.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, healthOut, error) {
		res, err := client.GetHealth(ctx)
		if err != nil {
			return nil, healthOut{}, err
		}
		out := healthOut{}
		for _, h := range res.Model {
			out.Checks = append(out.Checks, healthRow{Source: h.Source, Type: string(h.Type), Message: h.Message, WikiURL: h.WikiUrl})
		}
		// errors first: they are what stops Radarr working
		slices.SortStableFunc(out.Checks, func(a, b healthRow) int {
			return severity(b.Type) - severity(a.Type)
		})

		return nil, out, nil
	})

	type diskOut struct {
		Disks []diskRow `json:"disks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_disk_space",
		Description: "List the disks Radarr can see with their free and total space, as Radarr measures it from inside its own container or host.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, diskOut, error) {
		res, err := client.GetDiskspace(ctx)
		if err != nil {
			return nil, diskOut{}, err
		}
		out := diskOut{}
		for _, d := range res.Model {
			row := diskRow{Path: d.Path, Label: d.Label, FreeSpace: d.FreeSpace, TotalSpace: d.TotalSpace}
			if d.TotalSpace > 0 {
				row.PercentFree = float64(d.FreeSpace*1000/d.TotalSpace) / 10
			}
			out.Disks = append(out.Disks, row)
		}

		return nil, out, nil
	})

	type logsOut struct {
		Total   int      `json:"total"   jsonschema:"entries at the level asked for"`
		Entries []logRow `json:"entries" jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_logs",
		Description: "Read Radarr's recent log entries, newest first, at a level or worse (default info): what it grabbed, imported and failed at, and why. server_log_file reads the rotating files on disk instead, which keep more.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, logsOut, error) {
		level := strings.ToLower(strings.TrimSpace(in.Level))
		if level == "" {
			level = "info"
		}
		if !slices.Contains(logLevels, level) {
			return nil, logsOut{}, fmt.Errorf("level %q: want one of %s", in.Level, strings.Join(logLevels, ", "))
		}
		res, err := client.GetLog(ctx, radarr.GetLogOperationOptions{
			Page:          max(in.Page, 1),
			PageSize:      limitOr(in.Limit, 50),
			SortKey:       "time",
			SortDirection: radarr.SortDirectionDescending,
			Level:         level,
		})
		if err != nil {
			return nil, logsOut{}, err
		}
		out := logsOut{Total: res.Model.TotalRecords}
		for _, l := range res.Model.Records {
			out.Entries = append(out.Entries, logRow{Time: l.Time, Level: l.Level, Logger: l.Logger, Message: l.Message, Exception: firstLines(l.Exception, 5)})
		}

		return nil, out, nil
	})

	type logFileOut struct {
		Files []logFileRow `json:"files,omitempty" jsonschema:"the log files, when no name was given"`
		Name  string       `json:"name,omitempty"`
		Lines []string     `json:"lines,omitempty" jsonschema:"the last lines of the file"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_log_file",
		Description: "List Radarr's log files on disk, or with a name read the last lines of one (default 100): the full record, where server_logs holds only the recent entries Radarr keeps in its database.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logFileIn) (*mcp.CallToolResult, logFileOut, error) {
		files, err := client.GetLogFile(ctx)
		if err != nil {
			return nil, logFileOut{}, err
		}
		if in.Name == "" {
			out := logFileOut{}
			for _, f := range files.Model {
				out.Files = append(out.Files, logFileRow{Name: f.Filename, LastWriteTime: f.LastWriteTime})
			}
			return nil, out, nil
		}
		var names []string
		for _, f := range files.Model {
			names = append(names, f.Filename)
		}
		if !slices.Contains(names, in.Name) {
			return nil, logFileOut{}, fmt.Errorf("no log file %q; the files are %s", in.Name, strings.Join(names, ", "))
		}
		res, err := client.GetLogFileByFilename(ctx, in.Name)
		if err != nil {
			return nil, logFileOut{}, err
		}
		defer func() { _ = res.HttpResponse.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(res.HttpResponse.Body, 32<<20))
		if err != nil {
			return nil, logFileOut{}, err
		}

		return nil, logFileOut{Name: in.Name, Lines: lastLines(string(body), limitOr(in.Tail, 100))}, nil
	})

	type backupsOut struct {
		Backups []backupRow `json:"backups" jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_backups",
		Description: "List Radarr's backups of its database and settings, newest first: the scheduled ones, the ones taken before an update, and any made by hand (server_backup_create).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, backupsOut, error) {
		rows, err := backups(ctx, client)
		return nil, backupsOut{Backups: rows}, err
	})

	type backupCreateOut struct {
		Command commandOut `json:"command"`
		Backup  *backupRow `json:"backup,omitempty" jsonschema:"the backup it made"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_backup_create",
		Description: "Take a backup of Radarr's database and settings now, and answer with the backup it made: worth doing before a bulk edit or a change of root folders.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, backupCreateOut, error) {
		before, err := backups(ctx, client)
		if err != nil {
			return nil, backupCreateOut{}, err
		}
		cmd, err := runCommand(ctx, client, map[string]any{"name": "Backup"}, defaultWait)
		out := backupCreateOut{Command: cmd}
		if err != nil {
			return nil, out, err
		}
		if cmd.Status != string(radarr.CommandStatusCompleted) {
			return nil, out, fmt.Errorf("the backup is still %s; server_backups will list it when it finishes", cmd.Status)
		}
		after, err := backups(ctx, client)
		if err != nil {
			return nil, out, err
		}
		for i := range after {
			if !slices.ContainsFunc(before, func(b backupRow) bool { return b.ID == after[i].ID }) {
				out.Backup = &after[i]
				break
			}
		}
		if out.Backup == nil {
			return nil, out, errors.New("the backup command completed but no new backup is listed")
		}

		return nil, out, nil
	})
}

// backups lists the backups, newest first.
func backups(ctx context.Context, c *radarr.Client) ([]backupRow, error) {
	res, err := c.GetSystemBackup(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backupRow, 0, len(res.Model))
	for _, b := range res.Model {
		out = append(out, backupRow{ID: b.Id, Name: b.Name, Type: string(b.Type), Time: b.Time, Size: b.Size, Path: b.Path})
	}
	slices.SortStableFunc(out, func(a, b backupRow) int { return strings.Compare(b.Time, a.Time) })

	return out, nil
}

// severity ranks a health check's level for sorting.
func severity(level string) int {
	switch level {
	case string(radarr.HealthCheckResultError):
		return 3
	case string(radarr.HealthCheckResultWarning):
		return 2
	case string(radarr.HealthCheckResultNotice):
		return 1
	default:
		return 0
	}
}

// firstLines keeps the first n lines of s, for a stack trace that would
// otherwise fill the answer.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}

	return strings.Join(lines, "\n")
}

// lastLines keeps the last n lines of s.
func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return lines
}
