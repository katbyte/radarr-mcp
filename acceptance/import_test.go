//go:build integration

package acceptance

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// importDrop is a folder outside every root folder a file can be dropped in
// by hand, the way a download that no client reported ends up.
const importDrop = "/media/downloads/manual"

// import_scan lists what Radarr would make of a folder's files, and why it
// would refuse each: the 1080p copy of Contact beside the 2160p one Radarr
// took is not an upgrade.
func TestImportScan(t *testing.T) {
	out := call(t, "import_scan", map[string]any{"folder": messyRoot + "/Contact (1997)"})
	files := rows(t, out["files"], "files")
	if len(files) != 1 {
		t.Fatalf("Contact's folder = %v", files)
	}
	f := files[0]
	if str(f["relative_path"]) != "Contact (1997) Bluray-1080p.mkv" || str(f["movie"]) != "Contact (1997)" || str(f["quality"]) != "Bluray-1080p" ||
		f["importable"] != false || !strings.Contains(strings.Join(strs(t, f["rejections"], "rejections"), " "), "Not an upgrade") {
		t.Errorf("the untracked copy = %v", f)
	}

	// scanning a film's own folder finds the same file, and none it tracks
	byMovie := rows(t, call(t, "import_scan", map[string]any{"movie": "Contact"})["files"], "files")
	if len(byMovie) != 1 || str(byMovie[0]["path"]) != str(f["path"]) {
		t.Errorf("Contact by film = %v", byMovie)
	}
	// a folder whose one file the library tracks, though its name reads as
	// a film of 2049, has nothing to import
	if got := rows(t, call(t, "import_scan", map[string]any{"movie": "Blade Runner 2049"})["files"], "files"); len(got) != 0 {
		t.Errorf("Blade Runner 2049's folder = %v", got)
	}
	// Ronin, on disk and not in the library, is an unknown film
	ronin := rows(t, call(t, "import_scan", map[string]any{"folder": unmappedRonin})["files"], "files")
	if len(ronin) != 1 || str(ronin[0]["movie"]) != "" || !slices.Contains(strs(t, ronin[0]["rejections"], "rejections"), "Unknown Movie") || ronin[0]["importable"] != true {
		t.Errorf("Ronin = %v", ronin)
	}
	if msg := callErr(t, "import_scan", nil); !strings.Contains(msg, "name a folder, a download_id or a movie") {
		t.Errorf("a scan of nothing = %q", msg)
	}
}

// import_identify says which film each folder no film is in holds: what is
// in it, and what TMDB offers for its name - the step before movie_add takes
// the folder into the library.
func TestImportIdentify(t *testing.T) {
	out := call(t, "import_identify", nil)
	if num(t, out["total"], "total") != 2 {
		t.Fatalf("import_identify = %v", out)
	}
	byFolder := map[string]map[string]any{}
	for _, f := range rows(t, out["folders"], "folders") {
		byFolder[str(f["folder"])] = f
	}

	// a film on disk Radarr has never heard of: its file, and the film it is
	ronin := byFolder[unmappedRonin]
	if ronin == nil || str(ronin["name"]) != "Ronin (1998)" {
		t.Fatalf("Ronin's folder = %v", byFolder)
	}
	if files := rows(t, ronin["files"], "files"); len(files) != 1 || str(files[0]["quality"]) != "Bluray-1080p" {
		t.Errorf("what is in Ronin's folder = %v", ronin["files"])
	}
	best := rows(t, ronin["candidates"], "candidates")
	if len(best) == 0 || str(best[0]["title"]) != "Ronin" || num(t, best[0]["year"], "year") != 1998 ||
		num(t, best[0]["tmdb_id"], "tmdb_id") != 8195 || best[0]["in_library"] != false {
		t.Errorf("Ronin's candidates = %v", ronin["candidates"])
	}

	// a second copy of a film the library holds: its best candidate is that film
	cut := byFolder[unmappedAlienCut]
	if cut == nil {
		t.Fatalf("the director's cut folder = %v", byFolder)
	}
	if c := rows(t, cut["candidates"], "candidates"); len(c) == 0 || c[0]["in_library"] != true ||
		num(t, c[0]["library_id"], "library_id") != movieID(t, "Alien", 1979) {
		t.Errorf("the director's cut candidates = %v", cut["candidates"])
	}

	// one folder by name, and a folder with nothing in it Radarr can use
	one := call(t, "import_identify", map[string]any{"folders": []any{unmappedRonin}, "candidates": 1})
	if got := rows(t, one["folders"], "folders"); len(got) != 1 || len(rows(t, got[0]["candidates"], "candidates")) != 1 {
		t.Errorf("one folder = %v", one)
	}
	empty := moviesRoot + "/Some Folder Of Nothing"
	mediaMkdir(t, hostPath(empty))
	t.Cleanup(func() { _ = os.RemoveAll(hostPath(empty)) })
	got := rows(t, call(t, "import_identify", map[string]any{"folders": []any{empty}})["folders"], "folders")
	if len(got) != 1 || len(rowsOf(got[0]["files"])) != 0 || !strings.Contains(str(got[0]["note"]), "no video file") {
		t.Errorf("an empty folder = %v", got)
	}
}

