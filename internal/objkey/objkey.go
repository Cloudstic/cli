// Package objkey holds the compact in-memory form of a repository object key,
// for the structures that must hold one entry per object in the repository.
//
// Almost every key is "<namespace>/<64 hex characters>": a namespace from a
// fixed set, and a SHA-256. Held as a string that is 73 bytes of text plus a
// header, for 32 bytes of hash — and, as a map key, an interior pointer the
// garbage collector must trace on a structure whose size is the repository's.
// Decoding the hash and reducing the namespace to a byte puts the key inline in
// the map: no separate allocation, and nothing for the collector to chase.
//
// Measured on 200,000 entries by the pack catalog, the first structure to use
// this: 158 B/entry as a map keyed by the key string, against 73 B/entry here.
//
// This is not repository format. A Key never reaches a store — every caller
// hands back full key strings — so it lives here rather than in internal/core,
// which holds the types that *are* written down. What it must be is faithful:
// see Set for the totality the callers depend on.
package objkey

import (
	"encoding/hex"
	"strings"
)

// DigestHexLen is the length of a hex-encoded SHA-256 digest, the suffix shape
// of every content-addressed object key.
const DigestHexLen = 64

// Key is a namespace byte followed by a decoded SHA-256.
type Key [1 + 32]byte

// Namespaces are the prefixes a Key can encode: the content-addressed object
// namespaces of the repository object model.
//
// The index into this slice is the namespace byte. That byte never leaves the
// process, so the table is not repository format and reordering it could not
// corrupt anything stored. It would still be a bug — a Key encoded before the
// change decodes to a different namespace after it — so treat the table as
// append-only.
var Namespaces = []string{"filemeta/", "node/", "snapshot/", "chunk/", "content/"}

// Encode renders key compactly, reporting false for a key that does not have
// the "<namespace><64 lowercase hex chars>" shape.
//
// Encoding is injective: two keys that differ as strings never produce the same
// Key. That rests on rejecting non-canonical hex — see DecodeDigest — and it is
// what lets a caller treat a Key as standing for its key rather than merely
// hashing it.
func Encode(key string) (Key, bool) {
	for i, prefix := range Namespaces {
		hexPart, found := strings.CutPrefix(key, prefix)
		if !found {
			continue
		}
		digest, ok := DecodeDigest(hexPart)
		if !ok {
			return Key{}, false
		}
		var k Key
		k[0] = byte(i)
		copy(k[1:], digest[:])
		return k, true
	}
	return Key{}, false
}

// String returns the object key k encodes. Encode(k.String()) == k, for every
// Key that Encode itself produced.
func (k Key) String() string {
	return Namespaces[k[0]] + hex.EncodeToString(k[1:])
}

// DecodeDigest decodes s as a raw SHA-256 digest, reporting false for anything
// that is not exactly DigestHexLen canonical (lowercase) hex characters.
//
// Only the canonical spelling decodes. core.ComputeHash always produces
// lowercase hex, so every key this module writes takes that form; a decoder
// that also accepted uppercase would fold two byte-distinct object keys
// ("aa..." and "AA...") into one digest — a collision that turns an exact
// structure into an approximate one. A non-canonical key is simply not
// recognized, and its caller keeps it byte-exact in a string-keyed fallback.
//
// It decodes by hand rather than through hex.DecodeString because it is called
// on every lookup: that function allocates a fresh []byte per call, which would
// put a heap allocation on the path this representation exists to keep free of
// them.
func DecodeDigest(s string) ([32]byte, bool) {
	var digest [32]byte
	if len(s) != DigestHexLen {
		return digest, false
	}
	for i := range 32 {
		hi, ok1 := lowerHexNibble(s[2*i])
		lo, ok2 := lowerHexNibble(s[2*i+1])
		if !ok1 || !ok2 {
			return [32]byte{}, false
		}
		digest[i] = hi<<4 | lo
	}
	return digest, true
}

// lowerHexNibble decodes a single canonical (lowercase) hex digit. It
// deliberately rejects 'A'-'F': see DecodeDigest.
func lowerHexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	default:
		return 0, false
	}
}
