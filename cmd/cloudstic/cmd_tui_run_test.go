package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunTUIProgram_Success(t *testing.T) {
	restoreRun := swapTUIProgramRunner(func(context.Context, tea.Model, ...tea.ProgramOption) error {
		return nil
	})
	defer restoreRun()
	restoreCfg := swapTUILoadConfig(func(string) (*profile.Config, error) {
		return &profile.Config{Version: 1}, nil
	})
	defer restoreCfg()

	r := &runner{out: &strings.Builder{}, errOut: &strings.Builder{}}
	if code := runTUIProgram(r, context.Background(), "profiles.yaml", ""); code != 0 {
		t.Fatalf("exit code=%d want 0", code)
	}
}

func TestRunTUIProgram_ProgramError(t *testing.T) {
	restoreRun := swapTUIProgramRunner(func(context.Context, tea.Model, ...tea.ProgramOption) error {
		return errors.New("boom")
	})
	defer restoreRun()
	restoreCfg := swapTUILoadConfig(func(string) (*profile.Config, error) {
		return &profile.Config{Version: 1}, nil
	})
	defer restoreCfg()

	errOut := &strings.Builder{}
	r := &runner{out: &strings.Builder{}, errOut: errOut}
	if code := runTUIProgram(r, context.Background(), "profiles.yaml", ""); code == 0 {
		t.Fatalf("exit code=0 want non-zero on error")
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("errOut missing error detail: %q", errOut.String())
	}
}

func TestRunTUIProgram_ConfigError(t *testing.T) {
	restoreCfg := swapTUILoadConfig(func(string) (*profile.Config, error) {
		return nil, errors.New("bad config")
	})
	defer restoreCfg()

	errOut := &strings.Builder{}
	r := &runner{out: &strings.Builder{}, errOut: errOut}
	if code := runTUIProgram(r, context.Background(), "profiles.yaml", ""); code == 0 {
		t.Fatalf("exit code=0 want non-zero on config error")
	}
	if !strings.Contains(errOut.String(), "bad config") {
		t.Fatalf("errOut missing config error: %q", errOut.String())
	}
}

func swapTUIProgramRunner(fn func(context.Context, tea.Model, ...tea.ProgramOption) error) func() {
	prev := tuiRunProgram
	tuiRunProgram = fn
	return func() { tuiRunProgram = prev }
}

func swapTUILoadConfig(fn func(string) (*profile.Config, error)) func() {
	prev := tuiLoadConfig
	tuiLoadConfig = fn
	return func() { tuiLoadConfig = prev }
}
