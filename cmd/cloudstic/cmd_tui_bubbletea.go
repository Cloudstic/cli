package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/app"
	"github.com/cloudstic/cli/internal/tui"
)

// tuiRunBubbleTea is overridable in tests so the launcher can be exercised
// without a real terminal program.
var tuiRunBubbleTea = func(ctx context.Context, model tea.Model, opts ...tea.ProgramOption) error {
	_, err := tea.NewProgram(model, append(opts, tea.WithContext(ctx))...).Run()
	return err
}

// tuiLoadConfig loads the profiles config for the Bubble Tea renderer. It is a
// package var so tests can supply a fixed config.
var tuiLoadConfig = func(profilesFile string) (*cloudstic.ProfilesConfig, error) {
	return app.NewTUIService(nil).LoadDashboardConfig(profilesFile)
}

// runBubbleTeaTUI launches the experimental Bubble Tea renderer. It builds a
// probe-less dashboard skeleton and lets the model probe stores concurrently
// (issue #339) rather than probing serially before the first frame.
func runBubbleTeaTUI(r *runner, ctx context.Context, profilesFile string) int {
	cfg, err := tuiLoadConfig(profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}

	skeleton := tui.BuildDashboard(cfg, nil)
	model := tui.NewModel(skeleton).
		WithConfig(cfg, bubbleStoreProber{r: r}).
		WithRunner(bubbleActionRunner{r: r, profilesFile: profilesFile}).
		WithForms(newBubbleFormsBackend(r, profilesFile, cfg))

	stdin := r.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	opts := []tea.ProgramOption{
		tea.WithInput(stdin),
		tea.WithOutput(r.out),
		tea.WithAltScreen(),
	}
	if err := tuiRunBubbleTea(ctx, model, opts...); err != nil {
		return r.fail("TUI exited with error: %v", err)
	}
	return 0
}

// bubbleStoreProber probes a single store's health by listing its snapshots,
// the same signal the serial dashboard builder used, but now bounded by the
// per-store timeout the model applies.
type bubbleStoreProber struct {
	r *runner
}

func (p bubbleStoreProber) Probe(ctx context.Context, name string, store cloudstic.ProfileStore) tui.StoreProbe {
	snapshots, err := (tuiCLIBackend{r: p.r}).LoadStoreSnapshots(ctx, name, store)
	if err != nil {
		return tui.StoreProbe{Status: "error", Error: err.Error()}
	}
	return tui.StoreProbe{Status: "ok", Snapshots: snapshots}
}

// bubbleActionRunner adapts the shared TUIService action methods to the model's
// streaming ActionRunner contract. It reuses tuiActionState so progress and log
// lines match the legacy renderer's activity panel.
type bubbleActionRunner struct {
	r            *runner
	profilesFile string
}

func (a bubbleActionRunner) Start(ctx context.Context, profile tui.ProfileCard, kind tui.ActionKind) <-chan tui.ActionUpdate {
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

		service := tuiServiceFactory(a.r, a.profilesFile, log)
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
