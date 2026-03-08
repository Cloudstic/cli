package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type forgetArgs struct {
	g             *globalFlags
	prune         bool
	dryRun        bool
	keepLast      int
	keepHourly    int
	keepDaily     int
	keepWeekly    int
	keepMonthly   int
	keepYearly    int
	filterTags    stringArrayFlags
	filterSource  string
	filterAccount string
	filterPath    string
	groupBy       string
	snapshotID    string
	hasPolicy     bool
}

func parseForgetArgs() *forgetArgs {
	fs := flag.NewFlagSet("forget", flag.ExitOnError)
	a := &forgetArgs{}
	a.g = addGlobalFlags(fs)
	prune := fs.Bool("prune", false, "Run prune after forgetting")
	dryRun := fs.Bool("dry-run", false, "Only show what would be removed")
	keepLast := fs.Int("keep-last", 0, "Keep the last n snapshots")
	keepHourly := fs.Int("keep-hourly", 0, "Keep n hourly snapshots")
	keepDaily := fs.Int("keep-daily", 0, "Keep n daily snapshots")
	keepWeekly := fs.Int("keep-weekly", 0, "Keep n weekly snapshots")
	keepMonthly := fs.Int("keep-monthly", 0, "Keep n monthly snapshots")
	keepYearly := fs.Int("keep-yearly", 0, "Keep n yearly snapshots")
	fs.Var(&a.filterTags, "tag", "Filter by tag (can be specified multiple times)")
	filterSource := fs.String("source", "", "Filter by source type")
	filterAccount := fs.String("account", "", "Filter by account")
	filterPath := fs.String("path", "", "Filter by path")
	groupBy := fs.String("group-by", "source,account,path", "Group snapshots by fields (comma-separated)")
	mustParse(fs)
	a.prune = *prune
	a.dryRun = *dryRun
	a.keepLast = *keepLast
	a.keepHourly = *keepHourly
	a.keepDaily = *keepDaily
	a.keepWeekly = *keepWeekly
	a.keepMonthly = *keepMonthly
	a.keepYearly = *keepYearly
	a.filterSource = *filterSource
	a.filterAccount = *filterAccount
	a.filterPath = *filterPath
	a.groupBy = *groupBy
	a.hasPolicy = a.keepLast > 0 || a.keepHourly > 0 || a.keepDaily > 0 ||
		a.keepWeekly > 0 || a.keepMonthly > 0 || a.keepYearly > 0
	a.snapshotID = fs.Arg(0)
	if a.snapshotID == "" && !a.hasPolicy {
		fmt.Fprintln(os.Stderr, "Usage: cloudstic forget [options] <snapshot_id>")
		fmt.Fprintln(os.Stderr, "       cloudstic forget --keep-last n [--keep-daily n] [--prune] [--dry-run]")
		os.Exit(1)
	}
	return a
}

func (r *runner) runForget() int {
	a := parseForgetArgs()
	if err := r.openClient(a.g); err != nil {
		return r.fail("Failed to init store: %v", err)
	}

	if a.hasPolicy {
		return r.execForgetPolicy(a)
	}
	return r.execForgetSingle(a)
}

func (r *runner) execForgetSingle(a *forgetArgs) int {
	var forgetOpts []cloudstic.ForgetOption
	if a.prune {
		forgetOpts = append(forgetOpts, cloudstic.WithPrune())
	}
	if *a.g.verbose {
		forgetOpts = append(forgetOpts, cloudstic.WithForgetVerbose())
	}
	result, err := r.client.Forget(context.Background(), a.snapshotID, forgetOpts...)
	if err != nil {
		return r.fail("Forget failed: %v", err)
	}
	_, _ = fmt.Fprintln(r.out)
	_, _ = fmt.Fprintln(r.out, "Snapshot removed.")
	if result.Prune != nil {
		r.printPruneStats(result.Prune)
	}
	return 0
}

func (r *runner) execForgetPolicy(a *forgetArgs) int {
	opts := r.buildForgetPolicyOpts(a)
	result, err := r.client.ForgetPolicy(context.Background(), opts...)
	if err != nil {
		return r.fail("Forget failed: %v", err)
	}
	r.printPolicyResult(result, a.dryRun)
	return 0
}

func (r *runner) buildForgetPolicyOpts(a *forgetArgs) []cloudstic.ForgetOption {
	var opts []cloudstic.ForgetOption
	if a.prune {
		opts = append(opts, cloudstic.WithPrune())
	}
	if a.dryRun {
		opts = append(opts, cloudstic.WithDryRun())
	}
	if *a.g.verbose {
		opts = append(opts, cloudstic.WithForgetVerbose())
	}
	if a.keepLast > 0 {
		opts = append(opts, cloudstic.WithKeepLast(a.keepLast))
	}
	if a.keepHourly > 0 {
		opts = append(opts, cloudstic.WithKeepHourly(a.keepHourly))
	}
	if a.keepDaily > 0 {
		opts = append(opts, cloudstic.WithKeepDaily(a.keepDaily))
	}
	if a.keepWeekly > 0 {
		opts = append(opts, cloudstic.WithKeepWeekly(a.keepWeekly))
	}
	if a.keepMonthly > 0 {
		opts = append(opts, cloudstic.WithKeepMonthly(a.keepMonthly))
	}
	if a.keepYearly > 0 {
		opts = append(opts, cloudstic.WithKeepYearly(a.keepYearly))
	}
	for _, tag := range a.filterTags {
		opts = append(opts, cloudstic.WithFilterTag(tag))
	}
	if a.filterSource != "" {
		opts = append(opts, cloudstic.WithFilterSource(a.filterSource))
	}
	if a.filterAccount != "" {
		opts = append(opts, cloudstic.WithFilterAccount(a.filterAccount))
	}
	if a.filterPath != "" {
		opts = append(opts, cloudstic.WithFilterPath(a.filterPath))
	}
	opts = append(opts, cloudstic.WithGroupBy(a.groupBy))
	return opts
}

func (r *runner) printPolicyResult(result *cloudstic.PolicyResult, dryRun bool) {
	for _, group := range result.Groups {
		_, _ = fmt.Fprintf(r.out, "\nSnapshots for %s:\n", group.Key)

		if len(group.Keep) > 0 {
			_, _ = fmt.Fprintf(r.out, "\nkeep %d snapshots:\n", len(group.Keep))
			reasons := make(map[string]string, len(group.Keep))
			entries := make([]engine.SnapshotEntry, 0, len(group.Keep))
			for _, k := range group.Keep {
				entries = append(entries, k.Entry)
				reasons[k.Entry.Ref] = strings.Join(k.Reasons, ", ")
			}
			r.renderSnapshotTable(entries, reasons)
		}

		if len(group.Remove) > 0 {
			action := "remove"
			if dryRun {
				action = "would remove"
			}
			_, _ = fmt.Fprintf(r.out, "\n%s %d snapshots:\n", action, len(group.Remove))
			r.renderSnapshotTable(group.Remove, nil)
		}
	}

	totalRemoved := 0
	for _, g := range result.Groups {
		totalRemoved += len(g.Remove)
	}

	_, _ = fmt.Fprintln(r.out)
	if dryRun {
		_, _ = fmt.Fprintf(r.out, "%d snapshots would be removed (dry run)\n", totalRemoved)
	} else if totalRemoved > 0 {
		_, _ = fmt.Fprintf(r.out, "%d snapshots have been removed\n", totalRemoved)
		if result.Prune != nil {
			r.printPruneStats(result.Prune)
		}
	} else {
		_, _ = fmt.Fprintln(r.out, "No snapshots to remove")
	}
}
