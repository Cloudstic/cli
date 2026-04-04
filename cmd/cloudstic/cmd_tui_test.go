package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/tui"
	xterm "golang.org/x/term"
)

func TestTUIProfileSourceCompose(t *testing.T) {
	tests := []struct {
		name string
		src  tuiProfileSource
		want string
	}{
		{name: "local", src: tuiProfileSource{Type: "local", Value: "/docs"}, want: "local:/docs"},
		{name: "sftp", src: tuiProfileSource{Type: "sftp", Value: "backup@host/data"}, want: "sftp://backup@host/data"},
		{name: "gdrive root", src: tuiProfileSource{Type: "gdrive", Value: ""}, want: "gdrive"},
		{name: "gdrive path", src: tuiProfileSource{Type: "gdrive", Value: "/Team"}, want: "gdrive:/Team"},
		{name: "gdrive drive name", src: tuiProfileSource{Type: "gdrive", Value: "Shared Drive/Finance"}, want: "gdrive://Shared Drive/Finance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.src.Compose(); got != tt.want {
				t.Fatalf("Compose()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestTUIProfileModalSubmitReturnsTypedFieldError(t *testing.T) {
	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:bucket/test"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	modal, err := newTUIProfileModal(profilesPath, "", false)
	if err != nil {
		t.Fatalf("newTUIProfileModal: %v", err)
	}
	modal.fieldByKey("name").Value = ""

	_, err = modal.submit()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	fieldErr, ok := err.(*tuiFieldError)
	if !ok {
		t.Fatalf("expected *tuiFieldError, got %T", err)
	}
	if fieldErr.Field != "name" {
		t.Fatalf("field=%q want name", fieldErr.Field)
	}
}

func TestTUIProfileModalViewDoesNotMutateState(t *testing.T) {
	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:bucket/test"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	modal, err := newTUIProfileModal(profilesPath, "", false)
	if err != nil {
		t.Fatalf("newTUIProfileModal: %v", err)
	}
	before := modal.modal
	_ = modal.View()
	after := modal.modal
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("View mutated modal state")
	}
}

func TestNewTUIProfileModal_AllowsCreatingStoreWhenNoneExist(t *testing.T) {
	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	modal, err := newTUIProfileModal(profilesPath, "", false)
	if err != nil {
		t.Fatalf("newTUIProfileModal: %v", err)
	}
	storeField := modal.fieldByKey("store")
	if storeField == nil {
		t.Fatalf("missing store field")
	}
	if len(storeField.Options) != 1 || storeField.Options[0] != tuiCreateStoreOption {
		t.Fatalf("store options=%v want [%q]", storeField.Options, tuiCreateStoreOption)
	}
}

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
				{
					Name:     "documents",
					Source:   "local:/tmp/Documents",
					StoreRef: "remote",
					Enabled:  true,
					Status:   tui.ProfileStatusReady,
					Actions: []tui.ProfileAction{
						{Kind: tui.ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
						{Kind: tui.ActionKindCheck, Key: "c", Label: "Press c to run repository check", Enabled: true},
					},
				},
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
				{
					Name:        "documents",
					Source:      "local:/tmp/Documents",
					StoreRef:    "remote",
					Enabled:     true,
					Status:      tui.ProfileStatusReady,
					StoreHealth: tui.StoreHealthReady,
					Actions: []tui.ProfileAction{
						{Kind: tui.ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
						{Kind: tui.ActionKindCheck, Key: "c", Label: "Press c to run repository check", Enabled: true},
					},
				},
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

func TestRunTUI_CreateActionUsesModalAndSavesProfile(t *testing.T) {
	stubTUITestHooks(t)

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "tui"}

	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:bucket/test"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"docs": {Source: "local:/docs", Store: "remote"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("nphotos\t\t/photos\rq"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder

	r := &runner{
		out:        &out,
		errOut:     &errOut,
		stdoutFile: os.Stdout,
		stdin:      readEnd,
		lineIn:     bufio.NewReader(readEnd),
	}
	oldEnv := os.Getenv("CLOUDSTIC_PROFILES_FILE")
	t.Cleanup(func() { _ = os.Setenv("CLOUDSTIC_PROFILES_FILE", oldEnv) })
	_ = os.Setenv("CLOUDSTIC_PROFILES_FILE", profilesPath)
	if code := r.runTUI(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if got := cfg.Profiles["photos"].Source; got != "local:/photos" {
		t.Fatalf("saved profile source=%q want local:/photos", got)
	}
	if !strings.Contains(out.String(), "saved \"photos\"") {
		t.Fatalf("expected create activity in output, got:\n%s", out.String())
	}
}

func TestReadTUIAction_ParsesCSIArrowKeys(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[A")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction up: %v", err)
	}
	if ev.Kind != tuiActionUp {
		t.Fatalf("up action=%v want %v", ev.Kind, tuiActionUp)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[B")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction down: %v", err)
	}
	if ev.Kind != tuiActionDown {
		t.Fatalf("down action=%v want %v", ev.Kind, tuiActionDown)
	}
}

func TestReadTUIAction_ParsesParameterizedCSIArrowKeys(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[1;2A")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction param up: %v", err)
	}
	if ev.Kind != tuiActionUp {
		t.Fatalf("param up action=%v want %v", ev.Kind, tuiActionUp)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[1;2B")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction param down: %v", err)
	}
	if ev.Kind != tuiActionDown {
		t.Fatalf("param down action=%v want %v", ev.Kind, tuiActionDown)
	}
}

func TestReadTUIAction_ParsesSS3ArrowKeys(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1bOA")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction ss3 up: %v", err)
	}
	if ev.Kind != tuiActionUp {
		t.Fatalf("ss3 up action=%v want %v", ev.Kind, tuiActionUp)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1bOB")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction ss3 down: %v", err)
	}
	if ev.Kind != tuiActionDown {
		t.Fatalf("ss3 down action=%v want %v", ev.Kind, tuiActionDown)
	}
}

