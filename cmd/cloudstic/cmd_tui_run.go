package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cloudstic/cli/pkg/config"
	"github.com/cloudstic/cli/pkg/profile"

	tea "github.com/charmbracelet/bubbletea"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/app"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/tui"
)

// tuiRunProgram is overridable in tests so the launcher can be exercised
// without a real terminal program.
var tuiRunProgram = func(ctx context.Context, model tea.Model, opts ...tea.ProgramOption) error {
	_, err := tea.NewProgram(model, append(opts, tea.WithContext(ctx))...).Run()
	return err
}

// tuiLoadConfig loads the profiles config for the dashboard. It is a package
// var so tests can supply a fixed config.
var tuiLoadConfig = func(profilesFile string) (*profile.Config, error) {
	return app.NewTUIService(nil).LoadDashboardConfig(profilesFile)
}

var tuiServiceFactory = defaultTUIServiceFactory

func defaultTUIServiceFactory(r *runner, profilesFile, configDir string) *app.TUIService {
	return app.NewTUIService(tuiCLIBackend{r: r, profilesFile: profilesFile, configDir: configDir})
}

// runTUIProgram launches the dashboard. It builds a probe-less dashboard
// skeleton and lets the model probe stores concurrently rather than probing
// serially before the first frame. Bubble Tea owns the terminal: raw mode, the
// alternate screen, mouse tracking, and resize handling are all cross-platform
// (issue #341).
func runTUIProgram(r *runner, ctx context.Context, profilesFile, configDir string) int {
	cfg, err := tuiLoadConfig(profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}

	skeleton := tui.BuildDashboard(cfg, nil)
	model := tui.NewModel(skeleton).
		WithConfig(cfg, tuiStoreProber{r: r, configDir: configDir}).
		WithRunner(tuiActionRunner{r: r, profilesFile: profilesFile, configDir: configDir}).
		WithForms(newTUIFormsBackend(r, profilesFile, configDir, cfg))

	stdin := r.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	opts := []tea.ProgramOption{
		tea.WithInput(stdin),
		tea.WithOutput(r.out),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	}
	if err := tuiRunProgram(ctx, model, opts...); err != nil {
		return r.fail("TUI exited with error: %v", err)
	}
	return 0
}

// tuiCLIBackend implements app.TUIBackend by reusing the CLI's own command
// paths, so a dashboard action runs exactly what the equivalent command runs.
type tuiCLIBackend struct {
	r            *runner
	profilesFile string
	configDir    string
}

func (b tuiCLIBackend) LoadStoreSnapshots(ctx context.Context, storeName string, storeCfg profile.Store) ([]engine.SnapshotEntry, error) {
	cfg, err := tuiClientConfig(storeCfg, b.configDir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", storeName, err)
	}
	client, err := openClient(ctx, cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", storeName, err)
	}
	result, err := client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", storeName, err)
	}
	return result.Snapshots, nil
}

func (b tuiCLIBackend) InitProfile(ctx context.Context, profilesFile, profileName string, cfg *profile.Config) error {
	storeCfg, err := cfg.StoreFor(profileName)
	if err != nil {
		return err
	}
	if storeCfg == nil {
		return fmt.Errorf("profile %q names no store to initialize", profileName)
	}
	resolved, err := tuiClientConfig(*storeCfg, b.configDir)
	if err != nil {
		return err
	}
	resolved.Quiet = false
	if code := execInit(b.r, ctx, &initArgs{globalFlags: &globalFlags{}}, resolved); code != 0 {
		return fmt.Errorf("init failed")
	}
	return nil
}

