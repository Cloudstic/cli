package blob

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

func testSealer(t *testing.T, master string) *crypto.MemberSealer {
	t.Helper()
	key := sha256.Sum256([]byte(master))
	s, err := crypto.NewMemberSealer(key[:])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// addAll packs bodies in order and seals, failing the test on any error.
func addAll(t *testing.T, w *Writer, bodies ...[]byte) (string, []byte, []Placement) {
	t.Helper()
	for _, b := range bodies {
		if err := w.Add(hashOf(b), b); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	ref, data, members, err := w.Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return ref, data, members
}

// The read the format exists for: a caller holding one leaf entry has the
// blob's ref, a byte range and a content hash, and needs neither the index nor
// a second request.
func TestMemberReadsFromItsRangeAlone(t *testing.T) {
	s := testSealer(t, "master")
	bodies := [][]byte{
		[]byte("the first file"),
		bytes.Repeat([]byte("compressible "), 400),
		{},
		[]byte("the last file"),
	}
	ref, data, members := addAll(t, NewWriter(s), bodies...)

	if !strings.HasPrefix(ref, Prefix) {
		t.Fatalf("ref %q does not start with %q", ref, Prefix)
	}
	if len(members) != len(bodies) {
		t.Fatalf("got %d members, want %d", len(members), len(bodies))
	}

	for i, m := range members {
		// Exactly the bytes a ranged GET would return, and nothing else.
		ranged := data[m.Offset : m.Offset+m.Length]
		got, err := ReadMember(ranged, m.ContentHash, ref, s, m.PlainSize)
		if err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
		if !bytes.Equal(got, bodies[i]) {
			t.Fatalf("member %d round-tripped to %d bytes, want %d", i, len(got), len(bodies[i]))
		}
	}
}

func TestUnencryptedBlobRoundTrips(t *testing.T) {
	bodies := [][]byte{[]byte("alpha"), bytes.Repeat([]byte("beta "), 100)}
	ref, data, members := addAll(t, NewWriter(nil), bodies...)

	for i, m := range members {
		got, err := ReadMember(data[m.Offset:m.Offset+m.Length], m.ContentHash, ref, nil, m.PlainSize)
		if err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
		if !bytes.Equal(got, bodies[i]) {
			t.Fatalf("member %d did not round-trip", i)
		}
	}
	idx, err := ParseIndex(data, ref, nil)
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(idx.Members) != len(bodies) {
		t.Fatalf("index lists %d members, want %d", len(idx.Members), len(bodies))
	}
}

func TestParseIndexRecoversEveryPlacement(t *testing.T) {
	s := testSealer(t, "master")
	bodies := [][]byte{[]byte("one"), []byte("two"), bytes.Repeat([]byte("three "), 200)}
	ref, data, members := addAll(t, NewWriter(s), bodies...)

	idx, err := ParseIndex(data, ref, s)
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if idx.StoredBytes != int64(len(data)) {
		t.Errorf("StoredBytes = %d, want %d", idx.StoredBytes, len(data))
	}
	if len(idx.Members) != len(members) {
		t.Fatalf("index lists %d members, want %d", len(idx.Members), len(members))
	}
	for i := range members {
		if idx.Members[i] != members[i] {
			t.Errorf("member %d: index says %+v, Seal said %+v", i, idx.Members[i], members[i])
		}
	}
}

// Utilization is measured against the stored size, so the denominator a
// consolidation trigger uses has to be the object's real size on the store.
// A plaintext total would read compression as waste.
func TestStoredBytesIsTheObjectSizeNotThePlaintextSize(t *testing.T) {
	s := testSealer(t, "master")
	body := bytes.Repeat([]byte("highly compressible "), 2000)
	ref, data, _ := addAll(t, NewWriter(s), body)

	idx, err := ParseIndex(data, ref, s)
	if err != nil {
		t.Fatal(err)
	}
	if idx.StoredBytes >= int64(len(body)) {
		t.Fatalf("StoredBytes %d is not below the %d plaintext bytes; compression is not reflected",
			idx.StoredBytes, len(body))
	}
}

// A retried upload must produce byte-identical bytes, or a content-addressed
// store gains a second copy of the same blob every time a request is retried.
func TestSealIsDeterministic(t *testing.T) {
	bodies := [][]byte{[]byte("one"), []byte("two")}
	refA, dataA, _ := addAll(t, NewWriter(testSealer(t, "master")), bodies...)
	refB, dataB, _ := addAll(t, NewWriter(testSealer(t, "master")), bodies...)

	if refA != refB {
		t.Fatalf("refs differ: %s vs %s", refA, refB)
	}
	if !bytes.Equal(dataA, dataB) {
		t.Fatal("sealing the same members twice produced different bytes")
	}
}

// The ref names the members in order, so the same bodies packed in a different
// order are a different blob. Walk order is the packing order and it is part
// of what the blob is.
func TestOrderChangesTheRef(t *testing.T) {
	s := testSealer(t, "master")
	a, b := []byte("one"), []byte("two")
	refA, _, _ := addAll(t, NewWriter(s), a, b)
	refB, _, _ := addAll(t, NewWriter(s), b, a)
	if refA == refB {
		t.Fatal("reordering the members did not change the ref")
	}
}

// Two entries with the same content share one member. Storing the body twice
// would be pure waste, and the second Add is where it is avoided.
func TestDuplicateBodiesAreStoredOnce(t *testing.T) {
	s := testSealer(t, "master")
	body := []byte("shared between two files")
	w := NewWriter(s)
	for range 3 {
		if err := w.Add(hashOf(body), body); err != nil {
			t.Fatal(err)
		}
	}
	if w.Len() != 1 {
		t.Fatalf("blob holds %d members, want 1", w.Len())
	}
	if w.PlaintextBytes() != int64(len(body)) {
		t.Fatalf("PlaintextBytes = %d, want %d", w.PlaintextBytes(), len(body))
	}
	_, _, members, err := w.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("Seal returned %d placements, want 1", len(members))
	}
}

// The AAD binding is the only thing standing between a reader and a member
// substituted from another blob, since a blob's ref is never verified against
// its bytes — no reader assembles a blob's plaintext.
func TestMemberDoesNotOpenUnderAnotherBlobsRef(t *testing.T) {
	s := testSealer(t, "master")
	body := []byte("a body in exactly one blob")

	ref, data, members := addAll(t, NewWriter(s), body)
	otherRef, _, _ := addAll(t, NewWriter(s), []byte("a different blob entirely"))

	m := members[0]
	ranged := data[m.Offset : m.Offset+m.Length]
	if _, err := ReadMember(ranged, m.ContentHash, otherRef, s, m.PlainSize); err == nil {
		t.Fatal("a member opened under another blob's ref")
	}
	// Sanity: it opens under its own.
	if _, err := ReadMember(ranged, m.ContentHash, ref, s, m.PlainSize); err != nil {
		t.Fatalf("the member does not open under its own ref: %v", err)
	}
}

func TestBlobDoesNotOpenUnderAnotherMasterKey(t *testing.T) {
	ref, data, members := addAll(t, NewWriter(testSealer(t, "one")), []byte("secret"))
	other := testSealer(t, "two")

	m := members[0]
	if _, err := ReadMember(data[m.Offset:m.Offset+m.Length], m.ContentHash, ref, other, m.PlainSize); err == nil {
		t.Error("a member opened under a different master key")
	}
	if _, err := ParseIndex(data, ref, other); err == nil {
		t.Error("an index opened under a different master key")
	}
}

// Compression is per member, so an incompressible body must fall back to raw
// rather than being stored larger than it started.
func TestIncompressibleMemberFallsBackToRaw(t *testing.T) {
	body := make([]byte, 4096)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	_, _, members := addAll(t, NewWriter(nil), body)

	// codec byte plus the body verbatim, with no sealing in this repository.
	if want := int64(len(body) + 1); members[0].Length != want {
		t.Fatalf("incompressible member stored in %d bytes, want %d", members[0].Length, want)
	}
}

func TestCompressibleMemberIsCompressed(t *testing.T) {
	body := bytes.Repeat([]byte("aaaabbbb"), 1000)
	_, _, members := addAll(t, NewWriter(nil), body)
	if members[0].Length >= int64(len(body)) {
		t.Fatalf("compressible member stored in %d bytes, no smaller than its %d plaintext",
			members[0].Length, len(body))
	}
}

func TestSealRefusesAnEmptyBlob(t *testing.T) {
	if _, _, _, err := NewWriter(nil).Seal(); err == nil {
		t.Fatal("Seal accepted a blob with no members")
	}
}

// A wrong content hash produces a blob whose members cannot be opened, so it
// is refused where it is introduced rather than where it is read.
func TestAddRejectsAMalformedContentHash(t *testing.T) {
	w := NewWriter(nil)
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("ab", 40)} {
		if err := w.Add(bad, []byte("body")); err == nil {
			t.Errorf("Add accepted content hash %q", bad)
		}
	}
}

