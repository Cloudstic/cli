package main

import (
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestPrintInitResult_Encrypted(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printInitResult(&cloudstic.InitResult{
		Encrypted:    true,
		AdoptedSlots: false,
		RecoveryKey:  "",
	})

	got := errOut.String()
	if !strings.Contains(got, "Created new encryption key slots.") {
		t.Errorf("expected key slot message, got:\n%s", got)
	}
	if !strings.Contains(got, "encrypted: true") {
		t.Errorf("expected encrypted=true, got:\n%s", got)
	}
}

func TestPrintInitResult_AdoptedSlots(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printInitResult(&cloudstic.InitResult{
		Encrypted:    true,
		AdoptedSlots: true,
	})

	got := errOut.String()
	if !strings.Contains(got, "Adopted existing encryption key slots.") {
		t.Errorf("expected adopted slots message, got:\n%s", got)
	}
}

func TestPrintInitResult_WithRecoveryKey(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printInitResult(&cloudstic.InitResult{
		Encrypted:   true,
		RecoveryKey: "word1 word2 word3",
	})

	got := errOut.String()
	if !strings.Contains(got, "RECOVERY KEY") {
		t.Errorf("expected RECOVERY KEY header, got:\n%s", got)
	}
	if !strings.Contains(got, "word1 word2 word3") {
		t.Errorf("expected mnemonic in output, got:\n%s", got)
	}
}

func TestPrintInitResult_NoEncryption(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printInitResult(&cloudstic.InitResult{Encrypted: false})

	got := errOut.String()
	if !strings.Contains(got, "WARNING") {
		t.Errorf("expected WARNING for unencrypted repo, got:\n%s", got)
	}
	if !strings.Contains(got, "encrypted: false") {
		t.Errorf("expected encrypted=false, got:\n%s", got)
	}
}

func TestPrintRecoveryKey(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	r.printRecoveryKey("abandon ability able about above")

	got := errOut.String()
	if !strings.Contains(got, "RECOVERY KEY") {
		t.Errorf("expected RECOVERY KEY header, got:\n%s", got)
	}
	if !strings.Contains(got, "abandon ability able about above") {
		t.Errorf("expected mnemonic in output, got:\n%s", got)
	}
	if !strings.Contains(got, "24 words") {
		t.Errorf("expected instructions about 24 words, got:\n%s", got)
	}
}
