package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/tui"
)

func renderTUIScreenWidth(w io.Writer, dashboard tui.Dashboard, width int) error {
	if _, err := fmt.Fprint(w, "\x1b[2J\x1b[H"); err != nil {
		return err
	}
	return tui.RenderDashboardWidth(newCRLFWriter(w), dashboard, width)
}

func runTUIActionIntoDashboard(ctx context.Context, r *runner, profilesFile string, dashboard tui.Dashboard) tui.Dashboard {
	log := newTUIActionState(10)
	screen := r.out
	if profile, ok := selectedTUIProfile(dashboard); ok {
		if profileNeedsInit(profile) {
			log.Printf("Initializing store for profile %s", profile.Name)
		} else {
			log.Printf("Running backup for profile %s", profile.Name)
		}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				live := dashboard
				live.ActivityLines = log.Lines()
				_ = renderTUIScreenWidth(screen, live, tuiWidth(r))
			}
		}
	}()

	if err := runSelectedTUIAction(ctx, r, profilesFile, dashboard, log); err != nil {
		log.Printf("Action failed: %v", err)
	} else {
		log.Printf("Action completed successfully")
	}
	close(stop)
	<-done

	dashboard.ActivityLines = mergeTUIActivityLines(dashboard.ActivityLines, log.Lines())
	return dashboard
}

func runTUICheckIntoDashboard(ctx context.Context, r *runner, profilesFile string, dashboard tui.Dashboard) tui.Dashboard {
	log := newTUIActionState(10)
	screen := r.out
	if profile, ok := selectedTUIProfile(dashboard); ok {
		log.Printf("Running repository check for profile %s", profile.Name)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				live := dashboard
				live.ActivityLines = log.Lines()
				_ = renderTUIScreenWidth(screen, live, tuiWidth(r))
			}
		}
	}()

	if err := runSelectedTUICheck(ctx, r, profilesFile, dashboard, log); err != nil {
		log.Printf("Check failed: %v", err)
	} else {
		log.Printf("Check completed successfully")
	}
	close(stop)
	<-done

	dashboard.ActivityLines = mergeTUIActivityLines(dashboard.ActivityLines, log.Lines())
	return dashboard
}

func mergeTUIActivityLines(existing, recent []string) []string {
	merged := append([]string{}, recent...)
	merged = append(merged, existing...)
	if len(merged) > 10 {
		merged = merged[:10]
	}
	return merged
}

type crlfWriter struct {
	w io.Writer
}

func newCRLFWriter(w io.Writer) io.Writer {
	return crlfWriter{w: w}
}

func (w crlfWriter) Write(p []byte) (int, error) {
	s := strings.ReplaceAll(string(p), "\n", "\r\n")
	if _, err := io.WriteString(w.w, s); err != nil {
		return 0, err
	}
	return len(p), nil
}

func captureTUIRunnerOutput(r *runner, log *tuiActionState) func() {
	oldOut := r.out
	oldErrOut := r.errOut
	oldNoPrompt := r.noPrompt
	r.out = log.Writer()
	r.errOut = log.Writer()
	r.noPrompt = true
	return func() {
		r.out = oldOut
		r.errOut = oldErrOut
		r.noPrompt = oldNoPrompt
	}
}

type tuiActionState struct {
	mu    sync.Mutex
	lines []string
	limit int
	buf   bytes.Buffer
	phase *tuiPhaseState
}

type tuiPhaseState struct {
	name    string
	current int64
	total   int64
	isBytes bool
	state   string
}

func newTUIActionState(limit int) *tuiActionState {
	return &tuiActionState{limit: limit}
}

func (l *tuiActionState) Writer() io.Writer {
	return l
}

func (l *tuiActionState) Reporter() cloudstic.Reporter {
	return tuiReporter{state: l}
}

func (l *tuiActionState) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			break
		}
		l.append(strings.TrimSpace(line))
	}
	return len(p), nil
}

func (l *tuiActionState) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.append(fmt.Sprintf(format, args...))
}

func (l *tuiActionState) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if tail := strings.TrimSpace(l.buf.String()); tail != "" {
		l.append(tail)
		l.buf.Reset()
	}
	lines := append([]string{}, l.lines...)
	if summary := l.phaseSummary(); summary != "" {
		lines = append([]string{summary}, lines...)
	}
	return lines
}

func (l *tuiActionState) append(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	l.lines = append([]string{line}, l.lines...)
	if len(l.lines) > l.limit {
		l.lines = l.lines[:l.limit]
	}
}

func (l *tuiActionState) phaseSummary() string {
	if l.phase == nil || l.phase.name == "" {
		return ""
	}
	switch {
	case l.phase.total > 0 && l.phase.isBytes:
		return fmt.Sprintf("%s %s / %s", l.phase.name, formatBytes(l.phase.current), formatBytes(l.phase.total))
	case l.phase.total > 0:
		return fmt.Sprintf("%s %d / %d", l.phase.name, l.phase.current, l.phase.total)
	default:
		return l.phase.name
	}
}

type tuiReporter struct {
	state *tuiActionState
}

func (r tuiReporter) StartPhase(name string, total int64, isBytes bool) cloudstic.Phase {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.phase = &tuiPhaseState{name: name, total: total, isBytes: isBytes, state: "active"}
	return tuiReporterPhase(r)
}

type tuiReporterPhase struct {
	state *tuiActionState
}

func (p tuiReporterPhase) Increment(n int64) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if p.state.phase != nil {
		p.state.phase.current += n
	}
}

func (p tuiReporterPhase) Log(msg string) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	p.state.append(msg)
}

func (p tuiReporterPhase) Done() {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if p.state.phase != nil {
		p.state.phase.state = "done"
	}
}

func (p tuiReporterPhase) Error() {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if p.state.phase != nil {
		p.state.phase.state = "error"
	}
}

func tuiStoreFlags(profilesFile string, storeCfg cloudstic.ProfileStore) *globalFlags {
	fs := flag.NewFlagSet("tui-store", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	*g.profilesFile = profilesFile
	flagsSet := map[string]bool{}
	_ = applyProfileStoreToGlobalFlags(g, storeCfg, flagsSet)
	quiet := true
	debug := false
	verbose := false
	g.quiet = &quiet
	g.debug = &debug
	g.verbose = &verbose
	return g
}