// The sharper case: a well-formed digest that is simply not this body's. It
// would misname the blob, and — worse — a later different body offered under a
// hash already seen is dropped by the dedup, leaving its entry pointing at the
// first body's bytes. That is wrong data returned successfully.
func TestAddRejectsAValidHashOfDifferentBytes(t *testing.T) {
	w := NewWriter(nil)
	if err := w.Add(hashOf([]byte("some other body")), []byte("body")); err == nil {
		t.Fatal("Add accepted a valid digest that is not the body's")
	}
	if w.Len() != 0 {
		t.Fatalf("the rejected body was retained: %d members", w.Len())
	}
	// Uppercase hex is the same digest spelled differently, and would be a
	// second dedup key for one body. Verification rejects it for free.
	if err := w.Add(strings.ToUpper(hashOf([]byte("body"))), []byte("body")); err == nil {
		t.Error("Add accepted a non-canonical hex digest")
	}
}

// Member boundaries have to be part of a blob's identity. Hashing concatenated
// bodies would give these pairs one ref for two different layouts, and a
// content-addressed store would keep whichever arrived first while the other's
// leaf entries carried offsets into it.
func TestRefDistinguishesMemberBoundaries(t *testing.T) {
	s := testSealer(t, "master")
	cases := []struct {
		name string
		a, b [][]byte
	}{
		{"split differently", [][]byte{[]byte("a"), []byte("bc")}, [][]byte{[]byte("ab"), []byte("c")}},
		// An empty file reaches the same collision without contrivance.
		{"empty member", [][]byte{{}, []byte("abc")}, [][]byte{[]byte("abc")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refA, _, _ := addAll(t, NewWriter(s), tc.a...)
			refB, _, _ := addAll(t, NewWriter(s), tc.b...)
			if refA == refB {
				t.Fatalf("%v and %v share the ref %s", tc.a, tc.b, refA)
			}
		})
	}
}

