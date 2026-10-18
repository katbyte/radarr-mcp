//go:build acceptance

package radarr_test

import (
	"slices"
	"testing"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// TestBackups makes a backup and deletes it. Restoring one is left alone
// (see notExercised): it replaces the database the suite is using.
//
//nolint:paralleltest // the tests share one server
func TestBackups(t *testing.T) {
	ctx := skipUnlessUp(t)

	before := must(sdk.GetSystemBackup(ctx)).Answer
	completed(t, must(sdk.BackupAndWait(ctx, radarr.BackupCommand{}, commandWait)))
	after := must(sdk.GetSystemBackup(ctx)).Answer
	var made *radarr.BackupResource
	for _, b := range after {
		if b.Type == radarr.BackupTypeManual && !slices.ContainsFunc(before, func(o radarr.BackupResource) bool { return o.Id == b.Id }) {
			made = &b
		}
	}
	if made == nil {
		t.Fatalf("the backup command made no backup: %d before, %d after", len(before), len(after))
	}

	must(sdk.DeleteSystemBackupById(ctx, made.Id))
	if slices.ContainsFunc(must(sdk.GetSystemBackup(ctx)).Answer, func(b radarr.BackupResource) bool { return b.Id == made.Id }) {
		t.Errorf("%s is still listed after deleting it", made.Name)
	}
}
