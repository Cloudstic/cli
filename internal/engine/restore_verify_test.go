package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/internal/ui"
)

// setupBackupForRestoreLarge backs up one file that is large enough to take the
// chunked path (over inlineThreshold) but small enough to land in exactly one
// chunk (under cdcMinSize * 2), so a test can replace that chunk wholesale.
func setupBackupForRestoreLarge(t *testing.T) *MockStore {
	t.Helper()
	src := NewMockSource()
	dest := NewMockStore()

	content := bytes.Repeat([]byte("cloudstic restore integrity test payload\n"), 15000) // ~600 KiB
	if len(content) <= 512*1024 {
		t.Fatalf("test payload %d bytes is not above the inline threshold", len(content))
	}
	src.AddFile("big.txt", "id_big", content)

	bkMgr := NewBackupManager(src, dest, ui.NewNoOpReporter(), nil)
	if _, err := bkMgr.Run(context.Background()); err != nil {
		t.Fatalf("Backup setup failed: %v", err)
	}
	return dest
}

// tamperChunk replaces the single stored chunk object with attacker-chosen
// bytes, simulating write access to the storage backend. It returns the key it
// replaced so a test can assert it found one.
func tamperChunk(t *testing.T, dest *MockStore, replacement []byte) string {
	t.Helper()
	dest.mu.Lock()
	defer dest.mu.Unlock()
	for key := range dest.Data {
		if strings.HasPrefix(key, "chunk/") {
			dest.Data[key] = replacement
			return key
		}
	}
	t.Fatalf("no chunk/ object found in store; test setup no longer produces chunks")
	return ""
}

// A backend that can write to the repository can replace a chunk with
// unencrypted plaintext of its choosing. EncryptedStore does not recognise it
// as ciphertext, so GCM authentication never runs. The content hash is what
// catches it.
func TestRestore_RejectsTamperedChunk(t *testing.T) {
	dest := setupBackupForRestoreLarge(t)
	tamperChunk(t, dest, []byte("attacker controlled content"))

	rsMgr := NewRestoreManager(storelayer.NewCompressedStore(dest), ui.NewNoOpReporter())
	outDir := filepath.Join(t.TempDir(), "restored")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}

	result, err := rsMgr.Run(context.Background(), writer, "")
	if err != nil {
		t.Fatalf("Run returned a hard error, expected a per-file error: %v", err)
	}
	if result.Errors == 0 {
		t.Fatal("restore reported no errors after a chunk was replaced with attacker bytes")
	}

	// The corrupted file must not be left on disk under the user's filename.
	got, err := os.ReadFile(filepath.Join(outDir, "big.txt"))
	if err == nil {
		t.Fatalf("corrupt file was left on disk with %d bytes", len(got))
	}
	if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stating restored file: %v", err)
	}
}

// The escape hatch must still hand the bytes over, so that a snapshot with a
// disagreeing hash never leaves a user unable to recover anything at all.
func TestRestore_NoVerifyStillWritesTamperedChunk(t *testing.T) {
	dest := setupBackupForRestoreLarge(t)
	tamperChunk(t, dest, []byte("attacker controlled content"))

	rsMgr := NewRestoreManager(storelayer.NewCompressedStore(dest), ui.NewNoOpReporter())
	outDir := filepath.Join(t.TempDir(), "restored")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}

	result, err := rsMgr.Run(context.Background(), writer, "", WithRestoreNoVerify())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("-no-verify should not report hash errors, got %d", result.Errors)
	}
	got, err := os.ReadFile(filepath.Join(outDir, "big.txt"))
	if err != nil {
		t.Fatalf("expected the file to be written with -no-verify: %v", err)
	}
	if string(got) != "attacker controlled content" {
		t.Fatalf("unexpected content with -no-verify: %q", got)
	}
}

// Verification must not fire on healthy data.
func TestRestore_VerifiesCleanRestore(t *testing.T) {
	dest := setupBackupForRestore(t)
	rsMgr := NewRestoreManager(storelayer.NewCompressedStore(dest), ui.NewNoOpReporter())

	outDir := filepath.Join(t.TempDir(), "restored")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	result, err := rsMgr.Run(context.Background(), writer, "")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("clean restore reported %d verification errors", result.Errors)
	}
	b, err := os.ReadFile(filepath.Join(outDir, "restore_me.txt"))
	if err != nil {
		t.Fatalf("read restore_me.txt: %v", err)
	}
	if string(b) != "restore content" {
		t.Fatalf("restore_me.txt content=%q", string(b))
	}
}

// Inline (small) files bypass the chunker entirely; their hash is computed on a
// different code path, so cover it separately.
func TestRestore_VerifiesInlineContent(t *testing.T) {
	dest := setupBackupForRestore(t)

	// Only entries whose source reported a non-zero size take the inline path,
	// so find the content object that actually carries an inline payload rather
	// than assuming the first one does.
	var contentKey string
	var c core.Content
	dest.mu.Lock()
	for key, raw := range dest.Data {
		if !strings.HasPrefix(key, "content/") {
			continue
		}
		var candidate core.Content
		if err := json.Unmarshal(raw, &candidate); err != nil {
			continue
		}
		if len(candidate.DataInlineB64) > 0 {
			contentKey, c = key, candidate
			break
		}
	}
	dest.mu.Unlock()
	if contentKey == "" {
		t.Fatal("no content/ object with an inline payload found in store")
	}

	c.DataInlineB64 = bytes.Repeat([]byte("X"), len(c.DataInlineB64))
	tampered, err := json.Marshal(&c)
	if err != nil {
		t.Fatalf("marshal tampered content: %v", err)
	}

	dest.mu.Lock()
	dest.Data[contentKey] = tampered
	dest.mu.Unlock()

	rsMgr := NewRestoreManager(storelayer.NewCompressedStore(dest), ui.NewNoOpReporter())
	outDir := filepath.Join(t.TempDir(), "restored")
	writer, err := NewFSRestoreWriter(outDir)
	if err != nil {
		t.Fatalf("NewFSRestoreWriter failed: %v", err)
	}
	result, err := rsMgr.Run(context.Background(), writer, "")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Errors == 0 {
		t.Fatal("restore reported no errors after inline content was tampered with")
	}
}
