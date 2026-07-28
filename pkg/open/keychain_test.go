package open

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/keychain"
)

// These began as characterization tests in cmd/cloudstic, written before this
// package existed, and moved with the code they pin.
//
// The credential order is the important part: it is the order the client tries
// credentials in, so changing it silently changes which key unlocks a
// repository when a caller supplies more than one.
//
// One thing improved in the move. In cmd/cloudstic the prompt branch was gated
// on term.IsTerminal(os.Stdin.Fd()), so it was unreachable under `go test` and
// every case had to be written around that. Here prompting is decided by
// whether the caller passed WithPasswordPrompt, which makes the branch
// ordinary, deterministic, and testable in both directions.

// credKinds names the concrete type behind each credential in a chain.
//
// It deliberately asserts on unexported type names from pkg/keychain. That is
// the coupling we want: the property under test is *which* credential
// constructor was called and in what order, and the concrete type is the only
// thing that records it — keychain.Credential is a two-method interface that
// every credential satisfies identically.
func credKinds(chain keychain.Chain) []string {
	kinds := make([]string, 0, len(chain))
	for _, c := range chain {
		kinds = append(kinds, strings.TrimPrefix(fmt.Sprintf("%T", c), "keychain."))
	}
	return kinds
}

// validHexKey is 32 bytes, the size crypto.KeySize requires.
const validHexKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func promptOption() Option {
	return WithPasswordPrompt(
		func() (string, error) { return "prompted", nil },
		func() (string, error) { return "prompted", nil },
	)
}

func TestKeychain_ChainComposition(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Unlock
		opts []Option
		want []string
	}{
		{
			name: "no credentials and no prompt yields an empty chain",
			cfg:  config.Unlock{},
			want: []string{},
		},
		{
			name: "password only",
			cfg:  config.Unlock{Password: "hunter2"},
			want: []string{"passwordCred"},
		},
		{
			name: "encryption key only",
			cfg:  config.Unlock{EncryptionKey: validHexKey},
			want: []string{"platformKeyCred"},
		},
		{
			name: "recovery key only",
			cfg:  config.Unlock{RecoveryKey: "abandon abandon ability"},
			want: []string{"recoveryCred"},
		},
		{
			name: "kms only",
			cfg:  config.Unlock{KMS: config.KMS{KeyARN: "arn:aws:kms:us-east-1:1:key/x", Region: "us-east-1"}},
			want: []string{"kmsClientCred"},
		},
		{
			name: "every credential, in precedence order",
			cfg: config.Unlock{
				EncryptionKey: validHexKey,
				Password:      "hunter2",
				RecoveryKey:   "abandon abandon ability",
				KMS:           config.KMS{KeyARN: "arn:aws:kms:us-east-1:1:key/x", Region: "us-east-1"},
			},
			want: []string{"kmsClientCred", "platformKeyCred", "passwordCred", "recoveryCred"},
		},
		{
			name: "a prompt is the last resort when nothing else is available",
			cfg:  config.Unlock{},
			opts: []Option{promptOption()},
			want: []string{"promptCred"},
		},
		{
			name: "a prompt is not added when another credential exists",
			cfg:  config.Unlock{Password: "hunter2"},
			opts: []Option{promptOption()},
			want: []string{"passwordCred"},
		},
		{
			name: "Prompt asks for one even alongside another credential",
			cfg:  config.Unlock{Password: "hunter2", Prompt: true},
			opts: []Option{promptOption()},
			want: []string{"passwordCred", "promptCred"},
		},
		{
			name: "NoPrompt beats Prompt",
			cfg:  config.Unlock{Password: "hunter2", Prompt: true, NoPrompt: true},
			opts: []Option{promptOption()},
			want: []string{"passwordCred"},
		},
		{
			name: "NoPrompt leaves an empty chain rather than prompting",
			cfg:  config.Unlock{NoPrompt: true},
			opts: []Option{promptOption()},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, err := Keychain(context.Background(), tt.cfg, tt.opts...)
			if err != nil {
				t.Fatalf("Keychain: %v", err)
			}
			got := credKinds(chain)
			if len(got) != len(tt.want) {
				t.Fatalf("chain = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("chain[%d] = %s, want %s (full chain %v, want %v)",
						i, got[i], tt.want[i], got, tt.want)
				}
			}
		})
	}
}

