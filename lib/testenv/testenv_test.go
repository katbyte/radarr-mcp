package testenv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/test/replayproxy"
)

// The environment is read as .scripts/testenv.sh writes it, under this
// application's own names: a data dir that is not set is "", not ./media, so
// a suite with none skips rather than writing into the checkout, and the
// data dir is part of being configured.
func TestEnvironment(t *testing.T) {
	t.Setenv("RADARR_TEST_DATA", "")
	if got := DataDir(); got != "" {
		t.Errorf("DataDir() with nothing set = %q, want \"\"", got)
	}
	t.Setenv("RADARR_TEST_DATA", "/x/testenv/tools")
	if got := DataDir(); got != "/x/testenv/tools/media" {
		t.Errorf("DataDir() = %q", got)
	}
	if got := HostPath("/media/movies/Alien (1979)"); got != "/x/testenv/tools/media/movies/Alien (1979)" {
		t.Errorf("HostPath = %q", got)
	}
	if got := CassetteDir(); got != "testdata/cassettes" {
		t.Errorf("CassetteDir() = %q", got)
	}

	for k, v := range map[string]string{"RADARR_SERVER": "http://x", "RADARR_TOKEN": "t", "RADARR_TEST_DATA": ""} {
		t.Setenv(k, v)
	}
	if Configured() {
		t.Error("Configured() with no data dir")
	}
	t.Setenv("RADARR_TEST_DATA", "/x")
	if !Configured() || Server() != "http://x" || Token() != "t" {
		t.Errorf("Configured() = %v, Server() = %q, Token() = %q", Configured(), Server(), Token())
	}
	t.Setenv("RADARR_SERVER", "")
	if Configured() {
		t.Error("Configured() with no server")
	}
}

// How the container reaches this process, and the port the fake indexer is
// on, are the test script's to say, with its own defaults when it has not.
func TestHostAndIndexerPort(t *testing.T) {
	t.Setenv("RADARR_TEST_HOST", "")
	t.Setenv("RADARR_TEST_INDEXER_PORT", "")
	if got := Host(); got != "host.docker.internal" {
		t.Errorf("Host() with nothing set = %q", got)
	}
	if got, err := IndexerPort(); err != nil || got != DefaultIndexerPort {
		t.Errorf("IndexerPort() with nothing set = %d, %v", got, err)
	}

	t.Setenv("RADARR_TEST_HOST", "172.17.0.1")
	t.Setenv("RADARR_TEST_INDEXER_PORT", "17981")
	if got := Host(); got != "172.17.0.1" {
		t.Errorf("Host() = %q", got)
	}
	if got, err := IndexerPort(); err != nil || got != 17981 {
		t.Errorf("IndexerPort() = %d, %v", got, err)
	}

	t.Setenv("RADARR_TEST_INDEXER_PORT", "nope")
	if _, err := IndexerPort(); err == nil {
		t.Error("IndexerPort() took a port that is no number")
	}
}

// What a run does with the metadata service is read from the two variables
// the suites and make record set.
func TestMode(t *testing.T) {
	// set here as well as in the loop, so that this is seen to be a test
	// that cannot run beside others
	t.Setenv("RADARR_TEST_RECORD", "")
	for _, c := range []struct {
		record, verify string
		want           replayproxy.Mode
		recording      bool
	}{
		{"", "", replayproxy.Replay, false},
		{"1", "", replayproxy.Record, true},
		{"all", "", replayproxy.Rerecord, true},
		{"", "1", replayproxy.Verify, false},
	} {
		t.Setenv("RADARR_TEST_RECORD", c.record)
		t.Setenv("RADARR_TEST_VERIFY", c.verify)
		if Mode() != c.want || Recording() != c.recording || Verifying() != (c.verify != "") {
			t.Errorf("RADARR_TEST_RECORD=%q RADARR_TEST_VERIFY=%q: mode %v, recording %v, verifying %v", c.record, c.verify, Mode(), Recording(), Verifying())
		}
	}
}

// Radarr's questions of the host it calls home to are answered by the
// stand-in the proxy is given for it: whether there is an update, with none,
// in the shape Radarr reads, whatever the branch and whatever it says of
// itself; the time; and nothing it was asked goes unanswered.
func TestServices(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/v1/update/master?version=6.4.4.10685&os=linux&arch=arm64&runtime=netcore", "/v1/update/develop", "/v1/update/master/changes?version=6.4.4.10685", "/v1/time"} {
		rec := httptest.NewRecorder()
		Services.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+servicesHost+target, http.NoBody))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s = %d %q, want an answer", target, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	Services.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+servicesHost+"/v1/update/master", http.NoBody))
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Errorf("whether there is an update = %q, want none", rec.Body.String())
	}
	if report := Services.Report(); report != "" {
		t.Errorf("the stand-in had no answer for something Radarr asks: %s", report)
	}
}
