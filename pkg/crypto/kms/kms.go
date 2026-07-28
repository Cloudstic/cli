// Package kms implements crypto.KMSClient on top of the AWS KMS SDK.
//
// It is separate from pkg/crypto so that the encryption primitives — AES-GCM,
// HKDF, Argon2, BIP39 — and the crypto.KMSClient interface they are described
// in carry no cloud SDK. Importing pkg/crypto costs nothing; importing this
// package pulls the AWS SDK, and only a caller that actually uses KMS
// envelope encryption pays for it (RFC 0022 §6).
package kms

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
)

// API defines the subset of the AWS KMS SDK client required by Client.
type API interface {
	Encrypt(ctx context.Context, params *awskms.EncryptInput, optFns ...func(*awskms.Options)) (*awskms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *awskms.DecryptInput, optFns ...func(*awskms.Options)) (*awskms.DecryptOutput, error)
}

// Client wraps the AWS KMS SDK v2 client and implements crypto.KMSClient.
type Client struct {
	arn    string
	client API
}

// clientConfig holds parameters for creating a KMS client.
type clientConfig struct {
	arn          string
	loadOpts     []func(*awsconfig.LoadOptions) error
	customConfig *aws.Config
}

// Option configures a KMS client.
type Option func(*clientConfig)

// WithRegion sets the AWS region for KMS.
func WithRegion(region string) Option {
	return func(c *clientConfig) {
		if region != "" {
			c.loadOpts = append(c.loadOpts, awsconfig.WithRegion(region))
		}
	}
}

// WithEndpoint sets a custom base URL for KMS (e.g. for MinIO or localstack).
func WithEndpoint(url string) Option {
	return func(c *clientConfig) {
		if url != "" {
			c.loadOpts = append(c.loadOpts, awsconfig.WithBaseEndpoint(url))
		}
	}
}

// WithConfig sets the full AWS config for KMS.
func WithConfig(cfg aws.Config) Option {
	return func(c *clientConfig) {
		c.customConfig = &cfg
	}
}

// New creates a KMS client for arn with the provided options.
func New(ctx context.Context, arn string, opts ...Option) (*Client, error) {
	c := &clientConfig{arn: arn}
	for _, opt := range opts {
		opt(c)
	}

	var cfg aws.Config
	var err error
	if c.customConfig != nil {
		cfg = *c.customConfig
	} else {
		cfg, err = awsconfig.LoadDefaultConfig(ctx, c.loadOpts...)
		if err != nil {
			return nil, fmt.Errorf("load aws config for kms: %w", err)
		}
	}

	return &Client{
		arn:    arn,
		client: awskms.NewFromConfig(cfg),
	}, nil
}

func (d *Client) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := d.client.Decrypt(ctx, &awskms.DecryptInput{
		KeyId:          aws.String(d.arn),
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt: %w", err)
	}
	return out.Plaintext, nil
}

func (d *Client) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	out, err := d.client.Encrypt(ctx, &awskms.EncryptInput{
		KeyId:     aws.String(d.arn),
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, fmt.Errorf("kms encrypt: %w", err)
	}
	return out.CiphertextBlob, nil
}