func TestReadTUIAction_ParsesCheckShortcut(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("c")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction check: %v", err)
	}
	if ev.Kind != tuiActionCheck {
		t.Fatalf("check action=%v want %v", ev.Kind, tuiActionCheck)
	}
}

func TestReadTUIAction_ParsesManagementShortcuts(t *testing.T) {
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("n")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction create: %v", err)
	}
	if ev.Kind != tuiActionCreate {
		t.Fatalf("create action=%v want %v", ev.Kind, tuiActionCreate)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("e")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction edit: %v", err)
	}
	if ev.Kind != tuiActionEdit {
		t.Fatalf("edit action=%v want %v", ev.Kind, tuiActionEdit)
	}

	ev, err = readTUIAction(bufio.NewReader(bytes.NewBufferString("d")), tui.DashboardLayout{})
	if err != nil {
		t.Fatalf("readTUIAction delete: %v", err)
	}
	if ev.Kind != tuiActionDelete {
		t.Fatalf("delete action=%v want %v", ev.Kind, tuiActionDelete)
	}
}

func TestReadTUIAction_ParsesProfileClick(t *testing.T) {
	layout := tui.DashboardLayout{
		ProfileRows: map[int]string{8: "photos"},
		ProfileRect: tui.Rect{X: 1, Y: 5, W: 20, H: 6},
	}
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[<0;5;8M")), layout)
	if err != nil {
		t.Fatalf("readTUIAction click: %v", err)
	}
	if ev.Kind != tuiActionSelectProfile {
		t.Fatalf("click action=%v want %v", ev.Kind, tuiActionSelectProfile)
	}
	if ev.Profile != "photos" {
		t.Fatalf("click profile=%q want photos", ev.Profile)
	}
}

func TestReadTUIAction_ParsesActionClick(t *testing.T) {
	layout := tui.DashboardLayout{
		ActionRows: map[int]string{12: "c"},
		ActionRect: tui.Rect{X: 30, Y: 10, W: 40, H: 6},
	}
	ev, err := readTUIAction(bufio.NewReader(bytes.NewBufferString("\x1b[<0;35;12M")), layout)
	if err != nil {
		t.Fatalf("readTUIAction action click: %v", err)
	}
	if ev.Kind != tuiActionCheck {
		t.Fatalf("click action=%v want %v", ev.Kind, tuiActionCheck)
	}
}

func TestReadTUIModalInput_ParsesStandaloneEscape(t *testing.T) {
	ev, err := readTUIModalInput(bufio.NewReader(bytes.NewBufferString("\x1b")))
	if err != nil {
		t.Fatalf("readTUIModalInput escape: %v", err)
	}
	if ev.Kind != tuiModalInputEscape {
		t.Fatalf("escape kind=%v want %v", ev.Kind, tuiModalInputEscape)
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
				{
					Name:       "docs",
					Source:     "local:/docs",
					StoreRef:   "remote",
					Enabled:    true,
					Status:     tui.ProfileStatusReady,
					LastBackup: "2026-04-03 12:00",
					Actions: []tui.ProfileAction{
						{Kind: tui.ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
						{Kind: tui.ActionKindCheck, Key: "c", Label: "Press c to run repository check", Enabled: true},
					},
				},
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
			{
				Name:     "docs",
				Source:   "local:/docs",
				StoreRef: "remote",
				Enabled:  true,
				Status:   tui.ProfileStatusReady,
				Actions: []tui.ProfileAction{
					{Kind: tui.ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
					{Kind: tui.ActionKindCheck, Key: "c", Label: "Press c to run repository check", Enabled: true},
				},
			},
		},
	})

	if _, err := s.handleAction(context.Background(), tuiAction{Kind: tuiActionRun}); err != nil {
		t.Fatalf("handleAction(run): %v", err)
	}
	if s.dashboard.SelectedProfile != "docs" {
		t.Fatalf("selected profile lost after refresh: %+v", s.dashboard)
	}
	if len(s.dashboard.Activity.Lines) == 0 {
		t.Fatalf("expected activity lines after action")
	}
	if s.dashboard.Activity.Status != tui.ActivityStatusSuccess {
		t.Fatalf("unexpected activity status: %+v", s.dashboard.Activity)
	}
	if !strings.Contains(strings.Join(s.dashboard.Activity.Lines, "\n"), "Action completed successfully") {
		t.Fatalf("missing completion activity: %+v", s.dashboard.Activity)
	}
}

func TestTUISession_HandleActionRunRefreshFailureRestoresRawMode(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	}()

	oldBuild := tuiBuildDashboard
	oldAction := tuiRunProfileAction
	oldMakeRaw := tuiMakeRaw
	oldRestore := tuiRestoreTerminal
	oldEnterAlt := tuiEnterAltScreen
	oldLeaveAlt := tuiLeaveAltScreen
	t.Cleanup(func() {
		tuiBuildDashboard = oldBuild
		tuiRunProfileAction = oldAction
		tuiMakeRaw = oldMakeRaw
		tuiRestoreTerminal = oldRestore
		tuiEnterAltScreen = oldEnterAlt
		tuiLeaveAltScreen = oldLeaveAlt
	})

	var madeRaw, restored int
	state := &xterm.State{}
	tuiMakeRaw = func(int) (*xterm.State, error) { madeRaw++; return state, nil }
	tuiRestoreTerminal = func(int, *xterm.State) error { restored++; return nil }
	tuiEnterAltScreen = func(io.Writer) error { return nil }
	tuiLeaveAltScreen = func(io.Writer) error { return nil }

	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return tui.Dashboard{}, errors.New("boom")
	}
	tuiRunProfileAction = func(_ context.Context, _ *runner, _ string, _ tui.ProfileCard, log *tuiActionState) error {
		log.Printf("backup complete")
		return nil
	}

	s := newTUISession(&runner{out: io.Discard, stdoutFile: os.Stdout, stdin: readEnd}, "profiles.yaml", tui.Dashboard{
		SelectedProfile: "docs",
		Profiles: []tui.ProfileCard{
			{
				Name:     "docs",
				Source:   "local:/docs",
				StoreRef: "remote",
				Enabled:  true,
				Status:   tui.ProfileStatusReady,
				Actions: []tui.ProfileAction{
					{Kind: tui.ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
				},
			},
		},
	})
	s.rawState = state

	if _, err := s.handleAction(context.Background(), tuiAction{Kind: tuiActionRun}); err == nil {
		t.Fatalf("expected refresh failure")
	}
	if madeRaw != 1 || restored != 1 {
		t.Fatalf("unexpected raw lifecycle counts: make=%d restore=%d", madeRaw, restored)
	}
	if s.rawState != state {
		t.Fatalf("raw state not restored after refresh failure")
	}
}

