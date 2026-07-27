package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/keychain"
)

// initEncryptedRepoForKeyTest creates a fresh encrypted local repository and
// returns the store/password flags needed to address it.
func initEncryptedRepoForKeyTest(t *testing.T) (storeFlag, passwordFlag []string) {
	t.Helper()
	tmpDir := t.TempDir()
	storeFlag = []string{"-store", "local:" + tmpDir}
	passwordFlag = []string{"-password", "test-key-pass"}

	args := append(append([]string{}, storeFlag...), passwordFlag...)
	r := &runner{out: &strings.Builder{}, errOut: &strings.Builder{}}
	if code := initCommand().execute(r.withArgs(args), context.Background(), "init"); code != 0 {
		t.Fatalf("init failed: %s", r.errOut.(*strings.Builder).String())
	}
	return storeFlag, passwordFlag
}

func TestPrintKeySlots_Empty(t *testing.T) {
	var out, errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	printKeySlots(r.out, r.errOut, nil)

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

	printKeySlots(r.out, r.errOut, slots)

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

func TestRunKeyList_JSON(t *testing.T) {
	storeFlag, passwordFlag := initEncryptedRepoForKeyTest(t)

	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}
	args := append(append([]string{"list"}, storeFlag...), append(passwordFlag, "-json")...)
	if code := keyCommand().execute(r.withArgs(args), context.Background(), "key"); code != 0 {
		t.Fatalf("key list failed: %s", r.errOut.(*strings.Builder).String())
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if len(got) == 0 {
		t.Fatalf("expected at least one key slot in JSON output, got: %v", got)
	}
}

func TestRunAddRecoveryKey_JSON(t *testing.T) {
	storeFlag, passwordFlag := initEncryptedRepoForKeyTest(t)

	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}
	args := append(append([]string{"add-recovery"}, storeFlag...), append(passwordFlag, "-json")...)
	if code := keyCommand().execute(r.withArgs(args), context.Background(), "key"); code != 0 {
		t.Fatalf("key add-recovery failed: %s", r.errOut.(*strings.Builder).String())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	recoveryKey, _ := got["recovery_key"].(string)
	if recoveryKey == "" {
		t.Fatalf("expected non-empty recovery_key in JSON output, got: %v", got)
	}
}

func TestRunKeyPasswd_JSON(t *testing.T) {
	storeFlag, passwordFlag := initEncryptedRepoForKeyTest(t)

	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}}
	args := append(append([]string{"passwd"}, storeFlag...), append(passwordFlag, "-new-password", "new-key-pass", "-json")...)
	if code := keyCommand().execute(r.withArgs(args), context.Background(), "key"); code != 0 {
		t.Fatalf("key passwd failed: %s", r.errOut.(*strings.Builder).String())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if got["changed"] != true {
		t.Fatalf("expected changed=true in JSON output, got: %v", got)
	}
}
