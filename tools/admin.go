package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Running Radarr itself, beyond reading how it is: putting a backup back,
// deleting one, restarting it, shutting it down, seeing what releases there
// are to update to, cancelling a command, and looking at the folders it can
// reach. A restore, a restart and a shutdown take Radarr away from whatever
// else is using it, so each changes nothing without confirm.

// findBackup picks a backup by its name or id, and otherwise answers an error
// naming the ones there are.
func findBackup(list []backupRow, ref string) (*backupRow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("name the backup")
	}
	names := make([]string, 0, len(list))
	for i := range list {
		if strings.EqualFold(list[i].Name, ref) || strconv.Itoa(list[i].ID) == ref {
			return &list[i], nil
		}
		names = append(names, list[i].Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no backup %q: Radarr has none; server_backup_create takes one", ref)
	}

	return nil, fmt.Errorf("no backup %q (have: %s)", ref, strings.Join(names, ", "))
}

// folderEntry is a folder or a file as Radarr's own folder browser answers
// it, which its document does not give the shape of.
type folderEntry struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func registerAdminTools(r *registry) {
	c := r.client

	type backupDeleteIn struct {
		Backup string `json:"backup" jsonschema:"the backup to delete, by its name in server_backups or its id"`
		confirmIn
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "server_backup_delete",
		Description: "Delete one of Radarr's backups from its disk. Without confirm nothing changes and the answer says what would go.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backupDeleteIn) (*mcp.CallToolResult, deletedOut, error) {
		all, err := backups(ctx, c)
		if err != nil {
			return nil, deletedOut{}, err
		}
		b, err := findBackup(all, in.Backup)
		if err != nil {
			return nil, deletedOut{}, err
		}
		loses := []string{fmt.Sprintf("the file itself: %d bytes, taken %s (%s), and the way back to Radarr as it was then", b.Size, b.Time, b.Type)}
		out, err := deleting(in.Confirm, b.Name, loses, func() error {
			_, delErr := c.DeleteSystemBackupById(ctx, b.ID)
			return delErr
		})

		return nil, out, err
	})

	type backupRestoreIn struct {
		Backup  string `json:"backup"            jsonschema:"the backup to restore, by its name in server_backups or its id"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"true to restore; without it nothing changes and the answer says what a confirmed call would write over"`
	}
	type backupRestoreOut struct {
		Restored     string   `json:"restored,omitempty"      jsonschema:"the backup restored"`
		WouldRestore string   `json:"would_restore,omitempty" jsonschema:"without confirm: the backup a confirmed call restores; nothing has changed"`
		Overwrites   []string `json:"overwrites"              jsonschema:"what is written over, and cannot be had back unless a backup of it was taken first"`
		Note         string   `json:"note"                    jsonschema:"what has to happen next"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_backup_restore",
		Description: "Put a backup back: Radarr's database and settings become what they were when it was taken, and everything changed since is written over - films added, edits, profiles, indexers, download clients, tags, settings, and the history since. Nothing changes without confirm, and the answer says what would be written over. Radarr uses the restored database once it has restarted (server_restart).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backupRestoreIn) (*mcp.CallToolResult, backupRestoreOut, error) {
		all, err := backups(ctx, c)
		if err != nil {
			return nil, backupRestoreOut{}, err
		}
		b, err := findBackup(all, in.Backup)
		if err != nil {
			return nil, backupRestoreOut{}, err
		}
		out := backupRestoreOut{Overwrites: []string{
			"every film, file record, profile, custom format, indexer, download client, import list, notification, tag and setting as they are now, replaced with what they were at " + b.Time,
			"the history, the queue's record and the blocklist kept since " + b.Time,
			"Radarr's API key and address, if they have changed since: radarr-mcp then needs the ones in the backup",
		}}
		if !in.Confirm {
			out.WouldRestore = b.Name
			out.Note = "nothing has changed; server_backup_create first keeps what is there now"

			return nil, out, nil
		}

		// nothing else may write while the database is swapped out from under it
		unlock := r.locking().All()
		defer unlock()

		if _, err := c.PostSystemBackupRestoreById(ctx, b.ID); err != nil {
			return nil, backupRestoreOut{}, err
		}
		out.Restored = b.Name
		out.Note = "Radarr uses the restored database once it has restarted: server_restart"

		return nil, out, nil
	})

	type stopIn struct {
		Confirm bool `json:"confirm,omitempty" jsonschema:"true to do it; without it nothing happens and the answer says what would"`
	}
	type stopOut struct {
		Done      string   `json:"done,omitempty"  jsonschema:"what was asked of Radarr"`
		Would     string   `json:"would,omitempty" jsonschema:"without confirm: what a confirmed call does; nothing has happened"`
		Interrupt []string `json:"interrupts"      jsonschema:"what is cut off: the commands running or waiting now"`
		Note      string   `json:"note"            jsonschema:"what happens next"`
	}
	// stopping reads what a restart or a shutdown would cut off, and with confirm asks Radarr for it
	stopping := func(ctx context.Context, confirm bool, what, note string, ask func() error) (stopOut, error) {
		out := stopOut{Interrupt: []string{}, Note: note}
		commands, err := c.GetCommand(ctx)
		if err != nil {
			return stopOut{}, err
		}
		for i := range commands.Answer {
			if cmd := &commands.Answer[i]; !finished(cmd.Status) {
				out.Interrupt = append(out.Interrupt, fmt.Sprintf("%s (%s)", cmd.Name, cmd.Status))
			}
		}
		if !confirm {
			out.Would = what

			return out, nil
		}

		// nothing of this server's is left half written when Radarr goes
		unlock := r.locking().All()
		defer unlock()
		if err := ask(); err != nil {
			return stopOut{}, err
		}
		out.Done = what

		return out, nil
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_restart",
		Description: "Restart Radarr: needed after a backup is restored, and for a change to its port, address, URL base or SSL to take effect. Whatever it is doing is cut off and it is away for a few seconds; server_info answers again once it is back. Nothing happens without confirm, and the answer says what would be cut off.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopIn) (*mcp.CallToolResult, stopOut, error) {
		out, err := stopping(ctx, in.Confirm, "restart Radarr", "Radarr is away for a few seconds; server_info answers once it is back", func() error {
			_, askErr := c.PostSystemRestart(ctx)
			return askErr
		})

		return nil, out, err
	})
	add(r, writeTool, &mcp.Tool{
		Name:        "server_shutdown",
		Description: "Shut Radarr down. It stays down until someone starts it again on the machine it runs on, which no tool here can do, unless whatever runs it (Docker with a restart policy, a service manager) starts it by itself. Nothing happens without confirm, and the answer says what would be cut off. server_restart is the one to use for a change that needs a restart.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopIn) (*mcp.CallToolResult, stopOut, error) {
		out, err := stopping(ctx, in.Confirm, "shut Radarr down", "Radarr stays down until it is started again where it runs: no tool here can start it", func() error {
			_, askErr := c.PostSystemShutdown(ctx)
			return askErr
		})

		return nil, out, err
	})

	type updateRow struct {
		Version   string   `json:"version"`
		Released  string   `json:"released"`
		Installed bool     `json:"installed"`
		Latest    bool     `json:"latest"`
		New       []string `json:"new"`
		Fixed     []string `json:"fixed"`
	}
	type updatesOut struct {
		Current         string      `json:"current"`
		UpdateAvailable bool        `json:"update_available"`
		Mechanism       string      `json:"mechanism,omitempty"   jsonschema:"how this install is updated; docker means pull a newer image rather than update in place"`
		Updates         []updateRow `json:"updates"               jsonschema:"newest first"`
		Unavailable     string      `json:"unavailable,omitempty" jsonschema:"why the list is empty: Radarr could not reach its update service"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_updates",
		Description: "The Radarr releases newer than, and up to, the one installed, with what each added and fixed, and whether this install is behind. task_run with ApplicationUpdate installs the newest where Radarr updates itself; a Docker install is updated by pulling a newer image.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, updatesOut, error) {
		st, err := c.GetSystemStatus(ctx)
		if err != nil {
			return nil, updatesOut{}, err
		}
		out := updatesOut{Current: st.Answer.Version, Mechanism: string(st.Answer.PackageUpdateMechanism), Updates: []updateRow{}}
		res, err := c.GetUpdate(ctx)
		if err != nil {
			// Radarr asks its update service on every read, and answers a 500 carrying the reason when it cannot reach it
			if se, ok := errors.AsType[*client.StatusError](err); ok && se.StatusCode >= http.StatusInternalServerError {
				out.Unavailable = "Radarr could not reach its update service: " + outsideProse(strings.Join(se.Messages, "; "))
				return nil, out, nil
			}
			return nil, updatesOut{}, err
		}
		rows := make([]updateRow, 0, len(res.Answer))
		for _, u := range res.Answer {
			row := updateRow{Version: u.Version, Released: u.ReleaseDate, Installed: u.Installed, Latest: u.Latest, New: []string{}, Fixed: []string{}}
			if u.Changes != nil {
				for _, line := range u.Changes.New {
					row.New = append(row.New, outsideProse(line))
				}
				for _, line := range u.Changes.Fixed {
					row.Fixed = append(row.Fixed, outsideProse(line))
				}
			}
			if row.Latest && !row.Installed && u.Version != st.Answer.Version {
				out.UpdateAvailable = true
			}
			rows = append(rows, row)
		}
		out.Updates, out.pageOut = pageRows(rows, in)

		return nil, out, nil
	})

	type cancelIn struct {
		CommandID int `json:"command_id" jsonschema:"the command to take out of the queue, by the id the tool that started it answered, or task_list shows"`
	}
	type cancelOut struct {
		Cancelled bool       `json:"cancelled"      jsonschema:"it was taken out of the queue before it started; false when it was already running or had finished"`
		Command   commandOut `json:"command"        jsonschema:"the command as it stood when it was asked"`
		Note      string     `json:"note,omitempty" jsonschema:"why it was not cancelled"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "task_cancel",
		Description: "Take a command out of Radarr's queue before it starts - a search of everything missing that is waiting behind a refresh of every film - by its id. Radarr only cancels one that is still waiting its turn: one already running runs to its end, and one that has finished is left alone. The answer says which it was.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cancelIn) (*mcp.CallToolResult, cancelOut, error) {
		if in.CommandID == 0 {
			return nil, cancelOut{}, errors.New("give the command_id of the command to cancel: the tool that started it answered it, and task_list shows the ones waiting")
		}
		one, err := c.GetCommandById(ctx, in.CommandID)
		if err != nil {
			return nil, cancelOut{}, fmt.Errorf("command %d: %w", in.CommandID, err)
		}
		out := cancelOut{Command: commandOf(one.Answer)}
		const running = "it is already running, and Radarr only cancels a command that is still waiting its turn: this one runs to its end"
		switch {
		case finished(one.Answer.Status):
			out.Note = "it had already finished, so there was nothing to cancel"

			return nil, out, nil
		case one.Answer.Status == radarr.CommandStatusStarted:
			out.Note = running

			return nil, out, nil
		}

		if _, err := c.DeleteCommandById(ctx, in.CommandID); err != nil {
			// it started between being read and being asked for: Radarr answers that it cannot cancel it
			if client.StatusCode(err) == http.StatusConflict {
				out.Note = running

				return nil, out, nil
			}

			return nil, out, err
		}
		out.Cancelled = true

		return nil, out, nil
	})

	type browseIn struct {
		Path  string `json:"path,omitempty"  jsonschema:"the folder to look in, as Radarr sees it (inside its container, when it runs in one); default the top, which lists the drives or the root's folders"`
		Files bool   `json:"files,omitempty" jsonschema:"list the files in it as well as the folders"`
		pageIn
	}
	type browseRow struct {
		Name string `json:"name"`
		Path string `json:"path"           jsonschema:"what to pass back as path to look inside, or to the tool that wants a folder"`
		Type string `json:"type"           jsonschema:"folder, file or drive"`
		Size int64  `json:"size,omitempty" jsonschema:"bytes, for a file"`
	}
	type browseOut struct {
		Path    string      `json:"path"`
		Parent  string      `json:"parent,omitempty" jsonschema:"the folder above, to pass back as path"`
		Entries []browseRow `json:"entries"          jsonschema:"folders first, then files, each by name"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "filesystem_browse",
		Description: "Look at the folders, and with files the files, in a folder on the machine Radarr runs on, as Radarr sees them: for finding the folder to add as a root folder, to scan for an import, or to point a film at. It reads names and sizes only, never what is in a file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in browseIn) (*mcp.CallToolResult, browseOut, error) {
		res, err := c.GetFileSystem(ctx, radarr.GetFileSystemOptions{Path: in.Path, IncludeFiles: new(in.Files), AllowFoldersWithoutTrailingSlashes: new(true)})
		if err != nil {
			return nil, browseOut{}, err
		}
		// the folder browser has no declared shape: it answers the folders and files of one folder, and the one above
		var body struct {
			Parent      string        `json:"parent"`
			Directories []folderEntry `json:"directories"`
			Files       []folderEntry `json:"files"`
		}
		if err := json.Unmarshal(res.Answer, &body); err != nil {
			return nil, browseOut{}, fmt.Errorf("reading %s: %w", in.Path, err)
		}
		rows := make([]browseRow, 0, len(body.Directories)+len(body.Files))
		for _, e := range slices.Concat(body.Directories, body.Files) {
			// a name on disk is whatever someone called it: it is shown made safe, and the path beside it is left as it is, since that is what is handed back
			rows = append(rows, browseRow{Name: outsideName(e.Name), Path: e.Path, Type: e.Type, Size: e.Size})
		}

		out := browseOut{Path: in.Path, Parent: body.Parent}
		out.Entries, out.pageOut = pageRows(rows, in.pageIn)

		return nil, out, nil
	})
}