// TestKeychain_WithoutAPromptOptionNeverBlocks is the property a
// non-interactive caller depends on: with no prompt supplied, the chain can
// fail for want of a credential but can never block on a read that will not be
// answered.
func TestKeychain_WithoutAPromptOptionNeverBlocks(t *testing.T) {
	chain, err := Keychain(context.Background(), config.Unlock{})
	if err != nil {
		t.Fatalf("Keychain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("chain = %v, want empty: without WithPasswordPrompt there is nothing "+
			"to prompt with, so no prompt credential may be added", credKinds(chain))
	}
}

func TestKeychain_PropagatesPlatformKeyError(t *testing.T) {
	_, err := Keychain(context.Background(), config.Unlock{EncryptionKey: "not-hex"})
	if err == nil {
		t.Fatal("expected an error for a non-hex encryption key, got nil")
	}
	if !strings.Contains(err.Error(), "hex-encoded") {
		t.Errorf("error = %q, want it to mention hex encoding so the user can act on it", err)
	}
}

func TestParsePlatformKey(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantLen int
		wantErr string
	}{
		{name: "empty is not an error", in: "", wantLen: 0},
		{name: "valid 32-byte key", in: validHexKey, wantLen: 32},
		{name: "uppercase hex is accepted", in: strings.ToUpper(validHexKey), wantLen: 32},
		{name: "non-hex", in: "zzzz", wantErr: "hex-encoded"},
		{name: "odd length", in: "abc", wantErr: "hex-encoded"},
		{name: "too short", in: "0011", wantErr: "must be 32 bytes"},
		{name: "too long", in: validHexKey + "00", wantErr: "must be 32 bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePlatformKey(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parsePlatformKey(%q) = %x, want error containing %q", tt.in, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePlatformKey(%q): %v", tt.in, err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

// TestNewKMSClient_NoARNIsNotAnError pins the branch that decides whether KMS
// participates at all: an absent ARN yields (nil, nil), and the chain reads
// that nil as "no KMS credential". An implementation that returned an error
// here instead would break every non-KMS unlock.
func TestNewKMSClient_NoARNIsNotAnError(t *testing.T) {
	client, err := newKMSClient(context.Background(), config.KMS{})
	if err != nil {
		t.Fatalf("newKMSClient with no ARN: %v", err)
	}
	if client != nil {
		t.Errorf("client = %v, want nil so the chain omits the KMS credential", client)
	}

	// A region or endpoint without an ARN still means "no KMS".
	client, err = newKMSClient(context.Background(), config.KMS{Region: "us-east-1", Endpoint: "http://localhost:4566"})
	if err != nil {
		t.Fatalf("newKMSClient with region but no ARN: %v", err)
	}
	if client != nil {
		t.Errorf("client = %v, want nil: the ARN alone decides whether KMS is used", client)
	}
}

// TestNewKMSClient_ARNBuildsOffline records that constructing the client
// contacts nothing — no credentials, no network, no IMDS probe. It is called
// eagerly on every unlock that names an ARN, so if this ever started reaching
// the network it would add latency to each one.
func TestNewKMSClient_ARNBuildsOffline(t *testing.T) {
	client, err := newKMSClient(context.Background(), config.KMS{
		KeyARN: "arn:aws:kms:us-east-1:123456789012:key/1234abcd",
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("newKMSClient: %v", err)
	}
	if client == nil {
		t.Fatal("client = nil, want a client: an ARN must produce one")
	}
}
