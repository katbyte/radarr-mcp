//go:build integration

package acceptance

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// server_info summarises the library the harness built: its counts are the
// fixtures', and its health line agrees with server_health. It also says
// which build of radarr-mcp answered, since a session keeps the binary it
// started with.
func TestServerInfo(t *testing.T) {
	out := call(t, "server_info", nil)
	if str(out["app"]) != "Radarr" || str(out["version"]) == "" || str(out["instance_name"]) != "radarr-mcp-test" || out["docker"] != true {
		t.Errorf("server_info = %v", out)
	}
	if str(out["radarr_mcp_version"]) == "" {
		t.Errorf("server_info does not say which radarr-mcp build answered: %v", out)
	}

	var withFiles, missing, unmonitored int
	for _, f := range fixtures() {
		switch {
		case f.Quality != "":
			withFiles++
		case f.Monitored:
			missing++
		}
		if !f.Monitored {
			unmonitored++
		}
	}
	for field, want := range map[string]int{"movies": len(fixtures()), "with_files": withFiles, "missing_monitored": missing, "unmonitored": unmonitored} {
		if got := num(t, out[field], field); got != want {
			t.Errorf("%s = %d, want %d", field, got, want)
		}
	}
	if num0(out["size_on_disk"]) <= 0 {
		t.Errorf("size_on_disk = %v, want the fixture files' total", out["size_on_disk"])
	}

	roots := rows(t, out["root_folders"], "root_folders")
	var paths []string
	for _, r := range roots {
		paths = append(paths, str(r["path"]))
		if r["accessible"] != true || num0(r["free_space"]) <= 0 {
			t.Errorf("root folder %v", r)
		}
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{messyRoot, moviesRoot}) {
		t.Errorf("root folders = %v", paths)
	}

	// the health line is server_health's count, and names its errors
	health := rows(t, call(t, "server_health", nil)["checks"], "checks")
	if got := num(t, out["health_problems"], "health_problems"); got != len(health) {
		t.Errorf("health_problems = %d, server_health lists %d", got, len(health))
	}
	var errs []string
	for _, h := range health {
		if str(h["type"]) == "error" {
			errs = append(errs, str(h["source"])+": "+str(h["message"]))
		}
	}
	if got := strs(t, out["health_errors"], "health_errors"); !slices.Equal(got, errs) {
		t.Errorf("health_errors = %v, server_health's errors are %v", got, errs)
	}
}

// server_health lists the failing checks, errors first. With the harness's
// indexer and download client in place, none of the checks that say Radarr
// cannot grab anything is failing - which is also the proof that the
// suite's download plumbing is wired up.
func TestServerHealth(t *testing.T) {
	checks := rows(t, call(t, "server_health", nil)["checks"], "checks")
	rank := map[string]int{"error": 3, "warning": 2, "notice": 1}
	last := 4
	for _, c := range checks {
		level := str(c["type"])
		if rank[level] == 0 || str(c["source"]) == "" || str(c["message"]) == "" {
			t.Errorf("check = %v", c)
		}
		if rank[level] > last {
			t.Errorf("checks are not errors first: %v", checks)
		}
		last = rank[level]
		switch str(c["source"]) {
		case "IndexerRssCheck", "IndexerSearchCheck", "DownloadClientCheck", "RootFolderCheck", "IndexerStatusCheck":
			t.Errorf("the harness's plumbing is failing a health check: %v", c)
		}
		if str(c["wiki_url"]) != "" && !strings.HasPrefix(str(c["wiki_url"]), "https://") {
			t.Errorf("wiki_url = %v", c["wiki_url"])
		}
	}
}

// server_disk_space reports the disks Radarr sees, with a percentage that is
// what the free and total spaces say.
func TestServerDiskSpace(t *testing.T) {
	disks := rows(t, call(t, "server_disk_space", nil)["disks"], "disks")
	if len(disks) == 0 {
		t.Fatal("no disks")
	}
	for _, d := range disks {
		free, total := num0(d["free_space"]), num0(d["total_space"])
		if str(d["path"]) == "" || total <= 0 || free < 0 || free > total {
			t.Errorf("disk = %v", d)
			continue
		}
		want := float64(int64(free)*1000/int64(total)) / 10
		if got := num0(d["percent_free"]); got != want {
			t.Errorf("%v percent_free = %v, want %v", d["path"], got, want)
		}
	}
}

