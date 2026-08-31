package main

import (
	"context"
	"fmt"
	"io"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/keychain"
)

// `migrate` moves a repository to a newer format by copying it into a new one.
//
// It is not an in-place upgrade, and could not be: format 3 (RFC 0026) is a
// different layout rather than a new era inside the same one, so rewriting the
// marker would leave a repository whose recorded format and stored structures
// disagree. Copying into a fresh repository leaves the source untouched, which
// is what makes a failed migration cost nothing — the old repository is the
// rollback, and it is still the only copy until the operator deletes it.
//
// The verb is stable and the format is an argument. `init -migrate` would give
// init a second job with a very different blast radius, and a version in the
// verb needs replacing at the next format.

// migrateArgs is the command's input surface.
type migrateArgs struct {
	*globalFlags

	to       string
	toFormat int
	readData bool
}

func declareMigrateArgs(g *globalFlags) (*migrateArgs, commandInput) {
	a := &migrateArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		stringFlag(&a.to, "to", "",
			"Destination repository URI, created if it does not exist: local:<path>, s3:<bucket>[/<prefix>], b2:<bucket>[/<prefix>], sftp://[user@]host[:port]/<path>",
			withPlaceholder("<uri>"), withCompleter("_cloudstic_store_prefixes"),
			withShortUsage("Destination repository URI")),
		intFlag(&a.toFormat, "to-format", 0,
			fmt.Sprintf("Repository format to migrate to (default: %d, the newest this build creates)", cloudstic.MaxSupportedRepoFormat),
			withShortUsage("Target repository format")),
		boolFlag(&a.readData, "read-data", false,
			"Re-hash all chunk data during the verification pass (slower, byte-level)"),
	}}
}

func runMigrate(r *runner, ctx context.Context, a *migrateArgs, cfg clientConfig) int {
	target := a.toFormat
	if target == 0 {
		target = cloudstic.MaxSupportedRepoFormat
	}
	if target > cloudstic.MaxSupportedRepoFormat {
		return r.fail("Error: -to-format %d is not a format this build creates (highest is %d)",
			target, cloudstic.MaxSupportedRepoFormat)
	}
	if a.to == "" {
		return r.fail("Error: specify the destination repository with -to")
	}

	// The destination is the same repository elsewhere, so it inherits the
	// source's credentials and unlock configuration wholesale and differs only
	// in where it points and what format it records. Migrating between two
	// stores that need *different* credentials is a copy into a repository the
	// operator initialized themselves, which `copy` already does.
	destCfg := cfg
	destCfg.Store.URI = a.to
	if sameStoreTarget(cfg.Store, destCfg.Store) {
		return r.fail("Error: -to names the repository being migrated (%s)", cfg.Store.URI)
	}

	src, err := openClient(ctx, cfg, nil)
	if err != nil {
		return r.fail("Failed to open the repository to migrate: %v", err)
	}
	sourceFormat := src.RepoFormat()
	if sourceFormat >= target {
		return r.fail("Error: %s already records format %d; there is nothing to migrate to format %d",
			cfg.Store.URI, sourceFormat, target)
	}

	if err := prepareMigrationTarget(r, ctx, cfg, destCfg, target); err != nil {
		return r.fail("Failed to prepare the destination repository: %v", err)
	}

	if err := r.openClient(ctx, destCfg); err != nil {
		return r.fail("Failed to open the destination repository: %v", err)
	}

	if !a.jsonEnabled() {
		printMigrateBanner(r.out, cfg.Store.URI, destCfg.Store.URI, sourceFormat, target)
	}

	// No copy filters and no -allow-copied: a migration is every snapshot or it
	// is not a migration. Re-running one continues rather than restarting,
	// because copy skips what the destination's provenance says it already has.
	result, err := r.client.CopyFrom(ctx, src)
	if err != nil {
		return r.fail("Migration failed: %v", err)
	}

	var checkOpts []cloudstic.CheckOption
	if a.readData {
		checkOpts = append(checkOpts, cloudstic.WithReadData())
	}
	verified, err := r.client.Check(ctx, checkOpts...)
	if err != nil {
		return r.fail("Migration copied %s but could not be verified: %v",
			plural(len(result.Copied), "snapshot"), err)
	}

	if a.jsonEnabled() {
		if exit := r.writeJSON(migrateJSON{
			SourceStore: cfg.Store.URI,
			DestStore:   destCfg.Store.URI,
			FromFormat:  sourceFormat,
			ToFormat:    target,
			Copy:        result,
			Check:       verified,
		}); exit != 0 {
			return exit
		}
		// The same rule as `check`: the report is the output, and a failing
		// verification is a failing exit code even though it was written out
		// successfully.
		if len(verified.Errors) > 0 {
			return 1
		}
		return 0
	}

	printCopyResult(r.out, result)
	if printCheckResult(r.errOut, verified) {
		// The source is untouched, so a destination that does not verify is a
		// failed attempt and nothing more. Saying so plainly matters more than
		// the exit code: the operator must not delete anything.
		_, _ = fmt.Fprintf(r.errOut,
			"\nMigration NOT complete: %s did not pass verification.\n"+
				"%s is unchanged. Investigate before deleting anything; re-running migrate resumes.\n",
			destCfg.Store.URI, cfg.Store.URI)
		return 1
	}
	printMigrateNextSteps(r.errOut, cfg.Store.URI, destCfg.Store.URI, a.profile)
	return 0
}

