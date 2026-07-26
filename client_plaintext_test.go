package cloudstic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/source"
	"github.com/cloudstic/cli/pkg/store"
)

// An encrypted repository at core.CiphertextOnlyFormat contains no plaintext
// under a content-addressed prefix, so anything that is not a ciphertext there
// was written by somebody other than a holder of the key. These tests cover the
// gate that refuses it, and the fallback that keeps older repositories — which
// may hold real plaintext objects — readable.

// forgedObject is what an attacker with write access to the backing store can
// produce without the encryption key: valid-looking repository content.
var forgedObject = []byte(`{"version":1,"root":"node/attacker","seq":99,"created":"2026-01-01T00:00:00Z"}`)

// newPlaintextTestRepo creates an encrypted repository holding one backup, and
// returns its directory alongside the raw store. Packfiles are off so objects
// keep their own keys in the backing store, which is what an attacker writes to.
func newPlaintextTestRepo(t *testing.T) (string, store.ObjectStore) {
	t.Helper()
	ctx := context.Background()

	storeDir := t.TempDir()
	writeRepoConfig(t, storeDir)

	base, err := store.NewLocalStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sourceDir := t.TempDir()
	writeSourceTree(t, sourceDir, map[string]string{"doc.txt": "real backed-up content"})

	client := newPlaintextTestClient(t, base)
	if _, err := client.Backup(ctx, source.NewLocalSource(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}
	return storeDir, base
}

func newPlaintextTestClient(t *testing.T, base store.ObjectStore, opts ...ClientOption) *Client {
	t.Helper()
	opts = append([]ClientOption{WithEncryptionKey(packfileTestKey()), WithPackfile(false)}, opts...)
	client, err := NewClient(context.Background(), base, opts...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// firstKey returns any object stored under prefix.
func firstKey(t *testing.T, s store.ObjectStore, prefix string) string {
	t.Helper()
	keys, err := s.List(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		t.Fatalf("no objects under %s", prefix)
	}
	return keys[0]
}

// The entry point for the forgery chain: a client holding the correct key must
// not read attacker-written plaintext as repository content.
func TestClient_RefusesPlaintextContentObject(t *testing.T) {
	ctx := context.Background()
	_, base := newPlaintextTestRepo(t)

	if err := base.Put(ctx, "snapshot/forged", forgedObject); err != nil {
		t.Fatal(err)
	}

	client := newPlaintextTestClient(t, base)
	got, err := client.Cat(ctx, "snapshot/forged")
	if err == nil {
		t.Fatalf("reading a forged object succeeded, returning %q", got[0].Data)
	}
	if !errors.Is(err, store.ErrPlaintextObject) {
		t.Fatalf("error = %v, want store.ErrPlaintextObject", err)
	}
	// The message has to tell the user what to do about it, since the two
	// causes — tampering and a repository converted from an unencrypted one —
	// call for opposite responses.
	for _, want := range []string{"snapshot/forged", "CLOUDSTIC_ALLOW_LEGACY_PLAINTEXT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// Substituting an object that a snapshot already references is the version of
// the attack that reaches user data. Restore counts per-file failures rather
// than aborting outright (a bad file must not sink an otherwise-good restore),
// so the assertion is that the substitution is reported as a failure and never
// reaches the restored file.
func TestClient_RestoreRefusesSubstitutedChunk(t *testing.T) {
	ctx := context.Background()
	_, base := newPlaintextTestRepo(t)

	// doc.txt is small enough to be stored inline in its content/ object rather
	// than as a separate chunk/ object; substituting that object is the same
	// attack either way.
	contentKey := firstKey(t, base, "content/")
	forgedContent := []byte(`{"type":"content","size":31,"data_inline_b64":"YXR0YWNrZXItc3VwcGxpZWQgZmlsZSBjb250ZW50"}`)
	if err := base.Put(ctx, contentKey, forgedContent); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(t.TempDir(), "restored")
	client := newPlaintextTestClient(t, base)
	res, err := client.RestoreToDir(ctx, outDir, "latest")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Errors == 0 {
		t.Fatal("restore should report the substituted object as a failure")
	}
	if res.FilesWritten != 0 {
		t.Errorf("FilesWritten = %d, want 0: the forged object must not be written out", res.FilesWritten)
	}

	// And nothing the attacker wrote reached the filesystem.
	restored, readErr := os.ReadFile(filepath.Join(outDir, "doc.txt"))
	if readErr == nil && strings.Contains(string(restored), "attacker-supplied") {
		t.Errorf("restored file contains the forged content: %q", restored)
	}
}

// Backward compatibility is permanent. A repository recording a format below
// the gate may hold genuine plaintext objects — one converted from an
// unencrypted repository by an earlier release does — and must still read them.
func TestClient_ReadsPlaintextBelowCiphertextOnlyFormat(t *testing.T) {
	ctx := context.Background()
	_, base := newPlaintextTestRepo(t)

	if err := base.Put(ctx, "snapshot/legacy", forgedObject); err != nil {
		t.Fatal(err)
	}
	rewindConfigVersion(t, base, core.CiphertextOnlyFormat-1)

	client := newPlaintextTestClient(t, base)
	got, err := client.Cat(ctx, "snapshot/legacy")
	if err != nil {
		t.Fatalf("a repository below format %d must still read plaintext objects: %v",
			core.CiphertextOnlyFormat, err)
	}
	if string(got[0].Data) != string(forgedObject) {
		t.Errorf("got %q, want %q", got[0].Data, forgedObject)
	}
}

// The escape valve for the case the format cannot express: a converted
// repository that a later write has already stamped at the current format.
func TestClient_LegacyPlaintextOptionRestoresTheFallback(t *testing.T) {
	ctx := context.Background()
	_, base := newPlaintextTestRepo(t)

	if err := base.Put(ctx, "snapshot/legacy", forgedObject); err != nil {
		t.Fatal(err)
	}

	client := newPlaintextTestClient(t, base, WithLegacyPlaintext(true))
	got, err := client.Cat(ctx, "snapshot/legacy")
	if err != nil {
		t.Fatalf("WithLegacyPlaintext should restore the fallback: %v", err)
	}
	if string(got[0].Data) != string(forgedObject) {
		t.Errorf("got %q, want %q", got[0].Data, forgedObject)
	}
}

// Key slots are plaintext by design — they hold the wrapped master key needed
// before any key exists — and refusing them would make an encrypted repository
// impossible to open.
func TestClient_KeySlotsStayReadableUnderTheGate(t *testing.T) {
	ctx := context.Background()
	_, base := newPlaintextTestRepo(t)

	slot := []byte(`{"slot_type":"platform","wrapped_key":"base64data"}`)
	if err := base.Put(ctx, "keys/platform-default", slot); err != nil {
		t.Fatal(err)
	}

	client := newPlaintextTestClient(t, base)
	got, err := client.Cat(ctx, "keys/platform-default")
	if err != nil {
		t.Fatalf("key slots must stay readable: %v", err)
	}
	if string(got[0].Data) != string(slot) {
		t.Errorf("got %q, want %q", got[0].Data, slot)
	}
}
