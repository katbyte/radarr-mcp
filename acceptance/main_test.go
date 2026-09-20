//go:build integration

// Package acceptance covers every tool against a real Radarr running in
// Docker - the API wrappers, the audits and the journeys between them - so
// response shapes, query encoding and Radarr's own behaviour are checked
// against the thing radarr-mcp actually talks to rather than a stub.
//
// Fixtures are built through the tools themselves (rootfolder_add,
// movie_add), so the setup is part of the coverage; the fake indexer and the
// blackhole download client, which no tool adds, go in through the SDK.
//
//	make testacc-acceptance                        # container started and torn down
//
//	eval "$(scripts/testenv.sh up)"                # or drive it by hand
//	go test -tags integration ./acceptance/...
//	scripts/testenv.sh down
package acceptance

import "testing"

func TestMain(m *testing.M) { testMain(m) }
