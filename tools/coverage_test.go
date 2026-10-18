package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// The tools cover everything Radarr can do, and this holds them to it: every
// operation of its API is one a tool calls, or is listed here with why no
// tool does. An operation the importer adds fails this until it is given a
// tool or a reason, and a reason for one a tool now calls fails as stale.

// Why an operation has no tool of its own.
const (
	// one of a list a tool reads whole, and finds what it wants in
	oneOfAList = "one record of a list the tools read whole"
	// the same thing as another operation, done to several at once
	severalAtOnce = "the same as the call a tool makes, for several at once: the tool makes it for each"
	// what Radarr's own web interface is made of or signs in with
	webInterface = "part of Radarr's web interface - its translations or routes - and nothing a tool has a use for"
	// one section of settings by id, which is always the one there is
	settingsByID = "a settings section by id: there is one of each, which the tools read without"
)

// notWrapped are the operations no tool calls, and why.
var notWrapped = map[string]string{
	"PostCommand":     "starts any command by name; each command a tool starts has a method of its own, which names it and its fields",
	"GetAltTitle":     "a film's alternative titles, which movie_get shows from the film itself",
	"GetAltTitleById": oneOfAList,

	"GetApi":                   webInterface,
	"GetPing":                  "whether Radarr answers at all, which server_info and server_health say with more",
	"GetLocalization":          webInterface,
	"GetLocalizationLanguage":  webInterface,
	"GetSystemRoutes":          webInterface,
	"GetSystemRoutesDuplicate": webInterface,

	"GetMediaCoverByMovieIdByFilename": "the bytes of a film's artwork; movie_get gives where each image is on the web",
	"GetFeedV3CalendarRadarrIcs":       "the calendar as a feed for a calendar application, whose address carries the API key; calendar_list reads the same releases",

	"GetAutoTaggingById":       oneOfAList,
	"GetCreditById":            oneOfAList,
	"GetCustomFilterById":      oneOfAList,
	"GetCustomFormatById":      oneOfAList,
	"GetDelayProfileById":      oneOfAList,
	"GetDownloadClientById":    oneOfAList,
	"GetExclusionsById":        oneOfAList,
	"GetImportListById":        oneOfAList,
	"GetIndexerById":           oneOfAList,
	"GetLanguageById":          oneOfAList,
	"GetMetadataById":          oneOfAList,
	"GetNotificationById":      oneOfAList,
	"GetReleaseProfileById":    oneOfAList,
	"GetRemotePathMappingById": oneOfAList,
	"GetRootFolderById":        oneOfAList,
	"GetSystemTaskById":        oneOfAList,
	"GetTagById":               oneOfAList,
	"GetTagDetailById":         oneOfAList,

	"GetConfigDownloadClientById":  settingsByID,
	"GetConfigHostById":            settingsByID,
	"GetConfigImportListById":      settingsByID,
	"GetConfigIndexerById":         settingsByID,
	"GetConfigMediaManagementById": settingsByID,
	"GetConfigMetadataById":        settingsByID,
	"GetConfigNamingById":          settingsByID,
	"GetConfigUiById":              settingsByID,

	"DeleteBlocklistBulk":        severalAtOnce,
	"DeleteCustomFormatBulk":     severalAtOnce,
	"PutCustomFormatBulk":        severalAtOnce,
	"DeleteDownloadClientBulk":   severalAtOnce,
	"PutDownloadClientBulk":      severalAtOnce,
	"DeleteExclusionsBulk":       severalAtOnce,
	"PostExclusionsBulk":         severalAtOnce,
	"DeleteImportListBulk":       severalAtOnce,
	"PutImportListBulk":          severalAtOnce,
	"DeleteIndexerBulk":          severalAtOnce,
	"PutIndexerBulk":             severalAtOnce,
	"DeleteMovieEditor":          severalAtOnce,
	"DeleteMovieFileBulk":        severalAtOnce,
	"DeleteQueueBulk":            severalAtOnce,
	"PostQueueGrabBulk":          severalAtOnce,
	"PutQualityDefinitionUpdate": severalAtOnce,

	"PostMetadata":       "the metadata consumers are a fixed set Radarr comes with: metadata_edit switches one on and sets it up",
	"DeleteMetadataById": "the metadata consumers are a fixed set Radarr comes with, none of which can be taken away",

	"PostSystemBackupRestoreUpload": "restores a backup file sent with the call, which a tool cannot be handed; server_backup_restore puts back one Radarr holds",

	"GetWantedMissing":           "the films with no file, which audit_missing_files finds from the library, with what Radarr is doing about each",
	"GetWantedCutoff":            "the films below their cutoff, which audit_cutoff_unmet finds from the library",
	"GetHistorySince":            "the history from a date, which history_list reads with days",
	"GetExclusions":              "the exclusions unpaged, which Radarr has deprecated: exclusion_list reads them a page at a time",
	"PutExclusionsById":          "an exclusion is a film and nothing else: one is changed by taking it off and adding another",
	"GetMovieFile":               "a film's files, which movie_get shows with the film",
	"PutMovieFileById":           "a file's quality, languages, group and edition, which moviefile_edit sets through the editor Radarr's own interface uses",
	"PutMovieFileEditor":         "the same edit for several files at once; moviefile_edit names one film",
	"PostMovieImport":            "films added for many folders at once, which movie_add does a film at a time, each checked first by import_identify",
	"PostImportListMovie":        "films a list offers added in one go, which movie_add does a film at a time",
	"PostManualImport":           "Radarr's second look at a file once a film is chosen for it, a step of its import dialog; import_scan and import_apply ask and act in one call each",
	"PutCollectionById":          "a collection changed one at a time, which collection_edit does through the call Radarr's own interface uses",
	"GetQueueDetails":            "the queue unpaged, which queue_list reads a page at a time and whole",
	"GetQueueStatus":             "the queue's counts, which queue_list's total and each item's health say",
	"GetQualityDefinitionLimits": "the smallest and largest sizes the web interface's sliders run between",
	"GetFileSystemMediaFiles":    "the video files under a folder, which import_scan reads with what Radarr makes of each",
	"GetFileSystemType":          "whether a path is a folder or a file, which filesystem_browse shows for everything in a folder",
	"GetIndexerFlag":             "the names of the flags an indexer can mark a release with, which release_search shows on the releases that carry them",
}

