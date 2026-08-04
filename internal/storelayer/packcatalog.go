package storelayer

import (
	"encoding/hex"
	"strings"
)

// packCatalog maps an object key to its location in a packfile.
//
// It is the largest resident structure a repository forces on an operation — one
// entry per packed object — so it is stored compactly rather than as the obvious
// map[string]PackEntry.
//
// Almost every key is "<prefix>/<64 hex characters>": a namespace from a fixed
// set, and a SHA-256. Held as a string that is 73 bytes of text plus a header,
// for 32 bytes of hash. Decoding the hash and reducing the prefix to a byte puts
// the key inline in the map, with no separate allocation and no pointer for the
// garbage collector to chase. Pack refs are interned to an index into packRefs,
// and offsets fit in uint32 because a packfile is bounded by maxPackSize.
//
// Measured on 200,000 entries: 158 B/entry as map[string]PackEntry with pack refs
// already interned, against 73 B/entry here.
//
// Keys that do not have that shape — a legacy repository packed index/latest
// before the packable-prefix restriction existed — live in a fallback map. There
// are a handful at most, so it costs nothing to keep them exact rather than
// refuse them.
type packCatalog struct {
	compact  map[compactKey]compactEntry
	fallback map[string]PackEntry

	packRefs []string
	packIdx  map[string]uint32
}

// compactKey is a namespace byte followed by a decoded SHA-256.
type compactKey [1 + 32]byte

type compactEntry struct {
	pack   uint32
	offset uint32
	length uint32
}

// packableNamespaces are the prefixes a compact key can encode. The order fixes
// the namespace byte, so entries must only ever be appended.
var packableNamespaces = []string{"filemeta/", "node/", "snapshot/", "chunk/", "content/"}

func newPackCatalog() *packCatalog {
	return &packCatalog{
		compact:  make(map[compactKey]compactEntry),
		fallback: make(map[string]PackEntry),
		packIdx:  make(map[string]uint32),
	}
}

// encodeKey renders key compactly, reporting false for a key that does not have
// the "<known prefix>/<64 hex>" shape.
func encodeKey(key string) (compactKey, bool) {
	var out compactKey
	for i, prefix := range packableNamespaces {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		hexPart := key[len(prefix):]
		if len(hexPart) != 64 {
			return out, false
		}
		if _, err := hex.Decode(out[1:], []byte(hexPart)); err != nil {
			return out, false
		}
		out[0] = byte(i)
		return out, true
	}
	return out, false
}

func decodeKey(k compactKey) string {
	return packableNamespaces[k[0]] + hex.EncodeToString(k[1:])
}

// internPack returns the index for ref, adding it if new.
func (c *packCatalog) internPack(ref string) uint32 {
	if i, ok := c.packIdx[ref]; ok {
		return i
	}
	i := uint32(len(c.packRefs))
	c.packRefs = append(c.packRefs, ref)
	c.packIdx[ref] = i
	return i
}

func (c *packCatalog) Len() int { return len(c.compact) + len(c.fallback) }

// Get returns the entry for key.
//
// The fallback map is consulted even for a well-shaped key, because Set sends a
// location that does not fit the compact form there rather than truncating it.
// Checking only the compact map would make such an entry vanish — and an entry
// that silently disappears from this catalog is an object prune cannot see.
func (c *packCatalog) Get(key string) (PackEntry, bool) {
	if k, ok := encodeKey(key); ok {
		if e, found := c.compact[k]; found {
			return PackEntry{
				PackRef: c.packRefs[e.pack],
				Offset:  int64(e.offset),
				Length:  int64(e.length),
			}, true
		}
	}
	e, found := c.fallback[key]
	return e, found
}

func (c *packCatalog) Has(key string) bool {
	if k, ok := encodeKey(key); ok {
		if _, found := c.compact[k]; found {
			return true
		}
	}
	_, found := c.fallback[key]
	return found
}

// Set records where key lives.
//
// An offset or length beyond what uint32 holds cannot come from a packfile this
// code writes — maxPackSize bounds both — but the catalog is also read from a
// store, so such an entry goes to the fallback map rather than being truncated
// into a location that points somewhere else entirely.
func (c *packCatalog) Set(key string, entry PackEntry) {
	k, ok := encodeKey(key)
	if !ok || entry.Offset > int64(^uint32(0)) || entry.Length > int64(^uint32(0)) ||
		entry.Offset < 0 || entry.Length < 0 {
		// Interned here too. A fallback entry is rare, but "entries sharing a
		// pack share one string" should hold whatever shape the key has —
		// otherwise the property depends on which branch an entry happened to
		// take, which is not something a caller can reason about.
		entry.PackRef = c.packRefs[c.internPack(entry.PackRef)]
		c.fallback[key] = entry
		return
	}
	c.compact[k] = compactEntry{
		pack:   c.internPack(entry.PackRef),
		offset: uint32(entry.Offset),
		length: uint32(entry.Length),
	}
}

// Delete removes key from wherever it is held. Both maps are touched for the
// same reason Get consults both: a well-shaped key may still be in the fallback.
func (c *packCatalog) Delete(key string) {
	if k, ok := encodeKey(key); ok {
		delete(c.compact, k)
	}
	delete(c.fallback, key)
}

// Each calls fn for every entry. Iteration order is unspecified, as it was for
// the map this replaced.
func (c *packCatalog) Each(fn func(key string, entry PackEntry)) {
	for k, e := range c.compact {
		fn(decodeKey(k), PackEntry{
			PackRef: c.packRefs[e.pack],
			Offset:  int64(e.offset),
			Length:  int64(e.length),
		})
	}
	for key, entry := range c.fallback {
		fn(key, entry)
	}
}

// Keys returns every key, for callers that need the set rather than the entries.
func (c *packCatalog) Keys() []string {
	out := make([]string, 0, c.Len())
	c.Each(func(key string, _ PackEntry) { out = append(out, key) })
	return out
}
