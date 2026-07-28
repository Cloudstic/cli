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
	WithReadData     = engine.WithReadData
	WithCheckVerbose = engine.WithCheckVerbose
	WithSnapshotRef  = engine.WithSnapshotRef
)

// Check verifies the integrity of the repository by walking the full
// reference chain (snapshots → HAMT nodes → filemeta → content → chunks)
// and checking that every referenced object can be read.
// With WithReadData(), chunk data is re-hashed for byte-level verification.
func (c *Client) Check(ctx context.Context, opts ...CheckOption) (*CheckResult, error) {
	mgr := engine.NewCheckManager(c.store, c.reporter, c.hmacKey)
	return mgr.Run(ctx, opts...)
}
