package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/moby/term"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/keychain"
)

type keyListArgs struct {
	*globalFlags
}

func declareKeyListArgs(g *globalFlags) (*keyListArgs, commandInput) {
	return &keyListArgs{globalFlags: g}, commandInput{}
}

func runKeyList(r *runner, ctx context.Context, a *keyListArgs) int {
	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	raw, err := openStore(ctx, cfg.Store)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	slots, err := cloudstic.ListKeySlots(ctx, raw)
	if err != nil {
		return r.fail("Failed to list key slots: %v", err)
	}

	if a.jsonEnabled() {
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
	*globalFlags
	newPassword string
}

func declareKeyPasswdArgs(g *globalFlags) (*keyPasswdArgs, commandInput) {
	a := &keyPasswdArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		stringFlag(&a.newPassword, "new-password", "", "New repository password (prompted interactively if not set)",
			withPlaceholder("<pw>"), asSecret()),
	}}
}

func runKeyPasswd(r *runner, ctx context.Context, a *keyPasswdArgs) int {
	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	raw, err := openStore(ctx, cfg.Store)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	kc, err := buildKeychain(ctx, cfg.Unlock)
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

	if a.jsonEnabled() {
		return r.writeJSON(&keyPasswordJSONResult{Changed: true})
	}
	_, _ = fmt.Fprintln(r.errOut, "Repository password has been changed.")
	return 0
}

type addRecoveryKeyArgs struct {
	*globalFlags
	label string
	force bool
}

func declareAddRecoveryKeyArgs(g *globalFlags) (*addRecoveryKeyArgs, commandInput) {
	a := &addRecoveryKeyArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		stringFlag(&a.label, "label", keychain.DefaultSlotLabel, "Label for the new recovery key slot",
			withPlaceholder("<name>")),
		boolFlag(&a.force, "force", false, "Replace an existing slot with the same label (invalidates its recovery key)"),
	}}
}

func runAddRecoveryKey(r *runner, ctx context.Context, a *addRecoveryKeyArgs) int {
	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	raw, err := openStore(ctx, cfg.Store)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	kc, err := buildKeychain(ctx, cfg.Unlock)
	if err != nil {
		return r.fail("%v", err)
	}

	mnemonic, err := cloudstic.AddRecoveryKey(ctx, raw, kc, cloudstic.AddRecoveryKeyOptions{
		Label:   a.label,
		Replace: a.force,
	})
	var exists *keychain.SlotExistsError
	if errors.As(err, &exists) {
		return r.fail("%v.\nReplacing it invalidates the recovery key it was issued for.\n"+
			"Use -label <name> to add a second recovery key, or -force to replace this one.", err)
	}
	if err != nil {
		return r.fail("Failed to create recovery key: %v", err)
	}

	if a.jsonEnabled() {
		return r.writeJSON(&recoveryKeyJSONResult{RecoveryKey: mnemonic})
	}
	printRecoveryKey(r.errOut, mnemonic)
	_, _ = fmt.Fprintln(r.errOut, "Recovery key slot has been added to the repository.")
	return 0
}

// keyCommand declares the `key` command group.
func keyCommand() command {
	return group("key", "Manage encryption key slots",
		leaf("list", "List all encryption key slots in the repository",
			repoCommandGroups, declareKeyListArgs, runKeyList),
		leaf("add-recovery", "Generate a 24-word recovery key for an encrypted repository",
			repoCommandGroups, declareAddRecoveryKeyArgs, runAddRecoveryKey),
		leaf("passwd", "Change the repository password",
			repoCommandGroups, declareKeyPasswdArgs, runKeyPasswd),
	)
}
