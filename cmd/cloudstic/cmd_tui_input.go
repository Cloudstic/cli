package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/cloudstic/cli/internal/tui"
)

type tuiAction int

const (
	tuiActionNone tuiAction = iota
	tuiActionUp
	tuiActionDown
	tuiActionRun
	tuiActionCheck
	tuiActionCreate
	tuiActionEdit
	tuiActionDelete
	tuiActionQuit
)

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

func readTUIAction(r io.ByteReader) (tuiAction, error) {
	b, err := r.ReadByte()
	if err != nil {
		return tuiActionNone, err
	}
	switch b {
	case 'q', 'Q':
		return tuiActionQuit, nil
	case 'j', 'J':
		return tuiActionDown, nil
	case 'k', 'K':
		return tuiActionUp, nil
	case 'b', 'B':
		return tuiActionRun, nil
	case 'c', 'C':
		return tuiActionCheck, nil
	case 'n', 'N':
		return tuiActionCreate, nil
	case 'e', 'E':
		return tuiActionEdit, nil
	case 'd', 'D':
		return tuiActionDelete, nil
	case 0x1b:
		next, err := r.ReadByte()
		if err != nil {
			return tuiActionNone, nil
		}
		if next == 'O' {
			dir, err := r.ReadByte()
			if err != nil {
				return tuiActionNone, nil
			}
			switch dir {
			case 'A':
				return tuiActionUp, nil
			case 'B':
				return tuiActionDown, nil
			default:
				return tuiActionNone, nil
			}
		}
		if next != '[' {
			return tuiActionNone, nil
		}
		csi, err := readTUICSISequence(r)
		if err != nil || len(csi) == 0 {
			return tuiActionNone, nil
		}
		switch csi[len(csi)-1] {
		case 'A':
			return tuiActionUp, nil
		case 'B':
			return tuiActionDown, nil
		default:
			return tuiActionNone, nil
		}
	default:
		return tuiActionNone, nil
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
