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

var WithListVerbose = engine.WithListVerbose

func (c *Client) List(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	mgr := engine.NewListManager(c.store)
	return mgr.Run(ctx, opts...)
}

// ---------------------------------------------------------------------------
// LsSnapshot
// ---------------------------------------------------------------------------

type LsSnapshotOption = engine.LsSnapshotOption
type LsSnapshotResult = engine.LsSnapshotResult

var WithLsVerbose = engine.WithLsVerbose

// LsSnapshot lists a snapshot selected by latest, full hash, or unambiguous
// hash prefix. An ambiguous prefix is rejected.
func (c *Client) LsSnapshot(ctx context.Context, snapshotID string, opts ...LsSnapshotOption) (*LsSnapshotResult, error) {
	mgr := engine.NewLsSnapshotManager(c.store)
	return mgr.Run(ctx, snapshotID, opts...)
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

type FindOption = engine.FindOption
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
	WithFindPattern        = engine.WithFindPattern
	WithFindName           = engine.WithFindName
	WithFindPath           = engine.WithFindPath
	WithFindRegex          = engine.WithFindRegex
	WithFindIgnoreCase     = engine.WithFindIgnoreCase
	WithFindFileID         = engine.WithFindFileID
	WithFindContentHash    = engine.WithFindContentHash
	WithFindRef            = engine.WithFindRef
	WithFindType           = engine.WithFindType
	WithFindSize           = engine.WithFindSize
	WithFindNewer          = engine.WithFindNewer
	WithFindOlder          = engine.WithFindOlder
	WithFindSnapshots      = engine.WithFindSnapshots
	WithFindSource         = engine.WithFindSource
	WithFindTags           = engine.WithFindTags
	WithFindLatest         = engine.WithFindLatest
	WithFindSince          = engine.WithFindSince
	WithFindUntil          = engine.WithFindUntil
	WithFindGroupByContent = engine.WithFindGroupByContent
	WithFindMaxResults     = engine.WithFindMaxResults
	WithFindNoDelta        = engine.WithFindNoDelta
	WithFindVerbose        = engine.WithFindVerbose

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
func (c *Client) Find(ctx context.Context, opts ...FindOption) (*FindResult, error) {
	mgr := engine.NewFindManager(c.store)
	return mgr.Run(ctx, opts...)
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

var WithDiffVerbose = engine.WithDiffVerbose

// Diff compares snapshots selected by latest, full hashes, or unambiguous hash
// prefixes. An ambiguous prefix is rejected.
func (c *Client) Diff(ctx context.Context, snap1, snap2 string, opts ...DiffOption) (*DiffResult, error) {
	mgr := engine.NewDiffManager(c.store)
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
