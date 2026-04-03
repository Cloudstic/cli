package main

import (
	"context"
	"fmt"
	"io"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/app"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui"
	xterm "golang.org/x/term"
)

var (
	tuiServiceFactory   = defaultTUIServiceFactory
	tuiBuildDashboard   = defaultBuildTUIDashboard
	tuiRunProfileAction = defaultRunTUIProfileAction
	tuiRunProfileCheck  = defaultRunTUIProfileCheck
	tuiMakeRaw          = xterm.MakeRaw
	tuiRestoreTerminal  = xterm.Restore
	tuiGetTerminalSize  = xterm.GetSize
	tuiEnterAltScreen   = defaultEnterAltScreen
	tuiLeaveAltScreen   = defaultLeaveAltScreen
)

type tuiCLIBackend struct {
	r            *runner
	profilesFile string
}

func (b tuiCLIBackend) LoadStoreSnapshots(ctx context.Context, storeName string, storeCfg cloudstic.ProfileStore) ([]engine.SnapshotEntry, error) {
	g := tuiStoreFlags(b.profilesFile, storeCfg)
	client, err := g.openClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", storeName, err)
	}
	result, err := client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", storeName, err)
	}
	return result.Snapshots, nil
}

func (b tuiCLIBackend) InitProfile(ctx context.Context, profilesFile, profileName string, profileCfg cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig) error {
	storeCfg, ok := cfg.Stores[profileCfg.Store]
	if !ok {
		return fmt.Errorf("profile %q references unknown store %q", profileName, profileCfg.Store)
	}
	g := tuiStoreFlags(profilesFile, storeCfg)
	*g.quiet = false
	if code := b.r.runInitWithArgs(ctx, &initArgs{g: g}); code != 0 {
		return fmt.Errorf("init failed")
	}
	return nil
}

func (b tuiCLIBackend) BackupProfile(ctx context.Context, profilesFile, profileName string, profileCfg cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig, reporter cloudstic.Reporter) error {
	base := &backupArgs{
		g:            tuiStoreFlags(profilesFile, cloudstic.ProfileStore{}),
		profile:      profileName,
		profilesFile: profilesFile,
		flagsSet:     map[string]bool{},
	}
	*base.g.profilesFile = profilesFile
	effective, err := mergeProfileBackupArgs(base, profileName, profileCfg, cfg)
	if err != nil {
		return err
	}
	client, err := effective.g.openClientWithReporter(ctx, reporter)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	b.r.client = client
	defer func() { b.r.client = nil }()
	if code := b.r.runSingleBackup(effective); code != 0 {
		return fmt.Errorf("backup failed")
	}
	return nil
}

func (b tuiCLIBackend) CheckProfile(ctx context.Context, profilesFile, profileName string, profileCfg cloudstic.BackupProfile, cfg *cloudstic.ProfilesConfig, reporter cloudstic.Reporter) error {
	storeCfg, ok := cfg.Stores[profileCfg.Store]
	if !ok {
		return fmt.Errorf("profile %q references unknown store %q", profileName, profileCfg.Store)
	}
	g := tuiStoreFlags(profilesFile, storeCfg)
	client, err := g.openClientWithReporter(ctx, reporter)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	result, err := client.Check(ctx, cloudstic.WithSnapshotRef("latest"))
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}
	if b.r.printCheckResult(result) {
		return fmt.Errorf("repository check reported errors")
	}
	return nil
}

func defaultEnterAltScreen(w io.Writer) error {
	_, err := fmt.Fprint(w, "\x1b[?1049h\x1b[?1007h\x1b[2J\x1b[H\x1b[?25l")
	return err
}

func defaultLeaveAltScreen(w io.Writer) error {
	_, err := fmt.Fprint(w, "\x1b[?25h\x1b[?1007l\x1b[?1049l")
	return err
}

func defaultTUIServiceFactory(r *runner, profilesFile string, log *tuiActionState) *app.TUIService {
	return app.NewTUIService(tuiCLIBackend{r: r, profilesFile: profilesFile})
}

func defaultBuildTUIDashboard(ctx context.Context, profilesFile string) (tui.Dashboard, error) {
	return tuiServiceFactory(nil, profilesFile, nil).BuildDashboard(ctx, profilesFile)
}

func defaultRunTUIProfileAction(ctx context.Context, r *runner, profilesFile string, profile tui.ProfileCard, log *tuiActionState) error {
	restoreOutput := captureTUIRunnerOutput(r, log)
	defer restoreOutput()
	return tuiServiceFactory(r, profilesFile, log).RunProfileAction(ctx, profilesFile, profile, log.Reporter())
}

func defaultRunTUIProfileCheck(ctx context.Context, r *runner, profilesFile string, profile tui.ProfileCard, log *tuiActionState) error {
	restoreOutput := captureTUIRunnerOutput(r, log)
	defer restoreOutput()
	return tuiServiceFactory(r, profilesFile, log).RunProfileCheck(ctx, profilesFile, profile, log.Reporter())
}

type tuiSession struct {
	r            *runner
	profilesFile string
	dashboard    tui.Dashboard
	stdin        *os.File
	stdinFD      int
	rawState     *xterm.State
	rawActive    bool
}

