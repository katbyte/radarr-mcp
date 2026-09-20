package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	t.Parallel()

	all, err := Select("")
	if err != nil || len(all) != len(Services) {
		t.Errorf("Select(\"\") = %d services, %v", len(all), err)
	}
	one, err := Select(" radarr ")
	if err != nil || len(one) != 1 || one[0].Package != "radarr" {
		t.Errorf("Select(radarr) = %+v, %v", one, err)
	}
	if _, err := Select("radarr,sonarr"); err == nil || !strings.Contains(err.Error(), `unknown service "sonarr" (have radarr)`) {
		t.Errorf("Select with an unknown service = %v", err)
	}
}

func TestPaths(t *testing.T) {
	t.Parallel()

	svc, ok := Find("radarr")
	if !ok {
		t.Fatal("no radarr service")
	}
	rooted := svc.In("/repo")
	// the configured paths stay repository-relative; they are recorded in the definitions
	if rooted.Spec != svc.Spec || rooted.Path(rooted.Spec) != filepath.Join(string(filepath.Separator)+"repo", "docs", "radarr-openapi.json") {
		t.Errorf("In(/repo) = %+v", rooted)
	}
	if svc.Path(svc.Output) != filepath.Join("lib", "radarr") {
		t.Errorf("Path without a root = %q", svc.Path(svc.Output))
	}
	// every Radarr route but a handful is under /api/v3, which method names leave out
	if svc.NamePrefix != "/api/v3" {
		t.Errorf("NamePrefix = %q", svc.NamePrefix)
	}
}
