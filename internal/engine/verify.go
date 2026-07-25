package engine

import (
	"context"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/pkg/store"
)

// getVerified reads ref and refuses bytes that are not what ref names.
//
// The object store is not a trusted component. It is a bucket someone else
// operates, reached over a network, and its contents outlive the process that
// wrote them — so bit rot, a truncated write, and a deliberate substitution all
// present the same way: the bytes under a key are not the bytes put there.
//
// For filemeta, node, and snapshot objects the key is a SHA-256 of those bytes,
// which makes the check free of any secret and available to every reader. That
// matters more than it first appears, because the store stack above this point
// will not catch a substitution on its own: the encryption layer returns any
// object that does not look encrypted verbatim, so plaintext written under a
// key by whoever controls the bucket is passed straight through to the decoder.
// Verifying the content address is what makes such an object unusable — an
// attacker can choose the bytes or choose the key they are filed under, never
// both, and the key is named by a parent object subject to the same check.
//
// Objects outside core.SelfAddressedPrefixes are read unchanged; they are
// verified by other means (see core.SelfAddressedPrefixes).
func getVerified(ctx context.Context, s store.ObjectStore, ref string) ([]byte, error) {
	data, err := s.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !core.IsSelfAddressed(ref) {
		return data, nil
	}
	if err := core.VerifyRef(ref, data); err != nil {
		return nil, err
	}
	return data, nil
}