func newTUISession(r *runner, profilesFile string, dashboard tui.Dashboard) *tuiSession {
	stdin := r.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	return &tuiSession{
		r:            r,
		profilesFile: profilesFile,
		dashboard:    ensureSelectedProfile(dashboard),
		stdin:        stdin,
		stdinFD:      int(stdin.Fd()),
		rawActive:    tuiMakeRaw != nil && tuiRestoreTerminal != nil,
	}
}

func (s *tuiSession) run(ctx context.Context) int {
	if err := s.enter(); err != nil {
		return s.r.fail("Failed to enter TUI screen: %v", err)
	}
	defer s.leave()

	if err := s.render(); err != nil {
		return s.r.fail("Failed to render TUI: %v", err)
	}

	eventCh := make(chan tuiAction, 32)
	readErrCh := make(chan error, 1)
	go s.readInput(eventCh, readErrCh)

	resizeCh := make(chan os.Signal, 1)
	tuiNotifyResize(resizeCh)
	defer tuiStopResize(resizeCh)

	for {
		select {
		case <-ctx.Done():
			return 0
		case <-resizeCh:
			if err := s.render(); err != nil {
				return s.r.fail("Failed to render TUI: %v", err)
			}
		case readErr := <-readErrCh:
			if readErr != nil {
				return 0
			}
		case action, ok := <-eventCh:
			if !ok {
				return 0
			}
			code, err := s.handleAction(ctx, action)
			if err != nil {
				return s.r.fail("%v", err)
			}
			if code >= 0 {
				return code
			}
		}
	}
}

func (s *tuiSession) enter() error {
	if tuiEnterAltScreen != nil {
		if err := tuiEnterAltScreen(s.r.out); err != nil {
			return err
		}
	}
	if !s.rawActive {
		return nil
	}
	state, err := tuiMakeRaw(s.stdinFD)
	if err != nil {
		return err
	}
	s.rawState = state
	return nil
}

func (s *tuiSession) leave() {
	if s.rawActive && s.rawState != nil {
		_ = tuiRestoreTerminal(s.stdinFD, s.rawState)
		s.rawState = nil
	}
	if tuiLeaveAltScreen != nil {
		_ = tuiLeaveAltScreen(s.r.out)
	}
}

func (s *tuiSession) suspendRaw() error {
	if s.rawActive && s.rawState != nil {
		if err := tuiRestoreTerminal(s.stdinFD, s.rawState); err != nil {
			return err
		}
		s.rawState = nil
	}
	return nil
}

func (s *tuiSession) resumeRaw() error {
	if !s.rawActive || s.rawState != nil {
		return nil
	}
	state, err := tuiMakeRaw(s.stdinFD)
	if err != nil {
		return err
	}
	s.rawState = state
	return nil
}

func (s *tuiSession) render() error {
	return renderTUIScreenWidth(s.r.out, s.dashboard, tuiWidth(s.r))
}

func (s *tuiSession) readInput(eventCh chan<- tuiAction, readErrCh chan<- error) {
	defer close(eventCh)
	for {
		event, err := readTUIAction(s.r.lineReader())
		if err != nil {
			if err != io.EOF {
				readErrCh <- err
			}
			return
		}
		eventCh <- event
	}
}

func (s *tuiSession) handleAction(ctx context.Context, action tuiAction) (int, error) {
	switch action {
	case tuiActionQuit:
		return 0, nil
	case tuiActionUp:
		s.dashboard = moveTUISelection(s.dashboard, -1)
	case tuiActionDown:
		s.dashboard = moveTUISelection(s.dashboard, 1)
	case tuiActionRun:
		if err := s.suspendRaw(); err != nil {
			return -1, fmt.Errorf("failed to configure terminal: %v", err)
		}
		s.dashboard = runTUIActionIntoDashboard(ctx, s.r, s.profilesFile, s.dashboard)
		if err := s.refresh(ctx); err != nil {
			return -1, fmt.Errorf("failed to refresh TUI dashboard: %v", err)
		}
		if err := s.resumeRaw(); err != nil {
			return -1, fmt.Errorf("failed to configure terminal: %v", err)
		}
	case tuiActionCheck:
		if err := s.suspendRaw(); err != nil {
			return -1, fmt.Errorf("failed to configure terminal: %v", err)
		}
		s.dashboard = runTUICheckIntoDashboard(ctx, s.r, s.profilesFile, s.dashboard)
		if err := s.refresh(ctx); err != nil {
			return -1, fmt.Errorf("failed to refresh TUI dashboard: %v", err)
		}
		if err := s.resumeRaw(); err != nil {
			return -1, fmt.Errorf("failed to configure terminal: %v", err)
		}
	default:
		return -1, nil
	}
	return -1, s.render()
}

func (s *tuiSession) refresh(ctx context.Context) error {
	selected := s.dashboard.SelectedProfile
	activity := append([]string{}, s.dashboard.ActivityLines...)
	dashboard, err := tuiBuildDashboard(ctx, s.profilesFile)
	if err != nil {
		return err
	}
	dashboard.SelectedProfile = selected
	dashboard.ActivityLines = activity
	s.dashboard = ensureSelectedProfile(dashboard)
	return nil
}
