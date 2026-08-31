package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudstic/cli/internal/blob"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

// blobReader fetches one body out of the blob holding it.
//
// The whole point of the layout is that this needs one ranged request and
// nothing else: the entry names the blob and the byte range, and its metadata
// carries the content hash that keys the member's seal. No index, no catalog.
type blobReader struct {
	store  store.ObjectStore
	sealer *crypto.MemberSealer
}

func newBlobReader(s store.ObjectStore, sealer *crypto.MemberSealer) *blobReader {
	return &blobReader{store: s, sealer: sealer}
}

// Body returns the bytes of the entry whose payload is p and whose metadata is
// meta.
//
// contentHash comes from the metadata rather than the body reference on
// purpose. It is the member's key material, so an entry repointed at another
// member of the same blob derives the wrong key and fails to open — the AAD
// cannot catch that, since every member of a blob shares it.
func (r *blobReader) Body(ctx context.Context, p *hamt.Payload, contentHash string) ([]byte, error) {
	if err := r.usable(p); err != nil {
		return nil, err
	}
	b := p.Body
	sealed, err := store.GetRange(ctx, r.store, b.Blob, b.Offset, b.Length)
	if err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", b.Blob, b.Offset, b.Length, err)
	}
	return r.member(sealed, p, contentHash)
}

// span reads one coalesced run of a blob's bytes: the request a batch of
// entries whose bodies sit near each other issues instead of one apiece.
//
// It knows nothing about members. Splitting the returned bytes back up is the
// span's own job (blobSpan.slice), and decoding each piece is member's, which
// is what keeps the coalesced path and the single-member path the same code
// below the fetch.
func (r *blobReader) span(ctx context.Context, s blobSpan) ([]byte, error) {
	if r == nil {
		return nil, errNoBlobStore
	}
	data, err := store.GetRange(ctx, r.store, s.blob, s.offset, s.length)
	if err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", s.blob, s.offset, s.length, err)
	}
	if int64(len(data)) != s.length {
		// A store that served a short range would otherwise be discovered one
		// member at a time, as a decryption failure with no hint that the
		// range rather than the data was wrong.
		return nil, fmt.Errorf("read %s at %d+%d: store returned %d bytes",
			s.blob, s.offset, s.length, len(data))
	}
	return data, nil
}

// member decodes one body from exactly its own sealed bytes.
//
// It is split from Body so that a caller holding those bytes already — a
// restore that coalesced this member's read with its neighbours' into one
// request — spends nothing to decode them. Everything below the fetch is
// identical between the two paths, contentHash included: see Body for why that
// value must keep coming from the entry's metadata.
func (r *blobReader) member(sealed []byte, p *hamt.Payload, contentHash string) ([]byte, error) {
	if err := r.usable(p); err != nil {
		return nil, err
	}
	b := p.Body
	body, err := blob.ReadMember(sealed, contentHash, b.Blob, r.sealer, p.Size)
	if err != nil {
		return nil, err
	}
	// The member authenticated, which in an encrypted repository already ties
	// these bytes to this content hash. An unencrypted one authenticates
	// nothing, so the hash is checked rather than assumed — a store anyone can
	// write to must not be able to substitute a body silently.
	if r.sealer == nil {
		if got := core.ComputeHash(body); got != contentHash {
			return nil, fmt.Errorf("body in %s at %d+%d hashes to %s, entry records %s",
				b.Blob, b.Offset, b.Length, got, contentHash)
		}
	}
	return body, nil
}

// errNoBlobStore reports a format-v3 manager built without a blob store.
// Reported rather than dereferenced, so the misconfiguration names itself
// instead of arriving as a nil-pointer panic partway through a restore.
var errNoBlobStore = errors.New("this repository's blobs are unreachable: no blob store was configured")

// usable reports whether this reader and this entry can name a body between
// them.
func (r *blobReader) usable(p *hamt.Payload) error {
	if r == nil {
		return errNoBlobStore
	}
	if p == nil || p.Body == nil {
		return fmt.Errorf("entry carries no body reference")
	}
	return nil
}