// migrateJSON is the machine-readable result: what moved where, and the two
// sub-results in full, so a script can assert on either.
type migrateJSON struct {
	SourceStore string                 `json:"source_store"`
	DestStore   string                 `json:"dest_store"`
	FromFormat  int                    `json:"from_format"`
	ToFormat    int                    `json:"to_format"`
	Copy        *cloudstic.CopyResult  `json:"copy"`
	Check       *cloudstic.CheckResult `json:"check"`
}

// prepareMigrationTarget makes sure the destination exists at the target
// format, creating it if it does not.
//
// An existing destination is adopted rather than refused, because that is what
// resuming an interrupted migration looks like — but only when it already
// records the format being migrated to. Copying into a repository at some other
// format would succeed and produce something correct that is not what was
// asked for, which is worse than stopping.
func prepareMigrationTarget(r *runner, ctx context.Context, srcCfg, destCfg clientConfig, target int) error {
	raw, err := openStore(ctx, destCfg.Store)
	if err != nil {
		return err
	}

	status, err := cloudstic.InspectRepo(ctx, raw)
	if err != nil {
		return err
	}
	if status.Initialized {
		return confirmMigrationTargetFormat(ctx, destCfg, target)
	}

	sourceRaw, err := openStore(ctx, srcCfg.Store)
	if err != nil {
		return err
	}
	sourceStatus, err := cloudstic.InspectRepo(ctx, sourceRaw)
	if err != nil {
		return err
	}

	kc, err := buildKeychain(ctx, destCfg.Unlock)
	if err != nil {
		return err
	}
	opts, err := migrationInitOpts(sourceStatus.Encrypted, target, kc)
	if err != nil {
		return err
	}

	result, err := cloudstic.InitRepo(ctx, raw, opts...)
	if err != nil {
		return err
	}
	if !r.jsonEnabled() {
		_, _ = fmt.Fprintf(r.errOut, "Created %s at format %d (encrypted: %v).\n",
			destCfg.Store.URI, target, result.Encrypted)
	}
	return nil
}

// migrationInitOpts settles how the new repository is protected.
//
// The destination gets its own master key — every object is re-addressed and
// re-encrypted under it, which is what copy does between any two repositories —
// but it must be reachable by the same credentials, or the migration produces a
// repository the operator cannot open.
//
// In practice the source has already been opened by the time this runs, so an
// encrypted source implies resolvable credentials. The guard stands anyway: the
// failure it prevents is a silently *unencrypted* copy of an encrypted
// repository, which nothing downstream would report and no exit code would
// betray.
func migrationInitOpts(sourceEncrypted bool, target int, kc keychain.Chain) ([]cloudstic.InitOption, error) {
	opts := []cloudstic.InitOption{cloudstic.WithInitFormat(target)}
	if !sourceEncrypted {
		return append(opts, cloudstic.WithInitNoEncryption()), nil
	}
	if len(kc) == 0 {
		return nil, fmt.Errorf(
			"the repository is encrypted but no credentials were given to protect the new one; " +
				"pass -password, -encryption-key or -kms-key-arn")
	}
	return append(opts, cloudstic.WithInitCredentials(kc)), nil
}

// confirmMigrationTargetFormat checks that a destination that already exists is
// at the format being migrated to.
//
// It opens a full client rather than reading the marker, which also settles the
// question the marker cannot: whether the inherited credentials actually unlock
// the destination. Finding that out here costs one open; finding it out after
// the copy costs the copy.
func confirmMigrationTargetFormat(ctx context.Context, destCfg clientConfig, target int) error {
	client, err := openClient(ctx, destCfg, nil)
	if err != nil {
		return fmt.Errorf("%s already exists but could not be opened: %w", destCfg.Store.URI, err)
	}
	if got := client.RepoFormat(); got != target {
		return fmt.Errorf(
			"%s already exists and records format %d, not the format %d being migrated to",
			destCfg.Store.URI, got, target)
	}
	return nil
}

func printMigrateBanner(out io.Writer, sourceURI, destURI string, from, to int) {
	_, _ = fmt.Fprintf(out, "migrating %s (format %d)\n", sourceURI, from)
	_, _ = fmt.Fprintf(out, "       to %s (format %d)\n", destURI, to)
	_, _ = fmt.Fprintln(out, "the source is only read; nothing there is modified or deleted")
}

// printMigrateNextSteps says what the operator has to do, because migration
// deliberately stops short of doing it: repointing configuration and deleting
// the old repository are the two irreversible halves, and the whole reason this
// is a copy is that neither happens on its own.
func printMigrateNextSteps(errOut io.Writer, sourceURI, destURI, profileName string) {
	_, _ = fmt.Fprintf(errOut, "\nMigration complete. %s is unchanged.\n\n", sourceURI)
	_, _ = fmt.Fprintln(errOut, "Next steps:")
	if profileName != "" {
		_, _ = fmt.Fprintf(errOut, "  1. Repoint the %q profile at %s (cloudstic profile new, or edit profiles.yaml).\n",
			profileName, destURI)
	} else {
		_, _ = fmt.Fprintf(errOut, "  1. Use the new repository:  -store %s\n", destURI)
	}
	_, _ = fmt.Fprintf(errOut, "  2. Back up and restore against it until you are satisfied.\n")
	_, _ = fmt.Fprintf(errOut, "  3. Only then delete %s. Until you do, it is your rollback.\n", sourceURI)
}

// migrateCommand declares the `migrate` command.
func migrateCommand() command {
	return repoLeaf("migrate",
		"Migrate a repository to a newer format by copying it into a new one",
		repoCommandGroups, declareMigrateArgs, runMigrate)
}
