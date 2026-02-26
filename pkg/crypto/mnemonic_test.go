package crypto

import (
	"strings"
	"testing"
)

func TestGenerateRecoveryMnemonic_RoundTrip(t *testing.T) {
	mnemonic, rawKey, err := GenerateRecoveryMnemonic()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(rawKey) != KeySize {
		t.Fatalf("raw key length = %d, want %d", len(rawKey), KeySize)
	}
	words := strings.Fields(mnemonic)
	if len(words) != 24 {
		t.Fatalf("mnemonic has %d words, want 24", len(words))
	}

	got, err := MnemonicToKey(mnemonic)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != KeySize {
		t.Fatalf("decoded key length = %d, want %d", len(got), KeySize)
	}
	for i := range rawKey {
		if got[i] != rawKey[i] {
			t.Fatalf("decoded key differs at byte %d", i)
		}
	}
}

func TestGenerateRecoveryMnemonic_Unique(t *testing.T) {
	m1, _, _ := GenerateRecoveryMnemonic()
	m2, _, _ := GenerateRecoveryMnemonic()
	if m1 == m2 {
		t.Fatal("two generated mnemonics should be different")
	}
}

func TestMnemonicToKey_InvalidMnemonic(t *testing.T) {
	if _, err := MnemonicToKey("not a valid mnemonic phrase"); err == nil {
		t.Fatal("expected error for invalid mnemonic")
	}
}

func TestMnemonicToKey_BadChecksum(t *testing.T) {
	mnemonic, _, _ := GenerateRecoveryMnemonic()
	words := strings.Fields(mnemonic)
	// Swap the last word to break the checksum.
	if words[23] == "abandon" {
		words[23] = "ability"
	} else {
		words[23] = "abandon"
	}
	bad := strings.Join(words, " ")
	if _, err := MnemonicToKey(bad); err == nil {
		t.Fatal("expected error for bad checksum")
	}
}

func TestMnemonicToKey_KnownVector(t *testing.T) {
	// BIP39 test vector: 128-bit entropy (12 words) should fail our 256-bit check.
	short := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	_, err := MnemonicToKey(short)
	if err == nil {
		t.Fatal("expected error for 12-word (128-bit) mnemonic")
	}
}