func (b tuiCLIBackend) BackupProfile(ctx context.Context, profilesFile, profileName string, cfg *profile.Config, reporter cloudstic.Reporter) error {
	g := &globalFlags{profile: profileName, profilesFile: profilesFile, configDir: b.configDir, quiet: true}
	base := &backupArgs{globalFlags: g}
	bcfg, err := config.MergeProfileBackup(backupConfigFromFlags(base), nil, profileName, cfg)
	if err != nil {
		return err
	}
	resolved, err := resolveClientConfig(g)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	client, err := openClient(ctx, resolved, reporter)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	b.r.client = client
	defer func() { b.r.client = nil }()
	if code := execBackup(b.r, ctx, base, bcfg); code != 0 {
		return fmt.Errorf("backup failed")
	}
	return nil
}

func (b tuiCLIBackend) CheckProfile(ctx context.Context, profilesFile, profileName string, cfg *profile.Config, reporter cloudstic.Reporter) error {
	storeCfg, err := cfg.StoreFor(profileName)
	if err != nil {
		return err
	}
	if storeCfg == nil {
		return fmt.Errorf("profile %q names no store to check", profileName)
	}
	resolved, err := tuiClientConfig(*storeCfg, b.configDir)
	if err != nil {
		return err
	}
	client, err := openClient(ctx, resolved, reporter)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	result, err := client.Check(ctx, cloudstic.WithSnapshotRef("latest"))
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}
	if printCheckResult(b.r.errOut, result) {
		return fmt.Errorf("repository check reported errors")
	}
	return nil
}

// tuiStoreProber probes a single store's health by listing its snapshots,
// bounded by the per-store timeout the model applies.
type tuiStoreProber struct {
	r         *runner
	configDir string
}

func (p tuiStoreProber) Probe(ctx context.Context, name string, store profile.Store) tui.StoreProbe {
	snapshots, err := (tuiCLIBackend{r: p.r, configDir: p.configDir}).LoadStoreSnapshots(ctx, name, store)
	if err != nil {
		return tui.StoreProbe{Status: "error", Error: err.Error()}
	}
	return tui.StoreProbe{Status: "ok", Snapshots: snapshots}
}

// tuiActionRunner adapts the shared TUIService action methods to the model's
// streaming ActionRunner contract. It reuses tuiActionState so progress and log
// lines feed the activity panel.
type tuiActionRunner struct {
	r            *runner
	profilesFile string
	configDir    string
}

func (a tuiActionRunner) Start(ctx context.Context, profile tui.ProfileCard, kind tui.ActionKind) <-chan tui.ActionUpdate {
	ch := make(chan tui.ActionUpdate, 32)
	log := newTUIActionState(10)
	seedActionLabel(log, profile, kind)

	go func() {
		defer close(ch)

		restore := captureTUIRunnerOutput(a.r, log)
		defer restore()

		// Stream periodic progress snapshots until the action returns.
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					ch <- tui.ActionUpdate{Panel: log.Snapshot()}
				}
			}
		})

		service := tuiServiceFactory(a.r, a.profilesFile, a.configDir)
		var err error
		if kind == tui.ActionKindCheck {
			err = service.RunProfileCheck(ctx, a.profilesFile, profile, log.Reporter())
		} else {
			err = service.RunProfileAction(ctx, a.profilesFile, profile, log.Reporter())
		}

		close(stop)
		wg.Wait()

		if err != nil {
			log.Fail(err.Error())
			log.Printf("Action failed: %v", err)
		} else {
			log.Succeed("completed successfully")
			log.Printf("Action completed successfully")
		}
		ch <- tui.ActionUpdate{Panel: log.Snapshot(), Done: true, Err: err}
	}()

	return ch
}

func seedActionLabel(log *tuiActionState, profile tui.ProfileCard, kind tui.ActionKind) {
	target := fmt.Sprintf("profile %s", profile.Name)
	switch kind {
	case tui.ActionKindCheck:
		log.Start("Run repository check", target)
		log.Printf("Running repository check for profile %s", profile.Name)
	case tui.ActionKindInit:
		log.Start("Initialize store", target)
		log.Printf("Initializing store for profile %s", profile.Name)
	default:
		log.Start("Run backup", target)
		log.Printf("Running backup for profile %s", profile.Name)
	}
}