// import_apply imports a file dropped outside the library into a film, moved
// and named by the scheme; with copy, the dropped file stays where it was;
// and a quality given overrides the one Radarr parsed.
func TestImportApply(t *testing.T) {
	addScratch(t)
	dropped := importDrop + "/Moon.2009.720p.WEB-DL.x264-GRP.mkv"
	makeVideo(t, dropped, 97, "1280x720")
	t.Cleanup(func() { _ = os.RemoveAll(hostPath(importDrop)) })

	scan := rows(t, call(t, "import_scan", map[string]any{"folder": importDrop})["files"], "files")
	if len(scan) != 1 || str(scan[0]["movie"]) != "Moon (2009)" || str(scan[0]["quality"]) != "WEBDL-720p" {
		t.Fatalf("the dropped file = %v", scan)
	}

	// copied, graded by hand: the dropped file stays, the film has a copy
	out := call(t, "import_apply", map[string]any{
		"files": []any{map[string]any{"path": dropped, "movie": "Moon", "quality": "WEBRip-720p"}}, "mode": "copy",
	})
	if cmd := object(t, out["command"], "command"); str(cmd["status"]) != "completed" {
		t.Fatalf("the import = %v", out)
	}
	moon := call(t, "movie_get", map[string]any{"movie": "Moon"})
	file := object(t, moon["file"], "file")
	if str(file["quality"]) != "WEBRip-720p" || !strings.HasPrefix(str(file["path"]), scratchFolder+"/") {
		t.Errorf("after a copy, the film's file = %v", file)
	}
	if _, err := os.Stat(hostPath(dropped)); err != nil {
		t.Errorf("a copy took the dropped file away: %v", err)
	}

	// moved: the dropped file is gone, and the film has it
	call(t, "import_apply", map[string]any{"files": []any{map[string]any{"path": dropped, "movie": "tmdb:17431"}}, "mode": "move"})
	file = object(t, call(t, "movie_get", map[string]any{"movie": "Moon"})["file"], "file")
	if str(file["quality"]) != "WEBDL-720p" {
		t.Errorf("after a move, the film's file = %v", file)
	}
	if _, err := os.Stat(hostPath(dropped)); !os.IsNotExist(err) {
		t.Errorf("a move left the dropped file: %v", err)
	}

	for args, want := range map[string]map[string]any{
		"no files":                           {"files": []any{}},
		`mode "link"`:                        {"files": []any{map[string]any{"path": dropped, "movie": "Moon"}}, "mode": "link"},
		"is not a video file Radarr can see": {"files": []any{map[string]any{"path": importDrop + "/nothing.mkv", "movie": "Moon"}}},
		`no film in the library is titled "Primer"`: {"files": []any{map[string]any{"path": dropped, "movie": "Primer"}}},
	} {
		if msg := callErr(t, "import_apply", want); !strings.Contains(msg, args) {
			t.Errorf("import_apply %v = %q, want %q", want, msg, args)
		}
	}
}
