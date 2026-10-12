//go:build acceptance

// The harness: .scripts/testenv.sh brings the container up and exports
// RADARR_SERVER, RADARR_TOKEN and RADARR_TEST_*, and everything here drives
// it through the MCP tools rather than the HTTP API, so building the
// fixtures is itself a test of rootfolder_add and movie_add.
package tools_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/radarr-mcp/lib/testenv"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/katbyte/radarr-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The root folders, as the container sees them.
const (
	moviesRoot = "/media/movies"
	messyRoot  = "/media/messy"
	// the Usenet Blackhole's folders: a grab's .nzb lands in the first, and
	// whatever appears in the second is a finished download
	nzbFolder   = "/media/downloads/nzb"
	watchFolder = "/media/downloads/complete"
)

// The quality profiles Radarr creates on first start, by name.
const (
	profileAny = "Any"
	profileHD  = "HD-1080p"
	profileUHD = "Ultra-HD"
)

// movieFixture is a film the suite adds and what it expects Radarr to make
// of it. The catalogue is shaped so every audit has both something to find
// and something it must leave alone.
type movieFixture struct {
	Title  string
	Year   int
	TmdbID int
	// Root and Folder are where the film's folder is; Folder empty means
	// the film has no folder on disk.
	Root, Folder string
	Profile      string
	Monitored    bool
	// Quality is the quality Radarr grades the file it finds, "" for none.
	Quality string
	// Runtime is TMDB's, in minutes.
	Runtime int
}

// clean are the films in /media/movies, every one named, graded and probed
// as it should be.
var clean = []movieFixture{
	{"Alien", 1979, 348, moviesRoot, "Alien (1979)", profileHD, true, "Bluray-1080p", 117},
	{"Aliens", 1986, 679, moviesRoot, "Aliens (1986)", profileHD, true, "Bluray-1080p", 137},
	{"Blade Runner", 1982, 78, moviesRoot, "Blade Runner (1982)", profileHD, true, "Bluray-1080p", 118},
	{"Dune", 2021, 438631, moviesRoot, "Dune (2021)", profileHD, true, "Bluray-1080p", 155},
	{"Dune: Part Two", 2024, 693134, moviesRoot, "Dune - Part Two (2024)", profileHD, true, "Bluray-1080p", 167},
	{"Princess Mononoke", 1997, 128, moviesRoot, "Princess Mononoke (1997)", profileHD, true, "Bluray-1080p", 134},
	{"Arrival", 2016, 329865, moviesRoot, "Arrival (2016)", profileHD, true, "Bluray-1080p", 116},
	{"The Thirteenth Floor", 1999, 1090, moviesRoot, "The Thirteenth Floor (1999)", profileHD, true, "Bluray-1080p", 100},
}

// The messy films, by title, and the defect each carries (.scripts/testenv.sh
// lays their files out).
const (
	messyDune         = "Dune (1984)"       // in "Dune (2021)": year mismatch, and not named for 1984
	messyInterstellar = "Interstellar"      // 169 minutes, the file 40
	messy2049         = "Blade Runner 2049" // named 2160p, the video 720p
	messySpirited     = "Spirited Away"     // an XviD DVD rip
	messyAkira        = "Akira"             // English audio only, of a Japanese film
	messyHeat         = "Heat"              // HDTV-720p under an HD-1080p cutoff
	messyMatrix       = "The Matrix"        // scene named
	messyContact      = "Contact"           // two copies; one untracked
	messyGattaca      = "Gattaca"           // too small for WEBDL-1080p, once it has a minimum
	missingAlien3     = "Alien³"            // monitored, no file
	unmonitoredAlien4 = "Alien Resurrection"
)

var messy = []movieFixture{
	{"Dune", 1984, 841, messyRoot, "Dune (2021)", profileHD, true, "Bluray-1080p", 136},
	{"Interstellar", 2014, 157336, messyRoot, "Interstellar (2014)", profileHD, true, "Bluray-1080p", 169},
	{"Blade Runner 2049", 2017, 335984, messyRoot, "Blade Runner 2049 (2017)", profileUHD, true, "Bluray-720p", 164},
	{"Spirited Away", 2001, 129, messyRoot, "Spirited Away (2001)", profileAny, true, "DVD", 125},
	{"Akira", 1988, 149, messyRoot, "Akira (1988)", profileHD, true, "Bluray-1080p", 124},
	{"Heat", 1995, 949, messyRoot, "Heat (1995)", profileHD, true, "HDTV-720p", 170},
	{"The Matrix", 1999, 603, messyRoot, "The Matrix (1999)", profileHD, true, "Bluray-1080p", 136},
	{"Contact", 1997, 686, messyRoot, "Contact (1997)", profileHD, true, "Bluray-2160p", 150},
	{"Gattaca", 1997, 782, messyRoot, "Gattaca (1997)", profileHD, true, "WEBDL-1080p", 107},
	{"Alien³", 1992, 8077, messyRoot, "", profileHD, true, "", 114},
	{"Alien Resurrection", 1997, 8078, messyRoot, "", profileHD, false, "", 109},
}