// server_logs filters by level, a level and everything worse, newest first,
// and pages.
func TestServerLogs(t *testing.T) {
	totals := map[string]int{}
	severity := map[string]int{"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "fatal": 5}
	for _, level := range []string{"info", "warn", "error"} {
		out := call(t, "server_logs", map[string]any{"level": level, "limit": 20})
		totals[level] = num(t, out["total"], "total")
		entries := rows(t, out["entries"], "entries")
		if len(entries) > 20 || len(entries) > totals[level] {
			t.Errorf("%s: %d entries of %d, limit 20", level, len(entries), totals[level])
		}
		for i, e := range entries {
			got := strings.ToLower(str(e["level"]))
			if sev, ok := severity[got]; !ok || sev < severity[level] {
				t.Errorf("%s asked for, an entry at %q: %v", level, e["level"], e)
			}
			if str(e["time"]) == "" || str(e["logger"]) == "" || str(e["message"]) == "" {
				t.Errorf("entry lacks a field: %v", e)
			}
			if i > 0 && str(e["time"]) > str(entries[i-1]["time"]) {
				t.Errorf("%s entries are not newest first", level)
			}
			if lines := strings.Count(str(e["exception"]), "\n"); lines > 5 {
				t.Errorf("an exception of %d lines was not cut to its first five", lines+1)
			}
		}
	}
	// Radarr keeps info and worse in its database, so a harder level is a
	// subset of a softer one; the setup alone logged hundreds of lines
	if !(totals["info"] >= totals["warn"] && totals["warn"] >= totals["error"]) || totals["info"] < 20 {
		t.Errorf("totals by level = %v", totals)
	}

	first := rows(t, call(t, "server_logs", map[string]any{"limit": 2})["entries"], "entries")
	second := rows(t, call(t, "server_logs", map[string]any{"limit": 2, "page": 2})["entries"], "entries")
	if len(first) != 2 || len(second) != 2 || str(second[0]["time"]) > str(first[1]["time"]) {
		t.Errorf("page 2 is not the two entries after page 1: %v then %v", first, second)
	}

	if msg := callErr(t, "server_logs", map[string]any{"level": "verbose"}); !strings.Contains(msg, "want one of trace, debug, info, warn, error, fatal") {
		t.Errorf("an unknown level = %s", msg)
	}
}

// server_log_file lists Radarr's log files and reads the end of one; a name
// it does not have is an error listing the ones it does.
func TestServerLogFile(t *testing.T) {
	files := rows(t, call(t, "server_log_file", nil)["files"], "files")
	var names []string
	for _, f := range files {
		names = append(names, str(f["name"]))
		if str(f["last_write_time"]) == "" {
			t.Errorf("log file %v has no last write time", f)
		}
	}
	if !slices.Contains(names, "radarr.txt") {
		t.Fatalf("log files = %v, want radarr.txt among them", names)
	}

	out := call(t, "server_log_file", map[string]any{"name": "radarr.txt", "tail": 5})
	lines := strs(t, out["lines"], "lines")
	if str(out["name"]) != "radarr.txt" || len(lines) == 0 || len(lines) > 5 {
		t.Fatalf("the tail of radarr.txt = %v", out)
	}
	// Radarr's own line format: date time|Level|Logger|Message
	logLine := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} [\d:.]+\|(Trace|Debug|Info|Warn|Error|Fatal)\|`)
	if !slices.ContainsFunc(lines, logLine.MatchString) {
		t.Errorf("no log line in the tail: %q", lines)
	}
	// a longer tail is a longer read of the same file, ending in the same place
	if more := strs(t, call(t, "server_log_file", map[string]any{"name": "radarr.txt", "tail": 200})["lines"], "lines"); len(more) <= len(lines) {
		t.Errorf("tail 200 read %d lines, tail 5 read %d", len(more), len(lines))
	}

	msg := callErr(t, "server_log_file", map[string]any{"name": "nope.txt"})
	if !strings.Contains(msg, `no log file "nope.txt"`) || !strings.Contains(msg, "radarr.txt") {
		t.Errorf("an unknown log file = %s", msg)
	}
}

// server_backup_create takes a backup and answers with it; server_backups
// then lists it first. The backup is deleted afterwards, so the container is
// left as it was.
func TestServerBackups(t *testing.T) {
	before := rows(t, call(t, "server_backups", nil)["backups"], "backups")

	out := call(t, "server_backup_create", nil)
	cmd := object(t, out["command"], "command")
	backup := object(t, out["backup"], "backup")
	id := num(t, backup["id"], "id")
	t.Cleanup(func() {
		if _, err := sdk.DeleteSystemBackupById(ctx, id); err != nil {
			t.Errorf("deleting backup %d: %v", id, err)
		}
	})
	if str(cmd["name"]) != "Backup" || str(cmd["status"]) != "completed" {
		t.Errorf("command = %v", cmd)
	}
	if str(backup["type"]) != "manual" || !strings.HasSuffix(str(backup["name"]), ".zip") || num0(backup["size"]) <= 0 || str(backup["time"]) == "" {
		t.Errorf("backup = %v", backup)
	}

	after := rows(t, call(t, "server_backups", nil)["backups"], "backups")
	if len(after) != len(before)+1 || num(t, after[0]["id"], "id") != id {
		t.Fatalf("backups after = %v, want the new one first of %d", after, len(before)+1)
	}
	for i := 1; i < len(after); i++ {
		if str(after[i]["time"]) > str(after[i-1]["time"]) {
			t.Errorf("backups are not newest first: %v", after)
		}
	}
}

// task_list shows Radarr's scheduled tasks by name, with the name task_run
// takes, and the recent commands newest first.
func TestTaskList(t *testing.T) {
	out := call(t, "task_list", nil)
	tasks := rows(t, out["tasks"], "tasks")
	byName := map[string]map[string]any{}
	var names []string
	for _, task := range tasks {
		byName[str(task["task_name"])] = task
		names = append(names, str(task["name"]))
		if num0(task["interval_minutes"]) <= 0 {
			t.Errorf("task %v has no interval", task)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("tasks are not sorted by name: %v", names)
	}
	// every scheduled task task_run offers is one Radarr schedules
	for _, want := range []string{"RssSync", "RefreshMonitoredDownloads", "RefreshMovie", "RefreshCollections", "ImportListSync", "CheckHealth", "Housekeeping", "CleanUpRecycleBin", "Backup"} {
		if byName[want] == nil {
			t.Errorf("no scheduled task %s among %v", want, names)
		}
	}
	if str(byName["CheckHealth"]["name"]) != "Check Health" {
		t.Errorf("CheckHealth is named %v", byName["CheckHealth"]["name"])
	}

	commands := rows(t, out["commands"], "commands")
	if len(commands) == 0 || len(commands) > 20 {
		t.Errorf("%d commands, want the seeding's, at most 20", len(commands))
	}
	for i, c := range commands {
		if str(c["name"]) == "" || str(c["status"]) == "" {
			t.Errorf("command = %v", c)
		}
		if i > 0 && num0(c["id"]) > num0(commands[i-1]["id"]) {
			t.Errorf("commands are not newest first: %v", commands)
		}
	}
}

// task_run starts a task by the name task_list shows or Radarr's own, waits
// for it by default, and returns at once when told not to; restart and the
// like are not on offer.
func TestTaskRun(t *testing.T) {
	started := time.Now().UTC().Add(-time.Second)
	out := call(t, "task_run", map[string]any{"task": "Check Health"})
	cmd := object(t, out["command"], "command")
	if str(out["task"]) != "CheckHealth" || str(out["does"]) == "" || str(cmd["name"]) != "CheckHealth" || str(cmd["status"]) != "completed" {
		t.Fatalf("task_run Check Health = %v", out)
	}
	id := num(t, cmd["id"], "id")

	// the command is task_list's newest, and the scheduled task says it ran
	list := call(t, "task_list", nil)
	var seen bool
	for _, c := range rows(t, list["commands"], "commands") {
		if num(t, c["id"], "id") == id && str(c["status"]) == "completed" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("task_list does not show command %d completed: %v", id, list["commands"])
	}
	for _, task := range rows(t, list["tasks"], "tasks") {
		if str(task["task_name"]) != "CheckHealth" {
			continue
		}
		ran, err := time.Parse(time.RFC3339, str(task["last_execution"]))
		if err != nil || ran.Before(started) {
			t.Errorf("Check Health last ran %v, want after %v", task["last_execution"], started)
		}
	}

	// wait -1 returns before the task can have finished waiting; the task
	// still runs to the end
	out = call(t, "task_run", map[string]any{"task": "refreshmonitoreddownloads", "wait": -1})
	cmd = object(t, out["command"], "command")
	if str(out["task"]) != "RefreshMonitoredDownloads" || str(cmd["status"]) == "" {
		t.Fatalf("task_run without waiting = %v", out)
	}
	id = num(t, cmd["id"], "id")
	if !eventually(func() bool {
		for _, c := range rows(t, call(t, "task_list", nil)["commands"], "commands") {
			if num(t, c["id"], "id") == id {
				return str(c["status"]) == "completed"
			}
		}
		return false
	}) {
		t.Errorf("command %d never completed", id)
	}

	msg := callErr(t, "task_run", map[string]any{"task": "Restart"})
	if !strings.Contains(msg, `task "Restart" is not one task_run starts`) || !strings.Contains(msg, "RssSync") {
		t.Errorf("task_run Restart = %s", msg)
	}
}
