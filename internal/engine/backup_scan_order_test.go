package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/source"
)

// orderedSource emits its files in a fixed order, which MockSource cannot do —
// it ranges over a map. The scan's contract is about order, so the source has
// to have one.
type orderedSource struct {
	metas   []core.FileMeta
	content map[string][]byte
}

func newOrderedSource(files int) *orderedSource {
	s := &orderedSource{content: make(map[string][]byte, files)}
	for i := range files {
		id := fmt.Sprintf("id%06d", i)
		body := fmt.Appendf(nil, "contents of %s", id)
		s.metas = append(s.metas, core.FileMeta{
			FileID: id,
			Name:   fmt.Sprintf("file%06d.txt", i),
			Type:   core.FileTypeFile,
			Size:   int64(len(body)),
			Mtime:  1700000000,
		})
		s.content[id] = body
	}
	return s
}

func (s *orderedSource) Info() core.SourceInfo { return core.SourceInfo{Type: "ordered"} }

func (s *orderedSource) Walk(ctx context.Context, cb func(core.FileMeta) error) error {
	for _, m := range s.metas {
		if err := cb(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *orderedSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	body, ok := s.content[fileID]
	if !ok {
		return nil, fmt.Errorf("no such file %s", fileID)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *orderedSource) Size(ctx context.Context) (*source.SourceSize, error) {
	var total int64
	for _, m := range s.metas {
		total += m.Size
	}
	return &source.SourceSize{Bytes: total, Files: int64(len(s.metas))}, nil
}

// The scan buffers the walk so it can declare a batch's reads before making
// them, but what it queues has to come out in the order the source walked it.
// That order becomes the upload order, and the upload order is what gives newly
// written objects their locality in the packs — so a batch that leaked the
// store's preferred read order into the queue would trade a one-time read win
// for a permanent write regression.
//
// The source is deliberately larger than entryBatch: a single-batch scan cannot
// tell a preserved order from an accidental one.
func TestBackupManager_Scan_QueuesInWalkOrder(t *testing.T) {
	const files = entryBatch + entryBatch/2

	backend := NewMockStore()
	packed, err := storelayer.NewPackStore(backend)
	if err != nil {
		t.Fatalf("NewPackStore: %v", err)
	}

	src := newOrderedSource(files)
	bm := NewBackupManager(Deps{Store: packed, Reporter: ui.NewNoOpReporter()}, src)
	bm.stats = &backupStats{}

	pending, _, err := bm.scan(context.Background(), "")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(pending) != files {
		t.Fatalf("scan queued %d entries, want %d", len(pending), files)
	}
	for i, got := range pending {
		if want := src.metas[i].FileID; got.FileID != want {
			t.Fatalf("queued entry %d is %s, want %s — the batch reordered what it processed", i, got.FileID, want)
		}
	}
}

// Batching must not change what a scan concludes. A tree spanning several
// batches has to produce the same root as the same tree seen in one, and the
// second backup of an unchanged source has to detect every entry as unchanged
// — which is the path that reads the previous filemetas, and so the path the
// declaration was added to.
func TestBackupManager_Run_BatchedScanDetectsNoChange(t *testing.T) {
	ctx := context.Background()
	backend := NewMockStore()

	const files = entryBatch + 7
	src := newOrderedSource(files)

	run := func() *RunResult {
		t.Helper()
		packed, err := storelayer.NewPackStore(backend)
		if err != nil {
			t.Fatalf("NewPackStore: %v", err)
		}
		res, err := NewBackupManager(Deps{Store: packed, Reporter: ui.NewNoOpReporter()}, src).Run(ctx)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return res
	}

	first := run()
	if first.FilesNew != files {
		t.Fatalf("first backup reported %d new files, want %d", first.FilesNew, files)
	}

	second := run()
	if second.Root != first.Root {
		t.Errorf("second backup of an unchanged source produced root %s, want %s", second.Root, first.Root)
	}
	if second.FilesUnmodified != files {
		t.Errorf("second backup reported %d unmodified files, want %d", second.FilesUnmodified, files)
	}
	if second.FilesChanged != 0 || second.FilesNew != 0 {
		t.Errorf("second backup reported %d changed and %d new files, want 0 and 0",
			second.FilesChanged, second.FilesNew)
	}
}
