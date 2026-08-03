package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// captureStderr runs fn while capturing os.Stderr, so a test can assert that
// nothing leaks there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func mustMarshalCatalog(entries []core.SnapshotSummary) []byte {
	data, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return data
}

// These operations used to write their progress detail straight to os.Stderr,
// gated by a per-operation verbose option. A library caller could neither
// capture nor silence it, and the CLI needed a different option name for each
// operation. The detail now goes to the log writer the caller supplies, which is
// what these tests assert: the same content, somewhere a caller can reach.

func TestListManager_LogsProgressToTheCallersWriter(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	snap1 := core.Snapshot{Seq: 1, Root: "node/1", Created: "2025-01-01T00:00:00Z"}
	snap1Ref := saveSnapshot(ctx, s, &snap1)
	snap2 := core.Snapshot{Seq: 2, Root: "node/2", Created: "2025-01-02T00:00:00Z"}
	snap2Ref := saveSnapshot(ctx, s, &snap2)

	_ = s.Put(ctx, "index/latest", createIndex(snap2Ref, 2))
	_ = s.Put(ctx, "index/snapshots", mustMarshalCatalog([]core.SnapshotSummary{
		{Ref: snap1Ref, Seq: snap1.Seq, Created: snap1.Created, Root: snap1.Root},
		{Ref: snap2Ref, Seq: snap2.Seq, Created: snap2.Created, Root: snap2.Root},
	}))

	var log bytes.Buffer
	mgr := NewListManager(Deps{Store: s, LogSink: &log})
	result, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(result.Snapshots))
	}

	for _, want := range []string{"Loading snapshot catalog", "Found 2 snapshots"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not contain %q; got:\n%s", want, log.String())
		}
	}
}

// A caller that supplies no writer gets no detail — and, importantly, nothing
// leaks to the process's stderr instead.
func TestListManager_NoWriterMeansNoOutput(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()
	snap := core.Snapshot{Seq: 1, Root: "node/1", Created: "2025-01-01T00:00:00Z"}
	ref := saveSnapshot(ctx, s, &snap)
	_ = s.Put(ctx, "index/latest", createIndex(ref, 1))
	_ = s.Put(ctx, "index/snapshots", mustMarshalCatalog([]core.SnapshotSummary{
		{Ref: ref, Seq: snap.Seq, Created: snap.Created, Root: snap.Root},
	}))

	out := captureStderr(t, func() {
		if _, err := NewListManager(Deps{Store: s}).Run(ctx); err != nil {
			t.Fatalf("List: %v", err)
		}
	})
	if out != "" {
		t.Errorf("operation wrote to stderr with no writer configured: %q", out)
	}
}

func TestLsSnapshotManager_LogsProgressToTheCallersWriter(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	meta := createMeta(ctx, s, "file1.txt", 100)
	root := createHamt(ctx, t, s, []string{"file1"}, []string{meta})
	snap := core.Snapshot{Seq: 1, Root: root, Created: "2025-01-01T00:00:00Z"}
	ref := saveSnapshot(ctx, s, &snap)
	_ = s.Put(ctx, "index/latest", createIndex(ref, 1))

	var log bytes.Buffer
	if _, err := NewLsSnapshotManager(Deps{Store: s, LogSink: &log}).Run(ctx, ref); err != nil {
		t.Fatalf("LsSnapshot: %v", err)
	}
	if !strings.Contains(log.String(), "Resolving snapshot") {
		t.Errorf("log does not mention resolving the snapshot; got:\n%s", log.String())
	}
}

// The collected-entries line summarises the whole walk, so it belongs after the
// counting loop rather than inside it. Emitting it per entry turned a one-line
// summary into one line per file, which on a real snapshot buries every other
// diagnostic in the debug log.
func TestLsSnapshotManager_LogsTheCollectedSummaryOnce(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	metas := []string{
		createMeta(ctx, s, "file1.txt", 100),
		createMeta(ctx, s, "file2.txt", 200),
		createMeta(ctx, s, "file3.txt", 300),
	}
	root := createHamt(ctx, t, s, []string{"file1", "file2", "file3"}, metas)
	snap := core.Snapshot{Seq: 1, Root: root, Created: "2025-01-01T00:00:00Z"}
	ref := saveSnapshot(ctx, s, &snap)

	var log bytes.Buffer
	if _, err := NewLsSnapshotManager(Deps{Store: s, LogSink: &log}).Run(ctx, ref); err != nil {
		t.Fatalf("LsSnapshot: %v", err)
	}

	if got := strings.Count(log.String(), "Collected "); got != 1 {
		t.Errorf("collected summary logged %d times, want 1; got:\n%s", got, log.String())
	}
	if !strings.Contains(log.String(), "Collected 3 files, 0 directories") {
		t.Errorf("summary does not report the final totals; got:\n%s", log.String())
	}
}

func TestDiffManager_LogsProgressToTheCallersWriter(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	meta := createMeta(ctx, s, "file1.txt", 100)
	root := createHamt(ctx, t, s, []string{"file1"}, []string{meta})
	snap1 := core.Snapshot{Seq: 1, Root: root, Created: "2025-01-01T00:00:00Z"}
	ref1 := saveSnapshot(ctx, s, &snap1)
	snap2 := core.Snapshot{Seq: 2, Root: root, Created: "2025-01-02T00:00:00Z"}
	ref2 := saveSnapshot(ctx, s, &snap2)

	var log bytes.Buffer
	if _, err := NewDiffManager(Deps{Store: s, LogSink: &log}).Run(ctx, ref1, ref2); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(log.String(), "Resolving snapshot") {
		t.Errorf("log does not mention resolving the snapshots; got:\n%s", log.String())
	}
}
