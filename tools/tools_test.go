package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestClient(t *testing.T) *radarr.Client {
	t.Helper()

	c, err := radarr.New("http://127.0.0.1:1", "test")
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func register(t *testing.T, opts Options) []string {
	t.Helper()

	names, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), opts)
	if err != nil {
		t.Fatal(err)
	}

	return names
}

func TestRegisterAllKinds(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	dflt := register(t, Options{})
	ro := register(t, Options{ReadOnly: true})

	if len(all) <= len(dflt) || len(dflt) <= len(ro) || len(ro) == 0 {
		t.Fatalf("counts all=%d default=%d read-only=%d", len(all), len(dflt), len(ro))
	}
	for _, name := range []string{"movie_delete", "moviefile_delete"} {
		if slices.Contains(dflt, name) {
			t.Errorf("%s registered without --enable-delete", name)
		}
		if !slices.Contains(all, name) {
			t.Errorf("%s missing with --enable-delete", name)
		}
	}
	for _, name := range ro {
		for _, suffix := range []string{"_add", "_edit", "_delete", "_create", "_rename", "_remove", "_refresh", "_rescan", "_run", "_grab", "_download", "_apply", "_mark_failed"} {
			if strings.HasSuffix(name, suffix) && name != "audit_naming" {
				t.Errorf("%s registered under --read-only", name)
			}
		}
	}
	for _, name := range EssentialTools {
		if !slices.Contains(dflt, name) {
			t.Errorf("essential tool %s does not exist", name)
		}
	}
	if !slices.IsSorted(dflt) {
		t.Error("registered names not sorted")
	}
}

func TestRegisterAllFilters(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Allow: []string{"essential"}})
	want := slices.Clone(EssentialTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("essential = %v", got)
	}

	got = register(t, Options{Allow: []string{"movie_*,tag_list"}, Deny: []string{"*_rescan"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "movie_") && name != "tag_list" {
			t.Errorf("unexpected %s", name)
		}
		if name == "movie_rescan" {
			t.Error("denied tool registered")
		}
	}
	if !slices.Contains(got, "movie_list") || !slices.Contains(got, "tag_list") {
		t.Errorf("allow list not honoured: %v", got)
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Allow: []string{"bogus_*"}}); err == nil {
		t.Error("unknown allow pattern accepted")
	}
	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Deny: []string{"nope"}}); err == nil {
		t.Error("unknown deny pattern accepted")
	}
}

// Every tool belongs to exactly one toolset, every toolset names only real
// tools, and core comes along with whatever else is asked for.
func TestToolsetsPartition(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	seen := map[string]string{}
	for set, members := range Toolsets {
		for _, m := range members {
			if !slices.Contains(all, m) {
				t.Errorf("toolset %s names %s, which is not a tool", set, m)
			}
			if prev, dup := seen[m]; dup {
				t.Errorf("%s is in both %s and %s", m, prev, set)
			}
			seen[m] = set
		}
	}
	for _, name := range all {
		if seen[name] == "" {
			t.Errorf("%s belongs to no toolset", name)
		}
	}

	got := register(t, Options{Toolsets: []string{"organise"}})
	for _, core := range Toolsets["core"] {
		if !slices.Contains(got, core) {
			t.Errorf("core tool %s missing when only organise was asked for", core)
		}
	}
	for _, name := range got {
		if seen[name] != "core" && seen[name] != "organise" {
			t.Errorf("%s (%s) registered for --toolsets organise", name, seen[name])
		}
	}

	// a resource family is every tool with that prefix, plus core
	got = register(t, Options{Toolsets: []string{"tag"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "tag_") && seen[name] != "core" {
			t.Errorf("%s registered for the tag family", name)
		}
	}
	if !slices.Contains(got, "tag_list") {
		t.Errorf("the tag family lacks tag_list: %v", got)
	}

	// all is everything the kind gates allow
	if got = register(t, Options{Toolsets: []string{"all"}}); len(got) != len(register(t, Options{})) {
		t.Errorf("all registered %d tools, want %d", len(got), len(register(t, Options{})))
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Toolsets: []string{"nope"}}); err == nil {
		t.Error("unknown toolset accepted")
	} else if !strings.Contains(err.Error(), "curation") || !strings.Contains(err.Error(), "rootfolder") {
		t.Errorf("the error should name the sets and families: %v", err)
	}
}

