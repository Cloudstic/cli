package cloudstic

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/pkg/source/local"
)

// Client.Check is a four-line delegation to engine.CheckManager, but it is the
// only place the manager is handed the client's store, reporter and HMAC key.
// The engine's own tests construct the manager directly, so they cannot catch a
// wiring mistake here — passing the undecorated store, or a nil hmacKey, would
// make Check walk the wrong bytes or fail to resolve content refs while every
// engine test stayed green.
func TestClient_Check_VerifiesAFreshRepository(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	sourceDir := t.TempDir()

	writeSourceTree(t, sourceDir, map[string]string{
		"a.txt":        "alpha",
		"nested/b.txt": "beta",
	})
	writeRepoConfig(t, storeDir)

	client := newPackfileClient(t, storeDir)
	if _, err := client.Backup(ctx, local.New(sourceDir)); err != nil {
		t.Fatalf("backup: %v", err)
	}

	res, err := client.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res == nil {
		t.Fatal("Check returned no result")
	}
	if len(res.Errors) != 0 {
		t.Errorf("a repository written by this build must verify clean, got %d error(s): %v",
			len(res.Errors), res.Errors)
	}

	// WithReadData re-hashes chunk bytes rather than only resolving refs, so it
	// exercises the hmacKey path that plain Check can leave untouched.
	deep, err := client.Check(ctx, WithReadData())
	if err != nil {
		t.Fatalf("Check(WithReadData): %v", err)
	}
	if len(deep.Errors) != 0 {
		t.Errorf("byte-level verification failed on a fresh repository: %v", deep.Errors)
	}
}
