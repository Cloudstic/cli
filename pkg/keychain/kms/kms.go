// Package kms provides the AWS-backed keychain credential.
//
// keychain.WithKMSClient takes an already-constructed crypto.KMSClient and so
// needs no SDK. WithARN below builds one on demand from a key ARN, which does.
// Separating them is what keeps pkg/keychain importable without the AWS SDK
// (RFC 0022 §6).
package kms

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/pkg/crypto/kms"
	"github.com/cloudstic/cli/pkg/keychain"
)

// WithARN returns a credential using an AWS KMS key ARN, initializing the
// client on demand. Prefer keychain.WithKMSClient when a client is already
// available, or when the client needs a region, endpoint, or aws.Config —
// this constructor deliberately takes no options, so it resolves against the
// ambient AWS environment.
func WithARN(arn string) keychain.Credential {
	return arnCred{arn: arn}
}

type arnCred struct {
	arn string
}

// client builds the KMS client both methods need. The ARN is validated here
// so an empty one fails the same way on Resolve and on Wrap.
func (c arnCred) client(ctx context.Context) (keychain.Credential, error) {
	if c.arn == "" {
		return nil, fmt.Errorf("empty KMS ARN")
	}
	cl, err := kms.New(ctx, c.arn)
	if err != nil {
		return nil, fmt.Errorf("init kms client: %w", err)
	}
	return keychain.WithKMSClient(cl), nil
}

func (c arnCred) Resolve(ctx context.Context, slots []keychain.KeySlot) ([]byte, error) {
	cred, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	return cred.Resolve(ctx, slots)
}

func (c arnCred) Wrap(ctx context.Context, masterKey []byte) (keychain.KeySlot, error) {
	cred, err := c.client(ctx)
	if err != nil {
		return keychain.KeySlot{}, err
	}
	return cred.Wrap(ctx, masterKey)
}
