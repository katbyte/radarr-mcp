//go:build integration

// Package integration tests the generated SDK, lib/radarr, against a real
// Radarr running in Docker: that every operation's request is one Radarr
// accepts, and that its answer is the status and the shape the definitions
// say. The generated tests prove the SDK does what the definitions describe;
// this proves the definitions describe the server.
//
// Two layers do it. The read sweep (sweep_test.go) calls every GET the
// definitions hold and decodes the answer strictly, so a field the server
// sends that the document does not declare is a failure rather than a value
// silently dropped. The write tests call every POST, PUT and DELETE through
// a create, read, update and delete of their resource, and the harness
// records every request made and what it answered: an operation neither
// called nor listed in notExercised (coverage_test.go) fails the suite, and
// so does a write whose answer its definition does not describe.
//
//	make testacc-integration                       # container started and torn down
//
//	eval "$(RADARR_TEST_CONTAINER=radarr-mcp-integration RADARR_TEST_PORT=17978 \
//	  RADARR_TEST_PROXY_PORT=17980 RADARR_TEST_INDEXER_PORT=17981 scripts/testenv.sh up)"
//	go test -tags integration ./integration/...    # or drive it by hand
package integration

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(runSuite(m)) }
