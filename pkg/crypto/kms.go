package crypto

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSClient represents a KMS client capable of both encrypting and decrypting.
type KMSClient interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
}

// KMSAPI defines the subset of the AWS KMS SDK client required by AWSKMSClient.
type KMSAPI interface {
	Encrypt(ctx context.Context, params *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// AWSKMSClient wraps the AWS KMS SDK v2 client and implements both
// KMSEncrypter and KMSDecrypter.
type AWSKMSClient struct {
	arn    string
	client KMSAPI
}

// kmsClientConfig holds parameters for creating a KMS client.
type kmsClientConfig struct {
	arn          string
	loadOpts     []func(*config.LoadOptions) error
	customConfig *aws.Config
}

// KMSClientOption configures an AWS KMS client.
type KMSClientOption func(*kmsClientConfig)

// WithKMSRegion sets the AWS region for KMS.
func WithKMSRegion(region string) KMSClientOption {
	return func(c *kmsClientConfig) {
		if region != "" {
			c.loadOpts = append(c.loadOpts, config.WithRegion(region))
		}
	}
}

// WithKMSEndpoint sets a custom base URL for KMS (e.g. for MinIO or localstack).
func WithKMSEndpoint(url string) KMSClientOption {
	return func(c *kmsClientConfig) {
		if url != "" {
			c.loadOpts = append(c.loadOpts, config.WithBaseEndpoint(url))
		}
	}
}

// WithKMSConfig sets the full AWS config for KMS.
func WithKMSConfig(cfg aws.Config) KMSClientOption {
	return func(c *kmsClientConfig) {
		c.customConfig = &cfg
	}
}

// NewAWSKMSClient creates a KMS client with the provided options.
func NewAWSKMSClient(ctx context.Context, arn string, opts ...KMSClientOption) (*AWSKMSClient, error) {
	c := &kmsClientConfig{arn: arn}
	for _, opt := range opts {
		opt(c)
	}

	var cfg aws.Config
	var err error
	if c.customConfig != nil {
		cfg = *c.customConfig
	} else {
		cfg, err = config.LoadDefaultConfig(ctx, c.loadOpts...)
		if err != nil {
			return nil, fmt.Errorf("load aws config for kms: %w", err)
		}
	}

	return &AWSKMSClient{
		arn:    arn,
		client: kms.NewFromConfig(cfg),
	}, nil
}

func (d *AWSKMSClient) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := d.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          aws.String(d.arn),
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt: %w", err)
	}
	return out.Plaintext, nil
}

func (d *AWSKMSClient) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	out, err := d.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(d.arn),
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms encrypt: %w", err)
	}
	return out.CiphertextBlob, nil
}
