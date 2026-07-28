package main

import (
	"strings"
	"testing"
)

// Characterization tests for the inline-vs-secret-reference precedence that
// every credential field in a profile store goes through. RFC 0022 §7 moves
// this into pkg/config, where the precedence becomes a documented public
// contract rather than an implementation detail of applyProfileStore.
//
// env:// is used throughout because it is the one secret backend available on
// every platform and needs no keychain, so these stay hermetic.

func TestResolveProfileStoreValue_InlineWinsOverSecretRef(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_SECRET", "from-secret-ref")

	got, err := resolveProfileStoreValue("s3_access_key", "inline-value", "env://CLOUDSTIC_TEST_SECRET")
	if err != nil {
		t.Fatalf("resolveProfileStoreValue: %v", err)
	}
	if got != "inline-value" {
		t.Errorf("got %q, want %q: an inline value must win over a secret reference, "+
			"and must do so without resolving the reference at all", got, "inline-value")
	}
}

func TestResolveProfileStoreValue_FallsBackToSecretRef(t *testing.T) {
	t.Setenv("CLOUDSTIC_TEST_SECRET", "from-secret-ref")

	got, err := resolveProfileStoreValue("s3_access_key", "", "env://CLOUDSTIC_TEST_SECRET")
	if err != nil {
		t.Fatalf("resolveProfileStoreValue: %v", err)
	}
	if got != "from-secret-ref" {
		t.Errorf("got %q, want %q", got, "from-secret-ref")
	}
}

func TestResolveProfileStoreValue_NeitherIsEmptyNotAnError(t *testing.T) {
	got, err := resolveProfileStoreValue("s3_access_key", "", "")
	if err != nil {
		t.Fatalf("resolveProfileStoreValue: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestResolveProfileStoreValue_ErrorNamesTheField pins the diagnostic, which is
// the whole reason resolution happens here rather than at connect time: a
// missing secret must say which profile field asked for it and what reference
// failed, not surface later as an empty credential and an auth error from a
// cloud provider.
func TestResolveProfileStoreValue_ErrorNamesTheField(t *testing.T) {
	_, err := resolveProfileStoreValue("b2_app_key", "", "env://CLOUDSTIC_TEST_DEFINITELY_UNSET")
	if err == nil {
		t.Fatal("expected an error for an unresolvable secret reference, got nil")
	}
	for _, want := range []string{"b2_app_key", "env://CLOUDSTIC_TEST_DEFINITELY_UNSET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestResolveProfileStoreValue_RejectsUnknownScheme(t *testing.T) {
	_, err := resolveProfileStoreValue("password", "", "not-a-ref")
	if err == nil {
		t.Fatal("expected an error for a malformed secret reference, got nil")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error = %q, want it to name the field", err)
	}
}
