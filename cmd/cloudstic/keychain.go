package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/moby/term"

	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/crypto/kms"
	"github.com/cloudstic/cli/pkg/keychain"
)

// buildKeychain assembles the credential chain that unlocks a repository, in
// the order the client should try them.
func buildKeychain(ctx context.Context, cfg unlockConfig) (keychain.Chain, error) {
	platformKey, err := parsePlatformKey(cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}

	kmsClient, err := buildKMSClient(ctx, cfg.KMS)
	if err != nil {
		return nil, err
	}

	var chain keychain.Chain

	if kmsClient != nil {
		chain = append(chain, keychain.WithKMSClient(kmsClient))
	}
	if len(platformKey) > 0 {
		chain = append(chain, keychain.WithPlatformKey(platformKey))
	}
	if cfg.Password != "" {
		chain = append(chain, keychain.WithPassword(cfg.Password))
	}
	if cfg.RecoveryKey != "" {
		chain = append(chain, keychain.WithRecoveryKey(cfg.RecoveryKey))
	}
	if (len(chain) == 0 || cfg.Prompt) && !cfg.NoPrompt && term.IsTerminal(os.Stdin.Fd()) {
		chain = append(chain, keychain.WithPrompt(
			func() (string, error) { return ui.PromptPassword("Repository password") },
			func() (string, error) { return ui.PromptPasswordConfirm("Enter new repository password") },
		))
	}

	return chain, nil
}

// buildKMSClient creates an AWS KMS client if a key ARN is configured,
// otherwise returns nil.
func buildKMSClient(ctx context.Context, cfg kmsConfig) (crypto.KMSClient, error) {
	if cfg.KeyARN == "" {
		return nil, nil
	}
	var opts []kms.Option
	if cfg.Region != "" {
		opts = append(opts, kms.WithRegion(cfg.Region))
	}
	if cfg.Endpoint != "" {
		opts = append(opts, kms.WithEndpoint(cfg.Endpoint))
	}
	client, err := kms.New(ctx, cfg.KeyARN, opts...)
	if err != nil {
		return nil, fmt.Errorf("init KMS client: %w", err)
	}
	return client, nil
}

// parsePlatformKey decodes the hex-encoded platform key, if one is configured.
func parsePlatformKey(encKeyHex string) ([]byte, error) {
	if encKeyHex == "" {
		return nil, nil
	}
	platformKey, err := hex.DecodeString(encKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid --encryption-key (must be hex-encoded): %w", err)
	}
	if len(platformKey) != crypto.KeySize {
		return nil, fmt.Errorf("--encryption-key must be %d bytes (%d hex chars), got %d bytes", crypto.KeySize, crypto.KeySize*2, len(platformKey))
	}
	return platformKey, nil
}
