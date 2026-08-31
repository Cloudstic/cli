package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

// The bytes a Writer produces are what a repository stores. They are reached
// by a chain of independently deterministic steps — the member sequence names
// the blob, zstd frames each body, and a key and nonce derived from that
// body's hash seal it — and none of that announces itself when it moves. A
// refactor that perturbed any step would keep every round-trip test in this
// package green while writing objects a released build cannot open, because
// those tests read back with the same code that wrote. So the bytes are
// pinned here rather than inferred from the fact that reads still work.
//
// If this test fails, what a repository holds has changed: that is a format
// change governed by docs/compatibility.md, not something to regenerate away.
// Only re-run with -update when the change is deliberate and that checklist
// has been followed.
var updateGolden = flag.Bool("update", false, "rewrite the sealed-blob golden file")

// blobFixture is a named set of bodies whose sealed blob is pinned.
type blobFixture struct {
	name string
	// master is the master key the sealer is derived from. Empty means an
	// unencrypted repository, where members are stored as they compress —
	// the one place the blob's shape differs, so both are pinned.
	master string
	bodies [][]byte
}

// filler returns n bytes zstd cannot shrink, chained from seed so a fixture
// exercising the raw fallback needs no random source and stays reproducible.
func filler(seed string, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	block := sha256.Sum256([]byte(seed))
	for len(out) < n {
		out = append(out, block[:]...)
		block = sha256.Sum256(block[:])
	}
	return out[:n]
}

func blobFixtures() []blobFixture {
	// Small, mixed bodies: a compressible run, an empty file, and two short
	// ones. The empty body is the case the framing has to carry with no
	// payload at all.
	mixed := [][]byte{
		[]byte("the first file"),
		bytes.Repeat([]byte("compressible "), 400),
		{},
		[]byte("the last file"),
	}

	incompressible := [][]byte{filler("incompressible", 4096), []byte("tiny")}

	// Sixty-four members alternating between compressible and not, each a
	// different length from the last. One member cannot catch a writer that
	// carries state across members — a stale buffer, a length taken from the
	// wrong place — and a uniform run of them barely can. A varying run does.
	var many [][]byte
	for i := range 64 {
		if i%2 == 0 {
			many = append(many, bytes.Repeat([]byte(fmt.Sprintf("body-%02d ", i)), 3+i))
		} else {
			many = append(many, filler(fmt.Sprintf("seed-%02d", i), 17+i*37))
		}
	}

	return []blobFixture{
		{name: "encrypted-mixed", master: "master", bodies: mixed},
		{name: "encrypted-raw-fallback", master: "master", bodies: incompressible},
		{name: "encrypted-64-members", master: "master", bodies: many},
		{name: "unencrypted-mixed", bodies: mixed},
		{name: "unencrypted-raw-fallback", bodies: incompressible},
	}
}

// render seals the fixture and describes the result exactly: the blob's ref,
// its stored size, a digest over its whole stored bytes, and every placement.
// A digest rather than a hex dump because the 64-member fixture is the one
// that makes this pin worth having and its dump would be unreadable; it
// commits to the bytes exactly as strongly.
func render(t *testing.T, f blobFixture) string {
	t.Helper()

	var sealer *crypto.MemberSealer
	if f.master != "" {
		sealer = testSealer(t, f.master)
	}
	w := NewWriter(sealer)
	for _, b := range f.bodies {
		if err := w.Add(hashOf(b), b); err != nil {
			t.Fatalf("%s: Add: %v", f.name, err)
		}
	}
	ref, data, members, err := w.Seal()
	if err != nil {
		t.Fatalf("%s: Seal: %v", f.name, err)
	}

	// Pinning bytes nothing can read would be worse than not pinning them, so
	// every member is decoded from its own range before its digest is
	// recorded, and the index is parsed from the whole object.
	for i, m := range members {
		got, err := ReadMember(data[m.Offset:m.Offset+m.Length], m.ContentHash, ref, sealer, m.PlainSize)
		if err != nil {
			t.Fatalf("%s: member %d: %v", f.name, i, err)
		}
		if !bytes.Equal(got, f.bodies[i]) {
			t.Fatalf("%s: member %d round-tripped to %d bytes, want %d", f.name, i, len(got), len(f.bodies[i]))
		}
	}
	idx, err := ParseIndex(data, ref, sealer)
	if err != nil {
		t.Fatalf("%s: ParseIndex: %v", f.name, err)
	}
	if len(idx.Members) != len(members) {
		t.Fatalf("%s: index lists %d members, want %d", f.name, len(idx.Members), len(members))
	}

	sum := sha256.Sum256(data)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", f.name)
	fmt.Fprintf(&b, "ref %s\n", ref)
	fmt.Fprintf(&b, "bytes %d sha256:%s\n", len(data), hex.EncodeToString(sum[:]))
	for i, m := range members {
		fmt.Fprintf(&b, "member %d %s off=%d len=%d plain=%d\n",
			i, m.ContentHash, m.Offset, m.Length, m.PlainSize)
	}
	return b.String()
}

func TestSealedBlobGolden(t *testing.T) {
	var blocks []string
	for _, f := range blobFixtures() {
		blocks = append(blocks, render(t, f))
	}
	got := strings.Join(blocks, "\n")

	path := filepath.Join("testdata", "sealed_blobs.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("the bytes a sealed blob is made of changed — this is an on-disk format change.\ngot:\n%s\nwant:\n%s", got, want)
	}
}
