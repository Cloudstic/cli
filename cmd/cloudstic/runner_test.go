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

// The migration note is the one thing a person running against a packfile
// repository is told that the library has no opinion about, so its conditions
// are worth pinning: which formats get it, and who can silence it.
func TestPrintLegacyFormatNote(t *testing.T) {
	base := clientConfig{}
	base.Store.URI = "local:/srv/backup"

	quiet := base
	quiet.Quiet = true

	for _, tc := range []struct {
		name   string
		cfg    clientConfig
		format int
		want   bool
	}{
		{"packfile repository is told", base, cloudstic.RepoFormatV2, true},
		{"v3 repository is not", base, cloudstic.RepoFormatV3, false},
		{"a format above v3 is not", base, cloudstic.RepoFormatV3 + 1, false},
		// 0 is "not known yet", which happens for a client that never opened a
		// marker. Saying a repository is on format 0 would be worse than
		// saying nothing.
		{"unknown format is not", base, 0, false},
		{"quiet silences it", quiet, cloudstic.RepoFormatV2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printLegacyFormatNote(&buf, tc.cfg, tc.format)
			got := buf.String() != ""
			if got != tc.want {
				t.Fatalf("printed = %v, want %v (output %q)", got, tc.want, buf.String())
			}
			if !tc.want {
				return
			}
			// The note has to be actionable, which means naming the store the
			// user would have to pass to migrate.
			if !strings.Contains(buf.String(), "local:/srv/backup") {
				t.Errorf("note does not name the store: %q", buf.String())
			}
			if !strings.Contains(buf.String(), "cloudstic migrate") {
				t.Errorf("note does not give the command: %q", buf.String())
			}
		})
	}
}
