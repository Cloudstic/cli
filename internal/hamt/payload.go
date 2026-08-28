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

	// Inline is the entry's whole content, for entries small enough to store
	// in place. Nil when the content is chunked or empty.
	Inline []byte

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
	n := len(p.Meta) + len(p.Inline) + 16
	for _, c := range p.Chunks {
		n += len(c) + 2
	}
	return n
}
