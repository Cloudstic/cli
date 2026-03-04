package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/moby/term"
)

func runKey() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: cloudstic key <subcommand>")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list           List all encryption key slots in the repository")
		fmt.Fprintln(os.Stderr, "  add-recovery   Generate a 24-word recovery key")
		fmt.Fprintln(os.Stderr, "  passwd         Change the repository password")
		os.Exit(1)
	}

	sub := os.Args[2]
	// Shift os.Args so subcommand flag parsing works correctly:
	// "cloudstic key list -store ..." → args[0]="cloudstic" args[1]="key" args[2]="list" ...
	// After shift: args become ["cloudstic", "key", "-store", ...] and flags parse from args[2:].
	os.Args = append(os.Args[:2], os.Args[3:]...)

	switch sub {
	case "list":
		runKeyList()
	case "add-recovery":
		runAddRecoveryKey()
	case "passwd":
		runKeyPasswd()
	default:
		fmt.Fprintf(os.Stderr, "Unknown key subcommand: %s\n", sub)
		os.Exit(1)
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

func runKeyList() {
	a := parseKeyListArgs()

	raw, err := a.g.initObjectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}
	raw = a.g.applyDebug(raw)

	slots, err := cloudstic.ListKeySlots(context.Background(), raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list key slots: %v\n", err)
		os.Exit(1)
	}

	printKeySlots(slots)
}

// printKeySlots renders the key slot table to stdout.
func printKeySlots(slots []cloudstic.KeySlot) {
	if len(slots) == 0 {
		fmt.Fprintln(os.Stderr, "No key slots found.")
		return
	}
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.AppendHeader(table.Row{"Type", "Label", "KDF"})
	for _, slot := range slots {
		kdf := "—"
		if slot.KDFParams != nil {
			kdf = slot.KDFParams.Algorithm
		}
		t.AppendRow(table.Row{slot.SlotType, slot.Label, kdf})
	}
	t.Render()
	fmt.Fprintf(os.Stderr, "\n%d key slot(s) found.\n", len(slots))
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

func runKeyPasswd() {
	a := parseKeyPasswdArgs()

	ctx := context.Background()

	raw, err := a.g.initObjectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}
	raw = a.g.applyDebug(raw)

	creds, err := a.g.buildCredentials(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// Resolve new password.
	newPw := a.newPassword
	if newPw == "" {
		if !term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprintln(os.Stderr, "Provide --new-password or run interactively.")
			os.Exit(1)
		}
		p1, err := ui.PromptPassword("Enter new repository password")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read password: %v\n", err)
			os.Exit(1)
		}
		if p1 == "" {
			fmt.Fprintln(os.Stderr, "Error: password cannot be empty.")
			os.Exit(1)
		}
		p2, err := ui.PromptPassword("Confirm new repository password")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read password confirmation: %v\n", err)
			os.Exit(1)
		}
		if p1 != p2 {
			fmt.Fprintln(os.Stderr, "Error: passwords do not match.")
			os.Exit(1)
		}
		newPw = p1
	}

	if err := cloudstic.ChangePassword(ctx, raw, creds, newPw); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to change password: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "Repository password has been changed.")
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

func runAddRecoveryKey() {
	a := parseAddRecoveryKeyArgs()

	ctx := context.Background()

	raw, err := a.g.initObjectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}
	raw = a.g.applyDebug(raw)

	creds, err := a.g.buildCredentials(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	mnemonic, err := cloudstic.AddRecoveryKey(ctx, raw, creds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create recovery key: %v\n", err)
		os.Exit(1)
	}

	printRecoveryKey(mnemonic)
	fmt.Fprintln(os.Stderr, "Recovery key slot has been added to the repository.")
}
