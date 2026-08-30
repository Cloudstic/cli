// Package blob implements the format-v3 blob object: a packed run of file
// bodies that a reader can fetch one member of (RFC 0026).
//
// A v3 leaf entry names where its body lives — (blob ref, offset, length) —
// rather than carrying the body itself, so the structure that change
// detection, prune, ls, find and diff walk holds metadata and nothing else.
// The bodies go here.
//
// # Layout
//
//	member_1 || member_2 || ... || member_n || index || uint32 index length
//
// Each member is compressed and sealed **independently**: the aggregate is a
// container, not a cryptographic unit, so a reader holding one entry can fetch
// that entry's byte range and decrypt exactly it. A member's codec byte lives
// inside its sealed bytes rather than in the index, which is what makes a
// ranged read self-describing — needing the index to decode a member would
// cost the second request the range exists to avoid.
//
// The index trails the members rather than leading them so a writer can emit
// members as it packs them, which is restic's reason too. It makes a blob
// self-describing, so the repository needs no blob catalog — the same property
// that lets PackStore heal a missing catalog from footers (RFC 0018), reached
// here by not having one.
//
// # Addressing
//
// A blob's ref is the hash of its members' digests, in order — a manifest of
// what it holds rather than of the bytes it stores.
//
// Hashing the concatenated bodies instead would be ambiguous about member
// boundaries: ["a", "bc"] and ["ab", "c"] concatenate identically and would
// name one object with two different layouts, and an empty file reaches that
// without contrivance, since ["", "abc"] and ["abc"] also concatenate alike.
// The result would be a stored blob with one member partition and leaf entries
// carrying offsets from the other. Fixed-width digests remove the ambiguity,
// commit to the bodies exactly as strongly — a member's digest is its identity
// everywhere else here — and are already in hand, so naming a blob costs no
// second pass over its content.
//
// A blob is in any case the first object whose plaintext **no reader ever
// assembles**: readers want one member. So core.VerifyRef can never be applied
// to a blob, "blob/" is deliberately not in core.SelfAddressedPrefixes, and the
// binding whole-object addressing would have supplied comes from the AAD
// instead — every member is sealed against its containing blob's ref, so a
// member lifted from another blob, or moved within one, fails to authenticate.
// See crypto.MemberSealer.
package blob

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/cloudstic/cli/pkg/crypto"
)

// Prefix is the namespace blobs are stored under.
const Prefix = "blob/"

const (
	// codecRaw means the member's bytes are its body verbatim, used when
	// compression did not pay.
	codecRaw byte = 0x00
	// codecZstd means the member's bytes are zstd-compressed.
	codecZstd byte = 0x01
)

// indexMagic identifies a blob's trailing index once decoded. It is checked
// after opening rather than before, so a wrong key fails as a decryption error
// rather than as a malformed index.
var indexMagic = [4]byte{'C', 'S', 'B', 0x01}

// indexLenSize is the width of the trailing length field.
const indexLenSize = 4

// indexKeyDomain stands in for a member's content hash when deriving the key
// that seals a blob's index. See sealIndex for why the index cannot be keyed
// on its own hash, and why a constant is sound here.
//
// It is deliberately not a hex digest, so it can never collide with a real
// member's content hash and let an index be opened as a body or the reverse.
const indexKeyDomain = "cloudstic-blob-index-v1"

// ErrMalformed reports a blob whose framing is not self-consistent: a
// truncated object, an index length that does not fit, or an index that does
// not decode. It is never downgraded to "no members" — a garbage collector
// reading "cannot decode" as "not referenced" deletes a live repository
// (docs/compatibility.md).
var ErrMalformed = errors.New("blob: malformed blob object")

var (
	zstdEncoder *zstd.Encoder
	zstdDecoder *zstd.Decoder
	zstdOnce    sync.Once
)

func initZstd() {
	zstdOnce.Do(func() {
		var err error
		// Concurrency 1: blobs are sealed from many goroutines already, so an
		// encoder spawning workers of its own multiplies rather than helps.
		zstdEncoder, err = zstd.NewWriter(nil,
			zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			panic(err) // cannot fail with these options
		}
		zstdDecoder, err = zstd.NewReader(nil)
		if err != nil {
			panic(err)
		}
	})
}

