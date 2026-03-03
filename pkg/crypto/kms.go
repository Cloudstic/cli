package crypto

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSDecrypter can decrypt a KMS ciphertext blob.
// Implementations must be safe for concurrent use.
type KMSDecrypter interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// AWSKMSDecrypter wraps the AWS KMS SDK v2 client.
type AWSKMSDecrypter struct {
	client *kms.Client
}

// NewAWSKMSDecrypter creates a KMS decrypter using the default AWS credential
// chain. The key ARN is embedded in the ciphertext blob so it does not need
// to be supplied at decryption time.
func NewAWSKMSDecrypter(ctx context.Context) (*AWSKMSDecrypter, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config for kms: %w", err)
	}
	return &AWSKMSDecrypter{client: kms.NewFromConfig(cfg)}, nil
}

func (d *AWSKMSDecrypter) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := d.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt: %w", err)
	}
	return out.Plaintext, nil
}
