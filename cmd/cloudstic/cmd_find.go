package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
)

type findArgs struct {
	*globalFlags
	pattern     string
	name        string
	path        string
	regex       string
	ignoreCase  bool
	fileID      string
	contentHash string
	ref         string
	fileType    string
	size        string
	newer       string
	older       string
	snapshots   stringArrayFlags
	source      string
	tags        stringArrayFlags
	latest      int
	since       string
	until       string
	byContent   bool
	maxResults  int
	noDelta     bool
}

func declareFindArgs(g *globalFlags) (*findArgs, commandInput) {
	a := &findArgs{globalFlags: g}
	return a, commandInput{
		flags: []flagSpec{
			stringFlag(&a.name, "name", "", "Match the file's basename against a glob",
				withPlaceholder("<glob>")),
			stringFlag(&a.path, "path", "", "Match the file's full path against a glob (** spans directories)",
				withPlaceholder("<glob>")),
			stringFlag(&a.regex, "regex", "", "Match the file's full path against a regular expression",
				withPlaceholder("<re>"), withShortUsage("Match full path against a regexp")),
			boolFlag(&a.ignoreCase, "i", false, "Match case-insensitively"),
			stringFlag(&a.fileID, "id", "", "Match one source file ID (stable across renames)",
				withPlaceholder("<file-id>")),
			stringFlag(&a.contentHash, "content-hash", "", "Match files whose content has this SHA-256",
				withPlaceholder("<sha256>"), withShortUsage("Match an exact content hash")),
			stringFlag(&a.ref, "ref", "", "Match one exact metadata object (filemeta/<hash>)",
				withPlaceholder("<ref>")),
			stringFlag(&a.fileType, "type", "", "Match only files (f) or only folders (d)",
				withPlaceholder("f|d"), withCompleter("_cloudstic_find_types")),
			stringFlag(&a.size, "size", "", "Match by size: +10M (at least), -1k (at most), 4096 (exactly)",
				withPlaceholder("<size>"), withShortUsage("Match by size, e.g. +10M or -1k")),
			stringFlag(&a.newer, "newer", "", "Match files modified after a time (RFC3339, a date, or 7d)",
				withPlaceholder("<time>"), withShortUsage("Match files modified after a time")),
			stringFlag(&a.older, "older", "", "Match files modified before a time (RFC3339, a date, or 7d)",
				withPlaceholder("<time>"), withShortUsage("Match files modified before a time")),
			valueFlag(&a.snapshots, "snapshot", "Search only these snapshots (repeatable; accepts latest)",
				withPlaceholder("<ref>"), asRepeatable(), withShortUsage("Search only this snapshot")),
			stringFlag(&a.source, "source", "", "Search only snapshots of this source URI (e.g. local:./docs)",
				withPlaceholder("<uri>"), withCompleter("_cloudstic_source_prefixes"),
				withShortUsage("Search only this source's snapshots")),
			valueFlag(&a.tags, "tag", "Search only snapshots carrying this tag (repeatable)",
				withPlaceholder("<tag>"), asRepeatable()),
			intFlag(&a.latest, "latest", 0, "Search only the N newest selected snapshots",
				withPlaceholder("N")),
			stringFlag(&a.since, "since", "", "Search only snapshots created at or after a time",
				withPlaceholder("<time>"), withShortUsage("Search snapshots created after a time")),
			stringFlag(&a.until, "until", "", "Search only snapshots created at or before a time",
				withPlaceholder("<time>"), withShortUsage("Search snapshots created before a time")),
			boolFlag(&a.byContent, "by-content", false, "Group results by content hash instead of by file"),
			intFlag(&a.maxResults, "max-results", 1000, "Stop accumulating after N matching files",
				withPlaceholder("N")),
			boolFlag(&a.noDelta, "no-delta", false, "Walk every snapshot in full instead of diffing consecutive ones"),
		},
		positionals: []positionalSpec{
			optionalPositional(&a.pattern, "pattern", ""),
		},
	}
}

func runFind(r *runner, ctx context.Context, a *findArgs) int {
	opts, err := buildFindOpts(a)
	if err != nil {
		if !a.jsonEnabled() {
			r.printUsage(r.errOut)
		}
		return r.parseError(err)
	}
	cfg, err := resolveClientConfig(a.globalFlags)
	if err != nil {
		return r.fail("Failed to init store: %v", err)
	}
	if err := r.openClient(ctx, cfg); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	result, err := r.client.Find(ctx, opts...)
	if err != nil {
		return r.fail("Find failed: %v", err)
	}

	// Warnings go to stderr in both modes: they describe how the search ran,
	// not what it found, so they must not contaminate a -json pipeline.
	for _, w := range result.Warnings {
		_, _ = fmt.Fprintf(r.errOut, "warning: %s\n", w)
	}
	if a.jsonEnabled() {
		return r.writeJSON(result)
	}
	printFindResult(r.out, result)
	return 0
}

