package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

type taskRow struct {
	Name            string `json:"name"`
	TaskName        string `json:"task_name"                jsonschema:"what task_run takes"`
	IntervalMinutes int    `json:"interval_minutes"`
	LastExecution   string `json:"last_execution,omitempty"`
	LastDuration    string `json:"last_duration,omitempty"`
	NextExecution   string `json:"next_execution,omitempty"`
}

// task is one thing task_run starts: what it does, in words, and the command that does it.
type task struct {
	does string
	run  func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error)
}

// waited is a command as the wait for it left it.
type waited = radarr.GetCommandByIdResponse

// runnableTasks are the scheduled tasks and library-wide commands task_run starts. Restart, shutdown, update and restore are left out on
// purpose: none of them belongs in a conversation.
var runnableTasks = map[string]task{
	"RssSync": {"read every indexer's RSS feed and grab what qualifies", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.RssSyncAndWait(ctx, radarr.RssSyncCommand{}, wait)
	}},
	"RefreshMonitoredDownloads": {"ask the download clients what finished, and import it", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.RefreshMonitoredDownloadsAndWait(ctx, radarr.RefreshMonitoredDownloadsCommand{}, wait)
	}},
	"RefreshMovie": {"refresh every film's metadata from TMDB and rescan its folder", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.RefreshMovieAndWait(ctx, radarr.RefreshMovieCommand{}, wait)
	}},
	"RefreshCollections": {"refresh the TMDB collections the library's films belong to", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.RefreshCollectionsAndWait(ctx, radarr.RefreshCollectionsCommand{}, wait)
	}},
	"ImportListSync": {"read the import lists and add what they name", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.ImportListSyncAndWait(ctx, radarr.ImportListSyncCommand{}, wait)
	}},
	"MissingMoviesSearch": {"search the indexers for every monitored film with no file", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.MissingMoviesSearchAndWait(ctx, radarr.MissingMoviesSearchCommand{}, wait)
	}},
	"CutOffUnmetMoviesSearch": {"search the indexers for an upgrade to every file below its profile's cutoff", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.CutOffUnmetMoviesSearchAndWait(ctx, radarr.CutOffUnmetMoviesSearchCommand{}, wait)
	}},
	"CheckHealth": {"run the health checks now", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.CheckHealthAndWait(ctx, radarr.CheckHealthCommand{}, wait)
	}},
	"Housekeeping": {"clean up the database", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.HousekeepingAndWait(ctx, radarr.HousekeepingCommand{}, wait)
	}},
	"CleanUpRecycleBin": {"empty the recycle bin of what is older than its retention", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.CleanUpRecycleBinAndWait(ctx, radarr.CleanUpRecycleBinCommand{}, wait)
	}},
	"Backup": {"back up the database and settings", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.BackupAndWait(ctx, radarr.BackupCommand{}, wait)
	}},
	"ApplicationUpdate": {"install the newest release, where Radarr updates itself; a Docker install is updated by pulling a newer image, and this does nothing there", func(ctx context.Context, c *radarr.Client, wait time.Duration) (waited, error) {
		return c.ApplicationUpdateAndWait(ctx, radarr.ApplicationUpdateCommand{}, wait)
	}},
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

	type taskListIn struct {
		CommandID int `json:"command_id,omitempty" jsonschema:"answer this one command in commands, by the id the tool that started it gave: whether it is still queued or running, or how it ended"`
		pageIn
	}
	type taskListOut struct {
		Tasks    []taskRow    `json:"tasks"    jsonschema:"the scheduled tasks, with when each last and next runs: these are what the page is of"`
		Commands []commandOut `json:"commands" jsonschema:"the 20 newest commands queued, running or recently finished, newest first, whatever the page; with command_id, that command alone"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_list",
		Description: "List Radarr's scheduled tasks (RSS sync, refreshing films and collections, checking downloads, backups, housekeeping) with when each last ran and runs next, and the commands queued, running or recently finished. With command_id, the one command a tool started and stopped waiting for: Radarr starts several of its own every minute, which bury it in the list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, taskListOut, error) {
		tasks, err := client.GetSystemTask(ctx)
		if err != nil {
			return nil, taskListOut{}, err
		}
		rows := make([]taskRow, 0, len(tasks.Answer))
		for _, t := range tasks.Answer {
			rows = append(rows, taskRow{
				Name: t.Name, TaskName: t.TaskName, IntervalMinutes: t.Interval,
				LastExecution: shownTime(t.LastExecution), LastDuration: t.LastDuration, NextExecution: shownTime(t.NextExecution),
			})
		}
		slices.SortFunc(rows, func(a, b taskRow) int { return strings.Compare(a.Name, b.Name) })

		out := taskListOut{}
		out.Tasks, out.pageOut = pageRows(rows, in.pageIn)

		// a command still running when its tool stopped waiting is followed by its id, rather than looked for among the ones Radarr starts for itself every minute
		if in.CommandID != 0 {
			one, getErr := client.GetCommandById(ctx, in.CommandID)
			if getErr != nil {
				return nil, out, fmt.Errorf("command %d: %w", in.CommandID, getErr)
			}
			out.Commands = []commandOut{commandOf(one.Answer)}

			return nil, out, nil
		}

		commands, err := client.GetCommand(ctx)
		if err != nil {
			return nil, out, err
		}
		for i := range commands.Answer {
			out.Commands = append(out.Commands, commandOf(&commands.Answer[i]))
		}
		slices.SortFunc(out.Commands, func(a, b commandOut) int { return b.ID - a.ID })
		if len(out.Commands) > 20 {
			out.Commands = out.Commands[:20]
		}

		return nil, out, nil
	})

	type taskRunIn struct {
		Task string `json:"task"           jsonschema:"the task: RssSync, RefreshMonitoredDownloads, RefreshMovie, RefreshCollections, ImportListSync, MissingMoviesSearch, CutOffUnmetMoviesSearch, CheckHealth, ApplicationUpdate, Housekeeping, CleanUpRecycleBin or Backup (as task_list names them, spaces or not)"`
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
			"A restart and a shutdown have tools of their own (server_restart, server_shutdown), and server_updates says what there is to update to.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskRunIn) (*mcp.CallToolResult, taskRunOut, error) {
		name, ok := canonicalTask(in.Task)
		if !ok {
			return nil, taskRunOut{}, fmt.Errorf("task %q is not one task_run starts; the tasks are %s", in.Task, strings.Join(taskNames(), ", "))
		}
		task := runnableTasks[name]
		cmd, err := ran(task.run(ctx, client, waitFor(in.Wait)))

		return nil, taskRunOut{Task: name, Does: task.does, Command: cmd}, err
	})
}