// The default set is read-only and small: a client that just points
// radarr-mcp at a server spends its context on finding films, not on tools
// it will not call.
func TestCoreIsReadOnly(t *testing.T) {
	t.Parallel()

	infos, err := Describe(Options{Toolsets: []string{"core"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range infos {
		if ti.Kind != "read" {
			t.Errorf("%s is a %s tool in core", ti.Name, ti.Kind)
		}
	}
	if len(infos) != len(Toolsets["core"]) {
		t.Errorf("core registered %d tools, want %d", len(infos), len(Toolsets["core"]))
	}
}

// A session reads the tools it loaded and follows where they point, so a tool
// names only tools that load with it: its own set's, or core's, which comes
// with every set. A core tool loads on its own by default, so it names only
// core tools.
func TestToolsetsNameOnlyWhatTheyLoad(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	set := map[string]string{}
	for name, members := range Toolsets {
		for _, m := range members {
			set[m] = name
		}
	}
	res, err := session(t, newFakeRadarr(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	word := regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`)
	for _, tool := range res.Tools {
		// the description and the input and output schemas' own descriptions
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range word.FindAllString(string(raw), -1) {
			if ref == tool.Name || !slices.Contains(all, ref) {
				continue
			}
			if set[ref] != "core" && set[ref] != set[tool.Name] {
				t.Errorf("%s (%s) names %s, which only %s loads", tool.Name, set[tool.Name], ref, set[ref])
			}
		}
	}
}

// Every tool's annotations say what it does: a read never changes state, a
// write does, and only a delete is destructive.
func TestAnnotations(t *testing.T) {
	t.Parallel()

	res, err := session(t, newFakeRadarr(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := Describe(Options{Toolsets: []string{"all"}, EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, ti := range infos {
		kinds[ti.Name] = ti.Kind
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		switch kinds[tool.Name] {
		case "read":
			if !a.ReadOnlyHint || *a.DestructiveHint {
				t.Errorf("%s is a read but annotated %+v", tool.Name, a)
			}
		case "write":
			if a.ReadOnlyHint || *a.DestructiveHint {
				t.Errorf("%s is a write but annotated %+v", tool.Name, a)
			}
		case "delete":
			if a.ReadOnlyHint || !*a.DestructiveHint {
				t.Errorf("%s is a delete but annotated %+v", tool.Name, a)
			}
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", tool.Name)
		}
	}
}

// Describe reports the same selection RegisterAll makes, with its kinds and
// sets, and needs no server.
func TestDescribe(t *testing.T) {
	t.Parallel()

	list, err := Describe(Options{Toolsets: []string{"curation"}, EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	kinds := map[string]string{}
	for _, ti := range list {
		names = append(names, ti.Name)
		kinds[ti.Name] = ti.Kind
		if ti.Toolset != "core" && ti.Toolset != "curation" {
			t.Errorf("%s reported in %s", ti.Name, ti.Toolset)
		}
		if ti.Description == "" {
			t.Errorf("%s has no description", ti.Name)
		}
	}
	want := register(t, Options{Toolsets: []string{"curation"}, EnableDelete: true})
	if !slices.Equal(names, want) {
		t.Errorf("Describe = %v\nRegisterAll = %v", names, want)
	}
	if kinds["audit_all"] != "read" || kinds["movie_edit"] != "write" {
		t.Errorf("kinds = %v", kinds)
	}
	if _, err := Describe(Options{Allow: []string{"nope"}}); err == nil {
		t.Error("Describe accepted a pattern that matches nothing")
	}

	if fam := FamilyNames(); !slices.Contains(fam, "audit") || !slices.Contains(fam, "movie") {
		t.Errorf("families = %v", fam)
	}
	if sets := ToolsetNames(); !slices.Contains(sets, "core") || !slices.IsSorted(sets) {
		t.Errorf("toolset names = %v", sets)
	}
}

func TestMatchPattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"movie_get", "movie_get", true},
		{"movie_get", "movie_gets", false},
		{"movie_*", "movie_get", true},
		{"movie_*", "moviefile_edit", false},
		{"*_delete", "movie_delete", true},
		// the documented way to turn every destructive tool off has to reach this one
		{"*_delete", "moviefile_delete", true},
		{"*", "anything", true},
	} {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.name, got)
		}
	}
}

// A nil slice in a result must serialise as [], so a client can tell "none"
// from "not fetched".
func TestEmptyNilSlices(t *testing.T) {
	t.Parallel()

	type inner struct{ Tags []string }
	type out struct {
		Items  []inner
		Ptr    *inner
		Names  []string
		Nested [][]string
		Keep   []string
	}
	v := out{Items: []inner{{}}, Ptr: &inner{}, Keep: []string{"x"}}
	emptyNilSlices(reflect.ValueOf(&v).Elem())

	if v.Names == nil || v.Nested == nil || v.Items[0].Tags == nil || v.Ptr.Tags == nil {
		t.Errorf("nil slices survived: %+v", v)
	}
	if len(v.Keep) != 1 {
		t.Error("a populated slice was touched")
	}
}

// the films the pure checks below are run against
func film(id int, title string, year int) radarr.MovieResource {
	return radarr.MovieResource{Id: id, Title: title, Year: year, TmdbId: id * 10, Monitored: new(true), Path: "/m/" + titleYear(title, year)}
}

func TestMatchMovie(t *testing.T) {
	t.Parallel()

	library := []radarr.MovieResource{
		film(1, "Alien", 1979), film(2, "Dune", 1984), film(3, "Dune", 2021),
		{Id: 4, Title: "Spirited Away", OriginalTitle: "Sen to Chihiro no Kamikakushi", Year: 2001, TmdbId: 129, ImdbId: "tt0245429"},
	}
	for _, tc := range []struct {
		ref    string
		wantID int
		err    string
	}{
		{"1", 1, ""},
		{"alien", 1, ""},
		{"  ALIEN  ", 1, ""},
		{"Dune (1984)", 2, ""},
		{"dune(2021)", 3, ""},
		{"tmdb:129", 4, ""},
		{"TMDB: 129", 4, ""},
		{"imdb:TT0245429", 4, ""},
		{"Sen to Chihiro no Kamikakushi", 4, ""},
		{"spirited-away", 4, ""},
		{"Dune", 0, `"Dune" matches 2 films: Dune (1984) id 2, Dune (2021) id 3`},
		{"99", 0, "no film in the library has id 99"},
		{"tmdb:x", 0, "a tmdb id is a number"},
		{"tmdb:1", 0, "no film in the library has tmdb id 1"},
		{"imdb:tt1", 0, "no film in the library has imdb id tt1"},
		{"Heat", 0, `no film in the library is titled "Heat"`},
		{"Alien (1986)", 0, "no film in the library is titled"},
		{"", 0, "no film named"},
	} {
		m, err := matchMovie(library, tc.ref)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("matchMovie(%q) = %v, %v; want an error containing %q", tc.ref, m, err, tc.err)
		case tc.err == "" && (err != nil || m.Id != tc.wantID):
			t.Errorf("matchMovie(%q) = %v, %v; want id %d", tc.ref, m, err, tc.wantID)
		}
	}
}

func TestSmallParsers(t *testing.T) {
	t.Parallel()

	for s, want := range map[string]float64{"1:57:00": 117, "0:40:30": 40.5, "45:00": 45, "": 0, "soon": 0, "1:2:3:4": 0} {
		if got := runtimeMinutes(s); got != want {
			t.Errorf("runtimeMinutes(%q) = %v, want %v", s, got, want)
		}
	}
	if got := splitList(" eng/ jpn //"); !slices.Equal(got, []string{"eng", "jpn"}) {
		t.Errorf("splitList = %v", got)
	}
	for _, tc := range []struct {
		w, h, want int
	}{{3840, 2160, 2160}, {3840, 1600, 2160}, {1920, 1080, 1080}, {1920, 800, 1080}, {1280, 720, 720}, {1280, 536, 720}, {720, 480, 480}, {0, 0, 0}} {
		if got := resolutionClass(tc.w, tc.h); got != tc.want {
			t.Errorf("resolutionClass(%d, %d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
	for name, want := range map[string]int{
		"Blade Runner 2049 (2017) Bluray-2160p.mkv": 2160, "film.4K.HDR.mkv": 2160, "the.matrix.1999.1080p.bluray.x264-GRP.mkv": 1080,
		"Heat (1995) HDTV-720p.mkv": 720, "old.576p.mkv": 480, "Spirited Away (2001) DVDRip XviD.avi": 0, "a1080pb.mkv": 0,
	} {
		if got := nameClass(name); got != want {
			t.Errorf("nameClass(%q) = %d, want %d", name, got, want)
		}
	}
	for s, want := range map[string][2]int{"1920x1080": {1920, 1080}, "720X480": {720, 480}} {
		if w, h, ok := probeResolution(s); !ok || w != want[0] || h != want[1] {
			t.Errorf("probeResolution(%q) = %d, %d, %v", s, w, h, ok)
		}
	}
	if _, _, ok := probeResolution("unknown"); ok {
		t.Error("probeResolution read a resolution out of nothing")
	}
	for path, want := range map[string]int{"/m/Dune (2021)": 2021, "/m/Dune (2021)/": 2021, "/m/2001 A Space Odyssey (1968)": 1968, "/m/Dune": 0, "/m/Film (9999)": 0} {
		if got, _ := pathYear(path); got != want {
			t.Errorf("pathYear(%q) = %d, want %d", path, got, want)
		}
	}
	for name, want := range map[string]struct {
		title string
		year  int
		rest  string
	}{
		"Alien (1979) Directors Cut": {"Alien", 1979, "Directors Cut"},
		"Ronin (1998)":               {"Ronin", 1998, ""},
		"Untitled Home Video":        {"Untitled Home Video", 0, ""},
	} {
		title, year, rest := parseFolder(name)
		if title != want.title || year != want.year || rest != want.rest {
			t.Errorf("parseFolder(%q) = %q, %d, %q", name, title, year, rest)
		}
	}
	if got := truncate("one two three four", 9); got != "one two..." {
		t.Errorf("truncate = %q", got)
	}
	if got := firstLines("a\nb\nc", 2); got != "a\nb\n..." {
		t.Errorf("firstLines = %q", got)
	}
	if got := lastLines("a\nb\nc\n", 2); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("lastLines = %q", got)
	}
	if got := baseName("/media/movies/Alien (1979)/"); got != "Alien (1979)" {
		t.Errorf("baseName = %q", got)
	}
	if !inFolder("/media/movies/Alien (1979)", "/media/movies/") || inFolder("/media/movies2/Alien", "/media/movies") {
		t.Error("inFolder matches a sibling folder, or misses a child")
	}
	for in, want := range map[int]time.Duration{-1: 0, 0: defaultWait, 30: 30 * time.Second} {
		if got := waitFor(in); got != want {
			t.Errorf("waitFor(%d) = %v, want %v", in, got, want)
		}
	}
	for name, want := range map[string]string{"Rss Sync": "RssSync", "rsssync": "RssSync", "Refresh Monitored Downloads": "RefreshMonitoredDownloads"} {
		if got, ok := canonicalTask(name); !ok || got != want {
			t.Errorf("canonicalTask(%q) = %q, %v", name, got, ok)
		}
	}
	for _, name := range []string{"Restart", "Shutdown", "ApplicationUpdate", ""} {
		if _, ok := canonicalTask(name); ok {
			t.Errorf("task_run would start %q", name)
		}
	}
	if !slices.Equal(codesFor("Japanese"), []string{"jpn"}) || !slices.Equal(codesFor("French"), []string{"fre", "fra"}) || !slices.Equal(codesFor("KOR"), []string{"kor"}) {
		t.Error("codesFor maps a language wrong")
	}
}

// profile builds a quality profile the way Radarr answers one: items worst
// first, a group among them, the cutoff by quality or group id.
func profile() *radarr.QualityProfileResource {
	q := func(id int, name string, allowed bool) radarr.QualityProfileQualityItemResource {
		return radarr.QualityProfileQualityItemResource{Allowed: new(allowed), Quality: &radarr.Quality{Id: id, Name: name}}
	}
	return &radarr.QualityProfileResource{
		Id: 4, Name: "HD-1080p", Cutoff: 7, UpgradeAllowed: new(true),
		Items: []radarr.QualityProfileQualityItemResource{
			q(2, "DVD", false),
			q(4, "HDTV-720p", true),
			{Id: 1001, Name: "WEB 1080p", Allowed: new(true), Items: []radarr.QualityProfileQualityItemResource{q(3, "WEBDL-1080p", true), q(15, "WEBRip-1080p", true)}},
			q(7, "Bluray-1080p", true),
			q(19, "Bluray-2160p", false),
		},
	}
}

func TestProfileQualities(t *testing.T) {
	t.Parallel()

	p := profile()
	allowed, cutoff := profileQualities(p)
	if !slices.Equal(allowed, []string{"Bluray-1080p", "WEBRip-1080p", "WEBDL-1080p", "HDTV-720p"}) || cutoff != "Bluray-1080p" {
		t.Errorf("allowed %v, cutoff %q", allowed, cutoff)
	}
	p.Cutoff = 1001
	if _, cutoff := profileQualities(p); cutoff != "WEB 1080p" {
		t.Errorf("a group cutoff = %q", cutoff)
	}
	if id, err := cutoffID(p, "web 1080p"); err != nil || id != 1001 {
		t.Errorf("cutoffID(group) = %d, %v", id, err)
	}
	if id, err := cutoffID(p, "Bluray-1080p"); err != nil || id != 7 {
		t.Errorf("cutoffID(quality) = %d, %v", id, err)
	}
	if _, err := cutoffID(p, "Bluray-2160p"); err == nil || !strings.Contains(err.Error(), "does not allow") {
		t.Errorf("a quality the profile does not allow became the cutoff: %v", err)
	}
}

// withMedia gives a film a file with media info.
func withMedia(m *radarr.MovieResource, relativePath, quality string, resolution int, mi radarr.MediaInfoResource, size int64) *radarr.MovieResource {
	m.HasFile = new(true)
	m.MovieFile = &radarr.MovieFileResource{
		Id: m.Id * 100, RelativePath: relativePath, Size: size, MediaInfo: &mi,
		Quality: &radarr.QualityModel{Quality: &radarr.Quality{Name: quality, Resolution: resolution}},
	}

	return m
}

// check runs one film check against a hand-built film.
func check(t *testing.T, c filmCheck, e *auditEnv, m *radarr.MovieResource) (detail string, suspect bool) {
	t.Helper()

	detail, suspect, _, err := c(context.Background(), e, m)
	if err != nil {
		t.Fatal(err)
	}

	return detail, suspect
}

func TestFileChecks(t *testing.T) {
	t.Parallel()

	e := newAuditEnv(nil)
	hd := radarr.MediaInfoResource{Resolution: "1920x1080", RunTime: "1:57:00", VideoCodec: "x264", AudioLanguages: "eng", AudioStreamCount: 1}
	alien := withMedia(new(film(1, "Alien", 1979)), "Alien (1979) Bluray-1080p.mkv", "Bluray-1080p", 1080, hd, 30000)
	alien.Runtime = 117
	alien.OriginalLanguage = &radarr.Language{Name: "English"}

	t.Run("runtime", func(t *testing.T) {
		t.Parallel()

		if _, bad := check(t, runtimeCheck(0), e, alien); bad {
			t.Error("a file running the film's length flagged")
		}
		short := *alien
		short.MovieFile = &radarr.MovieFileResource{MediaInfo: &radarr.MediaInfoResource{RunTime: "0:40:00"}}
		detail, bad := check(t, runtimeCheck(0), e, &short)
		if !bad || !strings.Contains(detail, "40 minutes, the film 117") || !strings.Contains(detail, "shorter") {
			t.Errorf("a 40 minute file of a 117 minute film = %q, %v", detail, bad)
		}
		nearly := *alien
		nearly.MovieFile = &radarr.MovieFileResource{MediaInfo: &radarr.MediaInfoResource{RunTime: "1:53:00"}}
		if _, bad := check(t, runtimeCheck(0), e, &nearly); bad {
			t.Error("four minutes off flagged: under the five minute floor")
		}
		if _, bad := check(t, runtimeCheck(50), e, &short); !bad {
			t.Error("66% off not flagged at a 50% tolerance")
		}
		if _, bad := check(t, runtimeCheck(70), e, &short); bad {
			t.Error("66% off flagged at a 70% tolerance")
		}
		none := film(9, "Nothing", 2000)
		if _, suspect, examined, _ := runtimeCheck(0)(context.Background(), e, &none); suspect || examined {
			t.Error("a film with no file was examined")
		}
	})

	t.Run("resolution", func(t *testing.T) {
		t.Parallel()

		if _, bad := check(t, resolutionCheck, e, alien); bad {
			t.Error("an honest 1080p file flagged")
		}
		fake := withMedia(new(film(2, "Blade Runner 2049", 2017)), "Blade Runner 2049 (2017) Bluray-2160p.mkv", "Bluray-720p", 720,
			radarr.MediaInfoResource{Resolution: "1280x720"}, 1)
		detail, bad := check(t, resolutionCheck, e, fake)
		if !bad || !strings.Contains(detail, "named 2160p") || !strings.Contains(detail, "1280x720 (720p)") || strings.Contains(detail, "and graded") {
			t.Errorf("a 720p file named 2160p = %q, %v", detail, bad)
		}
		if detail, bad := check(t, resolutionCheck, e, withMedia(new(film(3, "Contact", 1997)), "Contact (1997).mkv", "Bluray-2160p", 2160, radarr.MediaInfoResource{Resolution: "1920x1080"}, 1)); !bad || !strings.Contains(detail, "graded Bluray-2160p") {
			t.Errorf("a 1080p file graded 2160p = %q, %v", detail, bad)
		}
		if _, bad := check(t, resolutionCheck, e, withMedia(new(film(4, "Scope", 2010)), "Scope (2010) Bluray-1080p.mkv", "Bluray-1080p", 1080, radarr.MediaInfoResource{Resolution: "1920x800"}, 1)); bad {
			t.Error("a widescreen 1920x800 file flagged as not 1080p")
		}
	})

	t.Run("quality", func(t *testing.T) {
		t.Parallel()

		if _, bad := check(t, qualityCheck(0, 0), e, alien); bad {
			t.Error("a 1080p h264 file flagged")
		}
		dvd := withMedia(new(film(5, "Spirited Away", 2001)), "Spirited Away (2001) DVDRip XviD.avi", "DVD", 0,
			radarr.MediaInfoResource{Resolution: "720x480", VideoCodec: "XviD", RunTime: "2:05:00"}, 196304)
		detail, bad := check(t, qualityCheck(0, 0), e, dvd)
		if !bad || !strings.Contains(detail, "480 lines (720x480), below 720") || !strings.Contains(detail, "a legacy codec (XviD)") {
			t.Errorf("an XviD DVD rip = %q, %v", detail, bad)
		}
		if _, bad := check(t, qualityCheck(480, 0), e, withMedia(new(film(6, "Old", 1990)), "old.mkv", "DVD", 0, radarr.MediaInfoResource{Resolution: "720x480", VideoCodec: "x264"}, 1)); bad {
			t.Error("a 480 line h264 file flagged at a 480 minimum")
		}
		// the bitrate comes from the file's size over its runtime when the
		// probe recorded none: 30 kB over two hours is nothing
		if detail, bad := check(t, qualityCheck(0, 1000), e, alien); !bad || !strings.Contains(detail, "kbps, below 1000") {
			t.Errorf("a two hour file of 30 kB at a 1000 kbps floor = %q, %v", detail, bad)
		}
	})

	t.Run("language", func(t *testing.T) {
		t.Parallel()

		if _, bad := check(t, languageCheck(nil), e, alien); bad {
			t.Error("an English film with English audio flagged")
		}
		dub := withMedia(new(film(7, "Akira", 1988)), "Akira (1988) Bluray-1080p.mkv", "Bluray-1080p", 1080, radarr.MediaInfoResource{AudioLanguages: "eng", AudioStreamCount: 1}, 1)
		dub.OriginalLanguage = &radarr.Language{Name: "Japanese"}
		detail, bad := check(t, languageCheck(nil), e, dub)
		if !bad || !strings.Contains(detail, "audio is eng, none in Japanese (the film's original language)") {
			t.Errorf("a dub-only copy = %q, %v", detail, bad)
		}
		if _, bad := check(t, languageCheck([]string{"English", "jpn"}), e, dub); bad {
			t.Error("English audio flagged when English was one of the languages asked for")
		}
		untagged := *dub
		untagged.MovieFile = &radarr.MovieFileResource{MediaInfo: &radarr.MediaInfoResource{AudioLanguages: "eng", AudioStreamCount: 2}}
		if _, bad := check(t, languageCheck(nil), e, &untagged); bad {
			t.Error("a file with an untagged track said to lack a language")
		}
	})
}

func TestFilmAudits(t *testing.T) {
	t.Parallel()

	byName := map[string]filmCheck{}
	for _, a := range filmAudits {
		byName[a.name] = a.check
	}
	e := &auditEnv{profiles: map[int]*radarr.QualityProfileResource{4: profile()}}

	missing := film(1, "Alien³", 1992)
	missing.IsAvailable = new(true)
	if detail, bad := check(t, byName["audit_missing_files"], e, &missing); !bad || !strings.Contains(detail, "never searched") {
		t.Errorf("a monitored, available film with no file = %q, %v", detail, bad)
	}
	missing.IsAvailable = new(false)
	if _, bad := check(t, byName["audit_missing_files"], e, &missing); bad {
		t.Error("a film not yet available counted as missing")
	}

	unmonitored := film(2, "Alien Resurrection", 1997)
	unmonitored.Monitored = new(false)
	if _, bad := check(t, byName["audit_unmonitored"], e, &unmonitored); !bad {
		t.Error("an unmonitored film with no file not flagged")
	}

	below := withMedia(new(film(3, "Heat", 1995)), "Heat (1995) HDTV-720p.mkv", "HDTV-720p", 720, radarr.MediaInfoResource{}, 1)
	below.QualityProfileId = 4
	below.MovieFile.QualityCutoffNotMet = new(true)
	if detail, bad := check(t, byName["audit_cutoff_unmet"], e, below); !bad || detail != "HDTV-720p is below HD-1080p's cutoff of Bluray-1080p" {
		t.Errorf("cutoff unmet = %q, %v", detail, bad)
	}

	outside := withMedia(new(film(4, "Contact", 1997)), "Contact (1997) Bluray-2160p.mkv", "Bluray-2160p", 2160, radarr.MediaInfoResource{}, 1)
	outside.QualityProfileId = 4
	if detail, bad := check(t, byName["audit_profile_mismatch"], e, outside); !bad || !strings.Contains(detail, "Bluray-2160p is not a quality HD-1080p allows") {
		t.Errorf("profile mismatch = %q, %v", detail, bad)
	}
	if _, bad := check(t, byName["audit_profile_mismatch"], e, below); bad {
		t.Error("an allowed quality flagged as outside its profile")
	}

	wrong := film(5, "Dune", 1984)
	wrong.Path = "/m/Dune (2021)"
	if detail, bad := check(t, byName["audit_year_mismatch"], e, &wrong); !bad || detail != "the folder says 2021, the film is 1984" {
		t.Errorf("year mismatch = %q, %v", detail, bad)
	}
	near := film(6, "Arrival", 2016)
	near.Path = "/m/Arrival (2017)"
	if _, bad := check(t, byName["audit_year_mismatch"], e, &near); bad {
		t.Error("a year apart flagged: that is a release-date quibble")
	}

	gone := film(7, "Merged", 2001)
	gone.Status = radarr.MovieStatusTypeDeleted
	if _, bad := check(t, byName["audit_removed"], e, &gone); !bad {
		t.Error("a film TMDB deleted not flagged")
	}

	stub := film(8, "Stub", 2020)
	if detail, bad := check(t, metadataCheck(defaultMetadataFields), e, &stub); !bad || !strings.Contains(detail, "no overview") || !strings.Contains(detail, "no poster") {
		t.Errorf("missing metadata = %q, %v", detail, bad)
	}
	if _, err := metadataFieldsFor("colour"); err == nil {
		t.Error("an unknown metadata field accepted")
	}
}

func TestSizeCheck(t *testing.T) {
	t.Parallel()

	floor := 1.0
	e := &auditEnv{definitions: map[string]definitionRow{
		"WEBDL-1080p":  {Quality: "WEBDL-1080p", MinSize: floor},
		"Bluray-1080p": {Quality: "Bluray-1080p", MaxSize: new(10.0)},
	}}
	tiny := withMedia(new(film(1, "Gattaca", 1997)), "Gattaca (1997) WEBDL-1080p.mkv", "WEBDL-1080p", 1080, radarr.MediaInfoResource{}, 30000)
	tiny.Runtime = 107
	if detail, bad := check(t, sizeCheck, e, tiny); !bad || !strings.Contains(detail, "below WEBDL-1080p's minimum of 1") {
		t.Errorf("a tiny file = %q, %v", detail, bad)
	}
	huge := withMedia(new(film(2, "Bloat", 2000)), "Bloat (2000) Bluray-1080p.mkv", "Bluray-1080p", 1080, radarr.MediaInfoResource{}, 100*20<<20)
	huge.Runtime = 100
	if detail, bad := check(t, sizeCheck, e, huge); !bad || !strings.Contains(detail, "20.00 MB a minute, above Bluray-1080p's maximum of 10") {
		t.Errorf("a bloated file = %q, %v", detail, bad)
	}
	fine := withMedia(new(film(3, "Fine", 2000)), "Fine (2000) Bluray-1080p.mkv", "Bluray-1080p", 1080, radarr.MediaInfoResource{}, 100*5<<20)
	fine.Runtime = 100
	if _, bad := check(t, sizeCheck, e, fine); bad {
		t.Error("a file inside its limits flagged")
	}
}

func TestStuck(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		q    radarr.QueueResource
		want bool
	}{
		{radarr.QueueResource{Status: radarr.QueueStatusDownloading, TrackedDownloadState: radarr.TrackedDownloadStateDownloading, TrackedDownloadStatus: radarr.TrackedDownloadStatusOk}, false},
		{radarr.QueueResource{Status: radarr.QueueStatusCompleted, TrackedDownloadState: radarr.TrackedDownloadStateImportPending}, false},
		{radarr.QueueResource{Status: radarr.QueueStatusCompleted, TrackedDownloadState: radarr.TrackedDownloadStateImportBlocked}, true},
		{radarr.QueueResource{Status: radarr.QueueStatusFailed}, true},
		{radarr.QueueResource{Status: radarr.QueueStatusDownloading, TrackedDownloadStatus: radarr.TrackedDownloadStatusWarning}, true},
	} {
		if got := stuck(&tc.q); got != tc.want {
			t.Errorf("stuck(%s/%s/%s) = %v", tc.q.Status, tc.q.TrackedDownloadState, tc.q.TrackedDownloadStatus, got)
		}
	}
}