// Placement is where one member ended up: the byte range a reader fetches, and
// the content hash that names it.
//
// Offset and Length address the **stored** object, not the plaintext, because
// they are what a ranged GET is given.
type Placement struct {
	// ContentHash is the hex SHA-256 of the member's body — the same value the
	// entry's metadata records, which is why a leaf entry needs no second copy.
	ContentHash string

	// Offset and Length delimit the member's sealed bytes within the blob.
	Offset int64
	Length int64

	// PlainSize is the body's length before compression. Carried so a reader
	// can size a buffer, and so check can spot a member that decodes to the
	// wrong length without hashing it.
	PlainSize int64
}

// Writer accumulates bodies and seals them into one blob.
//
// It is not safe for concurrent use: one goroutine packs a blob, which is what
// keeps members in walk order.
type Writer struct {
	// sealer is nil in an unencrypted repository, where members are stored as
	// they are compressed. The blob's shape does not otherwise change, so the
	// two cases differ in exactly one place.
	sealer *crypto.MemberSealer

	bodies []memberBody
	seen   map[string]int // content hash -> index in bodies
	plain  int64
}

type memberBody struct {
	// hash is the canonical lowercase hex digest, which is what the sealer
	// takes as key material.
	hash string
	// digest is the same value in raw form, used to name the blob. Add
	// computes it to verify the caller's hash, so keeping it costs nothing.
	digest [sha256.Size]byte
	body   []byte
}

// NewWriter returns a Writer sealing with sealer, or storing members
// unsealed when sealer is nil.
func NewWriter(sealer *crypto.MemberSealer) *Writer {
	initZstd()
	return &Writer{sealer: sealer, seen: make(map[string]int)}
}

// Add appends one body, which the Writer retains until Seal.
//
// contentHash must be the hex SHA-256 of body; it is the AEAD key material and
// the index's name for the member, so a wrong value is refused here rather
// than producing a blob nothing can open. A body already added is not stored
// twice — two entries with the same content share one member, and the second
// Add is what makes that free.
func (w *Writer) Add(contentHash string, body []byte) error {
	digest := sha256.Sum256(body)
	got := hex.EncodeToString(digest[:])
	if got != contentHash {
		// Checked rather than trusted because the failure is otherwise silent
		// and permanent. A wrong hash both misnames the blob and mis-keys the
		// member, but the sharper case is the dedup below: a second, different
		// body offered under a hash already seen would be dropped, and its
		// entry would then point at the first body's bytes. That is wrong data
		// returned successfully, which no later check catches.
		//
		// It costs a second pass over the content — about a tenth of what
		// sealing the same bytes costs — and it is what lets the blob's ref be
		// built from these digests rather than from the bodies again.
		return fmt.Errorf("blob: content hash %q does not match the body, which hashes to %s", contentHash, got)
	}
	if _, ok := w.seen[contentHash]; ok {
		return nil
	}
	w.seen[contentHash] = len(w.bodies)
	w.bodies = append(w.bodies, memberBody{hash: got, digest: digest, body: body})
	w.plain += int64(len(body))
	return nil
}

// Len is how many distinct members the blob holds.
func (w *Writer) Len() int { return len(w.bodies) }

// PlaintextBytes is the total body bytes added, which is what a caller budgets
// a blob against — the stored size is not known until Seal, and a budget that
// moved with compressibility would make blob sizes depend on content.
func (w *Writer) PlaintextBytes() int64 { return w.plain }

// Seal compresses, seals and lays out every member, returning the blob's ref,
// its stored bytes, and where each member landed.
//
// The ref is the hash of the members' bodies in order, so it is fixed before
// any member is sealed — which is what lets each member be sealed against it.
func (w *Writer) Seal() (ref string, data []byte, members []Placement, err error) {
	if len(w.bodies) == 0 {
		return "", nil, nil, errors.New("blob: refusing to seal a blob with no members")
	}

	ref = Prefix + w.memberSequenceHash()
	aad := []byte(ref)

	data = make([]byte, 0, w.plain+int64(len(w.bodies))*int64(crypto.MemberOverhead))
	members = make([]Placement, 0, len(w.bodies))

	for _, m := range w.bodies {
		sealed, err := w.sealMember(m, aad)
		if err != nil {
			return "", nil, nil, err
		}
		members = append(members, Placement{
			ContentHash: m.hash,
			Offset:      int64(len(data)),
			Length:      int64(len(sealed)),
			PlainSize:   int64(len(m.body)),
		})
		data = append(data, sealed...)
	}

	index, err := w.sealIndex(members, aad)
	if err != nil {
		return "", nil, nil, err
	}
	if int64(len(index)) > math.MaxUint32 {
		// Unreachable at any sane blob size, and checked anyway: the trailer
		// is a uint32, so a larger index would be silently truncated and the
		// blob would read back as malformed rather than as too big.
		return "", nil, nil, fmt.Errorf("blob: index of %d bytes exceeds the %d the trailer can express",
			len(index), int64(math.MaxUint32))
	}
	data = append(data, index...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(index)))

	return ref, data, members, nil
}

