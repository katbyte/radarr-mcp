package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type taskRow struct {
	Name            string `json:"name"`
	TaskName        string `json:"task_name"                jsonschema:"what task_run takes"`
	IntervalMinutes int    `json:"interval_minutes"`
	LastExecution   string `json:"last_execution,omitempty"`
	LastDuration    string `json:"last_duration,omitempty"`
	NextExecution   string `json:"next_execution,omitempty"`
}

// runnableTasks are the scheduled tasks and library-wide commands task_run
// starts, with what each does. Restart, shutdown, update and restore are
// left out on purpose: none of them belongs in a conversation.
var runnableTasks = map[string]string{
	"RssSync":                   "read every indexer's RSS feed and grab what qualifies",
	"RefreshMonitoredDownloads": "ask the download clients what finished, and import it",
	"RefreshMovie":              "refresh every film's metadata from TMDB and rescan its folder",
	"RefreshCollections":        "refresh the TMDB collections the library's films belong to",
	"ImportListSync":            "read the import lists and add what they name",
	"MissingMoviesSearch":       "search the indexers for every monitored film with no file",
	"CutOffUnmetMoviesSearch":   "search the indexers for an upgrade to every file below its profile's cutoff",
	"CheckHealth":               "run the health checks now",
	"Housekeeping":              "clean up the database",
	"CleanUpRecycleBin":         "empty the recycle bin of what is older than its retention",
	"Backup":                    "back up the database and settings",
}

// taskNames lists runnableTasks sorted, for errors and descriptions.
func taskNames() []string {
	out := make([]string, 0, len(runnableTasks))
	for name := range runnableTasks {
		out = append(out, name)
	}
	slices.Sort(out)

	return out
}

// canonicalTask finds a runnable task by name, spelled as task_list shows it
// ("Rss Sync") or as Radarr's commands do ("RssSync").
func canonicalTask(name string) (string, bool) {
	want := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	for task := range runnableTasks {
		if strings.EqualFold(task, want) {
			return task, true
		}
	}

	return "", false
}

func registerTaskTools(r *registry) {
	client := r.client

	type taskListOut struct {
		Tasks    []taskRow    `json:"tasks"    jsonschema:"the scheduled tasks, with when each last and next runs"`
		Commands []commandOut `json:"commands" jsonschema:"the commands queued, running or recently finished, newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_list",
		Description: "List Radarr's scheduled tasks (RSS sync, refreshing films and collections, checking downloads, backups, housekeeping) with when each last ran and runs next, and the commands queued, running or recently finished.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, taskListOut, error) {
		tasks, err := client.GetSystemTask(ctx)
		if err != nil {
			return nil, taskListOut{}, err
		}
		out := taskListOut{}
		for _, t := range tasks.Model {
			out.Tasks = append(out.Tasks, taskRow{
				Name: t.Name, TaskName: t.TaskName, IntervalMinutes: t.Interval,
				LastExecution: t.LastExecution, LastDuration: t.LastDuration, NextExecution: t.NextExecution,
			})
		}
		slices.SortFunc(out.Tasks, func(a, b taskRow) int { return strings.Compare(a.Name, b.Name) })

		commands, err := client.GetCommand(ctx)
		if err != nil {
			return nil, out, err
		}
		for i := range commands.Model {
			out.Commands = append(out.Commands, commandOf(&commands.Model[i]))
		}
		slices.SortFunc(out.Commands, func(a, b commandOut) int { return b.ID - a.ID })
		if len(out.Commands) > 20 {
			out.Commands = out.Commands[:20]
		}

		return nil, out, nil
	})

	type taskRunIn struct {
		Task string `json:"task"           jsonschema:"the task: RssSync, RefreshMonitoredDownloads, RefreshMovie, RefreshCollections, ImportListSync, MissingMoviesSearch, CutOffUnmetMoviesSearch, CheckHealth, Housekeeping, CleanUpRecycleBin or Backup (as task_list names them, spaces or not)"`
		Wait int    `json:"wait,omitempty" jsonschema:"seconds to wait for it to finish, default 120; -1 starts it and returns at once"`
	}
	type taskRunOut struct {
		Task    string     `json:"task"`
		Does    string     `json:"does"`
		Command commandOut `json:"command"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "task_run",
		Description: "Run one of Radarr's library-wide tasks now rather than waiting for its schedule - an RSS sync, checking the download clients for finished downloads, refreshing every film, searching for everything missing or below cutoff - and wait for it to finish. " +
			"Restart, shutdown and updates are not offered.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskRunIn) (*mcp.CallToolResult, taskRunOut, error) {
		task, ok := canonicalTask(in.Task)
		if !ok {
			return nil, taskRunOut{}, fmt.Errorf("task %q is not one task_run starts; the tasks are %s", in.Task, strings.Join(taskNames(), ", "))
		}
		cmd, err := runCommand(ctx, client, map[string]any{"name": task}, waitFor(in.Wait))

		return nil, taskRunOut{Task: task, Does: runnableTasks[task], Command: cmd}, err
	})
}
