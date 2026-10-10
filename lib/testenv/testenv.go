// Package testenv is what the two live suites share that is this
// application's own: the environment scripts/testenv.sh hands them, and how
// the record/replay proxy Radarr's calls to its metadata service go through
// is set up. The machinery is go-kt's (test/env and test/replayproxy).
//
// The acceptance suite (../../acceptance) drives the tools over MCP and the
// integration suite (../../integration) drives the generated SDK; each runs
// against a Radarr of its own, with cassettes of its own.
package testenv

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/replayproxy"
	"github.com/katbyte/go-kt/test/services/arrserver"
)

const (
	// DefaultProxyPort and DefaultIndexerPort are where the proxy and the
	// fake indexer listen when the environment names no port: the ports
	// scripts/testenv.sh starts a container pointed at when it is told none.
	DefaultProxyPort   = 17880
	DefaultIndexerPort = 17881

	// mediaMount is where the container sees the data directory's media.
	mediaMount = "/media/"
)

// environment is what scripts/testenv.sh writes, every variable of which
// begins RADARR_.
var environment = env.New("RADARR")

// Recording reports whether this run may call Radarr's real metadata
// service. RADARR_TEST_RECORD=1 fills in only the answers a cassette lacks,
// replaying the rest as recorded; RADARR_TEST_RECORD=all fetches every answer
// afresh (make record).
func Recording() bool { return environment.Recording() }

// Verifying reports whether to check the cassettes against the live service
// without rewriting them (RADARR_TEST_VERIFY).
func Verifying() bool { return environment.Verifying() }

// Mode is how a proxy treats a request, as the environment asks: replay
// unless recording (what is missing, or all of it afresh) or verifying.
func Mode() replayproxy.Mode { return environment.Mode() }

// Configured reports whether the container environment is present: a server,
// a token, and the directory its media is mounted from, which every fixture
// is laid out in.
func Configured() bool { return environment.Configured("TEST_DATA") }

// Server is the address of the Radarr under test, and Token its API key.
func Server() string { return environment.Server() }
func Token() string  { return environment.Token() }

// DataDir is the host path the container's /media is bind-mounted from, so a
// test can add or remove files and rescan. It is "" when RADARR_TEST_DATA is
// not set: joined onto nothing, the path was ./media, and a test that laid
// files out wrote into the checkout.
func DataDir() string { return environment.DataDir("media") }

// ConfigPath is where a path under the container's /config, Radarr's own
// folder, is on the host: a test that needs Radarr to find a file it only
// writes when it updates itself lays one out there.
func ConfigPath(elem ...string) string {
	return environment.DataDir(append([]string{"config"}, elem...)...)
}

// HostPath is where a container path under /media is on the host.
func HostPath(containerPath string) string {
	return filepath.Join(DataDir(), strings.TrimPrefix(containerPath, mediaMount))
}

// Host is how the container reaches this process - the proxy, the fake
// indexer and the webhook receiver (RADARR_TEST_HOST): host.docker.internal,
// or the bridge gateway's address where a Linux docker has no such name.
func Host() string { return environment.Host() }

// IndexerPort is the port the fake indexer listens on
// (RADARR_TEST_INDEXER_PORT), with the webhook receiver on the one above.
func IndexerPort() (int, error) {
	v := environment.Get("TEST_INDEXER_PORT")
	if v == "" {
		return DefaultIndexerPort, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", environment.Var("TEST_INDEXER_PORT"), v, err)
	}

	return n, nil
}

// CassetteDir is where a suite's recordings live, under its testdata.
func CassetteDir() string { return filepath.Join("testdata", "cassettes") }

// Proxy is the record/replay proxy a suite runs for the length of its run,
// and what it saw once stopped.
type Proxy = env.Proxy

// servicesHost is where Radarr asks its makers' own services for the time,
// its news and whether there is an update, with its version and the
// machine's architecture in the query: a recording would match one machine
// and one release, and a recorded time is a day wrong a day later. So the
// suites answer it themselves, with Services.
const servicesHost = "radarr.servarr.com"

// Services stands in for servicesHost: it answers the time with the time it
// is, and whether there is an update, and the list of releases, with none.
// Once a build is two weeks old Radarr's health check asks about an update,
// and an answer it cannot read ends the whole check, leaving no health
// recorded at all - so a suite that passed for a release's first fortnight
// failed from then on. What Radarr asked that it had no answer for is in its
// Report, for a suite to fail on at its end.
var Services = arrserver.New()

// StartProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA scripts/testenv.sh minted and
// mounted into the container, with its cassettes under cassetteDir, and
// proves the container can reach it, saying on stderr what the container
// sees of the network.
func StartProxy(ctx context.Context, cassetteDir string) (*Proxy, error) {
	// Radarr reaching itself is not metadata traffic, and go-kt answers the container's own addresses without a recording
	opts := replayproxy.Options{CassetteDir: cassetteDir}
	if environment.Get("TEST_PROXY_PORT") == "" {
		opts.Addr = ":" + strconv.Itoa(DefaultProxyPort)
	}

	proxy, err := environment.StartProxy(ctx, opts)
	if err != nil {
		return nil, err
	}
	proxy.Serve(servicesHost, Services)

	return proxy, nil
}
