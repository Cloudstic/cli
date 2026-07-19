package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden test files")

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden file: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	got = strings.ReplaceAll(got, "\r\n", "\n")
	wantText := strings.ReplaceAll(string(want), "\r\n", "\n")
	if got != wantText {
		t.Fatalf("output differs from %s\n--- want ---\n%s--- got ---\n%s", path, want, got)
	}
}