func TestTUISession_HandleActionCreateRefreshesDashboard(t *testing.T) {
	stubTUITestHooks(t)

	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })

	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:bucket/test"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"docs": {Source: "local:/docs", Store: "remote"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return defaultBuildTUIDashboard(context.Background(), profilesPath)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("photos\t\t/photos\r"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	var out strings.Builder
	s := newTUISession(&runner{out: &out, stdoutFile: os.Stdout, stdin: readEnd, lineIn: bufio.NewReader(readEnd)}, profilesPath, tui.Dashboard{})
	if _, err := s.handleAction(context.Background(), tuiAction{Kind: tuiActionCreate}); err != nil {
		t.Fatalf("handleAction(create): %v", err)
	}
	if s.dashboard.SelectedProfile != "photos" {
		t.Fatalf("selected profile=%q want photos", s.dashboard.SelectedProfile)
	}
	if s.dashboard.Activity.Status != tui.ActivityStatusSuccess {
		t.Fatalf("unexpected activity: %+v", s.dashboard.Activity)
	}
}

func TestTUISession_HandleActionCreateCanCreateStoreInline(t *testing.T) {
	stubTUITestHooks(t)

	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })

	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return defaultBuildTUIDashboard(context.Background(), profilesPath)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("photos\t\t/photos\t\rbackup-store\t\t/backups\r\r"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	s := newTUISession(&runner{out: io.Discard, stdoutFile: os.Stdout, stdin: readEnd, lineIn: bufio.NewReader(readEnd)}, profilesPath, tui.Dashboard{})
	if _, err := s.handleAction(context.Background(), tuiAction{Kind: tuiActionCreate}); err != nil {
		t.Fatalf("handleAction(create): %v", err)
	}
	cfg, err := cloudstic.LoadProfilesFile(profilesPath)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	if got := cfg.Stores["backup-store"].URI; got != "local:/backups" {
		t.Fatalf("saved store uri=%q want local:/backups", got)
	}
	if got := cfg.Profiles["photos"].Store; got != "backup-store" {
		t.Fatalf("saved profile store=%q want backup-store", got)
	}
}

