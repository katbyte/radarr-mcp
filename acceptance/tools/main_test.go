//go:build acceptance

// Package tools_test covers every tool against a real Radarr running in
// Docker - the API wrappers, the audits and the journeys between them - so
// response shapes, query encoding and Radarr's own behaviour are checked
// against the thing radarr-mcp actually talks to rather than a stub.
//
// Fixtures are built through the tools themselves (rootfolder_add,
// movie_add, indexer_add, downloadclient_add), so the setup is part of the
// coverage: the fake indexer and the blackhole download client every grab
// runs through are ones the tools added.
//
//	make testacc-tools                             # container started and torn down
//
//	eval "$(.scripts/testenv.sh up)"                # or drive it by hand
//	go test -tags acceptance ./acceptance/tools/...
//	.scripts/testenv.sh down
package tools_test

import (
	"testing"
)

func TestMain(m *testing.M) { testMain(m) }
