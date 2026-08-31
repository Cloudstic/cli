package hamt

// The format-v3 binary node encoding (RFC 0026 §2).
//
// v2 nodes are JSON documents hashed by core.ComputeJSONHash. That encoding is
// kept bit-for-bit for v2 repositories; a v3 repository writes every node in
// the binary form below and never mixes the two. NodeStore.load distinguishes
// them by their first bytes — JSON always starts with '{', a v3 node with the
// magic — so reading stays format-agnostic and needs no flag.
//
// The encoding is canonical: entries are sorted by key (an invariant the tree
// already maintains), field order is fixed, and integers are unsigned varints.
// The node's ref is "node/" + SHA-256 over exactly these bytes, so two trees
// holding the same logical content produce the same refs, which is what
// node-level deduplication between snapshots rests on.
//
// Layout:
//
//	magic   4 bytes  "CSN3"
//	kind    1 byte   0 = internal, 1 = leaf
//
//	internal:
//	  bitmap  4 bytes big-endian
//	  count   uvarint
//	  count × ref (uvarint length + bytes)
//
//	leaf:
//	  count   uvarint
//	  count × entry:
//	    key      uvarint length + bytes
//	    pathKey  uvarint length + bytes
//	    value    uvarint length + bytes
//	    flags    1 byte: 1 = has payload, 4 = has chunks, 8 = has body ref
//	                     (2 is retired; see entryFlagInlineRetired)
//	    payload (when flag 1):
//	      size    uvarint
//	      meta    uvarint length + bytes
//	      body    (when flag 8): blob ref (uvarint length + bytes),
//	                             offset, length, total (uvarint each)
//	      chunks  uvarint count, count × ref         (when flag 4)

import (
	"encoding/binary"
	"fmt"
)

const nodeMagicV3 = "CSN3"

const (
	nodeKindInternalV3 byte = 0
	nodeKindLeafV3     byte = 1

	entryFlagPayload byte = 1

	// entryFlagInlineRetired marked an entry whose content was carried in the
	// leaf itself. Format v3 was revised before release to keep the content in
	// a blob/ object and the reference in the entry, so nothing writes this
	// bit any more.
	//
	// The bit is retired rather than reused. entryFlagBody takes a new value,
	// so a leaf written by a pre-revision build is refused with an error that
	// says what it is, instead of having its inline bytes decoded as a blob
	// reference. Reusing 2 would have made the two encodings indistinguishable
	// on the one axis that matters.
	entryFlagInlineRetired byte = 2

	entryFlagChunks byte = 4

	// entryFlagBody marks an entry whose body lives in a blob/ object, with
	// this entry naming the byte range (RFC 0026).
	entryFlagBody byte = 8
)

// isV3NodeData reports whether data begins with the v3 node magic.
func isV3NodeData(data []byte) bool {
	return len(data) >= len(nodeMagicV3) && string(data[:len(nodeMagicV3)]) == nodeMagicV3
}

func appendUvarint(buf []byte, v uint64) []byte {
	return binary.AppendUvarint(buf, v)
}

func appendString(buf []byte, s string) []byte {
	buf = appendUvarint(buf, uint64(len(s)))
	return append(buf, s...)
}

func appendBytes(buf []byte, b []byte) []byte {
	buf = appendUvarint(buf, uint64(len(b)))
	return append(buf, b...)
}

