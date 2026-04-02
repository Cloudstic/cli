package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/jedib0t/go-pretty/v6/table"
)

func defaultProfilesPathNoCreate() string {
	if path := os.Getenv("CLOUDSTIC_PROFILES_FILE"); path != "" {
		return path
	}
	if dir := os.Getenv("CLOUDSTIC_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, defaultProfilesFilename)
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "cloudstic", defaultProfilesFilename)
	}
	return defaultProfilesFilename
}

func (r *runner) runSetup(ctx context.Context) int {
	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(r.errOut, "Usage: cloudstic setup <subcommand> [options]")
		_, _ = fmt.Fprintln(r.errOut, "")
		_, _ = fmt.Fprintln(r.errOut, "Available subcommands: workstation")
		return 1
	}

	switch os.Args[2] {
	case "workstation":
		return r.runSetupWorkstation(ctx)
	default:
		return r.fail("Unknown setup subcommand: %s", os.Args[2])
	}
}

type setupWorkstationArgs struct {
	dryRun       bool
	yes          bool
	jsonOutput   bool
	profilesFile string
	storeRef     string
}

func parseSetupWorkstationArgs() *setupWorkstationArgs {
	fs := flag.NewFlagSet("setup workstation", flag.ExitOnError)
	a := &setupWorkstationArgs{}
	dryRun := fs.Bool("dry-run", false, "Preview generated profiles without writing configuration")
	yes := fs.Bool("yes", false, "Accept default selections without prompting")
	jsonOutput := fs.Bool("json", false, "Write onboarding plan as JSON")
	profilesFile := fs.String("profiles-file", defaultProfilesPathNoCreate(), "Path to profiles YAML file")
	storeRef := fs.String("store-ref", "", "Existing store reference to attach to generated profiles")
	_ = fs.Parse(reorderArgs(fs, os.Args[3:]))
	a.dryRun = *dryRun
	a.yes = *yes
	a.jsonOutput = *jsonOutput
	a.profilesFile = *profilesFile
	a.storeRef = strings.TrimSpace(*storeRef)
	return a
}

func (r *runner) runSetupWorkstation(ctx context.Context) int {
	args := parseSetupWorkstationArgs()
	if !args.dryRun {
		return r.fail("setup workstation write mode is not implemented yet; use -dry-run")
	}

	cfg, err := loadProfilesOrInit(args.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	if r.client == nil {
		r.client = &cloudstic.Client{}
	}

	opts := []cloudstic.WorkstationSetupOption{cloudstic.WithWorkstationProfiles(cfg)}
	if args.storeRef != "" {
		opts = append(opts, cloudstic.WithWorkstationStoreRef(args.storeRef))
	}
	plan, err := r.client.PlanWorkstationSetup(ctx, opts...)
	if err != nil {
		return r.fail("Failed to plan workstation setup: %v", err)
	}

	if args.jsonOutput {
		return r.writeJSON(plan)
	}

	printWorkstationSetupPlan(r.out, plan)
	return 0
}

func printWorkstationSetupPlan(out io.Writer, plan *cloudstic.WorkstationSetupPlan) {
	_, _ = fmt.Fprintln(out, "Workstation setup plan (dry-run)")
	_, _ = fmt.Fprintf(out, "Host: %s\n", plan.Hostname)
	if plan.StoreRef != "" {
		_, _ = fmt.Fprintf(out, "Store: %s (%s)\n", plan.StoreRef, plan.StoreAction)
	} else {
		_, _ = fmt.Fprintf(out, "Store: unresolved (%s)\n", plan.StoreAction)
	}
	_, _ = fmt.Fprintln(out)

	if len(plan.Profiles) > 0 {
		t := table.NewWriter()
		t.SetOutputMirror(out)
		t.AppendHeader(table.Row{"Profile", "Source URI", "Store", "Tags", "Action"})
		for _, profile := range plan.Profiles {
			t.AppendRow(table.Row{
				profile.Name,
				profile.SourceURI,
				firstNonEmptyCLI(profile.StoreRef, "(none)"),
				strings.Join(profile.Tags, ","),
				profile.Action,
			})
		}
		t.Render()
	} else {
		_, _ = fmt.Fprintln(out, "No profile drafts generated.")
	}

	printWorkstationCoverage(out, plan)
}

func printWorkstationCoverage(out io.Writer, plan *cloudstic.WorkstationSetupPlan) {
	writeWorkstationLines := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		_, _ = fmt.Fprintf(out, "\n%s:\n", title)
		for _, item := range items {
			_, _ = fmt.Fprintf(out, "- %s\n", item)
		}
	}

	writeWorkstationLines("Protected now", plan.Coverage.ProtectedNow)
	writeWorkstationLines("Skipped intentionally", plan.Coverage.SkippedIntentionally)
	writeWorkstationLines("Not available now", plan.Coverage.NotAvailableNow)
	writeWorkstationLines("Warnings", plan.Coverage.Warnings)
}

func firstNonEmptyCLI(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
