package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
)

// The object store is untrusted. A backup provider, anyone with write access to
// the bucket, or bit rot can change the bytes stored under a key. Objects named
// by their own SHA-256 — filemeta, node, and snapshot — carry the means to
// detect that, and the tests below hold every reader to it.
//
// Chunks and content objects are covered elsewhere: restore hashes the
// reconstructed stream against the file's recorded ContentHash, and
// `check -read-data` re-hashes chunk bytes.

// findKey returns the single key under prefix, failing when the count is not one.
func findKey(t *testing.T, s *MockStore, prefix string) string {
	t.Helper()
	keys, err := s.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("list %s: %v", prefix, err)
	}
	if len(keys) != 1 {
		t.Fatalf("want exactly one %s object, got %d: %v", prefix, len(keys), keys)
	}
	return keys[0]
}

// tamperedFileMeta rewrites the filemeta for name so it claims different
// content, leaving it stored under the key it no longer hashes to. This is
// exactly what a store that substitutes an object achieves.
func tamperedFileMeta(t *testing.T, s *MockStore, name string, mutate func(*core.FileMeta)) string {
	t.Helper()
	ctx := context.Background()

	keys, err := s.List(ctx, "filemeta/")
	if err != nil {
		t.Fatalf("list filemeta: %v", err)
	}
	for _, key := range keys {
		data, err := s.Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		var fm core.FileMeta
		if err := json.Unmarshal(data, &fm); err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
		if fm.Name != name {
			continue
		}
		mutate(&fm)
		forged, err := json.Marshal(&fm)
		if err != nil {
			t.Fatalf("marshal forged filemeta: %v", err)
		}
		if err := s.Put(ctx, key, forged); err != nil {
			t.Fatalf("put forged %s: %v", key, err)
		}
		return key
	}
	t.Fatalf("no filemeta named %q", name)
	return ""
}

// TestRestoreRejectsTamperedFileMeta covers the case that turns a metadata
// substitution into wrong bytes on the user's disk: the forged filemeta renames
// the file, so restore writes real content under a name the user never backed
// up. The content-hash check restore already performs cannot see this — the
// content is genuine, it is the name that is forged.
func TestRestoreRejectsTamperedFileMeta(t *testing.T) {
	dest := setupBackupForRestore(t)
	tamperedFileMeta(t, dest, "restore_me.txt", func(fm *core.FileMeta) {
		fm.Name = "attacker_chosen_name.txt"
	})

	out := t.TempDir()
	writer, err := NewFSRestoreWriter(out)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	_, err = rm.Run(context.Background(), writer, "latest")

	if err == nil {
		t.Fatalf("restore accepted a filemeta that does not hash to its key; "+
			"directory now contains %v", entryNames(t, out))
	}
	if !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("want an integrity-check failure, got: %v", err)
	}
	if names := entryNames(t, out); len(names) != 0 {
		t.Errorf("restore aborted but still wrote %v", names)
	}
}

// TestRestoreRejectsTamperedSnapshot covers the root of the chain. Verifying
// filemeta and nodes is worth nothing if the snapshot naming the root can be
// swapped for one pointing somewhere else.
func TestRestoreRejectsTamperedSnapshot(t *testing.T) {
	dest := setupBackupForRestore(t)
	ctx := context.Background()

	snapKey := findKey(t, dest, "snapshot/")
	data, err := dest.Get(ctx, snapKey)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	var snap core.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	snap.Created = "1999-01-01T00:00:00Z"
	forged, err := json.Marshal(&snap)
	if err != nil {
		t.Fatalf("marshal forged snapshot: %v", err)
	}
	if err := dest.Put(ctx, snapKey, forged); err != nil {
		t.Fatalf("put forged snapshot: %v", err)
	}

	writer, err := NewFSRestoreWriter(t.TempDir())
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	rm := NewRestoreManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	if _, err := rm.Run(ctx, writer, "latest"); err == nil {
		t.Fatal("restore accepted a snapshot that does not hash to its key")
	} else if !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("want an integrity-check failure, got: %v", err)
	}
}

// TestCheckReportsTamperedFileMeta pins the other half: the operator who runs
// `check` to find out whether a repository is sound must be told about a
// substituted filemeta, and told before a restore is attempted.
func TestCheckReportsTamperedFileMeta(t *testing.T) {
	dest := setupBackupForRestore(t)
	key := tamperedFileMeta(t, dest, "nested.txt", func(fm *core.FileMeta) {
		fm.Size = 999999
	})

	cm := NewCheckManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	res, err := cm.Run(context.Background(), WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	for _, e := range res.Errors {
		if e.Key == key && e.Type == "corrupt" {
			return
		}
	}
	t.Fatalf("check found no corruption for the tampered %s; reported %v", key, res.Errors)
}

// TestCheckReportsTamperedInlineContent covers small files, which are stored
// inline in the content object rather than as chunks. `check -read-data`
// re-hashes chunk bytes, so without an inline case the whole small-file
// population is verified by nothing at all.
func TestCheckReportsTamperedInlineContent(t *testing.T) {
	src := NewMockSource()
	dest := NewMockStore()
	src.AddFile("small.txt", "id1", []byte("small enough to store inline"))

	bm := NewBackupManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()}, src)
	if _, err := bm.Run(context.Background()); err != nil {
		t.Fatalf("backup: %v", err)
	}

	ctx := context.Background()
	contentKey := findKey(t, dest, "content/")
	forged, err := json.Marshal(core.Content{
		Type:          core.ObjectTypeContent,
		Size:          int64(len("attacker supplied bytes!!!!!")),
		DataInlineB64: []byte("attacker supplied bytes!!!!!"),
	})
	if err != nil {
		t.Fatalf("marshal forged content: %v", err)
	}
	if err := dest.Put(ctx, contentKey, forged); err != nil {
		t.Fatalf("put forged content: %v", err)
	}

	cm := NewCheckManager(Deps{Store: dest, Reporter: ui.NewNoOpReporter()})
	res, err := cm.Run(ctx, WithReadData())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, e := range res.Errors {
		if e.Key == contentKey && e.Type == "corrupt" {
			return
		}
	}
	t.Fatalf("check found no corruption for the tampered inline %s; reported %v", contentKey, res.Errors)
}

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return names
}
