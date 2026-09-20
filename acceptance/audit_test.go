//go:build integration

package acceptance

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// finding is one audit finding, by the film's "Title (Year)".
func finding(t *testing.T, out map[string]any, name string) map[string]any {
	t.Helper()

	for _, f := range rows(t, out["findings"], "findings") {
		if titleYear(str(f["title"]), int(num0(f["year"]))) == name {
			return f
		}
	}
	t.Fatalf("no finding for %s in %v", name, out["findings"])

	return nil
}

// details are the details of an audit's findings, sorted.
func details(t *testing.T, out map[string]any) []string {
	t.Helper()

	var ds []string
	for _, f := range rows(t, out["findings"], "findings") {
		ds = append(ds, str(f["detail"]))
	}
	slices.Sort(ds)

	return ds
}

// filmAudits are the audits that check each film, and what each finds in the
// fixtures. The clean films are clean: every one of these leaves
// /media/movies alone.
var filmAudits = map[string][]string{
	"audit_missing_files":       {"Alien³ (1992)"},
	"audit_unmonitored":         {"Alien Resurrection (1997)"},
	"audit_cutoff_unmet":        {"Blade Runner 2049 (2017)", "Heat (1995)"},
	"audit_profile_mismatch":    {"Blade Runner 2049 (2017)", "Contact (1997)", "Heat (1995)"},
	"audit_runtime":             {"Interstellar (2014)"},
	"audit_resolution_mismatch": {"Blade Runner 2049 (2017)"},
	"audit_quality":             {"Spirited Away (2001)"},
	"audit_size":                nil, // nothing has a minimum until TestAuditSize gives one
	"audit_language":            {"Akira (1988)"},
	"audit_year_mismatch":       {"Dune (1984)"},
	"audit_missing_metadata":    nil,
	"audit_removed":             nil,
}

func TestAuditsFindWhatTheFixturesCarry(t *testing.T) {
	for audit, want := range filmAudits {
		t.Run(audit, func(t *testing.T) {
			out := call(t, audit, nil)
			if got := findings(t, out); !slices.Equal(got, want) {
				t.Errorf("%s = %v, want %v", audit, got, want)
			}
			if num(t, out["total_findings"], "total_findings") != len(want) || num(t, out["scanned"], "scanned") == 0 {
				t.Errorf("%s counts = %v scanned, %v found", audit, out["scanned"], out["total_findings"])
			}
			// every finding names its film and, when there is one, its file
			for _, f := range rows(t, out["findings"], "findings") {
				if num(t, f["id"], "id") == 0 || str(f["path"]) == "" || str(f["detail"]) == "" {
					t.Errorf("%s finding = %v", audit, f)
				}
			}

			// the clean root folder is clean
			clean := call(t, audit, map[string]any{"root_folder": moviesRoot})
			if got := findings(t, clean); len(got) != 0 {
				t.Errorf("%s found %v among the clean films", audit, got)
			}
		})
	}
}

// Each finding's detail says what is wrong in a way a person can act on:
// the numbers, the names, and what Radarr made of it.
func TestAuditDetails(t *testing.T) {
	for _, tc := range []struct {
		audit, film string
		want        []string
	}{
		{"audit_missing_files", "Alien³ (1992)", []string{"monitored and available (released), no file", "never searched"}},
		{"audit_unmonitored", "Alien Resurrection (1997)", []string{"Radarr will never download it"}},
		{"audit_cutoff_unmet", "Heat (1995)", []string{"HDTV-720p is below HD-1080p's cutoff of Bluray-1080p"}},
		{"audit_profile_mismatch", "Contact (1997)", []string{"Bluray-2160p is not a quality HD-1080p allows", "Bluray-1080p"}},
		{"audit_runtime", "Interstellar (2014)", []string{"the file runs 40 minutes, the film 169", "shorter"}},
		{"audit_resolution_mismatch", "Blade Runner 2049 (2017)", []string{"named 2160p", "the video is 1280x720 (720p)", "Radarr graded it Bluray-720p"}},
		{"audit_quality", "Spirited Away (2001)", []string{"480 lines (720x480), below 720", "a legacy codec (XviD)"}},
		{"audit_language", "Akira (1988)", []string{"audio is eng, none in Japanese (the film's original language)"}},
		{"audit_year_mismatch", "Dune (1984)", []string{"the folder says 2021, the film is 1984"}},
	} {
		f := finding(t, call(t, tc.audit, nil), tc.film)
		for _, w := range tc.want {
			if !strings.Contains(str(f["detail"]), w) {
				t.Errorf("%s %s = %q, want it to say %q", tc.audit, tc.film, f["detail"], w)
			}
		}
	}
}