// memberSequenceHash names the blob by its members' digests in order.
//
// Hashing the *bodies* would be ambiguous about where one member ends and the
// next begins: ["a", "bc"] and ["ab", "c"] concatenate identically, and so
// would name one object with two different layouts. An empty file makes that
// reachable without contrivance — ["", "abc"] and ["abc"] have the same
// concatenation — and the consequence is a repository whose stored blob has
// one member partition while some leaf's entries carry offsets from the other.
// Sealed, those reads fail to authenticate; unsealed, they return the wrong
// body.
//
// Fixed-width digests make the boundaries unambiguous, and they commit to the
// bodies exactly as strongly, since a member's digest is its identity
// everywhere else in the repository. They are also already in hand, so naming
// a blob no longer hashes its whole content a second time.
func (w *Writer) memberSequenceHash() string {
	h := sha256.New()
	for _, m := range w.bodies {
		_, _ = h.Write(m.digest[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sealMember compresses one body, prefixes the codec, and seals the result.
//
// The codec byte travels *inside* the sealed bytes so that a ranged read is
// self-describing: a reader holding only (offset, length) and the content hash
// can decode the member without touching the index.
func (w *Writer) sealMember(m memberBody, aad []byte) ([]byte, error) {
	framed := compress(m.body)
	if w.sealer == nil {
		return framed, nil
	}
	return w.sealer.SealMember(framed, m.hash, aad)
}

func compress(body []byte) []byte {
	out := zstdEncoder.EncodeAll(body, make([]byte, 1, len(body)+1))
	if len(out) > len(body)+1 {
		out = append(make([]byte, 1, len(body)+1), body...)
		out[0] = codecRaw
		return out
	}
	out[0] = codecZstd
	return out
}

func decompress(framed []byte, plainSize int64) ([]byte, error) {
	if len(framed) == 0 {
		return nil, fmt.Errorf("%w: member has no codec byte", ErrMalformed)
	}
	body := framed[1:]
	switch framed[0] {
	case codecRaw:
		out := make([]byte, len(body))
		copy(out, body)
		return out, nil
	case codecZstd:
		var dst []byte
		if plainSize > 0 {
			dst = make([]byte, 0, plainSize)
		}
		out, err := zstdDecoder.DecodeAll(body, dst)
		if err != nil {
			return nil, fmt.Errorf("%w: decompress member: %w", ErrMalformed, err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: unknown member codec 0x%02x", ErrMalformed, framed[0])
	}
}

// encodeIndex renders the index's plaintext.
//
//	magic(4) || count uvarint || count x { hash(32 raw) || offset || length || plain size }
//
// Hashes go in raw rather than as hex text: they are 32 bytes each and a blob
// holds hundreds of members, so the hex form would cost more than the index's
// other fields put together.
func encodeIndex(members []Placement) ([]byte, error) {
	buf := make([]byte, 0, 8+len(members)*(sha256.Size+12))
	buf = append(buf, indexMagic[:]...)
	buf = binary.AppendUvarint(buf, uint64(len(members)))
	for _, m := range members {
		raw, err := hex.DecodeString(m.ContentHash)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("blob: index entry has a bad content hash %q", m.ContentHash)
		}
		buf = append(buf, raw...)
		buf = binary.AppendUvarint(buf, uint64(m.Offset))
		buf = binary.AppendUvarint(buf, uint64(m.Length))
		buf = binary.AppendUvarint(buf, uint64(m.PlainSize))
	}
	return buf, nil
}

func (w *Writer) sealIndex(members []Placement, aad []byte) ([]byte, error) {
	plain, err := encodeIndex(members)
	if err != nil {
		return nil, err
	}
	if w.sealer == nil {
		return plain, nil
	}
	// The index is sealed like a member, but it cannot be keyed on its own
	// hash: a reader has to derive the key *before* it can decrypt the index,
	// and it does not know the plaintext until it has. So the key material is
	// a fixed domain string, and the blob's ref in the AAD is what makes the
	// key blob-specific.
	//
	// That is safe because a blob has exactly one index, and its contents are
	// a function of the ref: the ref is the hash of the members' bodies in
	// order, and compression, sealing and layout are all deterministic. Two
	// distinct indexes under one key would mean two distinct member lists
	// hashing to the same ref.
	return w.sealer.SealMember(plain, indexKeyDomain, aad)
}
