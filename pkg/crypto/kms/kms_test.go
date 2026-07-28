package kms

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/cloudstic/cli/pkg/crypto"
)

// Client must satisfy the interface pkg/crypto describes it in; that is the
// whole point of keeping the interface there and the implementation here.
var _ crypto.KMSClient = (*Client)(nil)

type mockAPI struct {
	encryptFunc func(ctx context.Context, params *awskms.EncryptInput, optFns ...func(*awskms.Options)) (*awskms.EncryptOutput, error)
	decryptFunc func(ctx context.Context, params *awskms.DecryptInput, optFns ...func(*awskms.Options)) (*awskms.DecryptOutput, error)
}

func (m *mockAPI) Encrypt(ctx context.Context, params *awskms.EncryptInput, optFns ...func(*awskms.Options)) (*awskms.EncryptOutput, error) {
	if m.encryptFunc != nil {
		return m.encryptFunc(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("encrypt not implemented")
}

func (m *mockAPI) Decrypt(ctx context.Context, params *awskms.DecryptInput, optFns ...func(*awskms.Options)) (*awskms.DecryptOutput, error) {
	if m.decryptFunc != nil {
		return m.decryptFunc(ctx, params, optFns...)
	}
	return nil, fmt.Errorf("decrypt not implemented")
}

func TestClient_EncryptDecrypt(t *testing.T) {
	ctx := context.Background()
	arn := "arn:aws:kms:us-east-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	plaintext := []byte("secret-message")
	ciphertext := []byte("encrypted-blob")

	mock := &mockAPI{
		encryptFunc: func(ctx context.Context, params *awskms.EncryptInput, optFns ...func(*awskms.Options)) (*awskms.EncryptOutput, error) {
			if *params.KeyId != arn {
				return nil, fmt.Errorf("wrong key id: got %s, want %s", *params.KeyId, arn)
			}
			if !reflect.DeepEqual(params.Plaintext, plaintext) {
				return nil, fmt.Errorf("wrong plaintext")
			}
			return &awskms.EncryptOutput{CiphertextBlob: ciphertext}, nil
		},
		decryptFunc: func(ctx context.Context, params *awskms.DecryptInput, optFns ...func(*awskms.Options)) (*awskms.DecryptOutput, error) {
			if *params.KeyId != arn {
				return nil, fmt.Errorf("wrong key id: got %s, want %s", *params.KeyId, arn)
			}
			if !reflect.DeepEqual(params.CiphertextBlob, ciphertext) {
				return nil, fmt.Errorf("wrong ciphertext")
			}
			return &awskms.DecryptOutput{Plaintext: plaintext}, nil
		},
	}

	client := &Client{
		arn:    arn,
		client: mock,
	}

	// Test Encrypt
	gotCiphertext, err := client.Encrypt(ctx, plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if !reflect.DeepEqual(gotCiphertext, ciphertext) {
		t.Errorf("Encrypt got %v, want %v", gotCiphertext, ciphertext)
	}

	// Test Decrypt
	gotPlaintext, err := client.Decrypt(ctx, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !reflect.DeepEqual(gotPlaintext, plaintext) {
		t.Errorf("Decrypt got %s, want %s", string(gotPlaintext), string(plaintext))
	}
}

func TestNew_Options(t *testing.T) {
	ctx := context.Background()
	arn := "test-arn"

	// Test WithConfig
	customCfg := aws.Config{Region: "us-west-2"}
	client, err := New(ctx, arn, WithConfig(customCfg))
	if err != nil {
		t.Fatalf("New with custom config failed: %v", err)
	}
	if client.arn != arn {
		t.Errorf("expected arn %s, got %s", arn, client.arn)
	}

	// Test Region and Endpoint options (this exercises LoadDefaultConfig path)
	_, err = New(ctx, arn, WithRegion("us-east-1"), WithEndpoint("http://localhost:4566"))
	if err != nil {
		// This might fail if no AWS environment is set up, but we want to exercise the code path.
		// config.LoadDefaultConfig usually succeeds even without credentials if region is set.
		t.Logf("New with region/endpoint: %v", err)
	}
}

func TestClient_Errors(t *testing.T) {
	ctx := context.Background()
	client := &Client{
		arn: "test-arn",
		client: &mockAPI{
			encryptFunc: func(ctx context.Context, params *awskms.EncryptInput, optFns ...func(*awskms.Options)) (*awskms.EncryptOutput, error) {
				return nil, fmt.Errorf("kms error")
			},
		},
	}

	_, err := client.Encrypt(ctx, []byte("data"))
	if err == nil {
		t.Error("expected error from Encrypt, got nil")
	}
}
