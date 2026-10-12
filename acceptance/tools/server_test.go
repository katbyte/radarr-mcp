//go:build acceptance

package tools_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// server_info summarises the library the harness built: its counts are the
// fixtures', and its health line agrees with server_health. It also says
// which build of radarr-mcp answered, since a session keeps the binary it
// started with.
func TestServerInfo(t *testing.T) {
	out := suite.Call(t, "server_info", nil)
	if acc.Str(out["app"]) != "Radarr" || acc.Str(out["version"]) == "" || acc.Str(out["instance_name"]) != "radarr-mcp-test" || out["docker"] != true {
		t.Errorf("server_info = %v", out)
	}
	if acc.Str(out["radarr_mcp_version"]) == "" {
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
		if got := acc.Num(t, out[field], field); got != want {
			t.Errorf("%s = %d, want %d", field, got, want)
		}
	}
	if acc.NumOr0(out["size_on_disk"]) <= 0 {
		t.Errorf("size_on_disk = %v, want the fixture files' total", out["size_on_disk"])
	}

	roots := acc.Rows(t, out["root_folders"], "root_folders")
	var paths []string
	for _, r := range roots {
		paths = append(paths, acc.Str(r["path"]))
		if r["accessible"] != true || acc.NumOr0(r["free_space"]) <= 0 {
			t.Errorf("root folder %v", r)
		}
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{messyRoot, moviesRoot}) {
		t.Errorf("root folders = %v", paths)
	}

	// the health line is server_health's count, and names its errors
	health := acc.Rows(t, suite.Call(t, "server_health", nil)["checks"], "checks")
	if got := acc.Num(t, out["health_problems"], "health_problems"); got != len(health) {
		t.Errorf("health_problems = %d, server_health lists %d", got, len(health))
	}
	var errs []string
	for _, h := range health {
		if acc.Str(h["type"]) == "error" {
			errs = append(errs, acc.Str(h["source"])+": "+acc.Str(h["message"]))
		}
	}
	if got := acc.Strs(t, acc.OrEmptyList(out["health_errors"]), "health_errors"); !slices.Equal(got, errs) {
		t.Errorf("health_errors = %v, server_health's errors are %v", got, errs)
	}
}

// server_health lists the failing checks, errors first. With the harness's
// indexer and download client in place, none of the checks that say Radarr
// cannot grab anything is failing - which is also the proof that the
// suite's download plumbing is wired up.
func TestServerHealth(t *testing.T) {
	checks := acc.Rows(t, suite.Call(t, "server_health", nil)["checks"], "checks")
	rank := map[string]int{"error": 3, "warning": 2, "notice": 1}
	last := 4
	for _, c := range checks {
		level := acc.Str(c["type"])
		if rank[level] == 0 || acc.Str(c["source"]) == "" || acc.Str(c["message"]) == "" {
			t.Errorf("check = %v", c)
		}
		if rank[level] > last {
			t.Errorf("checks are not errors first: %v", checks)
		}
		last = rank[level]
		switch acc.Str(c["source"]) {
		case "IndexerRssCheck", "IndexerSearchCheck", "DownloadClientCheck", "RootFolderCheck", "IndexerStatusCheck":
			t.Errorf("the harness's plumbing is failing a health check: %v", c)
		}
		if acc.Str(c["wiki_url"]) != "" && !strings.HasPrefix(acc.Str(c["wiki_url"]), "https://") {
			t.Errorf("wiki_url = %v", c["wiki_url"])
		}
	}
}