// operations are the operations of Radarr's API, as the client's methods.
func operations(t *testing.T) []string {
	t.Helper()

	var ops []string
	for m := range reflect.TypeFor[radarr.Client]().Methods() {
		// a method that reads every page of a paged operation, at once or one record at a time, is that operation again
		if strings.HasSuffix(m.Name, "Complete") || strings.HasSuffix(m.Name, "Each") {
			continue
		}
		if slices.ContainsFunc([]string{"Get", "Post", "Put", "Delete"}, func(verb string) bool { return strings.HasPrefix(m.Name, verb) }) {
			ops = append(ops, m.Name)
		}
	}

	return ops
}

// toolSource is the source of every tool, without its tests.
func toolSource(t *testing.T) string {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f) //nolint:gosec // this package's own source files, found by the glob above
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
	}

	return b.String()
}

func TestEveryOperationHasAToolOrAReason(t *testing.T) {
	t.Parallel()

	ops, src := operations(t), toolSource(t)
	if len(ops) != 231 {
		t.Errorf("the client has %d operations, want Radarr's 231: this test no longer finds them all", len(ops))
	}

	called := 0
	for _, op := range ops {
		// a tool calls an operation on the client it holds, by whichever name it gave it
		used := regexp.MustCompile(`\b(?:c|client)\.` + op + `(?:Complete)?\(`).MatchString(src)
		reason, listed := notWrapped[op]
		switch {
		case used && listed:
			t.Errorf("%s is called by a tool now; take it out of notWrapped (%s)", op, reason)
		case !used && !listed:
			t.Errorf("no tool calls %s: give it a tool, or say why not in notWrapped", op)
		case used:
			called++
		}
	}
	for op := range notWrapped {
		if !slices.Contains(ops, op) {
			t.Errorf("notWrapped names %s, which is not an operation", op)
		}
	}
	if called+len(notWrapped) != len(ops) {
		t.Errorf("%d operations are called and %d have a reason, of %d", called, len(notWrapped), len(ops))
	}
	t.Logf("%d of %d operations are called by a tool; %d have a reason not to be", called, len(ops), len(notWrapped))
}
