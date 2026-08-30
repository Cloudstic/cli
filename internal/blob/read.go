package blob

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/cloudstic/cli/pkg/crypto"
)

// ReadMember decodes one member from exactly its own bytes.
//
// This is the read the format exists for: a caller holding a leaf entry has
// the blob's ref, the member's byte range and its content hash, and needs no
// index and no second request. sealed must be precisely the bytes at that
// range — the member's codec travels inside them.
//
// The returned body is *not* checked against contentHash. The hash is AEAD key
// material, so a member that opens at all is overwhelmingly the right one, and
// the callers that need certainty (restore, check -read-data) hash the
// reconstructed stream anyway. Hashing here would hash the repository's whole
// content a second time on every read.
func ReadMember(sealed []byte, contentHash, ref string, sealer *crypto.MemberSealer, plainSize int64) ([]byte, error) {
	framed := sealed
	if sealer != nil {
		var err error
		framed, err = sealer.OpenMember(sealed, contentHash, []byte(ref))
		if err != nil {
			return nil, fmt.Errorf("open member %s of %s: %w", contentHash, ref, err)
		}
	}
	return decompress(framed, plainSize)
}

// Index is a blob's trailing member table.
type Index struct {
	Members []Placement
	// StoredBytes is the blob object's whole size, which is the denominator
	// for utilization: the live fraction of a blob is the summed Length of the
	// members still referenced over this. It is deliberately the stored size
	// rather than the plaintext size — comparing stored member lengths against
	// a plaintext total would read compression as waste and consolidate blobs
	// that are perfectly full.
	StoredBytes int64
}

// ParseIndex reads the index trailing a whole blob object.
//
// It needs the complete object because the index sits at the end; a caller
// that has only fetched a range should use ReadMember instead. check and any
// future consolidation pass are the callers here.
func ParseIndex(data []byte, ref string, sealer *crypto.MemberSealer) (*Index, error) {
	if len(data) < indexLenSize {
		return nil, fmt.Errorf("%w: %d bytes cannot hold an index length", ErrMalformed, len(data))
	}
	n := int64(binary.BigEndian.Uint32(data[len(data)-indexLenSize:]))
	end := int64(len(data) - indexLenSize)
	if n <= 0 || n > end {
		return nil, fmt.Errorf("%w: index length %d does not fit in %d bytes", ErrMalformed, n, end)
	}
	sealedIndex := data[end-n : end]

	plain := sealedIndex
	if sealer != nil {
		var err error
		plain, err = sealer.OpenMember(sealedIndex, indexKeyDomain, []byte(ref))
		if err != nil {
			return nil, fmt.Errorf("open index of %s: %w", ref, err)
		}
	}

	members, err := decodeIndex(plain)
	if err != nil {
		return nil, err
	}
	// Every member must lie inside the region the index itself does not
	// occupy. An offset reaching into the index would let a crafted blob make
	// a reader authenticate the index as though it were a body.
	bodyEnd := end - n
	for _, m := range members {
		if m.Offset < 0 || m.Length < 0 || m.Offset+m.Length > bodyEnd {
			return nil, fmt.Errorf("%w: member %s spans [%d,%d) outside the %d bytes of members",
				ErrMalformed, m.ContentHash, m.Offset, m.Offset+m.Length, bodyEnd)
		}
	}
	return &Index{Members: members, StoredBytes: int64(len(data))}, nil
}

func decodeIndex(plain []byte) ([]Placement, error) {
	if len(plain) < len(indexMagic) || [4]byte(plain[:4]) != indexMagic {
		return nil, fmt.Errorf("%w: index does not begin with the blob index magic", ErrMalformed)
	}
	d := &cursor{data: plain, off: len(indexMagic)}
	count, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	// A count is a length prefix over attacker-influenced bytes, so it is
	// checked against what remains rather than trusted to allocate.
	if minSize := count * uint64(sha256.Size); minSize > uint64(len(plain)-d.off) {
		return nil, fmt.Errorf("%w: index claims %d members, too many for %d remaining bytes",
			ErrMalformed, count, len(plain)-d.off)
	}
	out := make([]Placement, 0, count)
	for i := uint64(0); i < count; i++ {
		raw, err := d.take(sha256.Size)
		if err != nil {
			return nil, err
		}
		var p Placement
		p.ContentHash = hex.EncodeToString(raw)
		if p.Offset, err = d.varintInt64(); err != nil {
			return nil, err
		}
		if p.Length, err = d.varintInt64(); err != nil {
			return nil, err
		}
		if p.PlainSize, err = d.varintInt64(); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if d.off != len(plain) {
		return nil, fmt.Errorf("%w: %d trailing bytes after the index", ErrMalformed, len(plain)-d.off)
	}
	return out, nil
}

// cursor is a bounds-checked reader over an index's plaintext. The index has
// already been authenticated when the repository is encrypted, but the decoder
// must not trust even authenticated bytes to be well-formed — an unencrypted
// repository authenticates nothing.
type cursor struct {
	data []byte
	off  int
}

func (c *cursor) uvarint() (uint64, error) {
	v, n := binary.Uvarint(c.data[c.off:])
	if n <= 0 {
		return 0, fmt.Errorf("%w: truncated varint at offset %d", ErrMalformed, c.off)
	}
	c.off += n
	return v, nil
}

func (c *cursor) varintInt64() (int64, error) {
	v, err := c.uvarint()
	if err != nil {
		return 0, err
	}
	if v > 1<<62 {
		return 0, fmt.Errorf("%w: implausible value %d at offset %d", ErrMalformed, v, c.off)
	}
	return int64(v), nil
}

func (c *cursor) take(n int) ([]byte, error) {
	if n > len(c.data)-c.off {
		return nil, fmt.Errorf("%w: field of %d bytes exceeds the %d remaining", ErrMalformed, n, len(c.data)-c.off)
	}
	b := c.data[c.off : c.off+n]
	c.off += n
	return b, nil
}
