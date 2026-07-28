package open

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/crypto/kms"
	"github.com/cloudstic/cli/pkg/keychain"
)

// Keychain assembles the credential chain that unlocks a repository, in the
// order the client should try them.
//
// The order is the contract: KMS, then a raw platform key, then a password,
// then a recovery mnemonic, then an interactive prompt. Each is tried against
// the repository's key slots and the first that opens one wins, so a caller
// supplying several is not ambiguous — it is a preference list.
//
// A prompt is appended only when WithPasswordPrompt supplied one *and* the
// configuration allows it: either nothing else is available, or a prompt was
// asked for explicitly, and NoPrompt is not set. A caller that supplies no
// prompt gets a chain that can fail but can never block waiting for input.
func Keychain(ctx context.Context, cfg config.Unlock, opts ...Option) (keychain.Chain, error) {
	return openKeychain(ctx, cfg, newOptions(opts))
}

func openKeychain(ctx context.Context, cfg config.Unlock, o *options) (keychain.Chain, error) {
	platformKey, err := parsePlatformKey(cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}

	kmsClient, err := newKMSClient(ctx, cfg.KMS)
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
	if (len(chain) == 0 || cfg.Prompt) && !cfg.NoPrompt && o.promptResolve != nil {
		chain = append(chain, keychain.WithPrompt(o.promptResolve, o.promptWrap))
	}

	return chain, nil
}

// newKMSClient creates a KMS client if a key ARN is configured, and otherwise
// returns nil — which the chain above reads as "KMS does not participate".
// An absent ARN is not an error: it is how every non-KMS repository is opened.
func newKMSClient(ctx context.Context, cfg config.KMS) (crypto.KMSClient, error) {
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
