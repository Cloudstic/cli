package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunnerFailInterrupted(t *testing.T) {
	var errOut bytes.Buffer
	r := newRunner(nil)
	r.errOut = &errOut

	err := fmt.Errorf("chunking file: %w", context.Canceled)
	if code := r.fail("Backup failed: %v", err); code != exitInterrupted {
		t.Fatalf("fail() exit code = %d, want %d", code, exitInterrupted)
	}
	if got, want := errOut.String(), "Interrupted.\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunnerFailRepoLocked(t *testing.T) {
	var errOut bytes.Buffer
	r := newRunner(nil)
	r.errOut = &errOut

	err := fmt.Errorf("acquire shared lock: %w", cloudstic.ErrRepoLocked)
	code := r.fail("Backup failed: %v", err)
	if code != exitFailure {
		t.Fatalf("fail() exit code = %d, want %d", code, exitFailure)
	}
	if got := errOut.String(); !strings.Contains(got, "break-lock") {
		t.Fatalf("stderr = %q, want a break-lock hint", got)
	}
}

func TestRunnerFailJSON(t *testing.T) {
	var errOut bytes.Buffer
	r := newRunner([]string{"--json"})
	r.errOut = &errOut

	if code := r.fail("Backup failed: %s", "store unavailable"); code != exitFailure {
		t.Fatalf("fail() exit code = %d, want %d", code, exitFailure)
	}
	assertJSONError(t, errOut.Bytes(), "Backup failed: store unavailable")
}

func TestRunBackupInterruptedJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	sourcePath := t.TempDir()
	r := newRunner([]string{"-source", "local:" + sourcePath, "-json"})
	r.out, r.errOut = &out, &errOut
	r.client = &stubClient{backupErr: fmt.Errorf("chunking file: %w", context.Canceled)}

	if code := backupCommand().execute(r, context.Background(), "backup"); code != exitInterrupted {
		t.Fatalf("runBackup() exit code = %d, want %d", code, exitInterrupted)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	assertJSONError(t, errOut.Bytes(), "Interrupted.")
}

func TestRunBackupParseErrorJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	r := newRunner([]string{"-json", "-unknown"})
	r.out, r.errOut = &out, &errOut

	if code := backupCommand().execute(r, context.Background(), "backup"); code != exitFailure {
		t.Fatalf("runBackup() exit code = %d, want %d", code, exitFailure)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	var got errorOutput
	if err := json.Unmarshal(errOut.Bytes(), &got); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\n%s", err, errOut.Bytes())
	}
	if !strings.Contains(got.Error, "flag provided but not defined: -unknown") {
		t.Fatalf("JSON error = %q, want unknown-flag message", got.Error)
	}
}

func TestJSONFlagAfterPositionalEnablesStructuredErrors(t *testing.T) {
	var errOut bytes.Buffer
	r := newRunner([]string{"snapshot", "--json"})
	r.errOut = &errOut

	r.fail("failed")
	assertJSONError(t, errOut.Bytes(), "failed")
}

func assertJSONError(t *testing.T, data []byte, want string) {
	t.Helper()
	var got errorOutput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON error: %v\n%s", err, data)
	}
	if got.Error != want {
		t.Fatalf("JSON error = %q, want %q", got.Error, want)
	}
}
