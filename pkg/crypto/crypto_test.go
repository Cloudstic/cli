package crypto

import (
	"bytes"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello, encryption!")

	ct, err := Encrypt(plain, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(ct, plain) {
		t.Fatal("ciphertext should differ from plaintext")
	}

	got, err := Decrypt(ct, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}
}

func TestEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	key := testKey(t)
	ct, err := Encrypt([]byte{}, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(got))
	}
}

func TestEncrypt_UniqueNonce(t *testing.T) {
	key := testKey(t)
	plain := []byte("same data")
	ct1, _ := Encrypt(plain, key)
	ct2, _ := Encrypt(plain, key)
	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions should produce different ciphertext")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := testKey(t)
	key2 := testKey(t)
	ct, _ := Encrypt([]byte("secret"), key1)

	if _, err := Decrypt(ct, key2); err == nil {
		t.Fatal("expected error with wrong key")
	}
}

func TestDecrypt_TamperedData(t *testing.T) {
	key := testKey(t)
	ct, _ := Encrypt([]byte("secret"), key)

	tampered := make([]byte, len(ct))
	copy(tampered, ct)
	tampered[len(tampered)-1] ^= 0xff

	if _, err := Decrypt(tampered, key); err == nil {
		t.Fatal("expected error with tampered ciphertext")
	}
}

func TestDecrypt_TooShort(t *testing.T) {
	key := testKey(t)
	if _, err := Decrypt([]byte{0x01, 0x02}, key); err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestDecrypt_UnknownVersion(t *testing.T) {
	key := testKey(t)
	ct, _ := Encrypt([]byte("data"), key)
	ct[0] = 0xFF

	_, err := Decrypt(ct, key)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
}

func TestIsEncrypted(t *testing.T) {
	key := testKey(t)
	ct, _ := Encrypt([]byte("data"), key)

	if !IsEncrypted(ct) {
		t.Fatal("encrypted data should be detected")
	}
	if IsEncrypted([]byte(`{"json": true}`)) {
		t.Fatal("JSON should not be detected as encrypted")
	}
	if IsEncrypted([]byte{0x1f, 0x8b, 0x08}) {
		t.Fatal("gzip should not be detected as encrypted")
	}
	if IsEncrypted([]byte{0x01}) {
		t.Fatal("too-short data should not be detected as encrypted")
	}
	if IsEncrypted(nil) {
		t.Fatal("nil should not be detected as encrypted")
	}
}

func TestEncrypt_VersionByte(t *testing.T) {
	key := testKey(t)
	ct, _ := Encrypt([]byte("data"), key)
	if ct[0] != Version1 {
		t.Fatalf("first byte should be 0x%02x, got 0x%02x", Version1, ct[0])
	}
}

func TestEncrypt_InvalidKeySize(t *testing.T) {
	if _, err := Encrypt([]byte("data"), []byte("short")); err == nil {
		t.Fatal("expected error for invalid key size")
	}
}

func TestDeriveKey_Deterministic(t *testing.T) {
	master := testKey(t)
	k1, err := DeriveKey(master, "cloudstic-backup-v1")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DeriveKey(master, "cloudstic-backup-v1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("same master + info should produce same derived key")
	}
}

func TestDeriveKey_DifferentInfoProducesDifferentKeys(t *testing.T) {
	master := testKey(t)
	k1, _ := DeriveKey(master, "purpose-a")
	k2, _ := DeriveKey(master, "purpose-b")
	if bytes.Equal(k1, k2) {
		t.Fatal("different info should produce different keys")
	}
}

func TestDeriveKey_Length(t *testing.T) {
	master := testKey(t)
	k, _ := DeriveKey(master, "test")
	if len(k) != KeySize {
		t.Fatalf("derived key length = %d, want %d", len(k), KeySize)
	}
}

func TestGenerateKey_Length(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != KeySize {
		t.Fatalf("key length = %d, want %d", len(k), KeySize)
	}
}

func TestGenerateKey_Unique(t *testing.T) {
	k1, _ := GenerateKey()
	k2, _ := GenerateKey()
	if bytes.Equal(k1, k2) {
		t.Fatal("two generated keys should be different")
	}
}

func TestWrapUnwrapKey_RoundTrip(t *testing.T) {
	masterKey := testKey(t)
	wrappingKey := testKey(t)

	wrapped, err := WrapKey(masterKey, wrappingKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapKey(wrapped, wrappingKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, masterKey) {
		t.Fatal("unwrapped key should match original")
	}
}

func TestWrapKey_WrongWrappingKey(t *testing.T) {
	masterKey := testKey(t)
	wk1 := testKey(t)
	wk2 := testKey(t)

	wrapped, _ := WrapKey(masterKey, wk1)
	if _, err := UnwrapKey(wrapped, wk2); err == nil {
		t.Fatal("expected error with wrong wrapping key")
	}
}

func TestEncryptDecrypt_LargePayload(t *testing.T) {
	key := testKey(t)
	plain := make([]byte, 8*1024*1024) // 8 MB
	for i := range plain {
		plain[i] = byte(i % 256)
	}

	ct, err := Encrypt(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("large payload round-trip failed")
	}
}
