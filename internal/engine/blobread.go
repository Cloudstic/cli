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
	if r == nil {
		// A format-v3 manager built without a blob store. Reported rather than
		// dereferenced, so the misconfiguration names itself instead of
		// arriving as a nil-pointer panic partway through a restore.
		return nil, errors.New("this repository's blobs are unreachable: no blob store was configured")
	}
	b := p.Body
	if b == nil {
		return nil, fmt.Errorf("entry carries no body reference")
	}
	sealed, err := store.GetRange(ctx, r.store, b.Blob, b.Offset, b.Length)
	if err != nil {
		return nil, fmt.Errorf("read %s at %d+%d: %w", b.Blob, b.Offset, b.Length, err)
	}
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
