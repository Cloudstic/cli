package hamt

// Payload is the object bodies a format-v3 leaf entry carries alongside its
// value (RFC 0026 §2).
//
// In format v2 a leaf entry's value names a "filemeta/<sha256>" object stored
// standalone, and that filemeta names a "content/<hash>" object holding either
// inline bytes or a chunk list. In v3 both bodies move into the leaf: the value
// string stays exactly what it was — the content address of Meta, which is what
// keeps change detection, diff and dedup semantics identical across formats —
// but the bytes it names travel with the entry, so reading an entry's metadata
// or a small file's content costs no store request beyond the leaf itself.
//
// A payload is immutable once attached: entries carry a pointer, and everything
// that copies an entry (clone, buildNode, subtreeEntries) shares it.
type Payload struct {
	// Meta is the canonical encoding of the entry's metadata — the exact bytes
	// whose content address is the entry's value. The tree never decodes it.
	Meta []byte

	// Size is the entry's content size in bytes. Zero for folders.
	Size int64

	// Body locates the entry's content inside a blob object, for content small
	// enough not to be chunked. Nil when the content is chunked or empty.
	//
	// The content used to live here directly, as an Inline []byte. Measuring
	// that showed a leaf was 3% metadata and 97% content, while every
	// operation but restore reads only the metadata — so the content moved
	// out into blob/ objects and the entry keeps a reference to where it
	// landed (RFC 0026, "Revision: metadata and content become separate
	// objects").
	Body *BodyRef

	// Chunks is the ordered list of "chunk/<hash>" refs reconstructing the
	// entry's content. Nil when the content is inline or empty.
	Chunks []string
}

// approxSize is the payload's contribution to its leaf's encoded size, used by
// the byte-budget split rule. It intentionally overcounts slightly (fixed
// per-field overhead instead of varint widths); the budget is a target, not a
// contract.
func (p *Payload) approxSize() int {
	if p == nil {
		return 0
	}
	n := len(p.Meta) + 16
	if p.Body != nil {
		n += len(p.Body.Blob) + 24
	}
	for _, c := range p.Chunks {
		n += len(c) + 2
	}
	return n
}

// BodyRef locates one entry's file body within a blob object (RFC 0026).
//
// A reader holding this needs nothing else: the blob's ref, the byte range,
// and — from the entry's metadata — the content hash that keys the member's
// seal. No index read, no catalog, one ranged request.
type BodyRef struct {
	// Blob is the "blob/<hash>" object holding the body.
	Blob string

	// Offset and Length delimit the body's sealed bytes within that object.
	// They address the stored object, since they are what a ranged GET takes.
	Offset int64
	Length int64

	// Total is the blob object's whole stored size, repeated in every entry
	// that references it.
	//
	// Three or four bytes that give consolidation its denominator. Deciding
	// "this blob is mostly garbage" needs the blob's size, and an entry
	// otherwise knows only its own slice; with this, a backup accumulates live
	// bytes per blob as it walks and already holds what to divide by — no
	// lookup, no second index, no read of the blob itself.
	//
	// Stored rather than plaintext size, and the distinction is not pedantic:
	// members are compressed, so dividing summed stored lengths by a plaintext
	// total would report a full blob as roughly half empty and consolidate it,
	// turning compression into apparent waste.
	Total int64
}
