//go:build integration

// The harness: scripts/testenv.sh brings the container up and exports
// RADARR_SERVER, RADARR_TOKEN and RADARR_TEST_*, and everything here drives
// it through the MCP tools rather than the HTTP API, so building the
// fixtures is itself a test of rootfolder_add and movie_add.
package acceptance

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/radarr-mcp/internal/fakeindexer"
	"github.com/katbyte/radarr-mcp/lib/providerproxy"
	"github.com/katbyte/radarr-mcp/lib/radarr"
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

// The messy films, by title, and the defect each carries (scripts/testenv.sh
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
	ctx     context.Context
	session *mcp.ClientSession
	sdk     *radarr.Client
	ready   bool
	proxy   *providerproxy.Proxy
	indexer *fakeindexer.Indexer
)

// recording reports whether this run should call the real metadata service
// and refresh the cassettes, rather than replay them.
func recording() bool { return os.Getenv("RADARR_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live service
// without rewriting them.
func verifying() bool { return os.Getenv("RADARR_TEST_VERIFY") != "" }

// configured reports whether the container environment is present.
func configured() bool { return os.Getenv("RADARR_SERVER") != "" && os.Getenv("RADARR_TOKEN") != "" }

// dataDir is the host path the container's /media is bind-mounted from, so a
// test can add or remove files and rescan.
func dataDir() string { return filepath.Join(os.Getenv("RADARR_TEST_DATA"), "media") }

// hostPath is where a container path under /media is on the host.
func hostPath(containerPath string) string {
	return filepath.Join(dataDir(), strings.TrimPrefix(containerPath, "/media/"))
}

// testMain connects, starts the provider proxy and the fake indexer, seeds
// the fixtures, and runs.
func testMain(m *testing.M) {
	if !configured() {
		os.Exit(m.Run()) // every test skips
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}
	if err := start(); err != nil {
		stopProxy()
		fmt.Fprintln(os.Stderr, "acceptance setup:", err)
		os.Exit(1)
	}

	code := m.Run()
	stopProxy()
	if indexer != nil {
		_ = indexer.Close()
	}

	// a replay miss means a test ran against a 502 rather than a recording, so
	// say so loudly even when the assertions happened to survive it
	if misses := proxyMisses; len(misses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(misses))
		for _, m := range misses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "run `make record` to capture them")
		if code == 0 {
			code = 1
		}
	}

	// every registered tool must have been called by something above. Only a
	// whole-suite run can say that, so a -run filter skips the check.
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		missing, err := uncovered()
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "\ntool coverage: could not list tools:", err)
			code = 1
		case len(missing) > 0:
			fmt.Fprintf(os.Stderr, "\n%d registered tool(s) are never called by this suite:\n", len(missing))
			for _, name := range missing {
				fmt.Fprintln(os.Stderr, "  "+name)
			}
			fmt.Fprintln(os.Stderr, "every tool needs a test; add one or remove the tool")
			code = 1
		}
	}

	// drift is only collected under RADARR_TEST_VERIFY: the service still
	// answers, but no longer in the shape Radarr decodes
	if drifts := proxyDrifts; len(drifts) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(drifts))
		for _, d := range drifts {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record` to accept them")
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

var (
	proxyMisses []string
	proxyDrifts []providerproxy.Drift
)

var (
	calledMu sync.Mutex
	called   = map[string]bool{}
)

// uncovered names the registered tools no test called. A tool that is only
// listed is not tested, so adding one without a test fails the suite rather
// than quietly widening the untested surface.
func uncovered() ([]string, error) {
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}

	calledMu.Lock()
	defer calledMu.Unlock()

	var missing []string
	for _, tool := range res.Tools {
		if !called[tool.Name] {
			missing = append(missing, tool.Name)
		}
	}
	slices.Sort(missing)

	return missing, nil
}

// startProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA scripts/testenv.sh minted and
// mounted into the container.
func startProxy() error {
	port := 17880
	if v := os.Getenv("RADARR_TEST_PROXY_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("RADARR_TEST_PROXY_PORT=%q: %w", v, err)
		}
		port = n
	}

	mode := providerproxy.Replay
	switch {
	case recording():
		mode = providerproxy.Record
	case verifying():
		mode = providerproxy.Verify
	}

	opts := providerproxy.Options{
		Mode:        mode,
		CassetteDir: filepath.Join("testdata", "cassettes"),
		// every interface and both stacks: the container reaches this through
		// host.docker.internal, which docker maps to the host gateway
		Addr: ":" + strconv.Itoa(port),
		// Radarr itself, and radarr.servarr.com: Radarr checks its clock and
		// its news there with its version and architecture in the query, so
		// a recording would match one machine and one release, and a
		// recorded clock is a day wrong a day later
		IgnoreHosts: append(containerAddresses(), "radarr.servarr.com"),
	}
	if ca := os.Getenv("RADARR_TEST_PROXY_CA"); ca != "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	p, err := providerproxy.New(opts)
	if err != nil {
		return err
	}
	proxy = p

	return checkReachable(port)
}

func stopProxy() {
	if proxy == nil {
		return
	}
	proxyMisses = proxy.Misses()
	proxyDrifts = proxy.Drifts()
	if err := proxy.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy close:", err)
	}
	proxy = nil
}

func start() error {
	var err error
	sdk, err = radarr.New(os.Getenv("RADARR_SERVER"), os.Getenv("RADARR_TOKEN"))
	if err != nil {
		return err
	}

	port := os.Getenv("RADARR_TEST_INDEXER_PORT")
	if port == "" {
		port = "17881"
	}
	host := os.Getenv("RADARR_TEST_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	if indexer, err = fakeindexer.Start(":"+port, host); err != nil {
		return fmt.Errorf("the fake indexer: %w", err)
	}

	ctx = context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "radarr-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, sdk, tools.Options{EnableDelete: true}); err != nil {
		return err
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		return err
	}
	if session, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil); err != nil {
		return err
	}
	ready = true

	return seed()
}

// seed builds the fixtures: the root folders and every film through the
// tools, then the indexer and download client through the SDK. It is
// idempotent - a film already in the library is left alone - so the suite
// can be run again against a container that is still up.
func seed() error {
	folders, err := invoke("rootfolder_list", nil)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, row := range rowsOf(folders["root_folders"]) {
		have[str(row["path"])] = true
	}
	for _, root := range []string{moviesRoot, messyRoot} {
		if have[root] {
			continue
		}
		if _, err := invoke("rootfolder_add", map[string]any{"path": root}); err != nil {
			return err
		}
	}

	library, err := invoke("movie_list", map[string]any{"limit": 500})
	if err != nil {
		return err
	}
	held := map[float64]bool{}
	for _, row := range rowsOf(library["movies"]) {
		held[num0(row["tmdb_id"])] = true
	}
	for _, f := range fixtures() {
		if held[float64(f.TmdbID)] {
			continue
		}
		args := map[string]any{
			"movie": "tmdb:" + strconv.Itoa(f.TmdbID), "quality_profile": f.Profile, "root_folder": f.Root, "monitored": f.Monitored,
		}
		if f.Folder != "" {
			args["folder"] = f.Folder
		}
		if _, err := invoke("movie_add", args); err != nil {
			return fmt.Errorf("adding %s: %w", titleYear(f.Title, f.Year), err)
		}
	}

	return addDownloading()
}

// background is the release the fake indexer always has in its feed.
var background = fakeindexer.Release{Title: "Radarr.MCP.Test.Pattern.2020.1080p.WEB-DL.x264-NOBODY", Size: 4 << 30}

// addDownloading gives Radarr the fake indexer and a Usenet Blackhole to
// grab into, which no tool adds: they are the plumbing the release, queue
// and history tools are tested through.
func addDownloading() error {
	// Radarr tests an indexer before saving it, and a Newznab one fails the
	// test when its feed is empty; a real one never is, so this one always
	// offers a release no film in the library matches
	indexer.Offer(background)
	indexers, err := sdk.GetIndexer(ctx)
	if err != nil {
		return err
	}
	if len(indexers.Model) == 0 {
		_, err := sdk.PostIndexer(ctx, radarr.IndexerResource{
			Name: "Fake Indexer", Implementation: "Newznab", ConfigContract: "NewznabSettings", Protocol: radarr.DownloadProtocolUsenet,
			EnableRss: new(true), EnableAutomaticSearch: new(true), EnableInteractiveSearch: new(true), Priority: 25,
			Fields: []radarr.Field{
				{Name: "baseUrl", Value: indexer.BaseURL()},
				{Name: "apiPath", Value: "/api"},
				{Name: "apiKey", Value: indexer.APIKey},
				{Name: "categories", Value: []int{2000, 2030, 2040, 2045}},
			},
		}, radarr.PostIndexerOperationOptions{})
		if err != nil {
			return fmt.Errorf("adding the fake indexer at %s: %w", indexer.BaseURL(), err)
		}
	}
	clients, err := sdk.GetDownloadclient(ctx)
	if err != nil {
		return err
	}
	if len(clients.Model) == 0 {
		_, err := sdk.PostDownloadclient(ctx, radarr.DownloadClientResource{
			Name: "Blackhole", Implementation: "UsenetBlackhole", ConfigContract: "UsenetBlackholeSettings", Protocol: radarr.DownloadProtocolUsenet,
			Enable: new(true), Priority: 1, RemoveCompletedDownloads: new(true), RemoveFailedDownloads: new(true),
			Fields: []radarr.Field{
				{Name: "nzbFolder", Value: nzbFolder},
				{Name: "watchFolder", Value: watchFolder},
			},
		}, radarr.PostDownloadclientOperationOptions{})
		if err != nil {
			return fmt.Errorf("adding the blackhole download client: %w", err)
		}
	}

	return nil
}

// invoke calls a tool and returns its structured result. Every tool call in
// the suite comes through here, so this is also where coverage is recorded.
func invoke(name string, args map[string]any) (map[string]any, error) {
	calledMu.Lock()
	called[name] = true
	calledMu.Unlock()

	if args == nil {
		args = map[string]any{}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out, nil
}

// skipUnlessReady skips a test when the container is not configured.
func skipUnlessReady(t *testing.T) {
	t.Helper()

	if !ready {
		t.Skip("RADARR_SERVER and RADARR_TOKEN are not set; run: eval \"$(scripts/testenv.sh up)\"")
	}
}

// call invokes a tool, skipping the test when the container is not configured
// and failing it when the tool errors.
func call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	skipUnlessReady(t)
	out, err := invoke(name, args)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// callErr invokes a tool expecting it to fail, and returns the error message.
func callErr(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	skipUnlessReady(t)
	out, err := invoke(name, args)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: %v", name, out)
	}

	return err.Error()
}

// rowsOf pulls a list of objects out of a decoded JSON field, tolerating a
// missing one.
func rowsOf(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if row, ok := e.(map[string]any); ok {
			out = append(out, row)
		}
	}

	return out
}

// rows pulls a list of objects out of a decoded JSON field, failing when it
// is not one.
func rows(t *testing.T, v any, field string) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		row, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s contains %T, want objects", field, e)
		}
		out = append(out, row)
	}

	return out
}

// strs pulls a []string out of a decoded JSON field.
func strs(t *testing.T, v any, field string) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s contains %T, want strings", field, e)
		}
		out = append(out, s)
	}

	return out
}

// num pulls a JSON number out of a decoded field.
func num(t *testing.T, v any, field string) int {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T (%v), want a number", field, v, v)
	}

	return int(f)
}

// num0 is a JSON number, 0 when it was omitted.
func num0(v any) float64 {
	f, _ := v.(float64)
	return f
}

// str pulls a string out of a decoded field, "" when absent.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// object reads a nested object.
func object(t *testing.T, v any, field string) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T (%v), want an object", field, v, v)
	}

	return m
}

func titleYear(title string, year int) string {
	if year == 0 {
		return title
	}

	return fmt.Sprintf("%s (%d)", title, year)
}

// movieID finds a film's id by its title and year.
func movieID(t *testing.T, title string, year int) int {
	t.Helper()

	out := call(t, "movie_get", map[string]any{"movie": titleYear(title, year)})

	return num(t, out["id"], "id")
}

// findings returns the "Title (Year)" of each finding in an audit's
// worklist, sorted.
func findings(t *testing.T, out map[string]any) []string {
	t.Helper()

	var names []string
	for _, f := range rows(t, out["findings"], "findings") {
		names = append(names, titleYear(str(f["title"]), int(num0(f["year"]))))
	}
	slices.Sort(names)

	return names
}

// eventually polls f every half second for up to a minute, and reports
// whether it came true.
func eventually(f func() bool) bool {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}

	return f()
}

// mediaMkdir makes a directory under the bind-mounted media tree that
// Radarr's own user can write in, and mediaWrite writes a file there. The
// mode asked of MkdirAll and WriteFile is filtered by the process umask,
// which on Linux leaves a directory nobody but the test can write to - so
// Radarr cannot move a download it imports, or delete a file a test laid
// out. chmod is not filtered by the umask, so the mode asked for is the mode
// applied.
func mediaMkdir(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	root := dataDir()
	for p := dir; strings.HasPrefix(p, root) && p != root; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}
}

// copyFixture copies a fixture film's file to a new place under the media
// tree, for a test that needs a file Radarr has not seen.
func copyFixture(t *testing.T, from, to string) {
	t.Helper()

	data, err := os.ReadFile(hostPath(from)) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	mediaMkdir(t, filepath.Dir(hostPath(to)))
	if err := os.WriteFile(hostPath(to), data, 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	if err := os.Chmod(hostPath(to), 0o666); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}
}

// makeVideo writes a new film file of a runtime and size with ffmpeg, the way
// scripts/testenv.sh does, for a test that needs a file of its own.
func makeVideo(t *testing.T, containerPath string, minutes int, size string) {
	t.Helper()

	mediaMkdir(t, filepath.Dir(hostPath(containerPath)))
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-loglevel", "error", "-y", //nolint:gosec // fixed arguments and a test path
		"-f", "lavfi", "-i", "color=c=0x202030:s="+size+":r=1/10", "-f", "lavfi", "-t", "1", "-i", "anullsrc=r=8000:cl=mono",
		"-t", strconv.Itoa(minutes*60), "-c:v", "libx264", "-preset", "ultrafast", "-tune", "stillimage", "-crf", "51", "-g", "1000",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "8k", "-metadata:s:a:0", "language=eng", hostPath(containerPath))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	if err := os.Chmod(hostPath(containerPath), 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
}

// containerAddresses are the addresses Radarr reaches itself on, which go
// through the proxy because NO_PROXY is set before docker hands the
// container an address. The proxy answers those without a cassette.
func containerAddresses() []string {
	name := os.Getenv("RADARR_TEST_CONTAINER")
	if name == "" {
		return nil
	}
	out, err := exec.Command("docker", "inspect", "-f", //nolint:noctx // a one-off probe before the suite runs
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil // not our container to ask about
	}

	return strings.Fields(string(out))
}

// checkReachable proves, from inside the container, that Radarr can reach
// the provider proxy. A Radarr that cannot fails every lookup with a timeout
// of its own, which reads as dozens of unrelated failures rather than the
// one plumbing problem it is - so say it plainly, once, before the suite
// runs.
func checkReachable(port int) error {
	name := os.Getenv("RADARR_TEST_CONTAINER")
	if name == "" {
		return nil // not a container this suite started
	}
	host := os.Getenv("RADARR_TEST_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	// exit 3 says the image has no probe tool, which is not a failure
	script := fmt.Sprintf("command -v nc >/dev/null || exit 3; nc -z -w 5 %s %d", host, port)
	out, err := exec.Command("docker", "exec", name, "sh", "-c", script).CombinedOutput() //nolint:gosec,noctx // the test's own container
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "exit status 3"):
		return nil
	default:
		return fmt.Errorf("%s cannot reach the provider proxy on %s:%d, so every metadata lookup will time out: %w: %s", name, host, port, err, out)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