// encodeNodeV3 renders sn in the binary v3 form.
func encodeNodeV3(sn *storedNode) []byte {
	// A rough capacity guess; appends grow it when payloads are large.
	buf := make([]byte, 0, 256)
	buf = append(buf, nodeMagicV3...)

	if sn.Type == nodeTypeInternal {
		buf = append(buf, nodeKindInternalV3)
		buf = binary.BigEndian.AppendUint32(buf, sn.Bitmap)
		buf = appendUvarint(buf, uint64(len(sn.Children)))
		for _, ref := range sn.Children {
			buf = appendString(buf, ref)
		}
		return buf
	}

	buf = append(buf, nodeKindLeafV3)
	buf = appendUvarint(buf, uint64(len(sn.Entries)))
	for i := range sn.Entries {
		e := &sn.Entries[i]
		buf = appendString(buf, e.Key)
		buf = appendString(buf, e.PathKey)
		buf = appendString(buf, e.Value)

		var flags byte
		if e.payload != nil {
			flags |= entryFlagPayload
			if e.payload.Body != nil {
				flags |= entryFlagBody
			}
			if len(e.payload.Chunks) > 0 {
				flags |= entryFlagChunks
			}
		}
		buf = append(buf, flags)
		if e.payload == nil {
			continue
		}
		buf = appendUvarint(buf, uint64(e.payload.Size))
		buf = appendBytes(buf, e.payload.Meta)
		if flags&entryFlagBody != 0 {
			b := e.payload.Body
			buf = appendString(buf, b.Blob)
			buf = appendUvarint(buf, uint64(b.Offset))
			buf = appendUvarint(buf, uint64(b.Length))
			buf = appendUvarint(buf, uint64(b.Total))
		}
		if flags&entryFlagChunks != 0 {
			buf = appendUvarint(buf, uint64(len(e.payload.Chunks)))
			for _, c := range e.payload.Chunks {
				buf = appendString(buf, c)
			}
		}
	}
	return buf
}

// v3Decoder is a cursor over a binary node. Every read checks bounds, so a
// truncated or corrupted object fails with an error rather than a panic —
// load has already verified the content hash, but the decoder must not trust
// even verified bytes to be well-formed.
type v3Decoder struct {
	data []byte
	off  int
}

func (d *v3Decoder) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.data[d.off:])
	if n <= 0 {
		return 0, fmt.Errorf("truncated varint at offset %d", d.off)
	}
	d.off += n
	return v, nil
}

func (d *v3Decoder) take(n uint64) ([]byte, error) {
	if n > uint64(len(d.data)-d.off) {
		return nil, fmt.Errorf("field of %d bytes exceeds remaining %d at offset %d", n, len(d.data)-d.off, d.off)
	}
	b := d.data[d.off : d.off+int(n)]
	d.off += int(n)
	return b, nil
}

func (d *v3Decoder) str() (string, error) {
	n, err := d.uvarint()
	if err != nil {
		return "", err
	}
	b, err := d.take(n)
	return string(b), err
}

