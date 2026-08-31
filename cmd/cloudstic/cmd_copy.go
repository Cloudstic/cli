package main

import (
	"context"
	"fmt"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/config"
)

// fromFlagPrefix distinguishes the source repository's flags from the
// destination's. Destination flags keep their ordinary names, so every other
// command's spelling carries over unchanged.
const fromFlagPrefix = "from-"

type copyArgs struct {
	*globalFlags

	// from holds the source repository's parsed flags. It is a second
	// globalFlags rather than a bespoke struct so that resolveClientConfig,
	// profile resolution and store construction all work on it unchanged.
	from *globalFlags

	dryRun          bool
	allowCopied     bool
	rawFilterSource string
	filterSource    string
	filterPath      string
	filterAccount   string
	filterTags      stringArrayFlags
	since           string
	sinceTime       time.Time
	snapshotIDs     []string
}

// copyFromFlagSpecs derives the source repository's flags from the same groups
// that describe the destination's.
//
// Deriving rather than restating is what keeps the two sets from drifting: a
// repository flag added later is mirrored with no edit here. prefixed also
// strips the environment bindings, so an ambient CLOUDSTIC_* value configures
// the destination only and can never silently unlock both repositories.
func copyFromFlagSpecs(from *globalFlags) []flagSpec {
	var specs []flagSpec
	for _, group := range []flagGroup{repoFlagSpecs, storeSFTPFlagSpecs, encryptionFlagSpecs} {
		specs = append(specs, prefixed(fromFlagPrefix, "source repository", group(from))...)
	}

	// One profiles file serves both repositories. Mirroring it would offer a
	// second file that no configuration needs, and its default is a path inside
	// -config-dir, which is not mirrored either.
	out := specs[:0]
	for _, spec := range specs {
		if spec.name == fromFlagPrefix+"profiles-file" {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func declareCopyArgs(g *globalFlags) (*copyArgs, commandInput) {
	a := &copyArgs{globalFlags: g, from: &globalFlags{}}

	flags := []flagSpec{
		boolFlag(&a.dryRun, "dry-run", false, "Only show which snapshots would be copied"),
		stringFlag(&a.rawFilterSource, "source", "", "Copy only snapshots of this source URI (e.g. local:./docs, gdrive)",
			withPlaceholder("<uri>"), withCompleter("_cloudstic_source_prefixes"),
			withShortUsage("Copy only snapshots of this source")),
		stringFlag(&a.filterAccount, "account", "", "Copy only snapshots of this account", withPlaceholder("<id>")),
		valueFlag(&a.filterTags, "tag", "Copy only snapshots carrying this tag (can be specified multiple times)",
			withPlaceholder("<tag>"), asRepeatable(), withShortUsage("Copy only snapshots with this tag")),
		stringFlag(&a.since, "since", "", "Copy only snapshots created at or after this time (RFC3339 or YYYY-MM-DD)",
			withPlaceholder("<time>"), withShortUsage("Copy only snapshots newer than this")),
		boolFlag(&a.allowCopied, "allow-copied", false,
			"Allow copying snapshots that were themselves produced by copy"),
	}
	flags = append(flags, copyFromFlagSpecs(a.from)...)

	return a, commandInput{
		flags:       flags,
		positionals: []positionalSpec{remainingPositionals(&a.snapshotIDs, "snapshot ID")},
	}
}

// prepareCopyArgs derives everything that depends on more than one flag, and
// settles what the source repository inherits from the destination's
// invocation.
func prepareCopyArgs(a *copyArgs) error {
	if a.rawFilterSource != "" {
		switch a.rawFilterSource {
		case "local", "sftp", "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
			a.filterSource = a.rawFilterSource
		default:
			parts, err := config.ParseSourceURI(a.rawFilterSource)
			if err != nil {
				return fmt.Errorf("invalid -source filter: %w", err)
			}
			a.filterSource = parts.Scheme
			a.filterPath = parts.Path
		}
	}

	if a.since != "" {
		parsed, err := parseSinceTime(a.since)
		if err != nil {
			return err
		}
		a.sinceTime = parsed
	}

	// Profile resolution asks whether a field was set explicitly, and the
	// source's flags are recorded under their prefixed names. Translating the
	// map lets the source reuse the same precedence rules as any other command
	// rather than a parallel implementation of them.
	fromOrigins := strippedOrigins(a.origins, fromFlagPrefix)

	// The source must be named, and naming it means passing the flag — not
	// leaving it at a default. -store defaults to a local path, which is
	// reasonable for the one repository a command usually has and actively
	// dangerous for the second: a bare `cloudstic copy` would otherwise read
	// whatever happens to sit in ./backup_store rather than saying nothing was
	// specified. There is no environment fallback to consider, because the
	// mirrored flags deliberately have none.
	if fromOrigins["store"] != originFlag && a.from.profile == "" {
		return fmt.Errorf("specify the source repository with -from-store or -from-profile")
	}

	// The source is a second repository, not a second invocation: it shares
	// where configuration lives and how output is rendered, and differs only in
	// where it points and how it unlocks. Nothing credential-bearing is copied
	// across, which is the whole point of keeping the two structs apart.
	a.from.configDir = a.configDir
	a.from.profilesFile = a.profilesFile
	a.from.noPrompt = a.noPrompt
	a.from.quiet = a.quiet
	a.from.verbose = a.verbose
	a.from.debug = a.debug
	a.from.json = a.json

	a.from.origins = fromOrigins
	return nil
}

// strippedOrigins re-keys the flag provenance recorded for prefixed flags back
// onto the names the resolution code knows.
func strippedOrigins(origins map[string]flagOrigin, prefix string) map[string]flagOrigin {
	out := make(map[string]flagOrigin, len(origins))
	for name, origin := range origins {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			out[name[len(prefix):]] = origin
		}
	}
	return out
}

// parseSinceTime accepts a full RFC3339 timestamp or a bare date, which is what
// a person actually types.
func parseSinceTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid -since %q: use RFC3339 (2026-04-01T20:15:03Z) or a date (2026-04-01)", raw)
}

func runCopy(r *runner, ctx context.Context, a *copyArgs, cfg clientConfig) int {
	if err := prepareCopyArgs(a); err != nil {
		return r.fail("Error: %v", err)
	}

	fromCfg, err := resolveClientConfig(a.from)
	if err != nil {
		return r.fail("Failed to resolve source repository: %v", err)
	}
	if sameStoreTarget(cfg.Store, fromCfg.Store) {
		return r.fail("Error: -from-store and -store name the same repository (%s)", cfg.Store.URI)
	}

	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to open destination repository: %v", err)
	}

	src, err := openClient(ctx, fromCfg, nil)
	if err != nil {
		return r.fail("Failed to open source repository: %v", err)
	}

	if !a.json {
		printCopyBanner(r.out, fromCfg.Store.URI, cfg.Store.URI, a.dryRun)
	}

	result, err := r.client.CopyFrom(ctx, src, copyOptions(a)...)
	if err != nil {
		return r.fail("Copy failed: %v", err)
	}

	if a.json {
		return r.writeJSON(result)
	}
	printCopyResult(r.out, result)
	return 0
}

