package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cloudstic/cli/internal/tui"
)

type tuiActionKind int

const (
	tuiActionNone tuiActionKind = iota
	tuiActionUp
	tuiActionDown
	tuiActionRun
	tuiActionCheck
	tuiActionCreate
	tuiActionEdit
	tuiActionDelete
	tuiActionSelectProfile
	tuiActionQuit
)

type tuiAction struct {
	Kind    tuiActionKind
	Profile string
}

func ensureSelectedProfile(d tui.Dashboard) tui.Dashboard {
	if d.SelectedProfile != "" || len(d.Profiles) == 0 {
		return d
	}
	d.SelectedProfile = d.Profiles[0].Name
	return d
}

func tuiWidth(r *runner) int {
	stdout := r.stdoutFile
	if stdout == nil {
		stdout = os.Stdout
	}
	if tuiGetTerminalSize == nil {
		return 100
	}
	width, _, err := tuiGetTerminalSize(int(stdout.Fd()))
	if err != nil || width <= 0 {
		return 100
	}
	return width
}

func moveTUISelection(d tui.Dashboard, delta int) tui.Dashboard {
	if len(d.Profiles) == 0 || delta == 0 {
		return d
	}
	current := 0
	for i, profile := range d.Profiles {
		if profile.Name == d.SelectedProfile {
			current = i
			break
		}
	}
	next := current + delta
	if next < 0 {
		next = len(d.Profiles) - 1
	}
	if next >= len(d.Profiles) {
		next = 0
	}
	d.SelectedProfile = d.Profiles[next].Name
	return d
}

func readTUIAction(r io.ByteReader, layout tui.DashboardLayout) (tuiAction, error) {
	b, err := r.ReadByte()
	if err != nil {
		return tuiAction{}, err
	}
	switch b {
	case 'q', 'Q':
		return tuiAction{Kind: tuiActionQuit}, nil
	case 'j', 'J':
		return tuiAction{Kind: tuiActionDown}, nil
	case 'k', 'K':
		return tuiAction{Kind: tuiActionUp}, nil
	case 'b', 'B':
		return tuiAction{Kind: tuiActionRun}, nil
	case 'c', 'C':
		return tuiAction{Kind: tuiActionCheck}, nil
	case 'n', 'N':
		return tuiAction{Kind: tuiActionCreate}, nil
	case 'e', 'E':
		return tuiAction{Kind: tuiActionEdit}, nil
	case 'd', 'D':
		return tuiAction{Kind: tuiActionDelete}, nil
	case 0x1b:
		next, err := r.ReadByte()
		if err != nil {
			return tuiAction{}, nil
		}
		if next == 'O' {
			dir, err := r.ReadByte()
			if err != nil {
				return tuiAction{}, nil
			}
			switch dir {
			case 'A':
				return tuiAction{Kind: tuiActionUp}, nil
			case 'B':
				return tuiAction{Kind: tuiActionDown}, nil
			default:
				return tuiAction{}, nil
			}
		}
		if next != '[' {
			return tuiAction{}, nil
		}
		csi, err := readTUICSISequence(r)
		if err != nil || len(csi) == 0 {
			return tuiAction{}, nil
		}
		if csi[0] == '<' {
			return parseTUIMouseAction(csi, layout)
		}
		switch csi[len(csi)-1] {
		case 'A':
			return tuiAction{Kind: tuiActionUp}, nil
		case 'B':
			return tuiAction{Kind: tuiActionDown}, nil
		default:
			return tuiAction{}, nil
		}
	default:
		return tuiAction{}, nil
	}
}

func readTUICSISequence(r io.ByteReader) ([]byte, error) {
	var seq []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		seq = append(seq, b)
		if b >= 0x40 && b <= 0x7e {
			return seq, nil
		}
		if len(seq) > 32 {
			return seq, fmt.Errorf("csi sequence too long")
		}
	}
}

func parseTUIMouseAction(csi []byte, layout tui.DashboardLayout) (tuiAction, error) {
	if len(csi) < 2 {
		return tuiAction{}, nil
	}
	final := csi[len(csi)-1]
	if final != 'M' {
		return tuiAction{}, nil
	}
	parts := strings.Split(string(csi[1:len(csi)-1]), ";")
	if len(parts) != 3 {
		return tuiAction{}, nil
	}
	button, err := strconv.Atoi(parts[0])
	if err != nil || button != 0 {
		return tuiAction{}, nil
	}
	x, err := strconv.Atoi(parts[1])
	if err != nil {
		return tuiAction{}, nil
	}
	y, err := strconv.Atoi(parts[2])
	if err != nil {
		return tuiAction{}, nil
	}
	if !pointInRect(x, y, layout.ProfileRect) {
		return tuiAction{}, nil
	}
	profile := layout.ProfileRows[y]
	if profile == "" {
		return tuiAction{}, nil
	}
	return tuiAction{Kind: tuiActionSelectProfile, Profile: profile}, nil
}

func pointInRect(x, y int, rect tui.Rect) bool {
	if rect.W <= 0 || rect.H <= 0 {
		return false
	}
	return x >= rect.X && x < rect.X+rect.W && y >= rect.Y && y < rect.Y+rect.H
}

func runSelectedTUIAction(ctx context.Context, r *runner, profilesFile string, dashboard tui.Dashboard, log *tuiActionState) error {
	profile, ok := selectedTUIProfile(dashboard)
	if !ok {
		return fmt.Errorf("no profile selected")
	}
	if action, ok := profileAction(profile, "b"); !ok || !action.Enabled {
		return fmt.Errorf("backup action is not available")
	}
	return tuiRunProfileAction(ctx, r, profilesFile, profile, log)
}

func runSelectedTUICheck(ctx context.Context, r *runner, profilesFile string, dashboard tui.Dashboard, log *tuiActionState) error {
	profile, ok := selectedTUIProfile(dashboard)
	if !ok {
		return fmt.Errorf("no profile selected")
	}
	if action, ok := profileAction(profile, "c"); !ok || !action.Enabled {
		return fmt.Errorf("check action is not available")
	}
	return tuiRunProfileCheck(ctx, r, profilesFile, profile, log)
}

func selectedTUIProfile(d tui.Dashboard) (tui.ProfileCard, bool) {
	for _, profile := range d.Profiles {
		if profile.Name == d.SelectedProfile {
			return profile, true
		}
	}
	if len(d.Profiles) == 0 {
		return tui.ProfileCard{}, false
	}
	return d.Profiles[0], true
}

func profileNeedsInit(profile tui.ProfileCard) bool {
	return profile.StoreHealth == tui.StoreHealthNotInitialized
}

func profileAction(profile tui.ProfileCard, key string) (tui.ProfileAction, bool) {
	for _, action := range profile.Actions {
		if action.Key == key {
			return action, true
		}
	}
	return tui.ProfileAction{}, false
}
