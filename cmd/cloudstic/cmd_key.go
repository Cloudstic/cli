package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/moby/term"
)

func runKey(r *runner, ctx context.Context) int {
	if len(r.args) < 1 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic key <subcommand>")
		_, _ = fmt.Fprintln(r.errOut)
		_, _ = fmt.Fprintln(r.errOut, "Subcommands:")
		_, _ = fmt.Fprintln(r.errOut, "  list           List all encryption key slots in the repository")
		_, _ = fmt.Fprintln(r.errOut, "  add-recovery   Generate a 24-word recovery key")
		_, _ = fmt.Fprintln(r.errOut, "  passwd         Change the repository password")
		return 1
	}

	sub := r.args[0]
	subRunner := r.withArgs(r.args[1:])

	switch sub {
	case "list":
		return runKeyList(subRunner, ctx)
	case "add-recovery":
		return runAddRecoveryKey(subRunner, ctx)
	case "passwd":
		return runKeyPasswd(subRunner, ctx)
	default:
		return r.fail("Unknown key subcommand: %s", sub)
	}
}

type keyListArgs struct {
	g *globalFlags
}

func newKeyListFlagSet() (boundFlagSet, *keyListArgs) {
	fs := flag.NewFlagSet("key list", flag.ContinueOnError)
	g, globalSpecs := addGlobalFlags(fs, repoCommandGroups)
	return boundFlagSet{set: fs, global: globalSpecs}, &keyListArgs{g: g}
}

func parseKeyListArgs(args []string) (*keyListArgs, error) {
	b, a := newKeyListFlagSet()
	if err := parseFlags(b, args); err != nil {
		return nil, err
	}
	return a, nil
}

func runKeyList(r *runner, ctx context.Context) int {
	a, err := parseKeyListArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}

	raw, err := a.g.openStore(ctx)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	slots, err := cloudstic.ListKeySlots(ctx, raw)
	if err != nil {
		return r.fail("Failed to list key slots: %v", err)
	}

	if a.g.jsonEnabled() {
		return r.writeJSON(slots)
	}
	printKeySlots(r.out, r.errOut, slots)
	return 0
}

func printKeySlots(out io.Writer, errOut io.Writer, slots []cloudstic.KeySlot) {
	if len(slots) == 0 {
		_, _ = fmt.Fprintln(errOut, "No key slots found.")
		return
	}
	t := table.NewWriter()
	t.SetOutputMirror(out)
	t.AppendHeader(table.Row{"Type", "Label", "KDF"})
	for _, slot := range slots {
		kdf := "—"
		if slot.KDFParams != nil {
			kdf = slot.KDFParams.Algorithm
		}
		t.AppendRow(table.Row{slot.SlotType, slot.Label, kdf})
	}
	t.Render()
	_, _ = fmt.Fprintf(errOut, "\n%d key slot(s) found.\n", len(slots))
}

type keyPasswdArgs struct {
	g           *globalFlags
	newPassword string
}

func keyPasswdFlagSpecs(a *keyPasswdArgs) []flagSpec {
	return []flagSpec{
		stringFlag(&a.newPassword, "new-password", "", "New repository password (prompted interactively if not set)",
			withPlaceholder("<pw>"), asSecret()),
	}
}

func newKeyPasswdFlagSet() (boundFlagSet, *keyPasswdArgs) {
	fs := flag.NewFlagSet("key passwd", flag.ContinueOnError)
	a := &keyPasswdArgs{}
	g, globalSpecs := addGlobalFlags(fs, repoCommandGroups)
	a.g = g
	own := keyPasswdFlagSpecs(a)
	bindFlags(fs, own)
	return boundFlagSet{set: fs, global: globalSpecs, own: own}, a
}

func parseKeyPasswdArgs(args []string) (*keyPasswdArgs, error) {
	b, a := newKeyPasswdFlagSet()
	if err := parseFlags(b, args); err != nil {
		return nil, err
	}
	return a, nil
}

func runKeyPasswd(r *runner, ctx context.Context) int {
	a, err := parseKeyPasswdArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}

	raw, err := a.g.openStore(ctx)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	kc, err := a.g.buildKeychain(ctx)
	if err != nil {
		return r.fail("%v", err)
	}

	newPassword := cloudstic.PasswordProviderFunc(func(ctx context.Context) (string, error) {
		newPw := a.newPassword
		if newPw == "" {
			if r.noPrompt || !term.IsTerminal(os.Stdin.Fd()) {
				return "", errors.New("provide --new-password or run interactively")
			}
			p1, err := ui.PromptPasswordConfirm("Enter new repository password")
			if err != nil {
				return "", err
			}
			newPw = p1
		}
		return newPw, nil
	})

	if err := cloudstic.ChangePassword(ctx, raw, kc, newPassword); err != nil {
		return r.fail("Failed to change password: %v", err)
	}

	if a.g.jsonEnabled() {
		return r.writeJSON(&keyPasswordJSONResult{Changed: true})
	}
	_, _ = fmt.Fprintln(r.errOut, "Repository password has been changed.")
	return 0
}

type addRecoveryKeyArgs struct {
	g *globalFlags
}

func newAddRecoveryKeyFlagSet() (boundFlagSet, *addRecoveryKeyArgs) {
	fs := flag.NewFlagSet("add-recovery-key", flag.ContinueOnError)
	g, globalSpecs := addGlobalFlags(fs, repoCommandGroups)
	return boundFlagSet{set: fs, global: globalSpecs}, &addRecoveryKeyArgs{g: g}
}

func parseAddRecoveryKeyArgs(args []string) (*addRecoveryKeyArgs, error) {
	b, a := newAddRecoveryKeyFlagSet()
	if err := parseFlags(b, args); err != nil {
		return nil, err
	}
	return a, nil
}

func runAddRecoveryKey(r *runner, ctx context.Context) int {
	a, err := parseAddRecoveryKeyArgs(r.args)
	if err != nil {
		return r.parseError(err)
	}

	raw, err := a.g.openStore(ctx)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	kc, err := a.g.buildKeychain(ctx)
	if err != nil {
		return r.fail("%v", err)
	}

	mnemonic, err := cloudstic.AddRecoveryKey(ctx, raw, kc)
	if err != nil {
		return r.fail("Failed to create recovery key: %v", err)
	}

	if a.g.jsonEnabled() {
		return r.writeJSON(&recoveryKeyJSONResult{RecoveryKey: mnemonic})
	}
	printRecoveryKey(r.errOut, mnemonic)
	_, _ = fmt.Fprintln(r.errOut, "Recovery key slot has been added to the repository.")
	return 0
}