func TestTUISession_HandleActionDeleteRefreshesDashboard(t *testing.T) {
	stubTUITestHooks(t)

	oldBuild := tuiBuildDashboard
	t.Cleanup(func() { tuiBuildDashboard = oldBuild })

	dir := t.TempDir()
	profilesPath := dir + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {URI: "s3:bucket/test"},
		},
		Profiles: map[string]cloudstic.BackupProfile{
			"docs":   {Source: "local:/docs", Store: "remote"},
			"photos": {Source: "local:/photos", Store: "remote"},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	tuiBuildDashboard = func(context.Context, string) (tui.Dashboard, error) {
		return defaultBuildTUIDashboard(context.Background(), profilesPath)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	if _, err := writeEnd.WriteString("\r"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = writeEnd.Close()

	s := newTUISession(&runner{out: io.Discard, stdoutFile: os.Stdout, stdin: os.Stdin}, "profiles.yaml", tui.Dashboard{
		SelectedProfile: "docs",
		Profiles:        []tui.ProfileCard{{Name: "docs", Source: "local:/docs", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady}},
	})
	s.r.stdin = readEnd
	s.r.lineIn = bufio.NewReader(readEnd)
	s.profilesFile = profilesPath
	if _, err := s.handleAction(context.Background(), tuiAction{Kind: tuiActionDelete}); err != nil {
		t.Fatalf("handleAction(delete): %v", err)
	}
	if s.dashboard.SelectedProfile != "photos" {
		t.Fatalf("selected profile=%q want photos", s.dashboard.SelectedProfile)
	}
	if s.dashboard.Activity.Status != tui.ActivityStatusSuccess {
		t.Fatalf("unexpected activity: %+v", s.dashboard.Activity)
	}
}

func TestTUISession_HandleActionSelectProfileRefreshesSelection(t *testing.T) {
	stubTUITestHooks(t)

	s := newTUISession(&runner{out: io.Discard, stdoutFile: os.Stdout, stdin: os.Stdin}, "profiles.yaml", tui.Dashboard{
		SelectedProfile: "docs",
		Profiles: []tui.ProfileCard{
			{Name: "docs", Source: "local:/docs", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
			{Name: "photos", Source: "local:/photos", StoreRef: "remote", Enabled: true, Status: tui.ProfileStatusReady},
		},
	})
	if _, err := s.handleAction(context.Background(), tuiAction{Kind: tuiActionSelectProfile, Profile: "photos"}); err != nil {
		t.Fatalf("handleAction(select): %v", err)
	}
	if s.dashboard.SelectedProfile != "photos" {
		t.Fatalf("selected profile=%q want photos", s.dashboard.SelectedProfile)
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
		Activity:        tui.ActivityPanel{Status: tui.ActivityStatusRunning, Lines: []string{"running"}},
		Profiles:        []tui.ProfileCard{{Name: "docs"}},
	})
	if err := s.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if s.dashboard.SelectedProfile != "docs" {
		t.Fatalf("selection not preserved: %+v", s.dashboard)
	}
	if len(s.dashboard.Activity.Lines) != 1 || s.dashboard.Activity.Lines[0] != "running" {
		t.Fatalf("activity not preserved: %+v", s.dashboard.Activity)
	}
}

func TestRunTUIActionIntoDashboard_RedrawsUsingCurrentWidthDuringLongAction(t *testing.T) {
	stubTUITestHooks(t)

	oldWidth := tuiGetTerminalSize
	oldAction := tuiRunProfileAction
	t.Cleanup(func() {
		tuiGetTerminalSize = oldWidth
		tuiRunProfileAction = oldAction
	})

	var widthCalls int
	tuiGetTerminalSize = func(int) (int, int, error) {
		widthCalls++
		if widthCalls == 1 {
			return 120, 40, nil
		}
		return 72, 40, nil
	}
	tuiRunProfileAction = func(_ context.Context, _ *runner, _ string, _ tui.ProfileCard, log *tuiActionState) error {
		phase := log.Reporter().StartPhase("Uploading", 4, false)
		time.Sleep(120 * time.Millisecond)
		phase.Increment(2)
		time.Sleep(120 * time.Millisecond)
		phase.Increment(2)
		phase.Done()
		return nil
	}

	var out strings.Builder
	dashboard := tui.Dashboard{
		SelectedProfile: "docs",
		Profiles: []tui.ProfileCard{
			{
				Name:     "docs",
				Source:   "local:/docs",
				StoreRef: "remote",
				Enabled:  true,
				Status:   tui.ProfileStatusReady,
				Actions: []tui.ProfileAction{
					{Kind: tui.ActionKindBackup, Key: "b", Label: "Press b to run backup", Enabled: true},
				},
			},
		},
	}
	result := runTUIActionIntoDashboard(context.Background(), &runner{out: &out, stdoutFile: os.Stdout}, "profiles.yaml", dashboard)
	if widthCalls < 2 {
		t.Fatalf("expected multiple width polls during long action, got %d", widthCalls)
	}
	if result.Activity.Status != tui.ActivityStatusSuccess {
		t.Fatalf("unexpected activity status: %+v", result.Activity)
	}
	if !strings.Contains(out.String(), "Progress") {
		t.Fatalf("expected live renders with progress, got:\n%s", out.String())
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
	readPermitCh := make(chan tui.DashboardLayout, 1)
	eventCh := make(chan tuiAction, 2)
	errCh := make(chan error, 1)
	close(readPermitCh)
	s.readInput(readPermitCh, eventCh, errCh)

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
