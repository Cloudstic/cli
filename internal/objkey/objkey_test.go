package objkey

import (
	"fmt"
	"strings"
	"testing"
)

func TestEncode_RoundTripsEveryNamespace(t *testing.T) {
	for _, prefix := range Namespaces() {
		key := prefix + fmt.Sprintf("%064x", 42)
		k, ok := Encode(key)
		if !ok {
			t.Errorf("%s: did not encode", key)
			continue
		}
		if got := k.String(); got != key {
			t.Errorf("round trip: got %q, want %q", got, key)
		}
	}
}

// The shapes Encode must refuse. Each is a key some caller may legitimately be
// handed; refusing it is how it stays byte-exact in a fallback rather than being
// mis-filed under a Key that means something else.
func TestEncode_RefusesUnshapedKeys(t *testing.T) {
	for name, key := range map[string]string{
		"no namespace":      "index/latest",
		"empty":             "",
		"prefix only":       "chunk/",
		"short digest":      "filemeta/abc",
		"long digest":       "node/" + strings.Repeat("a", DigestHexLen+1),
		"non-hex digest":    "chunk/" + strings.Repeat("z", DigestHexLen),
		"one bad nibble":    "chunk/" + strings.Repeat("a", DigestHexLen-1) + "g",
		"unknown namespace": "keys/" + fmt.Sprintf("%064x", 1),
		"uppercase hex":     "chunk/" + strings.ToUpper(fmt.Sprintf("%064x", 0xabcdef)),
	} {
		if k, ok := Encode(key); ok {
			t.Errorf("%s: %q encoded as %x, want refused", name, key, k)
		}
	}
}

// Uppercase hex is the collision the canonical-only decoder exists to prevent:
// two byte-distinct object keys must never share a Key. hex.DecodeString accepts
// both cases, so a decoder built on it would fold these together.
func TestEncode_IsInjectiveAcrossHexCase(t *testing.T) {
	lower := "chunk/" + fmt.Sprintf("%064x", 0xdeadbeef)
	upper := "chunk/" + strings.ToUpper(fmt.Sprintf("%064x", 0xdeadbeef))

	s := NewSet()
	s.Add(lower)
	if s.Has(upper) {
		t.Errorf("adding %q reported %q as present", lower, upper)
	}
	s.Add(upper)
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2 — two distinct keys collapsed into one", s.Len())
	}
	if !s.Has(lower) || !s.Has(upper) {
		t.Errorf("both keys must be present: lower=%v upper=%v", s.Has(lower), s.Has(upper))
	}
}

// Set is total: whatever the shape, a key that was added is reported. A key
// silently dropped here is an object prune's sweep would delete.
func TestSet_KeepsUnshapedKeys(t *testing.T) {
	unshaped := []string{
		"index/latest",
		"index/packs",
		"chunk/valid",
		"content/valid-content-hash",
		"filemeta/" + strings.Repeat("Z", DigestHexLen),
		"chunk/" + strings.ToUpper(fmt.Sprintf("%064x", 0xabc)),
		"unknown-namespace/" + fmt.Sprintf("%064x", 7),
		"",
	}

	s := NewSet()
	for _, key := range unshaped {
		if !s.Add(key) {
			t.Errorf("%q: Add reported it was already present", key)
		}
	}
	for _, key := range unshaped {
		if !s.Has(key) {
			t.Errorf("%q: added but Has says no", key)
		}
	}
	if len(s.compact) != 0 {
		t.Errorf("unshaped keys were encoded compactly: %v", s.compact)
	}
	if s.Len() != len(unshaped) {
		t.Errorf("Len = %d, want %d", s.Len(), len(unshaped))
	}
}

// The two maps must not shadow each other: a mixed workload has to report every
// key exactly once, whichever side it landed on.
func TestSet_MixesShapedAndUnshapedKeys(t *testing.T) {
	s := NewSet()
	shaped := "filemeta/" + fmt.Sprintf("%064x", 1)
	unshaped := "index/latest"

	s.Add(shaped)
	s.Add(unshaped)

	if !s.Has(shaped) || !s.Has(unshaped) {
		t.Fatalf("shaped=%v unshaped=%v, want both present", s.Has(shaped), s.Has(unshaped))
	}
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2", s.Len())
	}
	if s.Has("filemeta/" + fmt.Sprintf("%064x", 2)) {
		t.Error("reported a key that was never added")
	}
	if len(s.compact) != 1 || len(s.fallback) != 1 {
		t.Errorf("keys were filed on the wrong side: compact=%d fallback=%d",
			len(s.compact), len(s.fallback))
	}
}

func TestSet_AddReportsFirstInsertionOnly(t *testing.T) {
	s := NewSet()
	for _, key := range []string{"chunk/" + fmt.Sprintf("%064x", 3), "index/latest"} {
		if !s.Add(key) {
			t.Errorf("%q: first Add reported a duplicate", key)
		}
		if s.Add(key) {
			t.Errorf("%q: second Add reported a fresh insertion", key)
		}
	}
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2", s.Len())
	}
}

func TestDecodeDigest(t *testing.T) {
	digest, ok := DecodeDigest(strings.Repeat("ff", 32))
	if !ok {
		t.Fatal("canonical hex did not decode")
	}
	for i, b := range digest {
		if b != 0xff {
			t.Fatalf("byte %d = %#x, want 0xff", i, b)
		}
	}
	if _, ok := DecodeDigest(strings.Repeat("f", 63)); ok {
		t.Error("a 63-character digest decoded")
	}
}
