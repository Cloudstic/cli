package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/tui"
	xterm "golang.org/x/term"
)

func stubTUITestHooks(t *testing.T) {
	t.Helper()

	oldIsTerminal := isTerminalFunc
	oldMakeRaw := tuiMakeRaw
	oldRestore := tuiRestoreTerminal
	oldEnterAlt := tuiEnterAltScreen
	oldLeaveAlt := tuiLeaveAltScreen

	isTerminalFunc = func(uintptr) bool { return true }
	tuiMakeRaw = func(int) (*xterm.State, error) { return nil, nil }
	tuiRestoreTerminal = func(int, *xterm.State) error { return nil }
	tuiEnterAltScreen = func(io.Writer) error { return nil }
	tuiLeaveAltScreen = func(io.Writer) error { return nil }

	t.Cleanup(func() {
		isTerminalFunc = oldIsTerminal
		tuiMakeRaw = oldMakeRaw
		tuiRestoreTerminal = oldRestore
		tuiEnterAltScreen = oldEnterAlt
		tuiLeaveAltScreen = oldLeaveAlt
	})
}

func TestRunTUI_Help(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "tui", "--help"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runTUI(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Usage: cloudstic tui [options]") {
		t.Fatalf("unexpected help output:\n%s", out.String())
	}
}

func TestRunTUI_RequiresInteractiveTerminal(t *testing.T) {
	oldIsTerminal := isTerminalFunc
	t.Cleanup(func() { isTerminalFunc = oldIsTerminal })
	isTerminalFunc = func(uintptr) bool { return false }

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "tui"}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{
		out:    &out,
		errOut: &errOut,
		stdin:  readEnd,
		lineIn: bufio.NewReader(readEnd),
	}
	if code := r.runTUI(context.Background()); code == 0 {
		t.Fatalf("expected failure for non-interactive terminal")
	}
	if !strings.Contains(errOut.String(), "requires an interactive terminal") {
		t.Fatalf("unexpected stderr:\n%s", errOut.String())
	}
}

