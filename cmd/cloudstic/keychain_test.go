package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/moby/term"

	"github.com/cloudstic/cli/pkg/keychain"
)

// These are characterization tests: they pin what buildKeychain and its
// helpers do today, quirks included, so that RFC 0022 §7 can move them into a
// public package and prove the move changed nothing. They were written before
// the move, against code that had no direct unit tests at all.
//
// The credential order buildKeychain produces is the important part. It is the
// order the client tries credentials in, so changing it silently changes which
// key unlocks a repository when a caller supplies more than one.

// credKinds names the concrete type behind each credential in a chain.
//
// It deliberately asserts on unexported type names from pkg/keychain. That is
// the coupling we want here: the property under test is *which* credential
// constructor was called and in what order, and the concrete type is the only
// thing that records it — keychain.Credential is a two-method interface that
// every credential satisfies identically. If pkg/keychain renames one of these
// types, this test should fail and be updated deliberately, rather than pass
// while the chain composition drifts.
func credKinds(t *testing.T, chain keychain.Chain) []string {
	t.Helper()
	kinds := make([]string, 0, len(chain))
	for _, c := range chain {
		kinds = append(kinds, strings.TrimPrefix(fmt.Sprintf("%T", c), "keychain."))
	}
	return kinds
}

// validHexKey is 32 bytes, the size crypto.KeySize requires.
const validHexKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestBuildKeychain_ChainComposition(t *testing.T) {
	// The prompt credential is appended only when stdin is a terminal, which it
	// is not under `go test`. Every case below therefore reflects the
	// non-interactive chain; TestBuildKeychain_PromptRequiresATerminal covers
	// the branch that is unreachable here and says why.
	if term.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is a terminal; the non-interactive chains asserted here would gain a prompt credential")
	}

	tests := []struct {
		name string
		cfg  unlockConfig
		want []string
	}{
		{
			name: "empty config yields an empty chain",
			cfg:  unlockConfig{},
			want: []string{},
		},
		{
			name: "password only",
			cfg:  unlockConfig{password: "hunter2"},
			want: []string{"passwordCred"},
		},
		{
			name: "encryption key only",
			cfg:  unlockConfig{encryptionKey: validHexKey},
			want: []string{"platformKeyCred"},
		},
		{
			name: "recovery key only",
			cfg:  unlockConfig{recoveryKey: "abandon abandon ability"},
			want: []string{"recoveryCred"},
		},
		{
			name: "kms only",
			cfg:  unlockConfig{kms: kmsConfig{keyARN: "arn:aws:kms:us-east-1:1:key/x", region: "us-east-1"}},
			want: []string{"kmsClientCred"},
		},
		{
			name: "every credential, in precedence order",
			cfg: unlockConfig{
				encryptionKey: validHexKey,
				password:      "hunter2",
				recoveryKey:   "abandon abandon ability",
				kms:           kmsConfig{keyARN: "arn:aws:kms:us-east-1:1:key/x", region: "us-east-1"},
			},
			want: []string{"kmsClientCred", "platformKeyCred", "passwordCred", "recoveryCred"},
		},
		{
			name: "prompt:true does not add a credential without a terminal",
			cfg:  unlockConfig{password: "hunter2", prompt: true},
			want: []string{"passwordCred"},
		},
		{
			name: "noPrompt suppresses nothing else",
			cfg:  unlockConfig{password: "hunter2", noPrompt: true},
			want: []string{"passwordCred"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, err := buildKeychain(context.Background(), tt.cfg)
			if err != nil {
				t.Fatalf("buildKeychain: %v", err)
			}
			got := credKinds(t, chain)
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

// TestBuildKeychain_PromptRequiresATerminal records a dependency the move in
// §7 has to deal with: whether a prompt credential is appended is decided by
// reading the process's own stdin, via term.IsTerminal(os.Stdin.Fd()).
//
// That is correct for a CLI and wrong for a library — a consumer's answer to
// "can I prompt?" is not this process's stdin. The condition is
// (chain is empty || prompt) && !noPrompt && stdin is a terminal, so under
// `go test` the third term is false and the branch cannot be reached at all.
// When buildKeychain becomes public, the terminal check must become an
// injected decision rather than an ambient one; this test is here to fail
// loudly if that lands without anyone noticing the behavior changed.
func TestBuildKeychain_PromptRequiresATerminal(t *testing.T) {
	if term.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is a terminal; this test characterizes the non-terminal path")
	}

	// An empty config satisfies both (len(chain) == 0) and !noPrompt, so the
	// terminal check is the only thing preventing a prompt credential.
	chain, err := buildKeychain(context.Background(), unlockConfig{})
	if err != nil {
		t.Fatalf("buildKeychain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("chain = %v, want empty: without a terminal the prompt credential must not be added, "+
			"which leaves nothing to unlock with", credKinds(t, chain))
	}
}

func TestBuildKeychain_PropagatesPlatformKeyError(t *testing.T) {
	_, err := buildKeychain(context.Background(), unlockConfig{encryptionKey: "not-hex"})
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

// TestBuildKMSClient_NoARNIsNotAnError pins the branch that decides whether
// KMS participates at all: an absent ARN yields (nil, nil), and buildKeychain
// reads that nil to mean "no KMS credential". An implementation that returned
// an error here instead would break every non-KMS unlock.
func TestBuildKMSClient_NoARNIsNotAnError(t *testing.T) {
	client, err := buildKMSClient(context.Background(), kmsConfig{})
	if err != nil {
		t.Fatalf("buildKMSClient with no ARN: %v", err)
	}
	if client != nil {
		t.Errorf("client = %v, want nil so buildKeychain omits the KMS credential", client)
	}

	// A region or endpoint without an ARN still means "no KMS".
	client, err = buildKMSClient(context.Background(), kmsConfig{region: "us-east-1", endpoint: "http://localhost:4566"})
	if err != nil {
		t.Fatalf("buildKMSClient with region but no ARN: %v", err)
	}
	if client != nil {
		t.Errorf("client = %v, want nil: the ARN alone decides whether KMS is used", client)
	}
}

// TestBuildKMSClient_ARNBuildsOffline records that constructing the client
// contacts nothing — no credentials, no network, no IMDS probe. buildKeychain
// calls it eagerly on every unlock that names an ARN, so if this ever started
// reaching the network it would add latency to each one.
func TestBuildKMSClient_ARNBuildsOffline(t *testing.T) {
	client, err := buildKMSClient(context.Background(), kmsConfig{
		keyARN: "arn:aws:kms:us-east-1:123456789012:key/1234abcd",
		region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("buildKMSClient: %v", err)
	}
	if client == nil {
		t.Fatal("client = nil, want a client: an ARN must produce one")
	}
}
