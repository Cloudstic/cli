package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	cloudstic "github.com/cloudstic/cli"
)

func TestRunBubbleTeaTUI_Success(t *testing.T) {
	restoreRun := swapBubbleTeaRunner(func(context.Context, tea.Model, ...tea.ProgramOption) error {
		return nil
	})
	defer restoreRun()
	restoreCfg := swapTUILoadConfig(func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{Version: 1}, nil
	})
	defer restoreCfg()

	r := &runner{out: &strings.Builder{}, errOut: &strings.Builder{}}
	if code := runBubbleTeaTUI(r, context.Background(), "profiles.yaml"); code != 0 {
		t.Fatalf("exit code=%d want 0", code)
	}
}

func TestRunBubbleTeaTUI_ProgramError(t *testing.T) {
	restoreRun := swapBubbleTeaRunner(func(context.Context, tea.Model, ...tea.ProgramOption) error {
		return errors.New("boom")
	})
	defer restoreRun()
	restoreCfg := swapTUILoadConfig(func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{Version: 1}, nil
	})
	defer restoreCfg()

	errOut := &strings.Builder{}
	r := &runner{out: &strings.Builder{}, errOut: errOut}
	if code := runBubbleTeaTUI(r, context.Background(), "profiles.yaml"); code == 0 {
		t.Fatalf("exit code=0 want non-zero on error")
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("errOut missing error detail: %q", errOut.String())
	}
}

func TestRunBubbleTeaTUI_ConfigError(t *testing.T) {
	restoreCfg := swapTUILoadConfig(func(string) (*cloudstic.ProfilesConfig, error) {
		return nil, errors.New("bad config")
	})
	defer restoreCfg()

	errOut := &strings.Builder{}
	r := &runner{out: &strings.Builder{}, errOut: errOut}
	if code := runBubbleTeaTUI(r, context.Background(), "profiles.yaml"); code == 0 {
		t.Fatalf("exit code=0 want non-zero on config error")
	}
	if !strings.Contains(errOut.String(), "bad config") {
		t.Fatalf("errOut missing config error: %q", errOut.String())
	}
}

func swapBubbleTeaRunner(fn func(context.Context, tea.Model, ...tea.ProgramOption) error) func() {
	prev := tuiRunBubbleTea
	tuiRunBubbleTea = fn
	return func() { tuiRunBubbleTea = prev }
}

func swapTUILoadConfig(fn func(string) (*cloudstic.ProfilesConfig, error)) func() {
	prev := tuiLoadConfig
	tuiLoadConfig = fn
	return func() { tuiLoadConfig = prev }
}
