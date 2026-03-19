package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/secretref"
	"golang.org/x/oauth2"
)

func TestOneDrive_TokenPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	tokPath := filepath.Join(tmpDir, "onedrive-token.json")
	tok := &oauth2.Token{AccessToken: "od-secret-token", RefreshToken: "od-refresh"}

	// 1. Save and Load from file
	if err := saveTokenJSON(tokPath, tok); err != nil {
		t.Fatalf("saveTokenJSON failed: %v", err)
	}
	got, err := loadToken(tokPath)
	if err != nil {
		t.Fatalf("loadToken failed: %v", err)
	}
	if got.AccessToken != tok.AccessToken {
		t.Errorf("got %q, want %q", got.AccessToken, tok.AccessToken)
	}

	// 2. Save and Load from ref
	resolver := secretref.NewDefaultResolver()
	refPath := filepath.Join(tmpDir, "od-ref-token.json")
	ref := "file://" + refPath
	ctx := context.Background()

	if err := saveTokenRefJSON(ctx, resolver, ref, tok); err != nil {
		t.Fatalf("saveTokenRefJSON failed: %v", err)
	}
	gotRef, err := loadTokenRef(ctx, resolver, ref)
	if err != nil {
		t.Fatalf("loadTokenRef failed: %v", err)
	}
	if gotRef.AccessToken != tok.AccessToken {
		t.Errorf("got %q, want %q", gotRef.AccessToken, tok.AccessToken)
	}
}

func TestOneDrive_Options(t *testing.T) {
	resolver := secretref.NewDefaultResolver()
	opts := []OneDriveOption{
		WithOneDriveClientID("client-id"),
		WithOneDriveResolver(resolver),
		WithOneDriveDriveName("Personal"),
		WithOneDriveRootPath("/Documents"),
		WithOneDriveTokenPath("/path/od.json"),
		WithOneDriveTokenRef("keychain://od-tok"),
		WithOneDriveExcludePatterns([]string{"Temp"}),
	}

	var cfg oneDriveOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.clientID != "client-id" {
		t.Error("clientID not set")
	}
	if cfg.resolver != resolver {
		t.Error("resolver not set")
	}
	if cfg.driveName != "Personal" {
		t.Error("driveName not set")
	}
	if cfg.rootPath != "/Documents" {
		t.Error("rootPath not set")
	}
	if cfg.tokenPath != "/path/od.json" {
		t.Error("tokenPath not set")
	}
	if cfg.tokenRef != "keychain://od-tok" {
		t.Error("tokenRef not set")
	}
}

func TestNewOneDriveSource_RequiresResolverForTokenRef(t *testing.T) {
	_, err := NewOneDriveSource(context.Background(), WithOneDriveTokenRef("config-token://onedrive/test"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a resolver") {
		t.Fatalf("unexpected error: %v", err)
	}
}
