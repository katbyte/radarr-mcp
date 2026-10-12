package tools

import (
	"strconv"

	"github.com/katbyte/go-kt/lock"
	"github.com/katbyte/radarr-mcp/sdk/radarr"
)

// The calls of one turn are kept from undoing each other with go-kt's locks.
// The MCP server runs a turn's calls at once, and an edit of a quality
// profile, of a quality's size limits or of the naming settings reads the
// record and sends it back whole: two edits of one profile, each changing its
// own setting, would each send the profile as it was before the other, and
// one change would be lost. Pointing a film at a folder sends the film back
// whole too, and would undo an edit of it made beside it.
//
// An edit locks the record it has resolved, and only then reads what it will
// write back. The locks are a set of this server's own (registry.locks),
// shared by every session it serves; Radarr's web app, or another radarr-mcp,
// is not held back.

// namingLock is the lock on the naming settings, which are one record.
const namingLock = "naming"

// profileLock is the quality profile with this id, for an edit that has
// resolved it and not yet read the record it will send back.
func profileLock(id int) lock.Thing {
	return lock.ID[radarr.QualityProfileResource](strconv.Itoa(id))
}

// definitionLock is the size limits of the quality with this definition id.
func definitionLock(id int) lock.Thing {
	return lock.ID[radarr.QualityDefinitionResource](strconv.Itoa(id))
}

// movieLocks are the films with these ids: an edit of a film holds it, so
// that pointing it at a folder, which sends the whole film back, cannot be
// read before the edit and written after it.
func movieLocks(ids ...radarr.MovieID) []lock.Thing {
	things := make([]lock.Thing, 0, len(ids))
	for _, id := range ids {
		things = append(things, lock.ID[radarr.MovieResource](strconv.Itoa(int(id))))
	}

	return things
}

// locking is this server's set of locks, made when the first is asked for.
func (r *registry) locking() *lock.Set {
	r.locksOnce.Do(func() { r.locks = lock.NewSet() })

	return r.locks
}
