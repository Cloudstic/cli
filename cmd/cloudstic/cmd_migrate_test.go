package main

import (
	"testing"

	"github.com/cloudstic/cli/pkg/keychain"
)

// TestMigrationInitOpts_NeverCreatesAnUnencryptedCopyOfAnEncryptedRepository
// pins the one failure this helper exists to prevent.
//
// Every other way a migration can go wrong is loud: a bad URI fails to open, a
// bad key fails to unlock, a corrupt copy fails verification. Creating the
// destination without encryption is silent — the copy succeeds, check passes,
// and the operator is told to delete the source. So the absence of credentials
// has to be an error rather than a default.
func TestMigrationInitOpts_NeverCreatesAnUnencryptedCopyOfAnEncryptedRepository(t *testing.T) {
	if _, err := migrationInitOpts(true, 3, nil); err == nil {
		t.Fatal("an encrypted source with no credentials must not yield init options")
	}

	opts, err := migrationInitOpts(true, 3, keychain.Chain{keychain.WithPassword("pw")})
	if err != nil {
		t.Fatalf("encrypted source with credentials: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("got %d init options, want the target format and the credentials", len(opts))
	}
}

// An unencrypted source produces an unencrypted destination, and does so
// without needing credentials it was never given.
func TestMigrationInitOpts_CarriesAnUnencryptedSourceAcross(t *testing.T) {
	opts, err := migrationInitOpts(false, 3, nil)
	if err != nil {
		t.Fatalf("unencrypted source: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("got %d init options, want the target format and no-encryption", len(opts))
	}
}
