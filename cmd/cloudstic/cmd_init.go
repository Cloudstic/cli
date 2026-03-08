package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/keychain"
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

func (r *runner) runInit() int {
	a := parseInitArgs()

	raw, err := a.g.openStore()
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	kc, err := a.g.buildKeychain(context.Background())
	if err != nil {
		return r.fail("Failed to build keychain: %v", err)
	}
	hasEncryptionCreds := len(kc) > 0

	if !hasEncryptionCreds && !a.noEncryption {
		if term.IsTerminal(os.Stdin.Fd()) {
			pw, err := ui.PromptPasswordConfirm("Enter new repository password")
			if err != nil {
				return r.fail("Error: %v", err)
			}
			*a.g.encryptionPassword = pw
			kc, _ = a.g.buildKeychain(context.Background())
		} else {
			_, _ = fmt.Fprintln(r.errOut, "Error: encryption is required by default.")
			_, _ = fmt.Fprintln(r.errOut, "Provide --encryption-password or --encryption-key to encrypt your repository.")
			_, _ = fmt.Fprintln(r.errOut, "To create an unencrypted repository, pass --no-encryption (not recommended).")
			return 1
		}
	}

	initOpts := buildInitOpts(a, kc)

	result, err := cloudstic.InitRepo(context.Background(), raw, initOpts...)
	if err != nil {
		return r.fail("Init failed: %v", err)
	}

	r.printInitResult(result)
	return 0
}

func buildInitOpts(a *initArgs, kc keychain.Chain) []cloudstic.InitOption {
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
	return initOpts
}

func (r *runner) printInitResult(result *cloudstic.InitResult) {
	if result.Encrypted {
		if result.AdoptedSlots {
			_, _ = fmt.Fprintln(r.errOut, "Adopted existing encryption key slots.")
		} else {
			_, _ = fmt.Fprintln(r.errOut, "Created new encryption key slots.")
		}
		if result.RecoveryKey != "" {
			r.printRecoveryKey(result.RecoveryKey)
		}
	} else {
		_, _ = fmt.Fprintln(r.errOut, "WARNING: creating unencrypted repository. Your backups will NOT be encrypted at rest.")
	}
	_, _ = fmt.Fprintf(r.errOut, "Repository initialized (encrypted: %v).\n", result.Encrypted)
}

func (r *runner) printRecoveryKey(mnemonic string) {
	_, _ = fmt.Fprintln(r.errOut)
	_, _ = fmt.Fprintln(r.errOut, "╔══════════════════════════════════════════════════════════════╗")
	_, _ = fmt.Fprintln(r.errOut, "║                      RECOVERY KEY                           ║")
	_, _ = fmt.Fprintln(r.errOut, "╠══════════════════════════════════════════════════════════════╣")
	_, _ = fmt.Fprintln(r.errOut, "║                                                              ║")
	_, _ = fmt.Fprintf(r.errOut, "║  %s\n", mnemonic)
	_, _ = fmt.Fprintln(r.errOut, "║                                                              ║")
	_, _ = fmt.Fprintln(r.errOut, "║  Write down these 24 words and store them in a safe place.   ║")
	_, _ = fmt.Fprintln(r.errOut, "║  This is the ONLY time the recovery key will be displayed.   ║")
	_, _ = fmt.Fprintln(r.errOut, "║  If you lose your password, this key is your only way to     ║")
	_, _ = fmt.Fprintln(r.errOut, "║  recover your encrypted backups.                             ║")
	_, _ = fmt.Fprintln(r.errOut, "║                                                              ║")
	_, _ = fmt.Fprintln(r.errOut, "╚══════════════════════════════════════════════════════════════╝")
	_, _ = fmt.Fprintln(r.errOut)
}