// fixtures is every film the suite adds.
func fixtures() []movieFixture { return append(slices.Clone(clean), messy...) }

// Folders on disk no film is added for.
const (
	unmappedRonin    = moviesRoot + "/Ronin (1998)"
	unmappedAlienCut = messyRoot + "/Alien (1979) Directors Cut"
)

var (
	ctx   context.Context
	sdk   *radarr.Client
	proxy *testenv.Proxy
	// indexers is the fake indexers' server, and indexer the one Radarr is
	// given
	indexers *newznab.Server
	indexer  *newznab.Indexer
	// suite drives the tools through a client session; until start connects
	// it, every call skips
	suite = &acc.Suite{NotReady: "RADARR_SERVER, RADARR_TOKEN and RADARR_TEST_DATA are not set; run: eval \"$(.scripts/testenv.sh up)\""}
)

// testMain connects, starts the provider proxy and the fake indexer, seeds
// the fixtures, and runs.
func testMain(m *testing.M) {
	if !testenv.Configured() {
		os.Exit(m.Run()) // every test skips
	}
	var err error
	if proxy, err = testenv.StartProxy(context.Background(), testenv.CassetteDir()); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}
	if err := start(); err != nil {
		_ = proxy.Stop()
		fmt.Fprintln(os.Stderr, "acceptance setup:", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := proxy.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	if indexers != nil {
		_ = indexers.Close()
	}

	// every registered tool must have answered something above, not only
	// refused it. Only a whole-suite run can say that, so a -run or -skip
	// filter skips the check.
	if acc.WholeRun() {
		report, err := suite.CoverageReport()
		if err != nil {
			fmt.Fprintln(os.Stderr, "\n"+err.Error())
			code = 1
		}
		if report != "" {
			fmt.Fprint(os.Stderr, report)
			code = 1
		}
	}

	// a key in an answer is said whichever tests ran: it takes no test to
	// think of the tool that shows one
	if shown := suite.LeakReport(); shown != "" {
		fmt.Fprint(os.Stderr, "\n"+shown)
		code = 1
	}

	// a replay miss means a test ran against a 502 rather than a recording,
	// a drift (collected under RADARR_TEST_VERIFY) that the service still
	// answers, but no longer in the shape Radarr decodes, and a question of
	// the host Radarr calls home to that its stand-in had no answer for that
	// Radarr has started asking something new: all are said loudly even when
	// the assertions happened to survive them
	if report := proxy.Report() + testenv.Services.Report(); report != "" {
		fmt.Fprint(os.Stderr, report)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

func start() error {
	var err error
	sdk, err = radarr.New(testenv.Server(), testenv.Token())
	if err != nil {
		return err
	}

	port, err := testenv.IndexerPort()
	if err != nil {
		return err
	}
	if indexers, err = newznab.New(newznab.Options{Addr: ":" + strconv.Itoa(port), PublicHost: testenv.Host(), Sites: []newznab.Site{{Name: indexerName, APIKey: indexerKey}}}); err != nil {
		return fmt.Errorf("the fake indexer: %w", err)
	}
	indexer = indexers.Indexer(indexerName)

	ctx = context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "radarr-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, sdk, tools.Options{EnableDelete: true}); err != nil {
		return err
	}
	connected, err := acc.Connect(ctx, srv)
	if err != nil {
		return err
	}
	connected.NotReady = suite.NotReady
	suite = connected
	// everything a tool answers or refuses with, all run long, is read for the keys the suite gives Radarr and for Radarr's own
	suite.Secret(indexerKey)
	suite.Secret(testenv.Token())

	return seed()
}

// seed builds the fixtures through the tools: the root folders, every film,
// and then the indexer and the download client. It is
// idempotent - a film already in the library is left alone - so the suite
// can be run again against a container that is still up.
func seed() error {
	folders, err := suite.Invoke("rootfolder_list", nil)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, row := range acc.RowsOf(folders["root_folders"]) {
		have[acc.Str(row["path"])] = true
	}
	for _, root := range []string{moviesRoot, messyRoot} {
		if have[root] {
			continue
		}
		if _, err := suite.Invoke("rootfolder_add", map[string]any{"path": root}); err != nil {
			return err
		}
	}

	library, err := suite.Invoke("movie_list", map[string]any{"limit": 500})
	if err != nil {
		return err
	}
	held := map[int]bool{}
	for _, row := range acc.RowsOf(library["movies"]) {
		held[acc.NumOr0(row["tmdb_id"])] = true
	}
	for _, f := range fixtures() {
		if held[f.TmdbID] {
			continue
		}
		args := map[string]any{
			"movie": "tmdb:" + strconv.Itoa(f.TmdbID), "quality_profile": f.Profile, "root_folder": f.Root, "monitored": f.Monitored,
		}
		if f.Folder != "" {
			args["folder"] = f.Folder
		}
		if _, err := suite.Invoke("movie_add", args); err != nil {
			return fmt.Errorf("adding %s: %w", titleYear(f.Title, f.Year), err)
		}
	}

	return addDownloading()
}

// background is the release the fake indexer always has in its feed.
var background = newznab.Release{Category: newznab.CategoryMoviesHD, Title: "Radarr.MCP.Test.Pattern.2020.1080p.WEB-DL.x264-NOBODY", Size: 4 << 30}

// addDownloading gives Radarr the fake indexer and a Usenet Blackhole to
// grab into, with the tools that set Radarr up: every search, grab, download
// and import the suite makes then runs through an indexer and a download
// client the tools added, each left switched on as a new one starts.
func addDownloading() error {
	// Radarr tests an indexer before saving it, and a Newznab one fails the
	// test when its feed is empty; a real one never is, so this one always
	// offers a release no film in the library matches
	if err := indexer.Offer(background); err != nil {
		return err
	}
	have, err := suite.Invoke("indexer_list", nil)
	if err != nil {
		return err
	}
	if len(acc.RowsOf(have["indexers"])) == 0 {
		settings := map[string]any{"baseUrl": indexer.URL(), "apiPath": "/api", "apiKey": indexerKey, "categories": []any{2000, 2030, 2040, 2045}}
		if _, err := suite.Invoke("indexer_add", map[string]any{"kind": "Newznab", "name": "Fake Indexer", "settings": settings}); err != nil {
			return fmt.Errorf("adding the fake indexer at %s: %w", indexer.URL(), err)
		}
	}
	clients, err := suite.Invoke("downloadclient_list", nil)
	if err != nil {
		return err
	}
	if len(acc.RowsOf(clients["download_clients"])) == 0 {
		settings := map[string]any{"nzbFolder": nzbFolder, "watchFolder": watchFolder}
		if _, err := suite.Invoke("downloadclient_add", map[string]any{"kind": "UsenetBlackhole", "name": "Blackhole", "settings": settings, "options": map[string]any{"priority": 1, "removeCompletedDownloads": true, "removeFailedDownloads": true}}); err != nil {
			return fmt.Errorf("adding the blackhole download client: %w", err)
		}
	}

	return nil
}

// skipUnlessReady skips a test when the container is not configured, for
// one that does something else before its first call.
func skipUnlessReady(t *testing.T) {
	t.Helper()

	if !suite.Ready {
		t.Skip(suite.NotReady)
	}
}

// indexerName is the fake indexer Radarr is given, which is its path on the
// indexers' server, and indexerKey the API key it asks of Radarr.
const (
	indexerName = "indexer"
	indexerKey  = "radarr-mcp-test-indexer"
)

func titleYear(title string, year int) string {
	if year == 0 {
		return title
	}

	return fmt.Sprintf("%s (%d)", title, year)
}

// movieID finds a film's id by its title and year.
func movieID(t *testing.T, title string, year int) int {
	t.Helper()

	out := suite.Call(t, "movie_get", map[string]any{"movie": titleYear(title, year)})

	return acc.Num(t, out["id"], "id")
}

// findings returns the "Title (Year)" of each finding in an audit's
// worklist, sorted.
func findings(t *testing.T, out map[string]any) []string {
	t.Helper()

	var names []string
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		names = append(names, titleYear(acc.Str(f["title"]), acc.NumOr0(f["year"])))
	}
	slices.Sort(names)

	return names
}

// copyFixture copies a fixture film's file to a new place under the media
// tree, for a test that needs a file Radarr has not seen.
func copyFixture(t *testing.T, from, to string) {
	t.Helper()

	data, err := os.ReadFile(testenv.HostPath(from)) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	env.Mkdir(t, testenv.DataDir(), filepath.Dir(testenv.HostPath(to)))
	env.WriteFile(t, testenv.HostPath(to), data)
}

// makeVideo writes a new film file of a runtime and size with ffmpeg, the way
// .scripts/testenv.sh does, for a test that needs a file of its own.
func makeVideo(t *testing.T, containerPath string, minutes int, size string) {
	t.Helper()

	env.Mkdir(t, testenv.DataDir(), filepath.Dir(testenv.HostPath(containerPath)))
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-loglevel", "error", "-y", //nolint:gosec // fixed arguments and a test path
		"-f", "lavfi", "-i", "color=c=0x202030:s="+size+":r=1/10", "-f", "lavfi", "-t", "1", "-i", "anullsrc=r=8000:cl=mono",
		"-t", strconv.Itoa(minutes*60), "-c:v", "libx264", "-preset", "ultrafast", "-tune", "stillimage", "-crf", "51", "-g", "1000",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "8k", "-metadata:s:a:0", "language=eng", testenv.HostPath(containerPath))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	if err := os.Chmod(testenv.HostPath(containerPath), 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