// A limit caps the worklist, never the count; scoping to a root folder or a
// tag narrows what is scanned.
func TestAuditLimitsAndScopes(t *testing.T) {
	out := call(t, "audit_profile_mismatch", map[string]any{"limit": 1})
	if n := len(rows(t, out["findings"], "findings")); n != 1 || num(t, out["total_findings"], "total_findings") != 3 {
		t.Errorf("limit 1 = %d findings of %v", n, out["total_findings"])
	}

	all := call(t, "audit_missing_metadata", nil)
	messyOnly := call(t, "audit_missing_metadata", map[string]any{"root_folder": messyRoot})
	if num(t, all["scanned"], "scanned") != len(fixtures()) || num(t, messyOnly["scanned"], "scanned") != len(messy) {
		t.Errorf("scanned %v in all, %v in %s; want %d and %d", all["scanned"], messyOnly["scanned"], messyRoot, len(fixtures()), len(messy))
	}

	// a tag scopes to the films carrying it: give two films one, then take it off
	ids := []any{titleYear(messyHeat, 1995), titleYear(messyAkira, 1988)}
	call(t, "movie_batch_edit", map[string]any{"movies": ids, "add_tags": []any{"audit-scope"}})
	t.Cleanup(func() {
		_, _ = invoke("movie_batch_edit", map[string]any{"movies": ids, "remove_tags": []any{"audit-scope"}})
		_, _ = invoke("tag_delete", map[string]any{"tag": "audit-scope"})
	})
	tagged := call(t, "audit_cutoff_unmet", map[string]any{"tag": "audit-scope"})
	if got := findings(t, tagged); !slices.Equal(got, []string{"Heat (1995)"}) || num(t, tagged["scanned"], "scanned") != 2 {
		t.Errorf("tagged cutoff unmet = %v (scanned %v)", got, tagged["scanned"])
	}

	for _, args := range []map[string]any{{"root_folder": "/nowhere"}, {"tag": "no-such-tag"}} {
		if msg := callErr(t, "audit_runtime", args); !strings.Contains(msg, "no root folder") && !strings.Contains(msg, "no tag") {
			t.Errorf("audit_runtime %v = %q", args, msg)
		}
	}
}

// The runtime audit's tolerance is a percentage of the film's runtime.
func TestAuditRuntimeTolerance(t *testing.T) {
	if got := findings(t, call(t, "audit_runtime", map[string]any{"tolerance_percent": 80})); len(got) != 0 {
		t.Errorf("a 76%% shortfall flagged at an 80%% tolerance: %v", got)
	}
	if got := findings(t, call(t, "audit_runtime", map[string]any{"tolerance_percent": 70})); !slices.Equal(got, []string{"Interstellar (2014)"}) {
		t.Errorf("a 76%% shortfall at a 70%% tolerance = %v", got)
	}
}

