package main

import (
	"context"
	"testing"
)

func TestRunCmd_ParsingErrorsReturnExitCode(t *testing.T) {
	t.Setenv("CLOUDSTIC_CONFIG_DIR", t.TempDir())

	tests := []struct {
		name string
		cmd  string
		args []string
	}{
		{name: "init", cmd: "init", args: []string{"-unknown"}},
		{name: "backup", cmd: "backup", args: []string{"-unknown"}},
		{name: "restore", cmd: "restore", args: []string{"-unknown"}},
		{name: "list", cmd: "list", args: []string{"-unknown"}},
		{name: "ls", cmd: "ls", args: []string{"-unknown"}},
		{name: "prune", cmd: "prune", args: []string{"-unknown"}},
		{name: "forget", cmd: "forget", args: []string{"-unknown"}},
		{name: "diff", cmd: "diff", args: []string{"-unknown"}},
		{name: "break lock", cmd: "break-lock", args: []string{"-unknown"}},
		{name: "key", cmd: "key", args: []string{"list", "-unknown"}},
		{name: "check", cmd: "check", args: []string{"-unknown"}},
		{name: "cat", cmd: "cat", args: []string{"-unknown"}},
		{name: "profile", cmd: "profile", args: []string{"list", "-unknown"}},
		{name: "auth", cmd: "auth", args: []string{"list", "-unknown"}},
		{name: "store", cmd: "store", args: []string{"list", "-unknown"}},
		{name: "source", cmd: "source", args: []string{"discover", "-unknown"}},
		{name: "setup", cmd: "setup", args: []string{"workstation", "-unknown"}},
		{name: "tui", cmd: "tui", args: []string{"-unknown"}},
		{name: "completion", cmd: "completion", args: []string{"unknown"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := runCmd(context.Background(), tt.cmd, tt.args); code != 1 {
				t.Fatalf("runCmd() exit code = %d, want 1", code)
			}
		})
	}
}
