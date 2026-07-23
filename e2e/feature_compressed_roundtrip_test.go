package e2e

import (
	"bytes"
	"compress/gzip"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Already-compressed files are the payloads the storage layer used to get
// wrong. zstd cannot shrink them, so the compression layer stores them
// verbatim; a reader that recovered the encoding by sniffing magic bytes then
// inflated the user's own gzip or zstd stream and returned its contents instead
// of the file. Small files came back silently wrong, larger ones failed with
// truncation errors.
//
// These fixtures are built rather than committed so the test states what makes
// them dangerous: they begin with a magic header the old read path recognised.
func gzipFixture(t *testing.T, payload []byte) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.String()
}

func zstdFixture(t *testing.T, payload []byte) string {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer func() { _ = enc.Close() }()
	return string(enc.EncodeAll(payload, nil))
}

// mustHaveExactBytes compares restored content byte-for-byte without dumping
// megabytes of binary into the failure message.
func mustHaveExactBytes(t *testing.T, dir, relPath, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("restore dir missing %s: %v", relPath, err)
	}
	if string(got) == want {
		return
	}
	t.Errorf("%s did not survive the round trip: restored %d bytes, backed up %d",
		relPath, len(got), len(want))
	if len(got) != len(want) {
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: first difference at byte %d: got 0x%02x, want 0x%02x",
				relPath, i, got[i], want[i])
			return
		}
	}
}

// incompressibleBytes returns n deterministic pseudo-random bytes. The payload
// has to be genuinely incompressible for these fixtures to mean anything: if
// the archive compresses, the storage layer takes the zstd branch and never
// reaches the stored-raw path where the corruption lived.
func incompressibleBytes(n int) []byte {
	rng := rand.New(rand.NewSource(0x5EED))
	b := make([]byte, n)
	_, _ = rng.Read(b)
	return b
}

// compressedFixtures returns the payloads a repository must round-trip
// unchanged.
//
// The sizes are chosen against two thresholds in the backup pipeline, and the
// test is worthless without them. Files at or below inlineThreshold (512 KiB)
// are embedded in the content manifest as JSON, so their bytes never reach the
// compression layer on their own and cannot exercise this bug at all. Above it,
// content is chunked with a 512 KiB minimum and a 1 MiB average, so a ~768 KiB
// archive lands in a single chunk — the case that used to be returned inflated
// and silently wrong — while a ~5 MiB archive spans several, where the first
// chunk carries the magic header and used to fail on truncation instead.
func compressedFixtures(t *testing.T) map[string]string {
	t.Helper()

	singleChunk := incompressibleBytes(768 * 1024)
	multiChunk := incompressibleBytes(5 * 1024 * 1024)

	return map[string]string{
		"single-chunk.gz":  gzipFixture(t, singleChunk),
		"single-chunk.zst": zstdFixture(t, singleChunk),
		"multi-chunk.gz":   gzipFixture(t, multiChunk),
		"multi-chunk.zst":  zstdFixture(t, multiChunk),
		// The stored-raw branch without a recognised magic header, and an
		// ordinary compressible file for contrast.
		"incompressible.bin": string(incompressibleBytes(768 * 1024)),
		"plain.txt":          "an ordinary file, for contrast",
	}
}

func TestCLI_Feature_CompressedFilesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name      string
		encrypted bool
	}{
		{"unencrypted", false},
		{"encrypted", true},
	} {
		runFeatureMatrix(t, featureSpec{
			name: "compressed_roundtrip_" + tc.name,
			// The encoding decision belongs to the store layer, so it is
			// identical for every source. The portable source is additionally
			// a 10 MB RAM disk, too small for fixtures that must clear the
			// 512 KiB inline threshold several times over.
			sourceFilter: localOnlySource,
			test: func(t *testing.T, h *harness, _ matrixEntry) {
				fixtures := compressedFixtures(t)
				for name, content := range fixtures {
					h.WithFile(name, content)
				}

				var r *repo
				if tc.encrypted {
					r = h.MustInitEncrypted()
				} else {
					r = h.MustInitUnencrypted()
				}

				r.Backup()
				out := r.RestoreDir("roundtrip")

				for name, content := range fixtures {
					mustHaveExactBytes(t, out.Path(), name, content)
				}
			},
		})
	}
}