func copyOptions(a *copyArgs) []cloudstic.CopyOption {
	opts := []cloudstic.CopyOption{}
	if len(a.snapshotIDs) > 0 {
		opts = append(opts, cloudstic.WithCopySnapshotIDs(a.snapshotIDs...))
	}
	if a.filterSource != "" {
		opts = append(opts, cloudstic.WithCopyFilterSource(a.filterSource))
	}
	if a.filterPath != "" {
		opts = append(opts, cloudstic.WithCopyFilterPath(a.filterPath))
	}
	if a.filterAccount != "" {
		opts = append(opts, cloudstic.WithCopyFilterAccount(a.filterAccount))
	}
	for _, tag := range a.filterTags {
		opts = append(opts, cloudstic.WithCopyFilterTag(tag))
	}
	if !a.sinceTime.IsZero() {
		opts = append(opts, cloudstic.WithCopySince(a.sinceTime))
	}
	if a.dryRun {
		opts = append(opts, cloudstic.WithCopyDryRun())
	}
	if a.allowCopied {
		opts = append(opts, cloudstic.WithCopyAllowCopied())
	}
	return opts
}

// sameStoreTarget reports whether two resolved store configurations address the
// same repository.
//
// The client guards on repository id, which is exact but unavailable for a
// repository that has none. This catches the common operator slip — the same
// URI on both sides — before either repository is opened, and so before any
// credential prompt.
func sameStoreTarget(a, b config.Store) bool {
	return a.URI != "" && a.URI == b.URI
}

// copyCommand declares the `copy` command.
func copyCommand() command {
	return repoLeaf("copy",
		"Copy snapshots from another repository into this one",
		repoCommandGroups, declareCopyArgs, runCopy)
}
