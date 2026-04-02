package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// ForgetOption configures a forget operation.
type ForgetOption func(*forgetConfig)

type forgetConfig struct {
	prune      bool
	dryRun     bool
	verbose    bool
	policy     ForgetPolicy
	groupBy    string
	groupBySet bool
	filter     snapshotFilter
}

// WithPrune runs a prune pass after forgetting snapshots.
func WithPrune() ForgetOption {
	return func(cfg *forgetConfig) { cfg.prune = true }
}

// WithDryRun shows what would be removed without actually removing anything.
func WithDryRun() ForgetOption {
	return func(cfg *forgetConfig) { cfg.dryRun = true }
}

// WithForgetVerbose enables verbose output for the forget operation.
func WithForgetVerbose() ForgetOption {
	return func(cfg *forgetConfig) { cfg.verbose = true }
}

// WithKeepLast keeps the n most recent snapshots.
func WithKeepLast(n int) ForgetOption {
	return func(cfg *forgetConfig) { cfg.policy.KeepLast = n }
}

// WithKeepHourly keeps one snapshot per hour for the last n hours that have snapshots.
func WithKeepHourly(n int) ForgetOption {
	return func(cfg *forgetConfig) { cfg.policy.KeepHourly = n }
}

// WithKeepDaily keeps one snapshot per day for the last n days that have snapshots.
func WithKeepDaily(n int) ForgetOption {
	return func(cfg *forgetConfig) { cfg.policy.KeepDaily = n }
}

// WithKeepWeekly keeps one snapshot per ISO week for the last n weeks that have snapshots.
func WithKeepWeekly(n int) ForgetOption {
	return func(cfg *forgetConfig) { cfg.policy.KeepWeekly = n }
}

// WithKeepMonthly keeps one snapshot per month for the last n months that have snapshots.
func WithKeepMonthly(n int) ForgetOption {
	return func(cfg *forgetConfig) { cfg.policy.KeepMonthly = n }
}

// WithKeepYearly keeps one snapshot per year for the last n years that have snapshots.
func WithKeepYearly(n int) ForgetOption {
	return func(cfg *forgetConfig) { cfg.policy.KeepYearly = n }
}

// WithGroupBy sets the fields used to group snapshots for policy application.
// Comma-separated list of: source, account, path, tags. Empty string disables grouping.
func WithGroupBy(fields string) ForgetOption {
	return func(cfg *forgetConfig) { cfg.groupBy = fields; cfg.groupBySet = true }
}

// WithFilterTag restricts the policy to snapshots that have this tag.
func WithFilterTag(tag string) ForgetOption {
	return func(cfg *forgetConfig) { cfg.filter.tags = append(cfg.filter.tags, tag) }
}

// WithFilterSource restricts the policy to snapshots from this source type.
func WithFilterSource(source string) ForgetOption {
	return func(cfg *forgetConfig) { cfg.filter.source = source }
}

// WithFilterAccount restricts the policy to snapshots from this account.
func WithFilterAccount(account string) ForgetOption {
	return func(cfg *forgetConfig) { cfg.filter.account = account }
}

// WithFilterPath restricts the policy to snapshots from this path.
func WithFilterPath(path string) ForgetOption {
	return func(cfg *forgetConfig) { cfg.filter.path = path }
}

// ForgetResult holds the outcome of a forget operation.
type ForgetResult struct {
	Prune *PruneResult // nil when prune was not requested
}

// ForgetManager removes a snapshot and its index pointers, optionally pruning
// unreachable objects afterwards.
type ForgetManager struct {
	store    store.ObjectStore
	reporter ui.Reporter
	pruner   *PruneManager
}

func NewForgetManager(s store.ObjectStore, reporter ui.Reporter) *ForgetManager {
	return &ForgetManager{
		store:    s,
		reporter: reporter,
		pruner:   NewPruneManager(store.NewMeteredStore(s), reporter),
	}
}

