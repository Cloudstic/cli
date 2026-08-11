package storelayer

import (
	"github.com/cloudstic/cli/internal/objkey"
)

// packCatalog maps an object key to its location in a packfile.
//
// It is the largest resident structure a repository forces on an operation — one
// entry per packed object — so it is stored compactly rather than as the obvious
// map[string]PackEntry.
//
// Keys are held in the compact form internal/objkey defines — a namespace byte
// and a decoded SHA-256, inline in the map with nothing for the garbage
// collector to chase — rather than as the 73-byte hex strings they arrive as.
// Pack refs are interned to an index into packRefs, and offsets fit in uint32
// because a packfile is bounded by maxPackSize.
//
// Measured on 200,000 entries: 158 B/entry as map[string]PackEntry with pack refs
// already interned, against 73 B/entry here.
//
// Keys that do not have that shape — a legacy repository packed index/latest
// before the packable-prefix restriction existed — live in a fallback map. There
// are a handful at most, so it costs nothing to keep them exact rather than
// refuse them.
type packCatalog struct {
	compact  map[objkey.Key]compactEntry
	fallback map[string]PackEntry

	packRefs []string
	packIdx  map[string]uint32
	// packEnd is parallel to packRefs: the highest Offset+Length recorded for
	// that pack, maintained on write so nobody has to scan for it. See
	// PackExtent.
	packEnd []int64
}

type compactEntry struct {
	pack   uint32
	offset uint32
	length uint32
}

func newPackCatalog() *packCatalog {
	return &packCatalog{
		compact:  make(map[objkey.Key]compactEntry),
		fallback: make(map[string]PackEntry),
		packIdx:  make(map[string]uint32),
	}
}

// internPack returns the index for ref, adding it if new.
func (c *packCatalog) internPack(ref string) uint32 {
	if i, ok := c.packIdx[ref]; ok {
		return i
	}
	i := uint32(len(c.packRefs))
	c.packRefs = append(c.packRefs, ref)
	c.packEnd = append(c.packEnd, 0)
	c.packIdx[ref] = i
	return i
}

// PackExtent reports how far into ref the entries recorded here reach — the
// highest Offset+Length seen for it — and whether ref is known at all.
//
// It is maintained on write because the alternative was reading it: admission
// needs a pack's size to decide whether transferring it whole beats ranging
// into it, and computing that by scanning the catalog made *every* PlanReads
// call allocate in proportion to the whole repository rather than to the keys
// it was asked about. Measured on a 512-key request: 2.1 MB and 30,013
// allocations against a 10,000-entry catalog, 41.6 MB and 600,013 against
// 200,000 — three allocations per object in the repository, spent rebuilding
// key strings that fillPackSizes then discarded. Backup made that visible by
// planning once per batch, but every planning caller was paying it.
//
// This is a high-water mark and does not fall when entries are deleted, which
// is deliberate: a deleted entry's bytes stay in the stored packfile until
// `Repack` rewrites it, so the mark tracks what fetching the pack would
// actually transfer. Recomputing from surviving entries — what the scan did —
// understated that for any repository that had pruned.
func (c *packCatalog) PackExtent(ref string) (int64, bool) {
	i, ok := c.packIdx[ref]
	if !ok {
		return 0, false
	}
	return c.packEnd[i], true
}

// noteExtent records that pack idx is occupied at least up to end.
func (c *packCatalog) noteExtent(idx uint32, end int64) {
	if end > c.packEnd[idx] {
		c.packEnd[idx] = end
	}
}

func (c *packCatalog) Len() int { return len(c.compact) + len(c.fallback) }

// Get returns the entry for key.
//
// The fallback map is consulted even for a well-shaped key, because Set sends a
// location that does not fit the compact form there rather than truncating it.
// Checking only the compact map would make such an entry vanish — and an entry
// that silently disappears from this catalog is an object prune cannot see.
func (c *packCatalog) Get(key string) (PackEntry, bool) {
	if k, ok := objkey.Encode(key); ok {
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
	if k, ok := objkey.Encode(key); ok {
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
	k, ok := objkey.Encode(key)
	if !ok || entry.Offset > int64(^uint32(0)) || entry.Length > int64(^uint32(0)) ||
		entry.Offset < 0 || entry.Length < 0 {
		// Interned here too. A fallback entry is rare, but "entries sharing a
		// pack share one string" should hold whatever shape the key has —
		// otherwise the property depends on which branch an entry happened to
		// take, which is not something a caller can reason about.
		idx := c.internPack(entry.PackRef)
		entry.PackRef = c.packRefs[idx]
		c.noteExtent(idx, entry.Offset+entry.Length)
		c.fallback[key] = entry
		return
	}
	idx := c.internPack(entry.PackRef)
	c.noteExtent(idx, entry.Offset+entry.Length)
	c.compact[k] = compactEntry{
		pack:   idx,
		offset: uint32(entry.Offset),
		length: uint32(entry.Length),
	}
}

// Delete removes key from wherever it is held. Both maps are touched for the
// same reason Get consults both: a well-shaped key may still be in the fallback.
func (c *packCatalog) Delete(key string) {
	if k, ok := objkey.Encode(key); ok {
		delete(c.compact, k)
	}
	delete(c.fallback, key)
}

// Each calls fn for every entry. Iteration order is unspecified, as it was for
// the map this replaced.
func (c *packCatalog) Each(fn func(key string, entry PackEntry)) {
	for k, e := range c.compact {
		fn(k.String(), PackEntry{
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