func (d *v3Decoder) bytes() ([]byte, error) {
	n, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	b, err := d.take(n)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	// Copy out of the transport buffer so a decoded node does not pin the
	// whole object's backing array through the node cache.
	//
	// Aliasing instead looks obviously cheaper and is not: it was measured and
	// it made a 20,000-file backup worse on every axis — peak RSS 957 MB to
	// 2231 MB, allocation 3123 MB to 3292 MB, wall 1.71s to 2.49s. The copies
	// are 24% of that backup's allocation, so the arithmetic seems to favour
	// aliasing right up until the retention is accounted for.
	//
	// What breaks it is who holds the result. A leaf's payloads are most of
	// its bytes, so a *node* aliasing its buffer retains about what copying
	// would have. But payloads outlive the node: the meta loader keeps
	// p.Meta — a few hundred bytes — and each surviving slice pins the entire
	// megabytes-wide leaf buffer it came from. Backup holds metas for many
	// entries at once, so a small retained slice per entry pinned a whole leaf
	// each.
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

// skipBytes advances past a length-prefixed field without copying it, for a
// decode that does not want the field. See payloadRefsOnly.
func (d *v3Decoder) skipBytes() error {
	n, err := d.uvarint()
	if err != nil {
		return err
	}
	_, err = d.take(n)
	return err
}

func (d *v3Decoder) byte() (byte, error) {
	if d.off >= len(d.data) {
		return 0, fmt.Errorf("truncated byte at offset %d", d.off)
	}
	b := d.data[d.off]
	d.off++
	return b, nil
}

// payloadDetail selects how much of an entry's payload a decode materialises.
//
// Meta is the bulk of what is left in a leaf now that content lives in blob/
// objects, and a caller that only needs to know what an entry *reaches* pays
// for it needlessly. prune is exactly that caller: it wants the chunk refs and
// the body reference, never the metadata, and marking one 357 MB repository
// allocated 17 GB, 45% of it copying payload fields it never looked at.
//
// Note which field the reduction may not skip. Chunk refs and the body
// reference are both reachability, so both are decoded at every level; only
// Meta is dropped. A reduction that skipped the body reference would make
// prune mark no blobs and sweep every one of them.
//
// A reduced node is never cached. The cache holds complete nodes only, so a
// later reader that does need Meta or Inline cannot be served a node missing
// them — which is the one way this could go quietly wrong. A traversal visits
// each node once, so it loses nothing by not caching.
type payloadDetail int

const (
	payloadFull payloadDetail = iota
	payloadRefsOnly
)

// decodeNodeV3 parses a binary v3 node into the in-memory form.
func decodeNodeV3(data []byte) (*node, error) {
	return decodeNodeV3Detail(data, payloadFull)
}

// decodeNodeV3Detail is decodeNodeV3 with control over how much of each
// payload is materialised.
func decodeNodeV3Detail(data []byte, detail payloadDetail) (*node, error) {
	d := &v3Decoder{data: data, off: len(nodeMagicV3)}
	kind, err := d.byte()
	if err != nil {
		return nil, err
	}

	switch kind {
	case nodeKindInternalV3:
		if d.off+4 > len(data) {
			return nil, fmt.Errorf("truncated bitmap")
		}
		bitmap := binary.BigEndian.Uint32(data[d.off:])
		d.off += 4
		count, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		n := &node{bitmap: bitmap, children: make([]child, 0, count)}
		for i := uint64(0); i < count; i++ {
			ref, err := d.str()
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child{ref: ref})
		}
		if d.off != len(data) {
			return nil, fmt.Errorf("%d trailing bytes after internal node", len(data)-d.off)
		}
		return n, nil

	case nodeKindLeafV3:
		count, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		n := &node{leaf: true, entries: make([]leafEntry, 0, count)}
		for i := uint64(0); i < count; i++ {
			var e leafEntry
			if e.Key, err = d.str(); err != nil {
				return nil, err
			}
			if e.PathKey, err = d.str(); err != nil {
				return nil, err
			}
			if e.Value, err = d.str(); err != nil {
				return nil, err
			}
			flags, err := d.byte()
			if err != nil {
				return nil, err
			}
			if flags&entryFlagInlineRetired != 0 {
				// A leaf from a pre-revision v3 build, whose entries carry
				// their content rather than a reference to it. Refused rather
				// than guessed at: format v3 has never been released, so no
				// repository anyone was promised compatibility with can hold
				// one, and decoding those bytes as a blob reference is the one
				// outcome that must not happen quietly.
				return nil, fmt.Errorf(
					"leaf entry %q carries inline content, written by a pre-release format-v3 build; "+
						"this repository predates the blob encoding and must be recreated", e.Key)
			}
			if flags&entryFlagPayload != 0 {
				p := &Payload{}
				size, err := d.uvarint()
				if err != nil {
					return nil, err
				}
				p.Size = int64(size)
				if detail == payloadRefsOnly {
					if err := d.skipBytes(); err != nil {
						return nil, err
					}
				} else if p.Meta, err = d.bytes(); err != nil {
					return nil, err
				}
				if flags&entryFlagBody != 0 {
					// Decoded at every detail level. A body reference is what
					// makes a blob reachable, so a reduced decode that skipped
					// it would have prune mark nothing and sweep every blob in
					// the repository.
					b := &BodyRef{}
					if b.Blob, err = d.str(); err != nil {
						return nil, err
					}
					off, err := d.uvarint()
					if err != nil {
						return nil, err
					}
					length, err := d.uvarint()
					if err != nil {
						return nil, err
					}
					total, err := d.uvarint()
					if err != nil {
						return nil, err
					}
					b.Offset, b.Length, b.Total = int64(off), int64(length), int64(total)
					p.Body = b
				}
				if flags&entryFlagChunks != 0 {
					m, err := d.uvarint()
					if err != nil {
						return nil, err
					}
					p.Chunks = make([]string, 0, m)
					for j := uint64(0); j < m; j++ {
						c, err := d.str()
						if err != nil {
							return nil, err
						}
						p.Chunks = append(p.Chunks, c)
					}
				}
				e.payload = p
			}
			n.entries = append(n.entries, e)
		}
		if d.off != len(data) {
			return nil, fmt.Errorf("%d trailing bytes after leaf node", len(data)-d.off)
		}
		return n, nil

	default:
		return nil, fmt.Errorf("unknown v3 node kind %d", kind)
	}
}
