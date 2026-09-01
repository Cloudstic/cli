package cloudstic

import (
	"context"

	"github.com/cloudstic/cli/internal/engine"
)

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

type CheckOption = engine.CheckOption
type CheckResult = engine.CheckResult
type CheckError = engine.CheckError

var (
	WithReadData    = engine.WithReadData
	WithSnapshotRef = engine.WithSnapshotRef
)

// Check verifies the integrity of the repository by walking the full
// reference chain (snapshots → HAMT nodes → filemeta → content → chunks)
// and checking that every referenced object can be read.
// With WithReadData(), chunk data is re-hashed for byte-level verification.
func (c *Client) Check(ctx context.Context, opts ...CheckOption) (*CheckResult, error) {
	// Verify the repository, not a local copy of it. A cached object is a
	// verified copy of what was fetched at some earlier point, which is
	// evidence about the cache and none at all about what the store holds
	// now — so a check reading through one would report a rotted repository
	// healthy. See storelayer.DiskCacheStore.BypassReads, which nests, so two
	// checks overlapping on one client cannot end each other's bypass.
	// Guarded because objectCache is an interface: a nil one has no method to
	// call, where the nil *DiskCacheStore it used to hold was safe to call.
	if c.objectCache != nil {
		defer c.objectCache.BypassReads()()
	}

	mgr := engine.NewCheckManager(c.engineDeps())
	return mgr.Run(ctx, opts...)
}