// The quality audit's floor and bitrate: at 480 lines the DVD rip is flagged
// for its codec alone, and a bitrate floor catches every one of these still
// frames.
func TestAuditQualityThresholds(t *testing.T) {
	out := call(t, "audit_quality", map[string]any{"min_resolution": 480})
	if d := details(t, out); len(d) != 1 || d[0] != "a legacy codec (XviD)" {
		t.Errorf("at 480 lines = %v", d)
	}
	out = call(t, "audit_quality", map[string]any{"min_bitrate_kbps": 1000})
	if n := num(t, out["total_findings"], "total_findings"); n != 17 {
		t.Errorf("a 1000 kbps floor found %d of the 17 files, want all", n)
	}
	// fewest lines first
	if first := rows(t, out["findings"], "findings")[0]; str(first["title"]) != messySpirited {
		t.Errorf("the worst file is not first: %v", first)
	}
}

// The language audit takes the languages a film should have when given
// them, rather than its original language.
func TestAuditLanguageChoice(t *testing.T) {
	out := call(t, "audit_language", map[string]any{"languages": []any{"Japanese"}})
	got := findings(t, out)
	// every tagged file is English but Princess Mononoke's; the XviD rip
	// carries no tag and is never said to lack one
	if slices.Contains(got, "Princess Mononoke (1997)") || slices.Contains(got, "Spirited Away (2001)") || !slices.Contains(got, "Alien (1979)") {
		t.Errorf("audit_language [Japanese] = %v", got)
	}
	if got := findings(t, call(t, "audit_language", map[string]any{"languages": []any{"eng", "Japanese"}})); len(got) != 0 {
		t.Errorf("with English or Japanese allowed = %v", got)
	}
}

// Nothing has a minimum size until one is set: then the thumbnail-sized
// WEBDL-1080p file is the one outside it.
func TestAuditSize(t *testing.T) {
	if got := findings(t, call(t, "audit_size", nil)); len(got) != 0 {
		t.Fatalf("with Radarr's default sizes = %v", got)
	}
	call(t, "qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p", "min_size": 1})
	t.Cleanup(func() {
		_, _ = invoke("qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p", "min_size": 0})
	})

	out := call(t, "audit_size", nil)
	if got := findings(t, out); !slices.Equal(got, []string{"Gattaca (1997)"}) {
		t.Fatalf("with a WEBDL-1080p minimum = %v", got)
	}
	if d := str(finding(t, out, "Gattaca (1997)")["detail"]); !strings.Contains(d, "below WEBDL-1080p's minimum of 1") {
		t.Errorf("detail = %q", d)
	}

	// the fix, when the limit is what is wrong rather than the file: a
	// limit the file falls inside clears it
	call(t, "qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p", "min_size": 0})
	if got := findings(t, call(t, "audit_size", nil)); len(got) != 0 {
		t.Errorf("after the minimum was lowered = %v", got)
	}
}

// Naming: with renaming off Radarr gives no file names, and the audit says
// so while still checking the folders; switched on, it finds the files not
// named by the scheme.
func TestAuditNaming(t *testing.T) {
	off := call(t, "audit_naming", nil)
	if got := details(t, off); !slices.Equal(got, []string{`folder "Dune (2021)" would be named "Dune (1984)"`}) || !strings.Contains(str(off["note"]), "renaming is switched off") {
		t.Errorf("renaming off = %v (note %q)", got, off["note"])
	}

	call(t, "naming_edit", map[string]any{"rename_movies": true})
	t.Cleanup(func() { _, _ = invoke("naming_edit", map[string]any{"rename_movies": false}) })

	on := call(t, "audit_naming", nil)
	want := []string{
		`file "Blade Runner 2049 (2017) Bluray-2160p.mkv" would be named "Blade Runner 2049 (2017) Bluray-720p.mkv"`,
		`file "Dune (2021) Bluray-1080p.mkv" would be named "Dune (1984) Bluray-1080p.mkv"`,
		`file "Spirited Away (2001) DVDRip XviD.avi" would be named "Spirited Away (2001) DVD.avi"`,
		`file "the.matrix.1999.1080p.bluray.x264-GRP.mkv" would be named "The Matrix (1999) Bluray-1080p.mkv"`,
		`folder "Dune (2021)" would be named "Dune (1984)"`,
	}
	if got := details(t, on); !slices.Equal(got, want) || on["note"] != nil {
		t.Errorf("renaming on = %v\nwant %v", got, want)
	}
	// folders false skips the one-request-a-film half
	if got := details(t, call(t, "audit_naming", map[string]any{"folders": false})); len(got) != 4 {
		t.Errorf("folders false = %v", got)
	}
}

