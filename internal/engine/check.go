package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

// CheckOption configures a check operation.
type CheckOption func(*checkConfig)

type checkConfig struct {
	readData    bool
	verbose     bool
	snapshotRef string
}

// WithReadData enables full byte-level verification: re-hash all chunk data
// and verify content manifests match their referenced chunks.
func WithReadData() CheckOption {
	return func(cfg *checkConfig) { cfg.readData = true }
}

// WithCheckVerbose logs each verified object.
func WithCheckVerbose() CheckOption {
	return func(cfg *checkConfig) { cfg.verbose = true }
}

// WithSnapshotRef limits the check to a single snapshot instead of all.
func WithSnapshotRef(ref string) CheckOption {
	return func(cfg *checkConfig) { cfg.snapshotRef = ref }
}

// CheckError describes a single integrity error found during a check.
type CheckError struct {
	Key     string // Object key (e.g. "chunk/abc123")
	Type    string // Error category: "missing", "read_error", "corrupt", "parse_error"
	Message string
}

func (e CheckError) String() string {
	return fmt.Sprintf("%s: %s: %s", e.Type, e.Key, e.Message)
}

// CheckResult holds the outcome of a check operation.
type CheckResult struct {
	SnapshotsChecked int
	ObjectsVerified  int
	Errors           []CheckError
}

// CheckManager verifies the integrity of a repository by walking the full
// reference chain and checking that every referenced object can be read.
type CheckManager struct {
	store    store.ObjectStore
	tree     *hamt.Tree
	reporter ui.Reporter
	verified map[string]bool
	hmacKey  []byte
}

// NewCheckManager creates a CheckManager.
func NewCheckManager(s store.ObjectStore, reporter ui.Reporter, hmacKey []byte) *CheckManager {
	return &CheckManager{
		store:    s,
		tree:     hamt.NewTree(hamt.NewTransactionalStore(s)),
		reporter: reporter,
		hmacKey:  hmacKey,
	}
}

// Run verifies the repository integrity.
func (cm *CheckManager) Run(ctx context.Context, opts ...CheckOption) (*CheckResult, error) {
	var cfg checkConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	cm.verified = make(map[string]bool)
	result := &CheckResult{}

	// Resolve which snapshots to check.
	snapRefs, err := cm.resolveSnapshots(ctx, cfg.snapshotRef)
	if err != nil {
		return nil, err
	}

	phase := cm.reporter.StartPhase("Checking repository integrity", int64(len(snapRefs)), false)

	for _, ref := range snapRefs {
		if err := cm.checkSnapshot(ctx, ref, result, &cfg, phase); err != nil {
			phase.Error()
			return nil, fmt.Errorf("check snapshot %s: %w", ref, err)
		}
		result.SnapshotsChecked++
		phase.Increment(1)
	}

	phase.Done()
	return result, nil
}

// resolveSnapshots returns the list of snapshot refs to check.
func (cm *CheckManager) resolveSnapshots(ctx context.Context, snapshotRef string) ([]string, error) {
	if snapshotRef != "" {
		ref := snapshotRef
		if ref == "latest" {
			data, err := cm.store.Get(ctx, "index/latest")
			if err != nil {
				return nil, fmt.Errorf("read index/latest: %w", err)
			}
			var idx core.Index
			if err := json.Unmarshal(data, &idx); err != nil {
				return nil, fmt.Errorf("parse index/latest: %w", err)
			}
			ref = idx.LatestSnapshot
		} else if !strings.HasPrefix(ref, "snapshot/") {
			ref = "snapshot/" + ref
		}
		return []string{ref}, nil
	}

	keys, err := cm.store.List(ctx, "snapshot/")
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	return keys, nil
}

