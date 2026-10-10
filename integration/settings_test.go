//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// TestSettings changes one thing in each group of settings, checks the
// answer and a fresh read both carry it, and puts it back.
//
//nolint:paralleltest // the tests share one server
func TestSettings(t *testing.T) {
	ctx := skipUnlessUp(t)

	t.Run("host", func(t *testing.T) {
		host := must(sdk.GetConfigHost(ctx)).Model
		id := host.Id

		// Radarr 6 will not take back the host settings it starts with:
		// with authentication off for local addresses it wants the hosts
		// it may be reached by, and it starts with none
		refused := *host
		refused.AuthenticationRequired = radarr.AuthenticationRequiredTypeDisabledForLocalAddresses
		refused.AllowedHosts = ""
		_, err := sdk.PutConfigHostById(ctx, id, refused)
		if client.StatusCode(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "Allowed Hosts is required") {
			t.Fatalf("host settings without allowed hosts: want 400 saying Allowed Hosts is required, got %v", err)
		}

		// with authentication required everywhere it takes none; the
		// container's External authentication never asks either way
		edit := *host
		edit.AuthenticationRequired = radarr.AuthenticationRequiredTypeEnabled
		edit.AllowedHosts = ""
		edit.BackupRetention = host.BackupRetention + 1
		checkSetting(t, "backup retention", edit.BackupRetention,
			func() int { return must(sdk.PutConfigHostById(ctx, id, edit)).Model.BackupRetention },
			func() int { return must(sdk.GetConfigHost(ctx)).Model.BackupRetention })
		edit.BackupRetention = host.BackupRetention
		must(sdk.PutConfigHostById(context.WithoutCancel(ctx), id, edit))
	})

	t.Run("ui", func(t *testing.T) {
		before := must(sdk.GetConfigUi(ctx)).Model
		edit := *before
		edit.ShowRelativeDates = new(!isTrue(before.ShowRelativeDates))
		checkSetting(t, "show relative dates", isTrue(edit.ShowRelativeDates),
			func() bool {
				return isTrue(must(sdk.PutConfigUiById(ctx, before.Id, edit)).Model.ShowRelativeDates)
			},
			func() bool { return isTrue(must(sdk.GetConfigUi(ctx)).Model.ShowRelativeDates) })
		must(sdk.PutConfigUiById(context.WithoutCancel(ctx), before.Id, *before))
	})

	t.Run("naming", func(t *testing.T) {
		before := must(sdk.GetConfigNaming(ctx)).Model
		edit := *before
		edit.ReplaceIllegalCharacters = new(!isTrue(before.ReplaceIllegalCharacters))
		checkSetting(t, "replace illegal characters", isTrue(edit.ReplaceIllegalCharacters),
			func() bool {
				return isTrue(must(sdk.PutConfigNamingById(ctx, before.Id, edit)).Model.ReplaceIllegalCharacters)
			},
			func() bool { return isTrue(must(sdk.GetConfigNaming(ctx)).Model.ReplaceIllegalCharacters) })
		must(sdk.PutConfigNamingById(context.WithoutCancel(ctx), before.Id, *before))

		// with renaming on, the preview has the file that is not named for its film: Dune (1984) still carries the name of the film its folder was taken for
		d := film(ctx, t, dune1984)
		renaming := *before
		renaming.RenameMovies = new(true)
		must(sdk.PutConfigNamingById(ctx, before.Id, renaming))
		defer func() { must(sdk.PutConfigNamingById(context.WithoutCancel(ctx), before.Id, *before)) }()
		preview := must(sdk.GetRename(ctx, radarr.GetRenameOperationOptions{MovieId: []int{d.Id}})).Model
		if len(preview) != 1 || preview[0].MovieId != d.Id || !strings.Contains(preview[0].ExistingPath, "Dune (2021)") || preview[0].NewPath == "" || preview[0].NewPath == preview[0].ExistingPath {
			t.Errorf("the rename preview of %s with renaming on = %+v, want its one file under a new name", dune1984.Title, preview)
		}
	})

	t.Run("media management", func(t *testing.T) {
		before := must(sdk.GetConfigMediaManagement(ctx)).Model
		edit := *before
		edit.RecycleBinCleanupDays = before.RecycleBinCleanupDays + 1
		checkSetting(t, "recycle bin cleanup days", edit.RecycleBinCleanupDays,
			func() int {
				return must(sdk.PutConfigMediaManagementById(ctx, before.Id, edit)).Model.RecycleBinCleanupDays
			},
			func() int { return must(sdk.GetConfigMediaManagement(ctx)).Model.RecycleBinCleanupDays })
		must(sdk.PutConfigMediaManagementById(context.WithoutCancel(ctx), before.Id, *before))
	})

	t.Run("indexers", func(t *testing.T) {
		before := must(sdk.GetConfigIndexer(ctx)).Model
		edit := *before
		edit.AllowHardcodedSubs = new(!isTrue(before.AllowHardcodedSubs))
		checkSetting(t, "allow hardcoded subs", isTrue(edit.AllowHardcodedSubs),
			func() bool {
				return isTrue(must(sdk.PutConfigIndexerById(ctx, before.Id, edit)).Model.AllowHardcodedSubs)
			},
			func() bool { return isTrue(must(sdk.GetConfigIndexer(ctx)).Model.AllowHardcodedSubs) })
		must(sdk.PutConfigIndexerById(context.WithoutCancel(ctx), before.Id, *before))
	})

	t.Run("download clients", func(t *testing.T) {
		before := must(sdk.GetConfigDownloadClient(ctx)).Model
		edit := *before
		edit.AutoRedownloadFailedFromInteractiveSearch = new(!isTrue(before.AutoRedownloadFailedFromInteractiveSearch))
		checkSetting(t, "redownload failed interactive grabs", isTrue(edit.AutoRedownloadFailedFromInteractiveSearch),
			func() bool {
				return isTrue(must(sdk.PutConfigDownloadClientById(ctx, before.Id, edit)).Model.AutoRedownloadFailedFromInteractiveSearch)
			},
			func() bool {
				return isTrue(must(sdk.GetConfigDownloadClient(ctx)).Model.AutoRedownloadFailedFromInteractiveSearch)
			})
		must(sdk.PutConfigDownloadClientById(context.WithoutCancel(ctx), before.Id, *before))
	})

	t.Run("import lists", func(t *testing.T) {
		before := must(sdk.GetConfigImportList(ctx)).Model
		edit := *before
		edit.ListSyncLevel = "logOnly"
		if before.ListSyncLevel == edit.ListSyncLevel {
			edit.ListSyncLevel = "disabled"
		}
		checkSetting(t, "list sync level", edit.ListSyncLevel,
			func() string {
				return must(sdk.PutConfigImportListById(ctx, before.Id, edit)).Model.ListSyncLevel
			},
			func() string { return must(sdk.GetConfigImportList(ctx)).Model.ListSyncLevel })
		must(sdk.PutConfigImportListById(context.WithoutCancel(ctx), before.Id, *before))
	})

	t.Run("metadata", func(t *testing.T) {
		before := must(sdk.GetConfigMetadata(ctx)).Model
		edit := *before
		edit.CertificationCountry = radarr.TMDbCountryCodeGb
		if before.CertificationCountry == edit.CertificationCountry {
			edit.CertificationCountry = radarr.TMDbCountryCodeUs
		}
		checkSetting(t, "certification country", edit.CertificationCountry,
			func() radarr.TMDbCountryCode {
				return must(sdk.PutConfigMetadataById(ctx, before.Id, edit)).Model.CertificationCountry
			},
			func() radarr.TMDbCountryCode { return must(sdk.GetConfigMetadata(ctx)).Model.CertificationCountry })
		must(sdk.PutConfigMetadataById(context.WithoutCancel(ctx), before.Id, *before))
	})
}

// checkSetting saves a change, and checks the answer and a fresh read both
// have the value it set.
func checkSetting[T comparable](t *testing.T, name string, want T, put, get func() T) {
	t.Helper()

	if got := put(); got != want {
		t.Errorf("%s: the answer has %v, want %v", name, got, want)
	}
	if got := get(); got != want {
		t.Errorf("%s: a fresh read has %v, want %v", name, got, want)
	}
}
