package main

import (
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/keychain"
)

func TestPrintKeySlots_Empty(t *testing.T) {
	var out, errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	r.printKeySlots(nil)

	if !strings.Contains(errOut.String(), "No key slots found") {
		t.Errorf("expected 'No key slots found', got:\n%s", errOut.String())
	}
	if out.String() != "" {
		t.Errorf("expected no table output for empty slots, got:\n%s", out.String())
	}
}

func TestPrintKeySlots_WithSlots(t *testing.T) {
	var out, errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	slots := []cloudstic.KeySlot{
		{SlotType: "password", Label: "main", KDFParams: &keychain.KDFParams{Algorithm: "argon2id"}},
		{SlotType: "recovery", Label: "backup", KDFParams: nil},
	}

	r.printKeySlots(slots)

	tableOut := out.String()
	if !strings.Contains(tableOut, "password") {
		t.Errorf("expected slot type 'password' in table, got:\n%s", tableOut)
	}
	if !strings.Contains(tableOut, "argon2id") {
		t.Errorf("expected KDF algorithm in table, got:\n%s", tableOut)
	}
	// Recovery slot has no KDFParams — expect em dash placeholder
	if !strings.Contains(tableOut, "—") {
		t.Errorf("expected '—' for nil KDFParams, got:\n%s", tableOut)
	}

	errStr := errOut.String()
	if !strings.Contains(errStr, "2 key slot(s) found") {
		t.Errorf("expected count message, got:\n%s", errStr)
	}
}
