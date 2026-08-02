package cloudstic

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/internal/engine"
)

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

type ListOption = engine.ListOption
type ListResult = engine.ListResult

func (c *Client) List(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	mgr := engine.NewListManager(c.store, c.logWriter)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// LsSnapshot
// ---------------------------------------------------------------------------

type LsSnapshotOption = engine.LsSnapshotOption
type LsSnapshotResult = engine.LsSnapshotResult

// LsSnapshot lists a snapshot selected by latest, full hash, or unambiguous
// hash prefix. An ambiguous prefix is rejected.
func (c *Client) LsSnapshot(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	mgr := engine.NewLsSnapshotManager(c.store, c.logWriter)
	return mgr.Run(ctx, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

type FindQuery = engine.FindQuery
type FindResult = engine.FindResult
type FileMatch = engine.FileMatch
type FileVersion = engine.FileVersion
type SnapshotRef = engine.SnapshotRef
type SizeCompare = engine.SizeCompare
type SizeOp = engine.SizeOp

const (
	SizeAtLeast = engine.SizeAtLeast
	SizeAtMost  = engine.SizeAtMost
	SizeExactly = engine.SizeExactly
)

var (
	ParseSizeCompare = engine.ParseSizeCompare
	ParseFindTime    = engine.ParseFindTime
)

// Find locates files across the repository's snapshots without the caller
// having to know which snapshot holds them.
//
// Unlike every other read operation, Find takes a snapshot as *output* rather
// than input: it searches every snapshot by default, and reports for each
// matching file the versions it has had and the snapshots each version lives in.
//
// It is a pure read path — no lock is taken, nothing is written, and the
// repository format is not stamped.
//
// The query is a value rather than a list of options because it already was
// one: FindQuery is JSON-tagged and grouped into predicates, snapshot selectors
// and presentation, so twenty-one option constructors existed only to set its
// fields one at a time. Building it directly also makes a query serializable —
// storable, loggable, sendable — which a closure over a private struct is not.
// Use FindQuery.SetPattern for a positional pattern, which routes by shape.
func (c *Client) Find(ctx context.Context, q FindQuery) (*FindResult, error) {
	mgr := engine.NewFindManager(c.store, c.logWriter)
	return mgr.Run(ctx, q)
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

type DiffOption = engine.DiffOption
type DiffResult = engine.DiffResult

// FileChange is one change reported by Diff, between two snapshots.
type FileChange = engine.FileChange

// ChangeType describes the kind of change a FileChange represents.
type ChangeType = engine.ChangeType

const (
	ChangeAdded    = engine.ChangeAdded
	ChangeRemoved  = engine.ChangeRemoved
	ChangeModified = engine.ChangeModified
)

// Diff compares snapshots selected by latest, full hashes, or unambiguous hash
// prefixes. An ambiguous prefix is rejected.
func (c *Client) Diff(ctx context.Context, snap1, snap2 string, opts ...DiffOption) (*DiffResult, error) {
	mgr := engine.NewDiffManager(c.store, c.logWriter)
	return mgr.Run(ctx, snap1, snap2, opts...)
}

// ---------------------------------------------------------------------------
// Cat
// ---------------------------------------------------------------------------

// CatResult contains the raw data for an object key.
type CatResult struct {
	Key  string // The object key requested
	Data []byte // Raw object data (typically JSON)
}

// Cat fetches the raw data for one or more object keys from the repository.
// Object keys can be snapshot/<hash>, filemeta/<hash>, content/<hash>,
// node/<hash>, chunk/<hash>, config, index/latest, keys/<slot>, etc.
//
// This is useful for debugging, inspection, and understanding the internal
// structure of the repository.
func (c *Client) Cat(ctx context.Context, keys ...string) ([]*CatResult, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("at least one object key is required")
	}

	results := make([]*CatResult, 0, len(keys))
	for _, key := range keys {
		data, err := c.store.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("fetch object %q: %w", key, err)
		}
		if data == nil {
			return nil, fmt.Errorf("object not found: %q", key)
		}
		results = append(results, &CatResult{
			Key:  key,
			Data: data,
		})
	}
	return results, nil
}
