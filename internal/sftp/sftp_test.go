package sftp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// generateKeyFile writes a PEM-encoded ECDSA private key to a temp file and returns the path.
func generateKeyFile(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(pemBlock)

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_ecdsa")
	if err := os.WriteFile(keyPath, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

func TestBuildAuthMethods_NoMethods(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg := Config{}
	_, err := buildAuthMethods(cfg)
	if err == nil {
		t.Fatal("expected error when no auth methods available")
	}
}

func TestBuildAuthMethods_Password(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg := Config{Password: "secret"}
	methods, err := buildAuthMethods(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("expected 1 auth method, got %d", len(methods))
	}
}

func TestBuildAuthMethods_PrivateKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	keyPath := generateKeyFile(t)

	cfg := Config{PrivateKeyPath: keyPath}
	methods, err := buildAuthMethods(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("expected 1 auth method, got %d", len(methods))
	}
}

func TestBuildAuthMethods_PrivateKeyMissingFile(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg := Config{PrivateKeyPath: "/nonexistent/path/id_rsa"}
	_, err := buildAuthMethods(cfg)
	if err == nil {
		t.Fatal("expected error for missing private key file")
	}
}

func TestBuildAuthMethods_PrivateKeyInvalidPEM(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "id_rsa")
	if err := os.WriteFile(keyPath, []byte("not a pem key"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{PrivateKeyPath: keyPath}
	_, err := buildAuthMethods(cfg)
	if err == nil {
		t.Fatal("expected error for invalid PEM key")
	}
}

func TestBuildAuthMethods_PasswordAndPrivateKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	keyPath := generateKeyFile(t)

	cfg := Config{Password: "secret", PrivateKeyPath: keyPath}
	methods, err := buildAuthMethods(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(methods) != 2 {
		t.Errorf("expected 2 auth methods, got %d", len(methods))
	}
}
