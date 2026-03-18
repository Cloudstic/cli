package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/secretref"
	"golang.org/x/oauth2"
)

func TestGDrive_TokenPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	tokPath := filepath.Join(tmpDir, "token.json")
	tok := &oauth2.Token{AccessToken: "secret-token", RefreshToken: "refresh"}

	// 1. Save and Load from file
	if err := saveToken(tokPath, tok); err != nil {
		t.Fatalf("saveToken failed: %v", err)
	}
	got, err := tokenFromFile(tokPath)
	if err != nil {
		t.Fatalf("tokenFromFile failed: %v", err)
	}
	if got.AccessToken != tok.AccessToken {
		t.Errorf("got %q, want %q", got.AccessToken, tok.AccessToken)
	}

	// 2. Save and Load from ref
	resolver := secretref.NewDefaultResolver()
	refPath := filepath.Join(tmpDir, "ref-token.json")
	ref := "file://" + refPath
	ctx := context.Background()

	if err := saveTokenRef(ctx, resolver, ref, tok); err != nil {
		t.Fatalf("saveTokenRef failed: %v", err)
	}
	gotRef, err := tokenFromRef(ctx, resolver, ref)
	if err != nil {
		t.Fatalf("tokenFromRef failed: %v", err)
	}
	if gotRef.AccessToken != tok.AccessToken {
		t.Errorf("got %q, want %q", gotRef.AccessToken, tok.AccessToken)
	}
}

func TestGDrive_Options(t *testing.T) {
	resolver := secretref.NewDefaultResolver()
	opts := []GDriveOption{
		WithResolver(resolver),
		WithCredsRef("keychain://creds"),
		WithTokenRef("config-token://google/tok"),
		WithCredsPath("/path/creds.json"),
		WithTokenPath("/path/tok.json"),
		WithDriveName("My Shared Drive"),
		WithDriveID("drive-id"),
		WithRootFolderID("root-id"),
		WithRootPath("/backup/path"),
		WithAccountEmail("user@example.com"),
		WithGDriveExcludePatterns([]string{"node_modules"}),
	}

	var cfg gDriveOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.resolver != resolver {
		t.Error("resolver not set")
	}
	if cfg.credsRef != "keychain://creds" {
		t.Error("credsRef not set")
	}
	if cfg.tokenRef != "config-token://google/tok" {
		t.Error("tokenRef not set")
	}
	if cfg.credsPath != "/path/creds.json" {
		t.Error("credsPath not set")
	}
	if cfg.tokenPath != "/path/tok.json" {
		t.Error("tokenPath not set")
	}
}

func TestOAuthClient_RequiresResolverForTokenRef(t *testing.T) {
	config := &oauth2.Config{}
	_, err := oauthClient(context.Background(), config, nil, "config-token://google/tok", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a resolver") {
		t.Fatalf("unexpected error: %v", err)
	}
}