// The unmapped folders: a film on disk the library has never heard of, and a
// second copy of one it keeps elsewhere, which says which.
func TestAuditUnmappedFolders(t *testing.T) {
	out := call(t, "audit_unmapped_folders", nil)
	if got := findings(t, out); !slices.Equal(got, []string{"Alien (1979)", "Ronin (1998)"}) {
		t.Fatalf("unmapped = %v", got)
	}
	ronin := finding(t, out, "Ronin (1998)")
	if str(ronin["path"]) != unmappedRonin || ronin["duplicate_of"] != nil || !strings.Contains(str(ronin["detail"]), "not in the library") {
		t.Errorf("Ronin = %v", ronin)
	}
	cut := finding(t, out, "Alien (1979)")
	if str(cut["path"]) != unmappedAlienCut || num(t, cut["duplicate_of"], "duplicate_of") != movieID(t, "Alien", 1979) ||
		!strings.Contains(str(cut["detail"]), "a second copy of Alien (1979), which the library keeps at /media/movies/Alien (1979) (this one says Directors Cut)") {
		t.Errorf("the director's cut = %v", cut)
	}
	if got := findings(t, call(t, "audit_unmapped_folders", map[string]any{"root_folder": moviesRoot})); !slices.Equal(got, []string{"Ronin (1998)"}) {
		t.Errorf("in %s = %v", moviesRoot, got)
	}
}

// The untracked files: the 1080p copy of Contact beside the 2160p one Radarr
// took, with Radarr's reason for not taking it - and none of the files the
// library does track, even ones whose names Radarr cannot parse as their
// film (Blade Runner 2049 reads as a film of 2049).
// A folder renamed behind Radarr's back: the film's folder is gone, the
// audit names the folder that reads as the film, and movie_edit points the
// film at it, with its file back.
func TestAuditMissingFolders(t *testing.T) {
	addScratch(t)
	// nothing is missing to start with
	if got := findings(t, call(t, "audit_missing_folders", nil)); len(got) != 0 {
		t.Fatalf("audit_missing_folders = %v, want nothing", got)
	}
	renamed := moviesRoot + "/Moon (2009) [remux]"
	if err := os.Rename(hostPath(scratchFolder), hostPath(renamed)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(hostPath(renamed)) })

	f := finding(t, call(t, "audit_missing_folders", nil), "Moon (2009)")
	if str(f["path"]) != scratchFolder || str(f["folder"]) != renamed || !strings.Contains(str(f["detail"]), "reads as this film") {
		t.Fatalf("the renamed film = %v", f)
	}

	row := rows(t, call(t, "movie_edit", map[string]any{"movie": "Moon", "folder": "Moon (2009) [remux]"})["movies"], "movies")
	if len(row) != 1 || str(row[0]["path"]) != renamed || row[0]["has_file"] != true {
		t.Fatalf("after pointing the film at the folder = %v", row)
	}
	if got := findings(t, call(t, "audit_missing_folders", nil)); len(got) != 0 {
		t.Errorf("still missing after the fix: %v", got)
	}
	file := object(t, call(t, "movie_get", map[string]any{"movie": "Moon"})["file"], "file")
	if !strings.HasPrefix(str(file["path"]), renamed+"/") {
		t.Errorf("the film's file is %v, want it in the renamed folder", file["path"])
	}
	// and the folder is no longer one no film is in
	for _, u := range rows(t, call(t, "audit_unmapped_folders", nil)["findings"], "findings") {
		if str(u["path"]) == renamed {
			t.Errorf("the folder is still unmapped: %v", u)
		}
	}
}

