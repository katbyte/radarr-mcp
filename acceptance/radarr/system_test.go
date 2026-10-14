//go:build acceptance

package radarr_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/url"
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

// TestLogin posts the login form. The container authenticates externally,
// so it has no users, and a login - like any failed one - is sent back to
// the login page marked failed. The answer is a redirect, which the client
// hands back as the answer it is.
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

	// the form answers with a redirect, to where it was asked to land or back
	// to itself, which the client hands back as it came rather than follows
	res, err := sdk.PostLogin(ctx, &form, w.FormDataContentType(), "/")
	if err != nil || res.HttpResponse.StatusCode != http.StatusFound {
		t.Fatalf("a login that fails = %v, want its redirect as the answer", err)
	}
	landed, err := url.Parse(res.HttpResponse.Header.Get("Location"))
	if err != nil || landed.Path != "/login" || landed.Query().Get("loginFailed") != "true" {
		t.Errorf("the login was sent to %s, want /login?loginFailed=true", res.HttpResponse.Header.Get("Location"))
	}
}
