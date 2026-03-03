package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
)

func mustMarshalCatalog(entries []core.SnapshotSummary) []byte {
	data, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return data
}

// captureStderr runs fn while capturing os.Stderr output and returns it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func TestListManager_Verbose(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	// Create 2 snapshots with a catalog.
	snap1 := core.Snapshot{Seq: 1, Root: "node/1", Created: "2025-01-01T00:00:00Z"}
	snap1Ref := saveSnapshot(ctx, s, &snap1)

	snap2 := core.Snapshot{Seq: 2, Root: "node/2", Created: "2025-01-02T00:00:00Z"}
	snap2Ref := saveSnapshot(ctx, s, &snap2)

	// Populate snapshot catalog.
	_ = s.Put(ctx, "index/latest", createIndex(snap2Ref, 2))
	_ = s.Put(ctx, "index/snapshots", mustMarshalCatalog([]core.SnapshotSummary{
		{Ref: snap1Ref, Seq: snap1.Seq, Created: snap1.Created, Root: snap1.Root},
		{Ref: snap2Ref, Seq: snap2.Seq, Created: snap2.Created, Root: snap2.Root},
	}))

	mgr := NewListManager(s)

	// Without verbose: no stderr output.
	out := captureStderr(t, func() {
		result, err := mgr.Run(ctx)
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(result.Snapshots) != 2 {
			t.Errorf("Expected 2 snapshots, got %d", len(result.Snapshots))
		}
	})
	if out != "" {
		t.Errorf("Expected no stderr output without verbose, got: %q", out)
	}

	// With verbose: should have stderr output.
	out = captureStderr(t, func() {
		result, err := mgr.Run(ctx, WithListVerbose())
		if err != nil {
			t.Fatalf("List verbose failed: %v", err)
		}
		if len(result.Snapshots) != 2 {
			t.Errorf("Expected 2 snapshots, got %d", len(result.Snapshots))
		}
	})
	if !strings.Contains(out, "Loading snapshot catalog") {
		t.Errorf("Expected verbose output to contain 'Loading snapshot catalog', got: %q", out)
	}
	if !strings.Contains(out, "Found 2 snapshots") {
		t.Errorf("Expected verbose output to contain 'Found 2 snapshots', got: %q", out)
	}
}

func TestLsSnapshotManager_Verbose(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	meta1 := createMeta(ctx, s, "file1.txt", 100)
	root := createHamt(t, s, []string{"file1"}, []string{meta1})
	snap := core.Snapshot{Seq: 1, Root: root, Created: "2025-01-01T00:00:00Z"}
	snapRef := saveSnapshot(ctx, s, &snap)
	_ = s.Put(ctx, "index/latest", createIndex(snapRef, 1))

	mgr := NewLsSnapshotManager(s)

	// Without verbose: no stderr output.
	out := captureStderr(t, func() {
		result, err := mgr.Run(ctx, "latest")
		if err != nil {
			t.Fatalf("Ls failed: %v", err)
		}
		if len(result.RefToMeta) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.RefToMeta))
		}
	})
	if out != "" {
		t.Errorf("Expected no stderr output without verbose, got: %q", out)
	}

	// With verbose: should have stderr output.
	out = captureStderr(t, func() {
		result, err := mgr.Run(ctx, "latest", WithLsVerbose())
		if err != nil {
			t.Fatalf("Ls verbose failed: %v", err)
		}
		if len(result.RefToMeta) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.RefToMeta))
		}
	})
	if !strings.Contains(out, "Resolving snapshot") {
		t.Errorf("Expected verbose output to contain 'Resolving snapshot', got: %q", out)
	}
	if !strings.Contains(out, "Collected 1 files") {
		t.Errorf("Expected verbose output to contain 'Collected 1 files', got: %q", out)
	}
}

func TestDiffManager_Verbose(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	meta1 := createMeta(ctx, s, "file1.txt", 100)
	meta2 := createMeta(ctx, s, "file2.txt", 200)

	root1 := createHamt(t, s, []string{"file1"}, []string{meta1})
	snap1Ref := saveSnapshotRef(ctx, s, root1, 1)

	root2 := createHamt(t, s, []string{"file1", "file2"}, []string{meta1, meta2})
	snap2Ref := saveSnapshotRef(ctx, s, root2, 2)
	_ = s.Put(ctx, "index/latest", createIndex(snap2Ref, 2))

	mgr := NewDiffManager(s)

	// Without verbose: no stderr output.
	out := captureStderr(t, func() {
		result, err := mgr.Run(ctx, snap1Ref, snap2Ref)
		if err != nil {
			t.Fatalf("Diff failed: %v", err)
		}
		if len(result.Changes) != 1 {
			t.Errorf("Expected 1 change, got %d", len(result.Changes))
		}
	})
	if out != "" {
		t.Errorf("Expected no stderr output without verbose, got: %q", out)
	}

	// With verbose: should have stderr output.
	out = captureStderr(t, func() {
		result, err := mgr.Run(ctx, snap1Ref, snap2Ref, WithDiffVerbose())
		if err != nil {
			t.Fatalf("Diff verbose failed: %v", err)
		}
		if len(result.Changes) != 1 {
			t.Errorf("Expected 1 change, got %d", len(result.Changes))
		}
	})
	if !strings.Contains(out, "Resolving snapshot") {
		t.Errorf("Expected verbose output to contain 'Resolving snapshot', got: %q", out)
	}
	if !strings.Contains(out, "Computing diff") {
		t.Errorf("Expected verbose output to contain 'Computing diff', got: %q", out)
	}
	if !strings.Contains(out, "1 added") {
		t.Errorf("Expected verbose output to contain '1 added', got: %q", out)
	}
}

func TestForgetManager_Verbose(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	snap1 := core.Snapshot{Seq: 1, Root: "node/1"}
	snap1Ref := saveSnapshot(ctx, s, &snap1)

	snap2 := core.Snapshot{Seq: 2, Root: "node/2"}
	snap2Ref := saveSnapshot(ctx, s, &snap2)

	_ = s.Put(ctx, "index/latest", createIndex(snap2Ref, 2))

	// Without verbose: "Forgetting" should NOT be logged.
	fm := NewForgetManager(s, ui.NewNoOpReporter())
	_, err := fm.Run(ctx, snap1Ref)
	if err != nil {
		t.Fatalf("Forget without verbose failed: %v", err)
	}
	assertNotExists(t, ctx, s, snap1Ref)

	// Re-create snap1 for verbose test.
	snap1 = core.Snapshot{Seq: 1, Root: "node/1"}
	snap1Ref = saveSnapshot(ctx, s, &snap1)

	// With verbose: phase.Log should be called with the snapshot ref.
	// Since NoOpReporter discards logs, we just verify it doesn't error.
	fm2 := NewForgetManager(s, ui.NewNoOpReporter())
	_, err = fm2.Run(ctx, snap1Ref, WithForgetVerbose())
	if err != nil {
		t.Fatalf("Forget with verbose failed: %v", err)
	}
	assertNotExists(t, ctx, s, snap1Ref)
}