// A forged member count must not be trusted to size an allocation: multiplied
// by the digest width it wraps a uint64 to zero, so the guard has to divide.
func TestIndexRejectsAForgedMemberCount(t *testing.T) {
	plain := append([]byte{}, indexMagic[:]...)
	plain = binary.AppendUvarint(plain, 1<<59)
	// The index is read from immediately before the trailer, so it has to sit
	// there — anything after it is read as the index instead.
	forged := append(bytes.Repeat([]byte("m"), 16), plain...)
	forged = binary.BigEndian.AppendUint32(forged, uint32(len(plain)))

	if _, err := ParseIndex(forged, "blob/whatever", nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("ParseIndex accepted an index claiming 1<<59 members: err = %v", err)
	}
}

// Offset and Length are attacker-influenced, so their sum must never be the
// thing that is bounds-checked: it can wrap negative and pass.
func TestIndexRejectsAnOverflowingPlacement(t *testing.T) {
	bad := Placement{ContentHash: hashOf([]byte("x")), Offset: 1 << 62, Length: 1 << 62}
	index, err := encodeIndex([]Placement{bad})
	if err != nil {
		t.Fatal(err)
	}
	forged := append(bytes.Repeat([]byte("m"), 16), index...)
	forged = binary.BigEndian.AppendUint32(forged, uint32(len(index)))

	if _, err := ParseIndex(forged, "blob/whatever", nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("ParseIndex accepted a placement whose offset+length overflows: err = %v", err)
	}
}

