package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestPrintInitResult_Encrypted(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	printInitResult(r.errOut, &cloudstic.InitResult{
		Encrypted:    true,
		AdoptedSlots: false,
		RecoveryKey:  "",
	})
	assertGolden(t, "print_init_encrypted", errOut.String())
}

func TestPrintInitResult_AdoptedSlots(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	printInitResult(r.errOut, &cloudstic.InitResult{
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
	printInitResult(r.errOut, &cloudstic.InitResult{
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
	printInitResult(r.errOut, &cloudstic.InitResult{Encrypted: false})

	got := errOut.String()
	if !strings.Contains(got, "WARNING") {
		t.Errorf("expected WARNING for unencrypted repo, got:\n%s", got)
	}
	if !strings.Contains(got, "encrypted: false") {
		t.Errorf("expected encrypted=false, got:\n%s", got)
	}
}

func TestRunInit_JSON(t *testing.T) {
	tmpDir := t.TempDir()
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}

	args := []string{"-store", "local:" + tmpDir, "-no-encryption", "-json"}
	if code := initCommand().execute(r.withArgs(args), context.Background(), "init"); code != 0 {
		t.Fatalf("runInit() exit = %d, want 0, stderr=%s", code, r.errOut.(*strings.Builder).String())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if got["Encrypted"] != false {
		t.Fatalf("expected Encrypted=false in JSON output, got: %v", got)
	}
}

func TestPrintRecoveryKey(t *testing.T) {
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut}
	printRecoveryKey(r.errOut, "abandon ability able about above")

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