func TestAuditUntrackedFiles(t *testing.T) {
	out := call(t, "audit_untracked_files", nil)
	if got := findings(t, out); !slices.Equal(got, []string{"Contact (1997)"}) {
		t.Fatalf("untracked = %v", got)
	}
	f := finding(t, out, "Contact (1997)")
	if str(f["file"]) != "Contact (1997) Bluray-1080p.mkv" || !strings.Contains(str(f["detail"]), "Not an upgrade for existing movie file") {
		t.Errorf("Contact = %v", f)
	}
	if num(t, out["scanned"], "scanned") != 17 {
		t.Errorf("scanned %v films with files, want 17", out["scanned"])
	}
}

// The collection gaps: the library holds one of The Matrix Collection's four
// films, and every film of the other collections it touches (Heat 2, only
// announced, is not a gap until it is released).
func TestAuditCollectionGaps(t *testing.T) {
	out := call(t, "audit_collection_gaps", nil)
	f := rows(t, out["findings"], "findings")
	if len(f) != 1 || str(f[0]["title"]) != "The Matrix Collection" ||
		str(f[0]["detail"]) != "holds 1 of 4: missing The Matrix Reloaded (2003), The Matrix Revolutions (2003), The Matrix Resurrections (2021)" {
		t.Fatalf("gaps = %v", out["findings"])
	}
	if missing := rows(t, f[0]["missing"], "missing"); len(missing) != 3 || num(t, missing[0]["tmdb_id"], "tmdb_id") != 604 {
		t.Errorf("missing = %v", missing)
	}
	with := call(t, "audit_collection_gaps", map[string]any{"include_unreleased": true})
	var heat bool
	for _, f := range rows(t, with["findings"], "findings") {
		if str(f["title"]) == "Heat Collection" && strings.Contains(str(f["detail"]), "Heat 2") {
			heat = true
		}
	}
	if !heat {
		t.Errorf("with the unreleased counted, Heat 2 is not a gap: %v", with["findings"])
	}
}

// audit_all reports each audit's count, and leaves the two slow ones to deep.
func TestAuditAll(t *testing.T) {
	shallow := call(t, "audit_all", nil)
	if got := strs(t, shallow["skipped"], "skipped"); !slices.Equal(got, []string{"audit_naming", "audit_untracked_files"}) {
		t.Errorf("skipped = %v", got)
	}
	deep := call(t, "audit_all", map[string]any{"deep": true})
	if deep["skipped"] != nil {
		t.Errorf("deep skipped %v", deep["skipped"])
	}
	total := 0
	for _, row := range rows(t, deep["audits"], "audits") {
		name := str(row["audit"])
		alone := call(t, name, nil)
		if row["total_findings"] != alone["total_findings"] || row["scanned"] != alone["scanned"] {
			t.Errorf("audit_all says %s found %v of %v, the audit %v of %v", name, row["total_findings"], row["scanned"], alone["total_findings"], alone["scanned"])
		}
		total += num(t, row["total_findings"], "total_findings")
	}
	if num(t, deep["total_findings"], "total_findings") != total {
		t.Errorf("total %v, the audits sum to %d", deep["total_findings"], total)
	}
	scoped := call(t, "audit_all", map[string]any{"root_folder": moviesRoot})
	for _, row := range rows(t, scoped["audits"], "audits") {
		if name := str(row["audit"]); name != "audit_unmapped_folders" && name != "audit_collection_gaps" && name != "audit_queue" && num(t, row["total_findings"], "total_findings") != 0 {
			t.Errorf("audit_all in %s: %s found %v", moviesRoot, name, row["total_findings"])
		}
	}
}
