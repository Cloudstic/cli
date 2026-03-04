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
}

func parseInitArgs() *initArgs {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	a := &initArgs{}
	a.g = addGlobalFlags(fs)
	recovery := fs.Bool("recovery", false, "Generate a recovery key (24-word seed phrase) during init")
	noEncryption := fs.Bool("no-encryption", false, "Create an unencrypted repository (NOT recommended)")
	mustParse(fs)
	a.recovery = *recovery
	a.noEncryption = *noEncryption
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

	platformKey, err := a.g.parsePlatformKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	password := *a.g.encryptionPassword
	kmsARN := ""
	if a.g.kmsKeyARN != nil {
		kmsARN = *a.g.kmsKeyARN
	}
	hasEncryptionCreds := len(platformKey) > 0 || password != "" || kmsARN != ""

	if !hasEncryptionCreds && !a.noEncryption {
		if term.IsTerminal(os.Stdin.Fd()) {
			pw, err := ui.PromptPassword("Enter new repository password")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to read password: %v\n", err)
				os.Exit(1)
			}
			if pw == "" {
				fmt.Fprintln(os.Stderr, "Error: encryption password cannot be empty.")
				os.Exit(1)
			}
			pw2, err := ui.PromptPassword("Confirm repository password")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to read password confirmation: %v\n", err)
				os.Exit(1)
			}
			if pw != pw2 {
				fmt.Fprintln(os.Stderr, "Error: passwords do not match.")
				os.Exit(1)
			}
			password = pw
		} else {
			fmt.Fprintln(os.Stderr, "Error: encryption is required by default.")
			fmt.Fprintln(os.Stderr, "Provide --encryption-password or --encryption-key to encrypt your repository.")
			fmt.Fprintln(os.Stderr, "To create an unencrypted repository, pass --no-encryption (not recommended).")
			os.Exit(1)
		}
	}

	// Build init options.
	var initOpts []cloudstic.InitOption
	if len(platformKey) > 0 {
		initOpts = append(initOpts, cloudstic.WithInitPlatformKey(platformKey))
	}
	if password != "" {
		initOpts = append(initOpts, cloudstic.WithInitPassword(password))
	}
	if kmsARN != "" {
		kmsClient, err := a.g.buildKMSClient(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to init KMS client: %v\n", err)
			os.Exit(1)
		}
		initOpts = append(initOpts, cloudstic.WithInitKMS(kmsClient, kmsClient, kmsARN))
	}
	if a.recovery {
		initOpts = append(initOpts, cloudstic.WithInitRecovery())
	}
	if a.noEncryption {
		initOpts = append(initOpts, cloudstic.WithInitNoEncryption())
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