// buildFindOpts turns the parsed flags into engine options, doing the semantic
// validation and derivation that depends on more than one value.
func buildFindOpts(a *findArgs) ([]cloudstic.FindOption, error) {
	var opts []cloudstic.FindOption

	if a.pattern != "" {
		if a.name != "" || a.path != "" {
			return nil, fmt.Errorf("the positional pattern is shorthand for -name or -path; give one or the other")
		}
		opts = append(opts, cloudstic.WithFindPattern(a.pattern))
	}
	if a.name != "" {
		opts = append(opts, cloudstic.WithFindName(a.name))
	}
	if a.path != "" {
		opts = append(opts, cloudstic.WithFindPath(a.path))
	}
	if a.regex != "" {
		opts = append(opts, cloudstic.WithFindRegex(a.regex))
	}
	if a.ignoreCase {
		opts = append(opts, cloudstic.WithFindIgnoreCase())
	}
	if a.fileID != "" {
		opts = append(opts, cloudstic.WithFindFileID(a.fileID))
	}
	if a.contentHash != "" {
		opts = append(opts, cloudstic.WithFindContentHash(a.contentHash))
	}
	if a.ref != "" {
		opts = append(opts, cloudstic.WithFindRef(a.ref))
	}
	if a.fileType != "" {
		t, err := parseFindType(a.fileType)
		if err != nil {
			return nil, err
		}
		opts = append(opts, cloudstic.WithFindType(t))
	}
	if a.size != "" {
		cmp, err := cloudstic.ParseSizeCompare(a.size)
		if err != nil {
			return nil, fmt.Errorf("-size: %w", err)
		}
		opts = append(opts, cloudstic.WithFindSize(cmp))
	}
	if a.newer != "" {
		opts = append(opts, cloudstic.WithFindNewer(a.newer))
	}
	if a.older != "" {
		opts = append(opts, cloudstic.WithFindOlder(a.older))
	}
	if len(a.snapshots) > 0 {
		opts = append(opts, cloudstic.WithFindSnapshots(a.snapshots...))
	}
	if a.source != "" {
		opts = append(opts, cloudstic.WithFindSource(a.source))
	}
	if len(a.tags) > 0 {
		opts = append(opts, cloudstic.WithFindTags(a.tags...))
	}
	if a.latest > 0 {
		opts = append(opts, cloudstic.WithFindLatest(a.latest))
	}
	if a.since != "" {
		opts = append(opts, cloudstic.WithFindSince(a.since))
	}
	if a.until != "" {
		opts = append(opts, cloudstic.WithFindUntil(a.until))
	}
	if a.byContent {
		opts = append(opts, cloudstic.WithFindGroupByContent())
	}
	if a.maxResults > 0 {
		opts = append(opts, cloudstic.WithFindMaxResults(a.maxResults))
	}
	if a.noDelta {
		opts = append(opts, cloudstic.WithFindNoDelta())
	}
	if a.verbose {
		opts = append(opts, cloudstic.WithFindVerbose())
	}
	return opts, nil
}

// parseFindType accepts find(1)'s single-letter type vocabulary as well as the
// repository's own spelling.
func parseFindType(raw string) (core.FileType, error) {
	switch strings.ToLower(raw) {
	case "f", "file":
		return core.FileTypeFile, nil
	case "d", "dir", "folder":
		return core.FileTypeFolder, nil
	default:
		return "", fmt.Errorf("-type %q: use f (file) or d (folder)", raw)
	}
}

// ---------------------------------------------------------------------------
// Presentation
// ---------------------------------------------------------------------------

func printFindResult(out io.Writer, result *cloudstic.FindResult) {
	if len(result.Matches) == 0 {
		_, _ = fmt.Fprintf(out, "No matches (searched %d snapshots in %s)\n",
			result.SnapshotsSearched, result.Elapsed)
		return
	}

	_, _ = fmt.Fprintln(out)
	renderFindMatches(out, result)
	_, _ = fmt.Fprintln(out)
	printFindSummary(out, result)
	printFindRestoreHint(out, result)
}

