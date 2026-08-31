package crypto

// Blob-member sealing for repository format v3 (RFC 0026, "Encryption").
//
// A v3 blob is a packed run of file bodies. Its members are sealed one by one
// rather than the blob as a whole, so a reader that wants one body can fetch
// its byte range and decrypt exactly that. Encrypt cannot do this job: it
// draws a random nonce and takes no additional data, and both are wrong here.
//
// Sealed form: version(1) || ciphertext || GCM_tag(16). No nonce is stored —
// it is derived, so it cannot be written wrongly, and a member costs 17 bytes
// of overhead rather than 29. The version byte is deliberately not Version1:
// the two framings are incompatible, and feeding one to the other's opener
// must fail loudly rather than read a ciphertext byte as a nonce.
//
// # Why the key comes from the plaintext
//
// Both the key and the nonce are derived from the member's plaintext hash, so
// sealing is a pure function of (repository secret, plaintext, AAD). Two
// consequences fall out:
//
//   - A retried upload produces byte-identical bytes, which a random nonce
//     cannot offer a content-addressed store.
//   - Nonce reuse under one key would require a SHA-256 collision, since two
//     members sharing a key and AAD are the same member of the same blob.
//
// # Why the AAD is load-bearing
//
// A blob's ref is the hash of its plaintext, following the discipline every
// self-addressed namespace here uses. But a blob is the first object whose
// plaintext no reader ever assembles — readers want one member — so
// core.VerifyRef can never be applied to it, and "blob/" is deliberately not
// in core.SelfAddressedPrefixes. The binding that whole-object addressing
// would have supplied comes from the AAD instead: callers pass the containing
// blob's ref, so a member lifted from another blob, or moved within one, fails
// to authenticate. FileMeta.ContentHash is a second, independent check on the
// same substitution, but not a replacement for this one — restore's -no-verify
// skips it by construction, and this binding holds on that path too.

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

	"crypto/sha256"
	"golang.org/x/crypto/hkdf"
)

const (
	// VersionMember1 frames a sealed blob member. Distinct from Version1
	// because the framings differ: a member carries no nonce.
	VersionMember1 byte = 0x02

	// MemberOverhead is what sealing adds to a member's plaintext.
	MemberOverhead = 1 + TagSize
)

// HKDFInfoBlobMemberV1 is the info string for the root key from which every
// blob member's key and nonce are derived. Blob members are sealed inside an
// object that passes through the store chain unencrypted, so like the pack
// index they need a key of their own rather than the master key.
const HKDFInfoBlobMemberV1 = "cloudstic-blob-member-v1"

// ErrInvalidMember reports bytes that are not a sealed member.
var ErrInvalidMember = errors.New("crypto: invalid sealed blob member")

// MemberSealer seals and opens the members of format-v3 blobs.
//
// It holds the derived root key so that sealing a member costs one HKDF
// expansion rather than two; a blob holds hundreds of members and a backup
// writes many blobs.
type MemberSealer struct {
	root []byte
}

// NewMemberSealer derives a member sealer from the repository's master key.
func NewMemberSealer(masterKey []byte) (*MemberSealer, error) {
	if len(masterKey) == 0 {
		return nil, errors.New("crypto: member sealer needs a master key")
	}
	root, err := DeriveKey(masterKey, HKDFInfoBlobMemberV1)
	if err != nil {
		return nil, err
	}
	return &MemberSealer{root: root}, nil
}

// derive expands the root key into this member's key and nonce. Binding the
// AAD into the derivation as well as passing it to GCM means a member sealed
// for one blob cannot even be decrypted under another's ref, rather than
// merely failing its tag check.
func (s *MemberSealer) derive(plaintextHash string, aad []byte) ([]byte, []byte, error) {
	h := sha256.New()
	// Length-prefixed so that (hash, aad) pairs cannot be confused with one
	// another by moving bytes across the boundary.
	_, _ = fmt.Fprintf(h, "%d:%s:%d:", len(plaintextHash), plaintextHash, len(aad))
	_, _ = h.Write(aad)
	info := h.Sum(nil)

	r := hkdf.Expand(sha256.New, s.root, info)
	out := make([]byte, KeySize+NonceSize)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, nil, fmt.Errorf("crypto: derive member key: %w", err)
	}
	return out[:KeySize], out[KeySize:], nil
}

func (s *MemberSealer) gcm(plaintextHash string, aad []byte) (cipher.AEAD, []byte, error) {
	key, nonce, err := s.derive(plaintextHash, aad)
	if err != nil {
		return nil, nil, err
	}
	g, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	return g, nonce, nil
}

// SealMember seals one blob member. plaintextHash is the hex SHA-256 of
// plaintext — the same value the entry's metadata already records as its
// content hash — and aad is the containing blob's ref.
//
// The hash is taken as a parameter rather than computed here because every
// caller already holds it; recomputing it would hash the repository's whole
// content a second time.
func (s *MemberSealer) SealMember(plaintext []byte, plaintextHash string, aad []byte) ([]byte, error) {
	return s.AppendSealMember(make([]byte, 0, MemberOverhead+len(plaintext)), plaintext, plaintextHash, aad)
}

// AppendSealMember seals one blob member as SealMember does and appends the
// result to dst, returning the extended slice — the shape cipher.AEAD.Seal
// has, and for the same reason. A member is never stored on its own: it is one
// run of bytes inside a blob, so the caller that has to assemble it into one
// can have the seal write it there directly rather than allocate a member and
// copy it in. That copy is what this exists to remove; a blob holds hundreds
// of members and the copy is over their whole content.
//
// dst's spare capacity must not overlap plaintext. That is cipher.AEAD.Seal's
// requirement, inherited unchanged: the ciphertext is written over the
// destination as the plaintext is read, so an overlap that is not exact
// corrupts the very bytes still to be encrypted.
//
// SealMember is this with a fresh, exactly-sized destination, so the two
// cannot drift — the framing lives here alone.
func (s *MemberSealer) AppendSealMember(dst, plaintext []byte, plaintextHash string, aad []byte) ([]byte, error) {
	g, nonce, err := s.gcm(plaintextHash, aad)
	if err != nil {
		return nil, err
	}
	return g.Seal(append(dst, VersionMember1), nonce, plaintext, aad), nil
}

// OpenMember reverses SealMember. It returns ErrDecryptFailed for bytes that
// do not authenticate under this key, hash and AAD — which covers a corrupted
// member, a member taken from another blob, and a member moved within one.
func (s *MemberSealer) OpenMember(sealed []byte, plaintextHash string, aad []byte) ([]byte, error) {
	if len(sealed) < MemberOverhead {
		return nil, ErrInvalidMember
	}
	if sealed[0] != VersionMember1 {
		return nil, fmt.Errorf("%w: unknown version 0x%02x", ErrInvalidMember, sealed[0])
	}
	g, nonce, err := s.gcm(plaintextHash, aad)
	if err != nil {
		return nil, err
	}
	plaintext, err := g.Open(nil, nonce, sealed[1:], aad)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}
