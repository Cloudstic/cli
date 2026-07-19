package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
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
			r := newRunner(tt.args)
			if code := runCmd(r, context.Background(), tt.cmd); code != 1 {
				t.Fatalf("runCmd() exit code = %d, want 1", code)
			}
		})
	}
}

func TestRunCmdVersionWritesToRunner(t *testing.T) {
	var out, errOut bytes.Buffer
	r := newRunner(nil)
	r.out, r.errOut = &out, &errOut

	if code := runCmd(r, context.Background(), "version"); code != 0 {
		t.Fatalf("runCmd() exit code = %d, want 0", code)
	}
	if got := out.String(); !strings.HasPrefix(got, "cloudstic ") {
		t.Fatalf("stdout = %q, want version output", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

func TestRunCmdUnknownWritesToStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	r := newRunner(nil)
	r.out, r.errOut = &out, &errOut

	if code := runCmd(r, context.Background(), "wat"); code != 1 {
		t.Fatalf("runCmd() exit code = %d, want 1", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if got := errOut.String(); !strings.HasPrefix(got, "Unknown command: wat\n") {
		t.Fatalf("stderr = %q, want unknown-command message", got)
	}
}

func TestResolveBuildMetadataUsesBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.2.3"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.time", Value: "2026-07-19T16:27:05Z"},
		},
	}

	gotVersion, gotCommit, gotDate := resolveBuildMetadata(info)
	if gotVersion != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", gotVersion)
	}
	if gotCommit != "abc123" {
		t.Fatalf("commit = %q, want abc123", gotCommit)
	}
	if gotDate != "2026-07-19T16:27:05Z" {
		t.Fatalf("date = %q, want build-info time", gotDate)
	}
}

func TestResolveBuildMetadataDefaultsWithoutBuildInfo(t *testing.T) {
	gotVersion, gotCommit, gotDate := resolveBuildMetadata(nil)
	if gotVersion != "dev" || gotCommit != "none" || gotDate != "unknown" {
		t.Fatalf("metadata = (%q, %q, %q), want development defaults", gotVersion, gotCommit, gotDate)
	}
}

func TestRunFlushesCPUProfileBeforeExit(t *testing.T) {
	if profilePath := os.Getenv("CLOUDSTIC_TEST_CPU_PROFILE_PATH"); profilePath != "" {
		os.Args = []string{"cloudstic", "-cpuprofile", profilePath, "version"}
		os.Exit(run())
	}

	profilePath := filepath.Join(t.TempDir(), "cpu.pprof")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunFlushesCPUProfileBeforeExit$")
	cmd.Env = append(os.Environ(), "CLOUDSTIC_TEST_CPU_PROFILE_PATH="+profilePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("profile subprocess failed: %v\n%s", err, output)
	}

	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("stat CPU profile: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("CPU profile is empty")
	}

	pprof := exec.Command("go", "tool", "pprof", "-top", os.Args[0], profilePath)
	pprof.Env = append(os.Environ(), "GOCACHE="+t.TempDir())
	if output, err := pprof.CombinedOutput(); err != nil {
		t.Fatalf("parse CPU profile: %v\n%s", err, output)
	}
}
