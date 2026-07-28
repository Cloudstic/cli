package cloudstic

import (
	"context"

	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/storelayer"
)

// ---------------------------------------------------------------------------
// Prune
// ---------------------------------------------------------------------------

type PruneOption = engine.PruneOption
type PruneResult = engine.PruneResult

var (
	WithPruneDryRun  = engine.WithPruneDryRun
	WithPruneVerbose = engine.WithPruneVerbose
)

func (c *Client) Prune(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	mgr := engine.NewPruneManager(c.store, c.reporter)
	result, err := mgr.Run(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if !result.DryRun {
		c.stampWriteFormat(ctx)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Forget
// ---------------------------------------------------------------------------

type ForgetOption = engine.ForgetOption
type ForgetResult = engine.ForgetResult

var (
	WithPrune         = engine.WithPrune
	WithDryRun        = engine.WithDryRun
	WithForgetVerbose = engine.WithForgetVerbose
	WithKeepLast      = engine.WithKeepLast
	WithKeepHourly    = engine.WithKeepHourly
	WithKeepDaily     = engine.WithKeepDaily
	WithKeepWeekly    = engine.WithKeepWeekly
	WithKeepMonthly   = engine.WithKeepMonthly
	WithKeepYearly    = engine.WithKeepYearly
	WithGroupBy       = engine.WithGroupBy
	WithFilterTag     = engine.WithFilterTag
	WithFilterSource  = engine.WithFilterSource
	WithFilterAccount = engine.WithFilterAccount
	WithFilterPath    = engine.WithFilterPath
)

type PolicyResult = engine.PolicyResult

// PolicyGroupResult holds the policy evaluation result for a single group of
// snapshots, as returned in PolicyResult.Groups.
type PolicyGroupResult = engine.PolicyGroupResult

// GroupKey identifies a group of snapshots for policy application.
type GroupKey = engine.GroupKey

// KeepReason pairs a snapshot with the reasons it was kept.
type KeepReason = engine.KeepReason

// SnapshotEntry is a snapshot loaded for policy evaluation, as referenced by
// KeepReason and by ListResult.Snapshots.
type SnapshotEntry = engine.SnapshotEntry

func (c *Client) Forget(ctx context.Context, snapshotID string, opts ...ForgetOption) (*ForgetResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	result, err := mgr.Run(ctx, snapshotID, opts...)
	if err != nil {
		return nil, err
	}
	if !result.DryRun {
		c.stampWriteFormat(ctx)
	}
	return result, nil
}

func (c *Client) ForgetPolicy(ctx context.Context, opts ...ForgetOption) (*PolicyResult, error) {
	mgr := engine.NewForgetManager(c.store, c.reporter)
	result, err := mgr.RunPolicy(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if !result.DryRun {
		c.stampWriteFormat(ctx)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// BreakLock
// ---------------------------------------------------------------------------

type RepoLock = engine.RepoLock

// ErrRepoLocked means Backup, Restore, or Prune could not proceed because the
// repository is held by another operation. Use errors.Is(err, ErrRepoLocked)
// to detect the condition and prompt the caller toward BreakLock.
var ErrRepoLocked = engine.ErrRepoLocked

// ErrPlaintextObject reports that an encrypted repository contains an object
// that is not ciphertext. Use errors.Is(err, ErrPlaintextObject) to tell this
// apart from a decryption failure: the object was never encrypted, rather than
// encrypted with a key you do not hold.
var ErrPlaintextObject = storelayer.ErrPlaintextObject

func (c *Client) BreakLock(ctx context.Context) ([]*RepoLock, error) {
	return engine.BreakRepoLock(ctx, c.store)
}
