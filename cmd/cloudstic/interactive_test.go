package main

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
)

func TestPromptValidatedLine_RetriesUntilValid(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()

	if _, err := writeEnd.WriteString("bad name!\ngood-name\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{
		out:    &out,
		errOut: &errOut,
		stdin:  readEnd,
		lineIn: bufio.NewReader(readEnd),
	}

	got, err := r.promptValidatedLine(context.Background(), "Profile name", "", func(v string) error {
		return validateRefName("profile", v)
	})
	if err != nil {
		t.Fatalf("promptValidatedLine: %v", err)
	}
	if got != "good-name" {
		t.Fatalf("got %q want good-name", got)
	}
	if !strings.Contains(errOut.String(), "invalid profile name") {
		t.Fatalf("expected validation error in stderr, got: %s", errOut.String())
	}
}
