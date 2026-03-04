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

// KMSEncrypter can encrypt plaintext using a KMS key.
// Implementations must be safe for concurrent use.
type KMSEncrypter interface {
	Encrypt(ctx context.Context, keyARN string, plaintext []byte) ([]byte, error)
}

// AWSKMSClient wraps the AWS KMS SDK v2 client and implements both
// KMSEncrypter and KMSDecrypter.
type AWSKMSClient struct {
	client *kms.Client
}

// AWSKMSDecrypter is an alias for backward compatibility.
type AWSKMSDecrypter = AWSKMSClient

// NewAWSKMSDecrypter creates a KMS client using the default AWS credential
// chain. The returned client implements both KMSDecrypter and KMSEncrypter.
func NewAWSKMSDecrypter(ctx context.Context) (*AWSKMSClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config for kms: %w", err)
	}
	return &AWSKMSClient{client: kms.NewFromConfig(cfg)}, nil
}

func (d *AWSKMSClient) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := d.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt: %w", err)
	}
	return out.Plaintext, nil
}

func (d *AWSKMSClient) Encrypt(ctx context.Context, keyARN string, plaintext []byte) ([]byte, error) {
	out, err := d.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     &keyARN,
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms encrypt: %w", err)
	}
	return out.CiphertextBlob, nil
}