// Run removes the snapshot identified by snapshotID.
func (fm *ForgetManager) Run(ctx context.Context, snapshotID string, opts ...ForgetOption) (*ForgetResult, error) {
	cfg := &forgetConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if snapshotID == "" {
		return nil, fmt.Errorf("snapshot id required")
	}

	targetRef, err := fm.resolveSnapshot(snapshotID)
	if err != nil {
		return nil, err
	}

	phase := fm.reporter.StartPhase("Forgetting snapshot", 0, false)
	if cfg.verbose {
		phase.Log(fmt.Sprintf("Forgetting %s", targetRef))
	}

	if err := fm.store.Delete(ctx, targetRef); err != nil {
		phase.Error()
		return nil, fmt.Errorf("delete snapshot %s: %w", targetRef, err)
	}

	// Update snapshot catalog (best-effort).
	RemoveFromSnapshotCatalog(fm.store, targetRef)

	if err := fm.fixupLatest(targetRef); err != nil {
		phase.Error()
		return nil, err
	}
	phase.Done()

	// Ensure changes to packstore catalog are persisted
	if err := fm.store.Flush(ctx); err != nil {
		return nil, fmt.Errorf("flush store: %w", err)
	}

	result := &ForgetResult{}
	if cfg.prune {
		pruneResult, err := fm.pruner.Run(ctx)
		if err != nil {
			return nil, err
		}
		result.Prune = pruneResult
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Snapshot resolution
// ---------------------------------------------------------------------------

func (fm *ForgetManager) resolveSnapshot(id string) (string, error) {
	if id == "latest" {
		ref, _ := resolveLatest(fm.store)
		if ref == "" {
			return "", fmt.Errorf("no latest snapshot found")
		}
		id = ref
	}
	if !strings.HasPrefix(id, "snapshot/") {
		id = "snapshot/" + id
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Latest fixup
// ---------------------------------------------------------------------------

// fixupLatest re-elects index/latest after a snapshot has been deleted.
// If the deleted ref was the latest, pick the remaining snapshot with the
// highest Seq. If no snapshots remain, delete index/latest.
func (fm *ForgetManager) fixupLatest(deletedRef string) error {
	curRef, _ := resolveLatest(fm.store)
	if curRef != deletedRef {
		return nil
	}

	entries, err := LoadSnapshotCatalog(fm.store)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return updateLatest(fm.store, "", 0)
	}

	best := entries[0]
	for _, e := range entries[1:] {
		if e.Snap.Seq > best.Snap.Seq {
			best = e
		}
	}
	return updateLatest(fm.store, best.Ref, best.Snap.Seq)
}

// ---------------------------------------------------------------------------
// Policy-based forget
// ---------------------------------------------------------------------------

// PolicyGroupResult holds the policy evaluation result for a single group.
type PolicyGroupResult struct {
	Key    GroupKey
	Keep   []KeepReason
	Remove []SnapshotEntry
}

// PolicyResult holds the outcome of a policy-based forget operation.
type PolicyResult struct {
	Groups []PolicyGroupResult
	Prune  *PruneResult
}

// RunPolicy applies a retention policy to all snapshots and removes those not
// matched by any keep rule. Use WithKeepLast, WithKeepDaily, etc. to configure.
func (fm *ForgetManager) RunPolicy(ctx context.Context, opts ...ForgetOption) (*PolicyResult, error) {
	cfg := &forgetConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.policy.IsEmpty() && cfg.filter.IsEmpty() {
		return nil, fmt.Errorf("empty policy: specify at least one -keep-* option or a tag/source/account/path filter")
	}

	entries, err := LoadSnapshotCatalog(fm.store)
	if err != nil {
		return nil, err
	}

	// Filter
	var filtered []SnapshotEntry
	for _, e := range entries {
		if matchesFilter(&e.Snap, cfg.filter) {
			filtered = append(filtered, e)
		}
	}

	// Group
	gf := defaultGroupFields()
	if cfg.groupBySet {
		gf = parseGroupBy(cfg.groupBy)
	}
	groups := groupSnapshots(filtered, gf)

	// Sort group keys for deterministic output
	keys := make([]GroupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].String() < keys[j].String()
	})

	// Apply policy per group
	result := &PolicyResult{}
	var toRemove []SnapshotEntry

	for _, key := range keys {
		group := groups[key]
		keep, remove := applyPolicy(group, cfg.policy)
		result.Groups = append(result.Groups, PolicyGroupResult{
			Key:    key,
			Keep:   keep,
			Remove: remove,
		})
		toRemove = append(toRemove, remove...)
	}

	if cfg.dryRun || len(toRemove) == 0 {
		return result, nil
	}

	// Batch-remove all snapshots
	phase := fm.reporter.StartPhase("Removing snapshots", int64(len(toRemove)), false)
	if err := fm.forgetBatch(ctx, toRemove); err != nil {
		phase.Error()
		return nil, err
	}
	phase.Done()

	if cfg.prune {
		pruneResult, err := fm.pruner.Run(ctx)
		if err != nil {
			return nil, err
		}
		result.Prune = pruneResult
	}

	return result, nil
}

// forgetBatch removes multiple snapshots and fixes up index/latest once.
func (fm *ForgetManager) forgetBatch(ctx context.Context, entries []SnapshotEntry) error {
	toRemove := make(map[string]bool, len(entries))
	for _, e := range entries {
		toRemove[e.Ref] = true
	}

	var removedRefs []string
	for _, e := range entries {
		_ = fm.store.Delete(ctx, e.Ref)
		removedRefs = append(removedRefs, e.Ref)
	}

	// Update snapshot catalog (best-effort).
	RemoveFromSnapshotCatalog(fm.store, removedRefs...)

	// Elect new latest from the remaining snapshots.
	remaining, err := LoadSnapshotCatalog(fm.store)
	if err != nil {
		return err
	}

	if len(remaining) == 0 {
		return updateLatest(fm.store, "", 0)
	}

	best := remaining[0]
	for _, e := range remaining[1:] {
		if e.Snap.Seq > best.Snap.Seq {
			best = e
		}
	}
	if err := updateLatest(fm.store, best.Ref, best.Snap.Seq); err != nil {
		return err
	}

	// Ensure changes to packstore catalog are persisted
	return fm.store.Flush(ctx)
}
