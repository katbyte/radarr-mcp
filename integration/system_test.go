//go:build integration

package integration

import (
	"bytes"
	"mime/multipart"
	"slices"
	"testing"

	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// TestBackups makes a backup and deletes it. Restoring one is left alone
// (see notExercised): it replaces the database the suite is using.
//
//nolint:paralleltest // the tests share one server
func TestBackups(t *testing.T) {
	ctx := skipUnlessUp(t)

	before := must(sdk.GetSystemBackup(ctx)).Model
	command(ctx, t, `{"name":"Backup"}`)
	after := must(sdk.GetSystemBackup(ctx)).Model
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
	if slices.ContainsFunc(must(sdk.GetSystemBackup(ctx)).Model, func(b radarr.BackupResource) bool { return b.Id == made.Id }) {
		t.Errorf("%s is still listed after deleting it", made.Name)
	}
}

// TestLogin posts the login form. The container authenticates externally,
// so it has no users, and a login - like any failed one - is sent back to
// the login page marked failed. The answer is a redirect, which the client
// follows to that page.
//
//nolint:paralleltest // the tests share one server
func TestLogin(t *testing.T) {
	ctx := skipUnlessUp(t)

	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	for k, v := range map[string]string{"username": "sdk", "password": "not-a-password", "rememberMe": "false"} {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	res := must(sdk.PostLogin(ctx, &form, w.FormDataContentType(), radarr.PostLoginOperationOptions{ReturnUrl: "/"}))
	landed := res.HttpResponse.Request.URL
	if landed.Path != "/login" || landed.Query().Get("loginFailed") != "true" {
		t.Errorf("the login landed on %s, want /login?loginFailed=true", landed)
	}
}
