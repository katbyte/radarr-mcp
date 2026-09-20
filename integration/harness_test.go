//go:build integration

// The harness: scripts/testenv.sh brings the container up and exports
// RADARR_SERVER, RADARR_TOKEN and RADARR_TEST_*; runSuite builds the SDK
// client, starts the provider proxy the container's HTTPS_PROXY points at,
// the fake indexer and the webhook receiver, runs the suite, removes what the
// fixtures created, and checks every write operation was called and that
// what each write answered is what its definition says.
//
// What is deliberately not exercised, and why:
//
//   - POST /api/v3/system/restart and /shutdown: they take the container out
//     from under the rest of the suite.
//   - POST /api/v3/system/backup/restore/{id} and restore/upload: a restore
//     replaces the database the suite is using and restarts Radarr.
//   - Updates (GET /api/v3/update answers, but installing one is a command
//     that replaces the binary): the image pins its version, and
//     radarr.servarr.com, where updates come from, is answered empty by the
//     proxy.
//   - Downloading through a real client: the download client is a Usenet
//     Blackhole, which is folders on disk, so a "download" is a file the test
//     puts in its watch folder. That is the whole of what Radarr sees of a
//     real client too.
//   - Notifications, import lists and metadata consumers that call out to a
//     third party (Plex, Trakt, Discord, ...): the webhook goes to a receiver
//     in this process, the import list is Radarr's own "another Radarr" list
//     pointed at itself, and the metadata consumers write files, not calls.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
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
	"github.com/katbyte/radarr-mcp/lib/client"
	"github.com/katbyte/radarr-mcp/lib/providerproxy"
	"github.com/katbyte/radarr-mcp/lib/radarr"
)

// The root folders, as the container sees them, and the Usenet Blackhole's
// folders: a grab's .nzb lands in the first, and what appears in the second
// is a finished download.
const (
	moviesRoot  = "/media/movies"
	messyRoot   = "/media/messy"
	nzbFolder   = "/media/downloads/nzb"
	watchFolder = "/media/downloads/complete"
	// hd1080 is the HD-1080p quality profile Radarr creates on first start
	hd1080 = 4
)

// filmFixture is a film the suite adds, from the catalogue scripts/testenv.sh
// lays out.
type filmFixture struct {
	Title  string
	Year   int
	TmdbID int
	// Path is the film's folder as the container sees it; File is the file
	// in it, "" when the folder holds none (or does not exist).
	Path string
	File string
}

var (
	alien       = filmFixture{"Alien", 1979, 348, moviesRoot + "/Alien (1979)", "Alien (1979) Bluray-1080p.mkv"}
	aliens      = filmFixture{"Aliens", 1986, 679, moviesRoot + "/Aliens (1986)", "Aliens (1986) Bluray-1080p.mkv"}
	bladeRunner = filmFixture{"Blade Runner", 1982, 78, moviesRoot + "/Blade Runner (1982)", "Blade Runner (1982) Bluray-1080p.mkv"}
	// dune1984 is the wrong match in the messy folder: Dune (1984) in a
	// folder named for 2021
	dune1984 = filmFixture{"Dune", 1984, 841, messyRoot + "/Dune (2021)", "Dune (2021) Bluray-1080p.mkv"}
	// alien3 has no folder on disk: the film the grab and import tests fetch
	alien3 = filmFixture{"Alien³", 1992, 8077, messyRoot + "/Alien³ (1992)", ""}
)

var films = []filmFixture{alien, aliens, bladeRunner, dune1984, alien3}

var (
	sdk        *radarr.Client
	proxy      *providerproxy.Proxy
	indexer    *fakeindexer.Indexer
	hooks      *webhooks
	requests   = &recorder{seen: map[string]bool{}}
	proxyMiss  []string
	proxyDrift []providerproxy.Drift
)