// "Cannot decode" must never become "no members": a garbage collector reading
// an unreadable blob as unreferenced deletes a live repository.
func TestMalformedBlobsAreRefusedRatherThanReadAsEmpty(t *testing.T) {
	s := testSealer(t, "master")
	ref, data, _ := addAll(t, NewWriter(s), []byte("one"), []byte("two"))

	truncated := func(n int) []byte { return bytes.Clone(data[:n]) }
	withIndexLen := func(v uint32) []byte {
		bad := bytes.Clone(data)
		binary.BigEndian.PutUint32(bad[len(bad)-indexLenSize:], v)
		return bad
	}

	cases := map[string][]byte{
		"empty":                    nil,
		"shorter than trailer":     truncated(2),
		"body only":                truncated(len(data) / 2),
		"zero index length":        withIndexLen(0),
		"index longer than object": withIndexLen(uint32(len(data))),
		"index length overflow":    withIndexLen(^uint32(0)),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			idx, err := ParseIndex(bad, ref, s)
			if err == nil {
				t.Fatalf("ParseIndex accepted a %s blob and returned %d members", name, len(idx.Members))
			}
		})
	}
}

// A member whose range reaches into the index would let a crafted blob have a
// reader authenticate the index as though it were a body.
func TestIndexRejectsAMemberOverlappingTheIndex(t *testing.T) {
	body := []byte("a real body")
	ref, data, members := addAll(t, NewWriter(nil), body)

	// Rebuild the same blob unsealed, with the member's length stretched to
	// swallow the index.
	bad := members[0]
	bad.Length = int64(len(data))
	index, err := encodeIndex([]Placement{bad})
	if err != nil {
		t.Fatal(err)
	}
	forged := append(bytes.Clone(data[:members[0].Offset+members[0].Length]), index...)
	forged = binary.BigEndian.AppendUint32(forged, uint32(len(index)))

	if _, err := ParseIndex(forged, ref, nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("ParseIndex accepted a member overlapping the index: err = %v", err)
	}
}

func TestReadMemberRejectsCorruptedBytes(t *testing.T) {
	s := testSealer(t, "master")
	body := bytes.Repeat([]byte("corrupt me "), 50)
	ref, data, members := addAll(t, NewWriter(s), body)

	m := members[0]
	for _, i := range []int{0, int(m.Length) / 2, int(m.Length) - 1} {
		ranged := bytes.Clone(data[m.Offset : m.Offset+m.Length])
		ranged[i] ^= 0x01
		if _, err := ReadMember(ranged, m.ContentHash, ref, s, m.PlainSize); err == nil {
			t.Errorf("flipping byte %d of a member was not detected", i)
		}
	}
}

// A blob big enough to exercise the varint widths in the index and the
// capacity arithmetic in Seal.
func TestManyMembersRoundTrip(t *testing.T) {
	s := testSealer(t, "master")
	const n = 500
	w := NewWriter(s)
	bodies := make([][]byte, n)
	for i := range n {
		bodies[i] = []byte(fmt.Sprintf("body number %d, padded out %s", i, strings.Repeat("x", i%97)))
		if err := w.Add(hashOf(bodies[i]), bodies[i]); err != nil {
			t.Fatal(err)
		}
	}
	ref, data, members, err := w.Seal()
	if err != nil {
		t.Fatal(err)
	}
	idx, err := ParseIndex(data, ref, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Members) != n {
		t.Fatalf("index lists %d members, want %d", len(idx.Members), n)
	}
	for i, m := range members {
		got, err := ReadMember(data[m.Offset:m.Offset+m.Length], m.ContentHash, ref, s, m.PlainSize)
		if err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
		if !bytes.Equal(got, bodies[i]) {
			t.Fatalf("member %d did not round-trip", i)
		}
	}
}
