package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/moby/term"
)

type initArgs struct {
	g            *globalFlags
	recovery     bool
	noEncryption bool
	adoptSlots   bool
}

func parseInitArgs() *initArgs {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	a := &initArgs{}
	a.g = addGlobalFlags(fs)
	recovery := fs.Bool("recovery", false, "Generate a recovery key (24-word seed phrase) during init")
	noEncryption := fs.Bool("no-encryption", false, "Create an unencrypted repository (NOT recommended)")
	adoptSlots := fs.Bool("adopt-slots", false, "Initialize by adopting existing key slots if found (prevents error if already has slots)")
	mustParse(fs)
	a.recovery = *recovery
	a.noEncryption = *noEncryption
	a.adoptSlots = *adoptSlots
	return a
}

// runInit bootstraps a new repository: creates encryption key slots and
// writes the "config" marker. Encryption is required by default; pass
// --no-encryption to explicitly create an unencrypted repository.
func runInit() {
	a := parseInitArgs()

	raw, err := a.g.initObjectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init store: %v\n", err)
		os.Exit(1)
	}
	raw = a.g.applyDebug(raw)

	kc, err := a.g.buildKeychain(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build keychain: %v\n", err)
		os.Exit(1)
	}
	hasEncryptionCreds := len(kc) > 0

	if !hasEncryptionCreds && !a.noEncryption {
		if term.IsTerminal(os.Stdin.Fd()) {
			pw, err := ui.PromptPasswordConfirm("Enter new repository password")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			*a.g.encryptionPassword = pw
			// rebuild keychain with the new password
			kc, _ = a.g.buildKeychain(context.Background())
		} else {
			fmt.Fprintln(os.Stderr, "Error: encryption is required by default.")
			fmt.Fprintln(os.Stderr, "Provide --encryption-password or --encryption-key to encrypt your repository.")
			fmt.Fprintln(os.Stderr, "To create an unencrypted repository, pass --no-encryption (not recommended).")
			os.Exit(1)
		}
	}

	// Build init options.
	var initOpts []cloudstic.InitOption
	if len(kc) > 0 {
		initOpts = append(initOpts, cloudstic.WithInitCredentials(kc))
	}
	if a.recovery {
		initOpts = append(initOpts, cloudstic.WithInitRecovery())
	}
	if a.noEncryption {
		initOpts = append(initOpts, cloudstic.WithInitNoEncryption())
	}
	if a.adoptSlots {
		initOpts = append(initOpts, cloudstic.WithInitAdoptSlots())
	}

	result, err := cloudstic.InitRepo(context.Background(), raw, initOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Init failed: %v\n", err)
		os.Exit(1)
	}

	printInitResult(result)
}

// printInitResult prints the init outcome to stderr.
func printInitResult(result *cloudstic.InitResult) {
	if result.Encrypted {
		if result.AdoptedSlots {
			fmt.Fprintln(os.Stderr, "Adopted existing encryption key slots.")
		} else {
			fmt.Fprintln(os.Stderr, "Created new encryption key slots.")
		}
		if result.RecoveryKey != "" {
			printRecoveryKey(result.RecoveryKey)
		}
	} else {
		fmt.Fprintln(os.Stderr, "WARNING: creating unencrypted repository. Your backups will NOT be encrypted at rest.")
	}
	fmt.Fprintf(os.Stderr, "Repository initialized (encrypted: %v).\n", result.Encrypted)
}

func printRecoveryKey(mnemonic string) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║                      RECOVERY KEY                           ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════╣")
	fmt.Fprintln(os.Stderr, "║                                                              ║")
	fmt.Fprintf(os.Stderr, "║  %s\n", mnemonic)
	fmt.Fprintln(os.Stderr, "║                                                              ║")
	fmt.Fprintln(os.Stderr, "║  Write down these 24 words and store them in a safe place.   ║")
	fmt.Fprintln(os.Stderr, "║  This is the ONLY time the recovery key will be displayed.   ║")
	fmt.Fprintln(os.Stderr, "║  If you lose your password, this key is your only way to     ║")
	fmt.Fprintln(os.Stderr, "║  recover your encrypted backups.                             ║")
	fmt.Fprintln(os.Stderr, "║                                                              ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr)
}