func TestRunTUI_RendersDashboardAndQuitsOnQ(t *testing.T) {
	stubTUITestHooks(t)

	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:bucket/prod"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"documents": {Source: "local:/tmp/Documents", Store: "remote"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	oldArgs := os.Args
	oldNoPrompt := os.Getenv("CLOUDSTIC_PROFILES_FILE")
	t.Cleanup(func() {
		os.Args = oldArgs
		_ = os.Setenv("CLOUDSTIC_PROFILES_FILE", oldNoPrompt)
	})
	_ = os.Setenv("CLOUDSTIC_PROFILES_FILE", profilesPath)
	os.Args = []string{"cloudstic", "tui", "-profiles-file", profilesPath}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("q"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{
			ProfileCount:    1,
			StoreCount:      1,
			AuthCount:       0,
			SelectedProfile: "documents",
			Profiles: []tui.ProfileCard{{
				Name:       "documents",
				Source:     "local:/tmp/Documents",
				StoreRef:   "remote",
				Enabled:    true,
				Status:     tui.ProfileStatusReady,
				LastBackup: "2026-04-03 11:05",
				LastRef:    "snapshot/abc123",
			}},
		}, nil
	}

	r := &runner{
		out:        &out,
		errOut:     &errOut,
		stdoutFile: os.Stdout,
		stdin:      readEnd,
		lineIn:     bufio.NewReader(readEnd),
	}
	if code := r.runTUI(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Cloudstic TUI") || !strings.Contains(got, "documents") || !strings.Contains(got, "enabled") || !strings.Contains(got, "›") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRunTUI_ArrowNavigationChangesSelection(t *testing.T) {
	stubTUITestHooks(t)

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "tui"}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("\x1b[Bq"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{
			ProfileCount: 2,
			StoreCount:   1,
			Profiles: []tui.ProfileCard{
				{Name: "documents", Source: "local:/tmp/Documents", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
				{Name: "photos", Source: "local:/tmp/Photos", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
			},
		}, nil
	}
	r := &runner{
		out:        &out,
		errOut:     &errOut,
		stdoutFile: os.Stdout,
		stdin:      readEnd,
		lineIn:     bufio.NewReader(readEnd),
	}
	if code := r.runTUI(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[36m› \x1b[0m\x1b[1mphotos\x1b[0m") {
		t.Fatalf("expected selection to move to photos, got:\n%s", got)
	}
}

func TestRunTUI_BackupActionRunsSelectedProfileAction(t *testing.T) {
	stubTUITestHooks(t)

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "tui"}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("bq"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	var ranProfile string
	oldBuild := tuiBuildDashboard
	oldAction := tuiRunProfileAction
	oldCheck := tuiRunProfileCheck
	t.Cleanup(func() {
		tuiBuildDashboard = oldBuild
		tuiRunProfileAction = oldAction
		tuiRunProfileCheck = oldCheck
	})
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{
			ProfileCount:    1,
			StoreCount:      1,
			SelectedProfile: "documents",
			Profiles: []tui.ProfileCard{
				{Name: "documents", Source: "local:/tmp/Documents", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
			},
		}, nil
	}
	tuiRunProfileAction = func(_ context.Context, _ *runner, _ string, profile tui.ProfileCard, _ *tuiActionState) error {
		ranProfile = profile.Name
		return nil
	}
	r := &runner{
		out:        &out,
		errOut:     &errOut,
		stdoutFile: os.Stdout,
		stdin:      readEnd,
		lineIn:     bufio.NewReader(readEnd),
	}
	if code := r.runTUI(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if ranProfile != "documents" {
		t.Fatalf("selected action ran for %q, want documents", ranProfile)
	}
	if !strings.Contains(out.String(), "Running backup for profile documents") {
		t.Fatalf("expected activity log in dashboard, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Action completed successfully") {
		t.Fatalf("expected success activity log in dashboard, got:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("expected no stderr spillover, got:\n%s", errOut.String())
	}
}

func TestRunTUI_CheckActionRunsSelectedProfileCheck(t *testing.T) {
	stubTUITestHooks(t)

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "tui"}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("cq"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	var checkedProfile string
	oldBuild := tuiBuildDashboard
	oldAction := tuiRunProfileAction
	oldCheck := tuiRunProfileCheck
	t.Cleanup(func() {
		tuiBuildDashboard = oldBuild
		tuiRunProfileAction = oldAction
		tuiRunProfileCheck = oldCheck
	})
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{
			ProfileCount:    1,
			StoreCount:      1,
			SelectedProfile: "documents",
			Profiles: []tui.ProfileCard{
				{Name: "documents", Source: "local:/tmp/Documents", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady, StoreHealth: tui.StoreHealthReady},
			},
		}, nil
	}
	tuiRunProfileAction = func(_ context.Context, _ *runner, _ string, _ tui.ProfileCard, _ *tuiActionState) error {
		t.Fatalf("backup action should not run")
		return nil
	}
	tuiRunProfileCheck = func(_ context.Context, _ *runner, _ string, profile tui.ProfileCard, _ *tuiActionState) error {
		checkedProfile = profile.Name
		return nil
	}
	r := &runner{
		out:        &out,
		errOut:     &errOut,
		stdoutFile: os.Stdout,
		stdin:      readEnd,
		lineIn:     bufio.NewReader(readEnd),
	}
	if code := r.runTUI(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if checkedProfile != "documents" {
		t.Fatalf("selected check ran for %q, want documents", checkedProfile)
	}
	if !strings.Contains(out.String(), "Running repository check for profile documents") {
		t.Fatalf("expected check activity log in dashboard, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Check completed successfully") {
		t.Fatalf("expected check success log in dashboard, got:\n%s", out.String())
	}
}

func TestReadTUIAction_ParsesCSIArrowKeys(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[A")))
	if err != nil {
		t.Fatalf("readTUIAction up: %v", err)
	}
	if ev != tuiActionUp {
		t.Fatalf("up action=%v want %v", ev, tuiActionUp)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[B")))
	if err != nil {
		t.Fatalf("readTUIAction down: %v", err)
	}
	if ev != tuiActionDown {
		t.Fatalf("down action=%v want %v", ev, tuiActionDown)
	}
}

func TestReadTUIAction_ParsesParameterizedCSIArrowKeys(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[1;2A")))
	if err != nil {
		t.Fatalf("readTUIAction param up: %v", err)
	}
	if ev != tuiActionUp {
		t.Fatalf("param up action=%v want %v", ev, tuiActionUp)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[1;2B")))
	if err != nil {
		t.Fatalf("readTUIAction param down: %v", err)
	}
	if ev != tuiActionDown {
		t.Fatalf("param down action=%v want %v", ev, tuiActionDown)
	}
}

func TestReadTUIAction_ParsesSS3ArrowKeys(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1bOA")))
	if err != nil {
		t.Fatalf("readTUIAction ss3 up: %v", err)
	}
	if ev != tuiActionUp {
		t.Fatalf("ss3 up action=%v want %v", ev, tuiActionUp)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1bOB")))
	if err != nil {
		t.Fatalf("readTUIAction ss3 down: %v", err)
	}
	if ev != tuiActionDown {
		t.Fatalf("ss3 down action=%v want %v", ev, tuiActionDown)
	}
}

func TestReadTUIAction_ParsesCheckShortcut(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("c")))
	if err != nil {
		t.Fatalf("readTUIAction check: %v", err)
	}
	if ev != tuiActionCheck {
		t.Fatalf("check action=%v want %v", ev, tuiActionCheck)
	}
}

func TestTUISession_EnterLeaveManagesTerminalState(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	}()

	oldMakeRaw := tuiMakeRaw
	oldRestore := tuiRestoreTerminal
	oldEnterAlt := tuiEnterAltScreen
	oldLeaveAlt := tuiLeaveAltScreen
	t.Cleanup(func() {
		tuiMakeRaw = oldMakeRaw
		tuiRestoreTerminal = oldRestore
		tuiEnterAltScreen = oldEnterAlt
		tuiLeaveAltScreen = oldLeaveAlt
	})

	var enteredAlt, leftAlt, madeRaw, restored int
	state := &xterm.State{}
	tuiEnterAltScreen = func(io.Writer) error { enteredAlt++; return nil }
	tuiLeaveAltScreen = func(io.Writer) error { leftAlt++; return nil }
	tuiMakeRaw = func(int) (*xterm.State, error) { madeRaw++; return state, nil }
	tuiRestoreTerminal = func(int, *xterm.State) error { restored++; return nil }

	s := newTUISession(&runner{out: io.Discard, stdin: readEnd}, "", tui.Dashboard{})
	if err := s.enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if s.rawState != state {
		t.Fatalf("rawState not set")
	}
	s.leave()
	if enteredAlt != 1 || leftAlt != 1 || madeRaw != 1 || restored != 1 {
		t.Fatalf("unexpected terminal lifecycle counts: alt=%d/%d raw=%d restore=%d", enteredAlt, leftAlt, madeRaw, restored)
	}
	if s.rawState != nil {
		t.Fatalf("rawState not cleared")
	}
}

func TestTUISession_HandleActionRunRefreshesDashboard(t *testing.T) {
	stubTUITestHooks(t)

	oldBuild := tuiBuildDashboard
	oldAction := tuiRunProfileAction
	t.Cleanup(func() {
		tuiBuildDashboard = oldBuild
		tuiRunProfileAction = oldAction
	})

	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{
			ProfileCount:    1,
			StoreCount:      1,
			SelectedProfile: "docs",
			Profiles: []tui.ProfileCard{
				{Name: "docs", Source: "local:/docs", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady, LastBackup: "2026-04-03 12:00"},
			},
		}, nil
	}
	tuiRunProfileAction = func(_ context.Context, _ *runner, _ string, _ tui.ProfileCard, log *tuiActionState) error {
		log.Printf("backup complete")
		return nil
	}

	var out strings.Builder
	s := newTUISession(&runner{out: &out, stdoutFile: os.Stdout, stdin: os.Stdin}, "profiles.yaml", tui.Dashboard{
		SelectedProfile: "docs",
		Profiles: []tui.ProfileCard{
			{Name: "docs", Source: "local:/docs", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
		},
	})

	if _, err := s.handleAction(context.Background(), tuiActionRun); err != nil {
		t.Fatalf("handleAction(run): %v", err)
	}
	if s.dashboard.SelectedProfile != "docs" {
		t.Fatalf("selected profile lost after refresh: %+v", s.dashboard)
	}
	if len(s.dashboard.ActivityLines) == 0 {
		t.Fatalf("expected activity lines after action")
	}
	if !strings.Contains(strings.Join(s.dashboard.ActivityLines, "\n"), "Action completed successfully") {
		t.Fatalf("missing completion activity: %+v", s.dashboard.ActivityLines)
	}
}

func TestTUISession_RefreshPreservesSelectionAndActivity(t *testing.T) {
	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{
			Profiles: []tui.ProfileCard{
				{Name: "docs", Source: "local:/docs", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
			},
		}, nil
	}

	s := newTUISession(&runner{}, "profiles.yaml", tui.Dashboard{
		SelectedProfile: "docs",
		ActivityLines:   []string{"running"},
		Profiles:        []tui.ProfileCard{{Name: "docs"}},
	})
	if err := s.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if s.dashboard.SelectedProfile != "docs" {
		t.Fatalf("selection not preserved: %+v", s.dashboard)
	}
	if len(s.dashboard.ActivityLines) != 1 || s.dashboard.ActivityLines[0] != "running" {
		t.Fatalf("activity not preserved: %+v", s.dashboard.ActivityLines)
	}
}

func TestCaptureTUIRunnerOutput_RestoresRunnerState(t *testing.T) {
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	log := newTUIActionState(5)

	restore := captureTUIRunnerOutput(r, log)
	if _, err := io.WriteString(r.out, "hello\n"); err != nil {
		t.Fatalf("write captured output: %v", err)
	}
	restore()

	if got := strings.Join(log.Lines(), "\n"); !strings.Contains(got, "hello") {
		t.Fatalf("captured log missing output: %q", got)
	}
	if r.out != &out || r.errOut != &errOut {
		t.Fatalf("runner outputs not restored")
	}
}

func TestReadInput_ClosesChannelOnEOF(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	_ = writeEnd.Close()

	s := newTUISession(&runner{stdin: readEnd, lineIn: bufio.NewReader(readEnd)}, "", tui.Dashboard{})
	eventCh := make(chan tuiAction, 2)
	errCh := make(chan error, 1)
	s.readInput(eventCh, errCh)

	if _, ok := <-eventCh; ok {
		t.Fatalf("expected event channel to be closed")
	}
	select {
	case err := <-errCh:
		t.Fatalf("unexpected read error: %v", err)
	default:
	}
}

func TestTUIBuildDashboardErrorPropagates(t *testing.T) {
	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{}, errors.New("boom")
	}

	oldArgs := os.Args
	oldIsTerminal := isTerminalFunc
	t.Cleanup(func() {
		os.Args = oldArgs
		isTerminalFunc = oldIsTerminal
	})
	os.Args = []string{"cloudstic", "tui"}
	isTerminalFunc = func(uintptr) bool { return true }

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	_ = writeEnd.Close()

	var errOut strings.Builder
	r := &runner{out: io.Discard, errOut: &errOut, stdin: readEnd, stdoutFile: os.Stdout, lineIn: bufio.NewReader(readEnd)}
	if code := r.runTUI(context.Background()); code == 0 {
		t.Fatalf("expected failure")
	}
	if !strings.Contains(errOut.String(), "Failed to build TUI dashboard") {
		t.Fatalf("unexpected stderr: %s", errOut.String())
	}
}