// server_disk_space reports the disks Radarr sees, with a percentage that is
// what the free and total spaces say.
func TestServerDiskSpace(t *testing.T) {
	disks := acc.Rows(t, suite.Call(t, "server_disk_space", nil)["disks"], "disks")
	if len(disks) == 0 {
		t.Fatal("no disks")
	}
	for _, d := range disks {
		free, total := acc.NumOr0(d["free_space"]), acc.NumOr0(d["total_space"])
		if acc.Str(d["path"]) == "" || total <= 0 || free < 0 || free > total {
			t.Errorf("disk = %v", d)
			continue
		}
		want := float64(int64(free)*1000/int64(total)) / 10
		if got := acc.DecimalOr0(d["percent_free"]); got != want {
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
		out := suite.Call(t, "server_logs", map[string]any{"level": level, "limit": 20})
		totals[level] = acc.Num(t, out["total"], "total")
		entries := acc.Rows(t, out["entries"], "entries")
		if len(entries) > 20 || len(entries) > totals[level] {
			t.Errorf("%s: %d entries of %d, limit 20", level, len(entries), totals[level])
		}
		for i, e := range entries {
			got := strings.ToLower(acc.Str(e["level"]))
			if sev, ok := severity[got]; !ok || sev < severity[level] {
				t.Errorf("%s asked for, an entry at %q: %v", level, e["level"], e)
			}
			if acc.Str(e["time"]) == "" || acc.Str(e["logger"]) == "" || acc.Str(e["message"]) == "" {
				t.Errorf("entry lacks a field: %v", e)
			}
			if i > 0 && acc.Str(e["time"]) > acc.Str(entries[i-1]["time"]) {
				t.Errorf("%s entries are not newest first", level)
			}
			if lines := strings.Count(acc.Str(e["exception"]), "\n"); lines > 5 {
				t.Errorf("an exception of %d lines was not cut to its first five", lines+1)
			}
		}
	}
	// Radarr keeps info and worse in its database, so a harder level is a
	// subset of a softer one; the setup alone logged hundreds of lines
	if !(totals["info"] >= totals["warn"] && totals["warn"] >= totals["error"]) || totals["info"] < 20 {
		t.Errorf("totals by level = %v", totals)
	}

	first := acc.Rows(t, suite.Call(t, "server_logs", map[string]any{"limit": 2})["entries"], "entries")
	second := acc.Rows(t, suite.Call(t, "server_logs", map[string]any{"limit": 2, "offset": 2})["entries"], "entries")
	if len(first) != 2 || len(second) != 2 || acc.Str(second[0]["time"]) > acc.Str(first[1]["time"]) {
		t.Errorf("the second two are not the two entries after the first two: %v then %v", first, second)
	}

	if msg := suite.CallErr(t, "server_logs", map[string]any{"level": "verbose"}); !strings.Contains(msg, "want one of trace, debug, info, warn, error, fatal") {
		t.Errorf("an unknown level = %s", msg)
	}
}

// server_log_file lists Radarr's log files and reads the end of one; a name
// it does not have is an error listing the ones it does.
func TestServerLogFile(t *testing.T) {
	files := acc.Rows(t, suite.Call(t, "server_log_file", nil)["files"], "files")
	var names []string
	for _, f := range files {
		names = append(names, acc.Str(f["name"]))
		if acc.Str(f["last_write_time"]) == "" {
			t.Errorf("log file %v has no last write time", f)
		}
	}
	if !slices.Contains(names, "radarr.txt") {
		t.Fatalf("log files = %v, want radarr.txt among them", names)
	}

	out := suite.Call(t, "server_log_file", map[string]any{"name": "radarr.txt", "tail": 5})
	lines := acc.Strs(t, acc.OrEmptyList(out["lines"]), "lines")
	if acc.Str(out["name"]) != "radarr.txt" || len(lines) == 0 || len(lines) > 5 {
		t.Fatalf("the tail of radarr.txt = %v", out)
	}
	// Radarr's own line format: date time|Level|Logger|Message
	logLine := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} [\d:.]+\|(Trace|Debug|Info|Warn|Error|Fatal)\|`)
	if !slices.ContainsFunc(lines, logLine.MatchString) {
		t.Errorf("no log line in the tail: %q", lines)
	}
	// a longer tail is a longer read of the same file, ending in the same place
	if more := acc.Strs(t, acc.OrEmptyList(suite.Call(t, "server_log_file", map[string]any{"name": "radarr.txt", "tail": 200})["lines"]), "lines"); len(more) <= len(lines) {
		t.Errorf("tail 200 read %d lines, tail 5 read %d", len(more), len(lines))
	}

	msg := suite.CallErr(t, "server_log_file", map[string]any{"name": "nope.txt"})
	if !strings.Contains(msg, `no log file "nope.txt"`) || !strings.Contains(msg, "radarr.txt") {
		t.Errorf("an unknown log file = %s", msg)
	}
}

// server_backup_create takes a backup and answers with it; server_backups
// then lists it first. The backup is deleted afterwards, so the container is
// left as it was.
func TestServerBackups(t *testing.T) {
	before := acc.Rows(t, suite.Call(t, "server_backups", nil)["backups"], "backups")

	out := suite.Call(t, "server_backup_create", nil)
	cmd := acc.Object(t, out["command"], "command")
	backup := acc.Object(t, out["backup"], "backup")
	id := acc.Num(t, backup["id"], "id")
	t.Cleanup(func() {
		if _, err := sdk.DeleteSystemBackupById(ctx, id); err != nil {
			t.Errorf("deleting backup %d: %v", id, err)
		}
	})
	if acc.Str(cmd["name"]) != "Backup" || acc.Str(cmd["status"]) != "completed" {
		t.Errorf("command = %v", cmd)
	}
	if acc.Str(backup["type"]) != "manual" || !strings.HasSuffix(acc.Str(backup["name"]), ".zip") || acc.NumOr0(backup["size"]) <= 0 || acc.Str(backup["time"]) == "" {
		t.Errorf("backup = %v", backup)
	}

	after := acc.Rows(t, suite.Call(t, "server_backups", nil)["backups"], "backups")
	if len(after) != len(before)+1 || acc.Num(t, after[0]["id"], "id") != id {
		t.Fatalf("backups after = %v, want the new one first of %d", after, len(before)+1)
	}
	for i := 1; i < len(after); i++ {
		if acc.Str(after[i]["time"]) > acc.Str(after[i-1]["time"]) {
			t.Errorf("backups are not newest first: %v", after)
		}
	}
}

// task_list shows Radarr's scheduled tasks by name, with the name task_run
// takes, and the recent commands newest first.
func TestTaskList(t *testing.T) {
	out := suite.Call(t, "task_list", nil)
	tasks := acc.Rows(t, out["tasks"], "tasks")
	byName := map[string]map[string]any{}
	var names []string
	for _, task := range tasks {
		byName[acc.Str(task["task_name"])] = task
		names = append(names, acc.Str(task["name"]))
		if acc.NumOr0(task["interval_minutes"]) <= 0 {
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
	if acc.Str(byName["CheckHealth"]["name"]) != "Check Health" {
		t.Errorf("CheckHealth is named %v", byName["CheckHealth"]["name"])
	}

	commands := acc.Rows(t, out["commands"], "commands")
	if len(commands) == 0 || len(commands) > 20 {
		t.Errorf("%d commands, want the seeding's, at most 20", len(commands))
	}
	for i, c := range commands {
		if acc.Str(c["name"]) == "" || acc.Str(c["status"]) == "" {
			t.Errorf("command = %v", c)
		}
		if i > 0 && acc.NumOr0(c["id"]) > acc.NumOr0(commands[i-1]["id"]) {
			t.Errorf("commands are not newest first: %v", commands)
		}
	}
}

// task_run starts a task by the name task_list shows or Radarr's own, waits
// for it by default, and returns at once when told not to; restart and the
// like are not on offer.
func TestTaskRun(t *testing.T) {
	started := time.Now().UTC().Add(-time.Second)
	out := suite.Call(t, "task_run", map[string]any{"task": "Check Health"})
	cmd := acc.Object(t, out["command"], "command")
	if acc.Str(out["task"]) != "CheckHealth" || acc.Str(out["does"]) == "" || acc.Str(cmd["name"]) != "CheckHealth" || acc.Str(cmd["status"]) != "completed" {
		t.Fatalf("task_run Check Health = %v", out)
	}
	id := acc.Num(t, cmd["id"], "id")

	// the command is task_list's newest, and the scheduled task says it ran
	list := suite.Call(t, "task_list", nil)
	var seen bool
	for _, c := range acc.Rows(t, list["commands"], "commands") {
		if acc.Num(t, c["id"], "id") == id && acc.Str(c["status"]) == "completed" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("task_list does not show command %d completed: %v", id, list["commands"])
	}
	// and it is followed by its id, alone, however many Radarr has started for itself since
	if one := acc.Rows(t, suite.Call(t, "task_list", map[string]any{"command_id": id})["commands"], "commands"); len(one) != 1 || acc.Num(t, one[0]["id"], "id") != id || acc.Str(one[0]["name"]) != "CheckHealth" || acc.Str(one[0]["status"]) != "completed" {
		t.Errorf("task_list for command %d = %v, want that command alone", id, one)
	}
	if msg := suite.CallErr(t, "task_list", map[string]any{"command_id": 987654}); !strings.Contains(msg, "command 987654") {
		t.Errorf("a command that does not exist = %q", msg)
	}
	for _, task := range acc.Rows(t, list["tasks"], "tasks") {
		if acc.Str(task["task_name"]) != "CheckHealth" {
			continue
		}
		ran, err := time.Parse(time.RFC3339, acc.Str(task["last_execution"]))
		if err != nil || ran.Before(started) {
			t.Errorf("Check Health last ran %v, want after %v", task["last_execution"], started)
		}
	}

	// wait -1 returns before the task can have finished waiting; the task
	// still runs to the end
	out = suite.Call(t, "task_run", map[string]any{"task": "refreshmonitoreddownloads", "wait": -1})
	cmd = acc.Object(t, out["command"], "command")
	if acc.Str(out["task"]) != "RefreshMonitoredDownloads" || acc.Str(cmd["status"]) == "" {
		t.Fatalf("task_run without waiting = %v", out)
	}
	id = acc.Num(t, cmd["id"], "id")
	if !acc.EventuallyWithin(time.Minute, func() bool {
		for _, c := range acc.Rows(t, suite.Call(t, "task_list", nil)["commands"], "commands") {
			if acc.Num(t, c["id"], "id") == id {
				return acc.Str(c["status"]) == "completed"
			}
		}
		return false
	}) {
		t.Errorf("command %d never completed", id)
	}

	msg := suite.CallErr(t, "task_run", map[string]any{"task": "Restart"})
	if !strings.Contains(msg, `task "Restart" is not one task_run starts`) || !strings.Contains(msg, "RssSync") {
		t.Errorf("task_run Restart = %s", msg)
	}
}