func renderFindMatches(out io.Writer, result *cloudstic.FindResult) {
	for i, m := range result.Matches {
		if i > 0 {
			_, _ = fmt.Fprintln(out)
		}
		heading := findMatchHeading(m)
		_, _ = fmt.Fprintf(out, "%s\n", heading)

		for n, v := range m.Versions {
			_, _ = fmt.Fprintf(out, "  v%d %s  %s  %s  %s\n",
				n+1,
				shortFilemetaRef(v.Ref),
				formatBytes(v.Size),
				formatFindTime(v.Mtime),
				findSnapshotSpan(v))
			// Every path the heading does not already show gets its own line.
			// That covers a multi-parent entry, which is genuinely in two
			// places at once, and a version sitting somewhere else than the
			// newest one — after an ancestor rename, or under -by-content, where
			// the group spans distinct files. Printing one path and leaving the
			// others implied would silently pick a branch.
			for _, p := range v.Paths {
				if p == m.Path() {
					continue
				}
				_, _ = fmt.Fprintf(out, "       at %s\n", p)
			}
		}
	}
}

// findMatchHeading labels a match by what identifies it: a path when grouping
// by file, the shared content hash when grouping by content.
func findMatchHeading(m cloudstic.FileMatch) string {
	if m.FileID == "" && m.ContentHash != "" {
		return fmt.Sprintf("content %s  (%d files)", shortHash(m.ContentHash), len(m.Versions))
	}
	heading := m.Path()
	if heading == "" {
		heading = "(unknown path)"
	}
	if len(m.Versions) > 1 {
		heading += fmt.Sprintf("  (%d versions)", len(m.Versions))
	}
	return heading
}

func findSnapshotSpan(v cloudstic.FileVersion) string {
	switch len(v.Snapshots) {
	case 0:
		return "no snapshots"
	case 1:
		return "1 snapshot " + shortSnapshotRef(v.Snapshots[0].Ref)
	default:
		return fmt.Sprintf("%d snapshots %s..%s",
			len(v.Snapshots),
			shortSnapshotRef(v.Snapshots[len(v.Snapshots)-1].Ref),
			shortSnapshotRef(v.Snapshots[0].Ref))
	}
}

func printFindSummary(out io.Writer, result *cloudstic.FindResult) {
	// Under -by-content a match is a group of distinct files that share bytes,
	// so calling it "1 file" would misreport exactly what the flag exists to
	// show.
	unit := "file"
	if result.GroupedBy == "content" {
		unit = "content group"
	}
	_, _ = fmt.Fprintf(out, "%s, %s across %s (searched %s in %s)\n",
		plural(len(result.Matches), unit),
		plural(result.TotalVersions(), "version"),
		plural(result.TotalSnapshots(), "snapshot"),
		plural(result.SnapshotsSearched, "snapshot"),
		result.Elapsed)
	if result.Truncated {
		_, _ = fmt.Fprintf(out,
			"Stopped after %d files; narrow the query or raise -max-results to see the rest.\n",
			len(result.Matches))
	}
}

// printFindRestoreHint spells out the restore that would bring the newest match
// back. The reason to run find is almost always that restore comes next, and
// the snapshot ref and path are both already on screen but tedious to assemble.
func printFindRestoreHint(out io.Writer, result *cloudstic.FindResult) {
	if len(result.Matches) == 0 {
		return
	}
	m := result.Matches[0]
	snap, ok := m.LatestSnapshot()
	if !ok || m.Path() == "" {
		return
	}
	// Name the file the command actually restores. With several matches on
	// screen, a bare "the newest version" reads as the newest of all of them,
	// which is not what the first match necessarily is.
	//
	// Reuse the short hash already shown in the result rows. Restore resolves an
	// unambiguous prefix and rejects a collision instead of choosing a snapshot.
	_, _ = fmt.Fprintf(out, "\nRestore the newest version of %s:\n  cloudstic restore -path %s %s -output ./restored\n",
		m.Path(), m.Path(), shortSnapshotRef(snap.Ref))
}

// plural renders a count with its noun, adding a trailing "s" for anything but
// one. Every noun this is used with pluralizes that way.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func shortFilemetaRef(ref string) string {
	return "filemeta/" + shortHash(strings.TrimPrefix(ref, "filemeta/"))
}

func shortSnapshotRef(ref string) string {
	return shortHash(strings.TrimPrefix(ref, "snapshot/"))
}

func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

func formatFindTime(unix int64) string {
	if unix == 0 {
		return "                "
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04")
}

// findCommand declares the `find` command.
func findCommand() command {
	return leaf("find", "Locate a file across every snapshot in the repository",
		repoCommandGroups, declareFindArgs, runFind)
}
