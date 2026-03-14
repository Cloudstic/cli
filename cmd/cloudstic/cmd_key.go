package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/moby/term"
)

func (r *runner) runKey() int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic key <subcommand>")
		_, _ = fmt.Fprintln(r.errOut)
		_, _ = fmt.Fprintln(r.errOut, "Subcommands:")
		_, _ = fmt.Fprintln(r.errOut, "  list           List all encryption key slots in the repository")
		_, _ = fmt.Fprintln(r.errOut, "  add-recovery   Generate a 24-word recovery key")
		_, _ = fmt.Fprintln(r.errOut, "  passwd         Change the repository password")
		return 1
	}

	sub := os.Args[2]
	os.Args = append(os.Args[:2], os.Args[3:]...)

	switch sub {
	case "list":
		return r.runKeyList()
	case "add-recovery":
		return r.runAddRecoveryKey()
	case "passwd":
		return r.runKeyPasswd()
	default:
		return r.fail("Unknown key subcommand: %s", sub)
	}
}

type keyListArgs struct {
	g *globalFlags
}

func parseKeyListArgs() *keyListArgs {
	fs := flag.NewFlagSet("key list", flag.ExitOnError)
	a := &keyListArgs{g: addGlobalFlags(fs)}
	mustParse(fs)
	return a
}

func (r *runner) runKeyList() int {
	a := parseKeyListArgs()

	raw, err := a.g.openStore()
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	slots, err := cloudstic.ListKeySlots(context.Background(), raw)
	if err != nil {
		return r.fail("Failed to list key slots: %v", err)
	}

	r.printKeySlots(slots)
	return 0
}

func (r *runner) printKeySlots(slots []cloudstic.KeySlot) {
	if len(slots) == 0 {
		_, _ = fmt.Fprintln(r.errOut, "No key slots found.")
		return
	}
	t := table.NewWriter()
	t.SetOutputMirror(r.out)
	t.AppendHeader(table.Row{"Type", "Label", "KDF"})
	for _, slot := range slots {
		kdf := "—"
		if slot.KDFParams != nil {
			kdf = slot.KDFParams.Algorithm
		}
		t.AppendRow(table.Row{slot.SlotType, slot.Label, kdf})
	}
	t.Render()
	_, _ = fmt.Fprintf(r.errOut, "\n%d key slot(s) found.\n", len(slots))
}

type keyPasswdArgs struct {
	g           *globalFlags
	newPassword string
}

func parseKeyPasswdArgs() *keyPasswdArgs {
	fs := flag.NewFlagSet("key passwd", flag.ExitOnError)
	a := &keyPasswdArgs{}
	a.g = addGlobalFlags(fs)
	newPassword := fs.String("new-password", "", "New repository password (prompted interactively if not set)")
	mustParse(fs)
	a.newPassword = *newPassword
	return a
}

func (r *runner) runKeyPasswd() int {
	a := parseKeyPasswdArgs()

	ctx := context.Background()

	raw, err := a.g.openStore()
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

	_, _ = fmt.Fprintln(r.errOut, "Repository password has been changed.")
	return 0
}

type addRecoveryKeyArgs struct {
	g *globalFlags
}

func parseAddRecoveryKeyArgs() *addRecoveryKeyArgs {
	fs := flag.NewFlagSet("add-recovery-key", flag.ExitOnError)
	a := &addRecoveryKeyArgs{g: addGlobalFlags(fs)}
	mustParse(fs)
	return a
}

func (r *runner) runAddRecoveryKey() int {
	a := parseAddRecoveryKeyArgs()

	ctx := context.Background()

	raw, err := a.g.openStore()
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

	r.printRecoveryKey(mnemonic)
	_, _ = fmt.Fprintln(r.errOut, "Recovery key slot has been added to the repository.")
	return 0
}