// recording reports whether this run should call the real metadata service
// and refresh the cassettes, rather than replay them.
func recording() bool { return os.Getenv("RADARR_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live service
// without rewriting them.
func verifying() bool { return os.Getenv("RADARR_TEST_VERIFY") != "" }

// configured reports whether the container environment is present.
func configured() bool { return os.Getenv("RADARR_SERVER") != "" && os.Getenv("RADARR_TOKEN") != "" }

// dataDir is the host path the container's /media is bind-mounted from.
func dataDir() string { return filepath.Join(os.Getenv("RADARR_TEST_DATA"), "media") }

// hostPath is where a container path under /media is on the host.
func hostPath(containerPath string) string {
	return filepath.Join(dataDir(), strings.TrimPrefix(containerPath, "/media/"))
}

// port reads a port from the environment, with a default.
func port(env string, dflt int) (int, error) {
	v := os.Getenv(env)
	if v == "" {
		return dflt, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", env, v, err)
	}

	return n, nil
}

// containerHost is how the container reaches this process: the proxy, the
// fake indexer and the webhook receiver.
func containerHost() string {
	if h := os.Getenv("RADARR_TEST_HOST"); h != "" {
		return h
	}

	return "host.docker.internal"
}

// runSuite connects, starts the proxy, the fake indexer and the webhook
// receiver, runs the tests, removes the fixtures, and returns the exit code.
func runSuite(m *testing.M) int {
	if !configured() {
		return m.Run() // every test skips
	}
	var err error
	if sdk, err = radarr.New(os.Getenv("RADARR_SERVER"), os.Getenv("RADARR_TOKEN")); err != nil {
		fmt.Fprintln(os.Stderr, "sdk client:", err)
		return 1
	}
	// every request the suite makes goes through the recorder, which is how
	// the write coverage is checked
	sdk.Client.HTTPClient = &http.Client{Timeout: 2 * time.Minute, Transport: requests}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		return 1
	}
	if err := startListeners(); err != nil {
		stopProxy()
		fmt.Fprintln(os.Stderr, "listeners:", err)
		return 1
	}

	code := m.Run()
	removeFixtures()
	stopProxy()
	_ = indexer.Close()
	hooks.close()

	if len(proxyMiss) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(proxyMiss))
		for _, m := range proxyMiss {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "run `make record` to capture them")
		if code == 0 {
			code = 1
		}
	}
	if len(proxyDrift) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(proxyDrift))
		for _, d := range proxyDrift {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record` to accept them")
		if code == 0 {
			code = 1
		}
	}
	if problems := writeShapes(); len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "\nwrite answers: %d problem(s):\n", len(problems))
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		code = 1
	}
	if code == 0 && !filtered() {
		problems, summary := writeCoverage()
		if len(problems) > 0 {
			fmt.Fprintf(os.Stderr, "\nwrite coverage: %d problem(s):\n", len(problems))
			for _, p := range problems {
				fmt.Fprintln(os.Stderr, "  "+p)
			}
			code = 1
		} else {
			fmt.Fprintln(os.Stderr, summary)
		}
	}

	return code
}

// startProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA scripts/testenv.sh minted and
// mounted into the container.
func startProxy() error {
	p, err := port("RADARR_TEST_PROXY_PORT", 17980)
	if err != nil {
		return err
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
		Addr:        ":" + strconv.Itoa(p),
		// Radarr itself, and radarr.servarr.com: Radarr asks it the time and
		// its news with its version and architecture in the query, so a
		// recording would match one machine and one release, and a recorded
		// time is a day wrong a day later
		IgnoreHosts: append(containerAddresses(), "radarr.servarr.com"),
	}
	if ca := os.Getenv("RADARR_TEST_PROXY_CA"); ca != "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	if proxy, err = providerproxy.New(opts); err != nil {
		return err
	}

	return checkReachable(p)
}

func stopProxy() {
	if proxy == nil {
		return
	}
	proxyMiss = proxy.Misses()
	proxyDrift = proxy.Drifts()
	if err := proxy.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy close:", err)
	}
	proxy = nil
}

// startListeners starts the fake indexer and, on the port above it, the
// webhook receiver the notification tests point Radarr at (which also
// passes /indexer through to the fake indexer, holding it when told).
func startListeners() error {
	p, err := port("RADARR_TEST_INDEXER_PORT", 17981)
	if err != nil {
		return err
	}
	if indexer, err = fakeindexer.Start(":"+strconv.Itoa(p), containerHost()); err != nil {
		return fmt.Errorf("the fake indexer: %w", err)
	}
	if hooks, err = startWebhooks(p+1, p); err != nil {
		_ = indexer.Close()
		return fmt.Errorf("the webhook receiver: %w", err)
	}

	return nil
}

// filtered reports whether the run was narrowed with -run, when the coverage
// of the whole surface cannot be judged.
func filtered() bool {
	for _, a := range os.Args {
		if strings.HasPrefix(a, "-test.run") {
			return true
		}
	}

	return false
}

// recorder is the SDK client's transport: it records the method and path of
// every request, for writeCoverage, and what every write answered, for
// writeShapes.
type recorder struct {
	mu     sync.Mutex
	seen   map[string]bool
	writes []answer
}

// answer is what one write answered.
type answer struct {
	method, path string
	status       int
	body         []byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen[req.Method+" "+req.URL.EscapedPath()] = true
	r.mu.Unlock()

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || req.Method == http.MethodGet {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	r.writes = append(r.writes, answer{method: req.Method, path: req.URL.EscapedPath(), status: resp.StatusCode, body: body})
	r.mu.Unlock()

	return resp, nil
}

func (r *recorder) answers() []answer {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.writes)
}

func (r *recorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.seen))
	for k := range r.seen {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// webhooks receives the Webhook notification Radarr sends, and remembers
// each event it was sent. On the same port, under /indexer, it passes
// requests through to the fake indexer: an indexer Radarr is given there is
// the fake indexer, except that it can be held unanswered (hold), and a
// search waiting on it holds the command that is searching.
type webhooks struct {
	srv     *http.Server
	mu      sync.Mutex
	got     []string
	gate    chan struct{}
	waiting int
}

func startWebhooks(p, indexerPort int) (*webhooks, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", ":"+strconv.Itoa(p))
	if err != nil {
		return nil, err
	}
	w := &webhooks{}
	fake := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(indexerPort))})
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.got = append(w.got, string(body))
		w.mu.Unlock()
		w.wait()
		rw.WriteHeader(http.StatusOK)
	})
	mux.Handle("/indexer/", http.StripPrefix("/indexer", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.wait()
		fake.ServeHTTP(rw, r)
	})))
	w.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: mux}
	go func() { _ = w.srv.Serve(ln) }()

	return w, nil
}

// wait holds a request while the receiver is holding them.
func (w *webhooks) wait() {
	w.mu.Lock()
	gate := w.gate
	if gate != nil {
		w.waiting++
	}
	w.mu.Unlock()
	if gate == nil {
		return
	}
	select {
	case <-gate:
	case <-time.After(2 * time.Minute):
	}
	w.mu.Lock()
	w.waiting--
	w.mu.Unlock()
}

// hold keeps every request from now on - a webhook, or a search of the held
// indexer - waiting for an answer, for up to two minutes, until the function
// it returns is called.
func (w *webhooks) hold() (release func()) {
	w.mu.Lock()
	defer w.mu.Unlock()

	gate := make(chan struct{})
	w.gate = gate
	var once sync.Once

	return func() {
		once.Do(func() {
			w.mu.Lock()
			w.gate = nil
			w.mu.Unlock()
			close(gate)
		})
	}
}

// held is how many requests are waiting for an answer.
func (w *webhooks) held() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.waiting
}

// url is where Radarr is told to send webhooks.
func (*webhooks) url(p int) string {
	return "http://" + net.JoinHostPort(containerHost(), strconv.Itoa(p)) + "/hook"
}

// heldIndexer is the fake indexer as Radarr is given it through the
// receiver, where a search of it can be held.
func heldIndexer(t *testing.T, name string) radarr.IndexerResource {
	t.Helper()

	p, err := port("RADARR_TEST_INDEXER_PORT", 17981)
	if err != nil {
		t.Fatal(err)
	}
	ix := fakeIndexer()
	ix.Name = name
	ix.Fields[0].Value = "http://" + net.JoinHostPort(containerHost(), strconv.Itoa(p+1)) + "/indexer"

	return ix
}

// received returns the bodies received so far.
func (w *webhooks) received() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.got)
}

func (w *webhooks) close() {
	if w == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = w.srv.Shutdown(ctx)
}

// webhookURL is where the notification tests point Radarr.
func webhookURL(t *testing.T) string {
	t.Helper()

	p, err := port("RADARR_TEST_INDEXER_PORT", 17981)
	if err != nil {
		t.Fatal(err)
	}

	return hooks.url(p + 1)
}

// library is what the fixtures created: the root folders and the films, by
// tmdb id.
type library struct {
	roots map[string]int
	films map[int]radarr.MovieResource
}

var (
	libOnce     sync.Once
	lib         library
	errFixtures error
)

// skipUnlessUp skips a test when there is no container, and returns its
// context.
func skipUnlessUp(t *testing.T) context.Context {
	t.Helper()

	if sdk == nil {
		t.Skip("RADARR_SERVER and RADARR_TOKEN are not set; run: eval \"$(scripts/testenv.sh up)\"")
	}

	return t.Context()
}

// fixtures returns the library the suite shares, creating it on first use:
// the two root folders and every film in films, the ones with a file on disk
// waited for until Radarr has found it. The fixtures are created lazily by
// whichever test runs first and removed after the last (removeFixtures), so
// one test's cleanup cannot pull them out from under another.
func fixtures(t *testing.T) library {
	t.Helper()

	ctx := skipUnlessUp(t)
	libOnce.Do(func() { lib, errFixtures = createFixtures(context.WithoutCancel(ctx)) })
	if errFixtures != nil {
		t.Fatalf("creating the fixtures: %v", errFixtures)
	}

	return lib
}

func createFixtures(ctx context.Context) (library, error) {
	l := library{roots: map[string]int{}, films: map[int]radarr.MovieResource{}}

	roots, err := sdk.GetRootfolder(ctx)
	if err != nil {
		return l, err
	}
	for _, r := range roots.Model {
		l.roots[r.Path] = r.Id
	}
	for _, path := range []string{moviesRoot, messyRoot} {
		if l.roots[path] != 0 {
			continue
		}
		res, postErr := sdk.PostRootfolder(ctx, radarr.RootFolderResource{Path: path})
		if postErr != nil {
			return l, fmt.Errorf("adding root folder %s: %w", path, postErr)
		}
		l.roots[path] = res.Model.Id
	}

	existing, err := sdk.GetMovie(ctx, radarr.GetMovieOperationOptions{})
	if err != nil {
		return l, err
	}
	for i := range existing.Model {
		l.films[existing.Model[i].TmdbId] = existing.Model[i]
	}
	for _, f := range films {
		if _, ok := l.films[f.TmdbID]; ok {
			continue
		}
		res, err := sdk.PostMovie(ctx, radarr.MovieResource{
			Title: f.Title, Year: f.Year, TmdbId: f.TmdbID, Path: f.Path, QualityProfileId: hd1080,
			Monitored: new(true), MinimumAvailability: radarr.MovieStatusTypeReleased,
			AddOptions: &radarr.AddMovieOptions{SearchForMovie: new(false), Monitor: radarr.MonitorTypesMovieOnly},
		})
		if err != nil {
			return l, fmt.Errorf("adding %s: %w", f.Title, err)
		}
		l.films[f.TmdbID] = *res.Model
	}

	// Radarr refreshes a new film and scans its folder in the background;
	// the films with a file are ready once it has found it
	for _, f := range films {
		if f.File == "" {
			continue
		}
		id := l.films[f.TmdbID].Id
		var m *radarr.MovieResource
		if !poll(2*time.Minute, func() bool {
			res, err := sdk.GetMovieById(ctx, id)
			if err != nil {
				return false
			}
			m = res.Model
			return isTrue(m.HasFile) && m.MovieFile != nil && m.MovieFile.MediaInfo != nil
		}) {
			return l, fmt.Errorf("%s never found its file", f.Title)
		}
		l.films[f.TmdbID] = *m
	}

	return l, nil
}

// film is a fixture film as Radarr has it now, once the test has the
// fixtures (fixtures, or downloads).
func film(ctx context.Context, t *testing.T, f filmFixture) radarr.MovieResource {
	t.Helper()

	m, ok := lib.films[f.TmdbID]
	if !ok {
		t.Fatalf("%s is not among the fixtures, or the test has not asked for them", f.Title)
	}

	return *must(sdk.GetMovieById(ctx, m.Id)).Model
}

// removeFixtures deletes what the fixtures created, after the last test: the
// films (never their files, which the next run lays out again anyway), the
// root folders, and the fake indexer and download client.
func removeFixtures() {
	ctx := context.Background()
	if errFixtures == nil && lib.films != nil {
		for tmdb := range lib.films {
			m := lib.films[tmdb]
			if _, err := sdk.DeleteMovieById(ctx, m.Id, radarr.DeleteMovieByIdOperationOptions{DeleteFiles: new(false)}); err != nil && !client.IsNotFound(err) {
				fmt.Fprintf(os.Stderr, "removing %s: %v\n", m.Title, err)
			}
		}
		for path, id := range lib.roots {
			if _, err := sdk.DeleteRootfolderById(ctx, id); err != nil && !client.IsNotFound(err) {
				fmt.Fprintf(os.Stderr, "removing root folder %s: %v\n", path, err)
			}
		}
	}
	if dl.indexer != 0 {
		_, _ = sdk.DeleteIndexerById(ctx, dl.indexer)
	}
	if dl.client != 0 {
		_, _ = sdk.DeleteDownloadclientById(ctx, dl.client)
	}
}

// downloading is the fake indexer and the Usenet Blackhole, added once for
// the tests that search, grab and import.
type downloading struct {
	indexer, client int
}

var (
	dlOnce       sync.Once
	dl           downloading
	errDownloads error
)

// background is the release the fake indexer always has in its feed:
// Radarr tests an indexer before saving it, and a Newznab one fails the
// test when its feed is empty.
var background = fakeindexer.Release{Title: "Radarr.MCP.Test.Pattern.2020.1080p.WEB-DL.x264-NOBODY", Size: 4 << 30}

// fakeIndexer is the fake indexer as Radarr is given it.
func fakeIndexer() radarr.IndexerResource {
	return radarr.IndexerResource{
		Name: "SDK Indexer", Implementation: "Newznab", ConfigContract: "NewznabSettings", Protocol: radarr.DownloadProtocolUsenet,
		EnableRss: new(true), EnableAutomaticSearch: new(true), EnableInteractiveSearch: new(true), Priority: 25,
		Fields: []radarr.Field{
			{Name: "baseUrl", Value: indexer.BaseURL()},
			{Name: "apiPath", Value: "/api"},
			{Name: "apiKey", Value: indexer.APIKey},
			{Name: "categories", Value: []int{2000, 2030, 2040, 2045}},
		},
	}
}

// blackhole is the Usenet Blackhole download client.
func blackhole(name string) radarr.DownloadClientResource {
	return radarr.DownloadClientResource{
		Name: name, Implementation: "UsenetBlackhole", ConfigContract: "UsenetBlackholeSettings", Protocol: radarr.DownloadProtocolUsenet,
		Enable: new(true), Priority: 1, RemoveCompletedDownloads: new(true), RemoveFailedDownloads: new(true),
		Fields: []radarr.Field{{Name: "nzbFolder", Value: nzbFolder}, {Name: "watchFolder", Value: watchFolder}},
	}
}

// downloads adds the fake indexer and the blackhole on first use.
func downloads(t *testing.T) downloading {
	t.Helper()

	ctx := context.WithoutCancel(skipUnlessUp(t))
	fixtures(t)
	dlOnce.Do(func() {
		indexer.Offer(background)
		ix, err := sdk.PostIndexer(ctx, fakeIndexer(), radarr.PostIndexerOperationOptions{})
		if err != nil {
			errDownloads = fmt.Errorf("adding the fake indexer at %s: %w", indexer.BaseURL(), err)
			return
		}
		dl.indexer = ix.Model.Id
		mediaMkdir(hostPath(nzbFolder))
		mediaMkdir(hostPath(watchFolder))
		c, err := sdk.PostDownloadclient(ctx, blackhole("SDK Blackhole"), radarr.PostDownloadclientOperationOptions{})
		if err != nil {
			errDownloads = fmt.Errorf("adding the blackhole: %w", err)
			return
		}
		dl.client = c.Model.Id
	})
	if errDownloads != nil {
		t.Fatal(errDownloads)
	}

	return dl
}

// must unwraps a call that must not fail. Go only allows a multi-value call
// as a function's sole argument, so this cannot also take *testing.T - it
// panics instead, which the test framework reports as a failure. That is the
// right severity here: if the server will not answer, nothing downstream is
// meaningful.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

// poll calls f every half second until it returns true or the timeout
// passes, and reports whether it did.
func poll(timeout time.Duration, f func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func isTrue(b *bool) bool { return b != nil && *b }

// command runs a Radarr command and waits for it to finish, failing the test
// when it fails or does not finish.
func command(ctx context.Context, t *testing.T, body string) radarr.CommandResource {
	t.Helper()

	started := must(sdk.PostCommand(ctx, []byte(body))).Model
	var cmd *radarr.CommandResource
	if !poll(2*time.Minute, func() bool {
		cmd = must(sdk.GetCommandById(ctx, started.Id)).Model
		return cmd.Status == radarr.CommandStatusCompleted || cmd.Status == radarr.CommandStatusFailed || cmd.Status == radarr.CommandStatusAborted
	}) {
		t.Fatalf("%s did not finish: %s", started.Name, cmd.Status)
	}
	if cmd.Status != radarr.CommandStatusCompleted {
		t.Fatalf("%s %s: %s %s", cmd.Name, cmd.Status, cmd.Message, cmd.Exception)
	}

	return *cmd
}

// mediaMkdir makes a directory under the bind-mounted media tree that
// Radarr's own user can write in: the mode MkdirAll is asked for is filtered
// by the umask, chmod's is not.
func mediaMkdir(dir string) {
	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container writes it as another user
		panic(err)
	}
	root := dataDir()
	for p := dir; strings.HasPrefix(p, root) && p != root; p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o777) //nolint:gosec // same
	}
}

// copyFile copies a fixture's file to a new place under the media tree, as
// a file Radarr has not seen, and backdates it: a blackhole takes a download
// to be finished only once nothing in it has changed for a while.
func copyFile(t *testing.T, from, to string) {
	t.Helper()

	data, err := os.ReadFile(hostPath(from))
	if err != nil {
		t.Fatal(err)
	}
	mediaMkdir(filepath.Dir(hostPath(to)))
	if err := os.WriteFile(hostPath(to), data, 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	_ = os.Chmod(hostPath(to), 0o666) //nolint:gosec // same
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(hostPath(to), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Dir(hostPath(to)), old, old); err != nil {
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
	out, err := exec.Command("docker", "inspect", "-f", //nolint:gosec,noctx // a one-off probe of the suite's own container before it runs
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil
	}

	return strings.Fields(string(out))
}

// checkReachable proves, from inside the container, that Radarr can reach
// the provider proxy: a Radarr that cannot fails every lookup with a timeout
// of its own, which reads as dozens of unrelated failures rather than the
// one plumbing problem it is.
func checkReachable(p int) error {
	name := os.Getenv("RADARR_TEST_CONTAINER")
	if name == "" {
		return nil
	}
	script := fmt.Sprintf("command -v nc >/dev/null || exit 3; nc -z -w 5 %s %d", containerHost(), p)
	out, err := exec.Command("docker", "exec", name, "sh", "-c", script).CombinedOutput() //nolint:gosec,noctx // the test's own container
	switch {
	case err == nil, strings.Contains(fmt.Sprint(err), "exit status 3"):
		return nil
	default:
		return fmt.Errorf("%s cannot reach the provider proxy on %s:%d, so every metadata lookup will time out: %w: %s", name, containerHost(), p, err, out)
	}
}

// sdkKey is the API key the suite runs with, which the self-pointing import
// list is given too.
func sdkKey() string { return os.Getenv("RADARR_TOKEN") }