// checkSnapshot verifies a single snapshot and its entire reference chain.
func (cm *CheckManager) checkSnapshot(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified[ref] {
		return nil
	}

	// 1. Read and parse the snapshot.
	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "read_error", Message: fmt.Sprintf("cannot read snapshot: %v", err),
		})
		return nil // continue checking other snapshots
	}
	cm.verified[ref] = true
	result.ObjectsVerified++
	if cfg.verbose {
		phase.Log(fmt.Sprintf("OK: %s", ref))
	}

	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse snapshot: %v", err),
		})
		return nil
	}

	// 2. Walk HAMT nodes — verify each node is readable.
	if err := cm.tree.NodeRefs(snap.Root, func(nodeRef string) error {
		return cm.verifyObject(ctx, nodeRef, result, cfg, phase)
	}); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: snap.Root, Type: "read_error", Message: fmt.Sprintf("cannot walk HAMT tree: %v", err),
		})
		return nil
	}

	// 3. Walk leaf entries — verify filemeta → content → chunks.
	if err := cm.tree.Walk(snap.Root, func(_, valueRef string) error {
		return cm.checkFileMeta(ctx, valueRef, result, cfg, phase)
	}); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: snap.Root, Type: "read_error", Message: fmt.Sprintf("cannot walk HAMT entries: %v", err),
		})
	}

	return nil
}

// verifyObject checks that an object can be read from the store.
func (cm *CheckManager) verifyObject(ctx context.Context, key string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified[key] {
		return nil
	}

	_, err := cm.store.Get(ctx, key)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: key, Type: "missing", Message: fmt.Sprintf("object not found or unreadable: %v", err),
		})
		cm.verified[key] = true
		return nil
	}

	cm.verified[key] = true
	result.ObjectsVerified++
	if cfg.verbose {
		phase.Log(fmt.Sprintf("OK: %s", key))
	}
	return nil
}

// checkFileMeta verifies a filemeta object and its content/chunk chain.
func (cm *CheckManager) checkFileMeta(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified[ref] {
		return nil
	}

	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: fmt.Sprintf("filemeta not found or unreadable: %v", err),
		})
		cm.verified[ref] = true
		return nil
	}
	cm.verified[ref] = true
	result.ObjectsVerified++
	if cfg.verbose {
		phase.Log(fmt.Sprintf("OK: %s", ref))
	}

	var meta core.FileMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse filemeta: %v", err),
		})
		return nil
	}

	if meta.ContentHash == "" {
		return nil // folder or file with no content
	}

	contentKey := meta.ContentRef
	if contentKey == "" {
		contentKey = meta.ContentHash
	}

	return cm.checkContent(ctx, "content/"+contentKey, result, cfg, phase)
}

// checkContent verifies a content object and its referenced chunks.
func (cm *CheckManager) checkContent(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified[ref] {
		return nil
	}

	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: fmt.Sprintf("content object not found or unreadable: %v", err),
		})
		cm.verified[ref] = true
		return nil
	}
	cm.verified[ref] = true
	result.ObjectsVerified++
	if cfg.verbose {
		phase.Log(fmt.Sprintf("OK: %s", ref))
	}

	var content core.Content
	if err := json.Unmarshal(data, &content); err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "parse_error", Message: fmt.Sprintf("cannot parse content: %v", err),
		})
		return nil
	}

	for _, chunkRef := range content.Chunks {
		if err := cm.checkChunk(ctx, chunkRef, result, cfg, phase); err != nil {
			return err
		}
	}
	return nil
}

// checkChunk verifies a chunk object. With --read-data, it also verifies the
// hash of the chunk data matches the key.
func (cm *CheckManager) checkChunk(ctx context.Context, ref string, result *CheckResult, cfg *checkConfig, phase ui.Phase) error {
	if cm.verified[ref] {
		return nil
	}

	data, err := cm.store.Get(ctx, ref)
	if err != nil {
		result.Errors = append(result.Errors, CheckError{
			Key: ref, Type: "missing", Message: fmt.Sprintf("chunk not found or unreadable: %v", err),
		})
		cm.verified[ref] = true
		return nil
	}
	cm.verified[ref] = true
	result.ObjectsVerified++

	if cfg.readData {
		// The key is "chunk/<hash>". Verify the data hashes to the expected value.
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 {
			var actual string
			if len(cm.hmacKey) > 0 {
				actual = crypto.ComputeHMAC(cm.hmacKey, data)
			} else {
				actual = core.ComputeHash(data)
			}
			if actual != parts[1] {
				result.Errors = append(result.Errors, CheckError{
					Key:     ref,
					Type:    "corrupt",
					Message: fmt.Sprintf("hash mismatch: expected %s, got %s", parts[1], actual),
				})
				if cfg.verbose {
					phase.Log(fmt.Sprintf("CORRUPT: %s", ref))
				}
				return nil
			}
		}
	}

	if cfg.verbose {
		phase.Log(fmt.Sprintf("OK: %s", ref))
	}
	return nil
}
