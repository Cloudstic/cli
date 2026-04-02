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
	cfg, err := loadProfilesOrInit(args.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	ensureProfilesMaps(cfg)
	if r.client == nil {
		r.client = &cloudstic.Client{}
	}

	if !args.dryRun && args.storeRef == "" {
		if len(cfg.Stores) == 0 {
			if !r.canPrompt() || args.yes {
				return r.fail("No store is configured; create one first with 'cloudstic store new' or rerun interactively")
			}
			ref, created, code := r.promptStoreSelection(ctx, cfg)
			if code != 0 {
				return code
			}
			args.storeRef = ref
			if created {
				s := cfg.Stores[args.storeRef]
				if !storeHasExplicitEncryption(s) {
					r.promptEncryptionConfig(ctx, cfg, args.storeRef, args.profilesFile)
				}
				if err := r.checkOrInitStoreWithRecovery(ctx, cfg, args.storeRef, args.profilesFile, checkOrInitOptions{
					allowMissingSecrets:  true,
					warnOnMissingSecrets: true,
					offerInit:            true,
				}, true); err != nil {
					_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
				}
			}
		} else if len(cfg.Stores) > 1 {
			if !r.canPrompt() || args.yes {
				return r.fail("Multiple stores are configured; pass -store-ref or rerun interactively")
			}
			ref, _, code := r.promptStoreSelection(ctx, cfg)
			if code != 0 {
				return code
			}
			args.storeRef = ref
		}
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

	printWorkstationSetupPlan(r.out, plan, args.dryRun)
	if args.dryRun {
		return 0
	}

	if plan.StoreRef == "" {
		return r.fail("Store selection is still unresolved; pass -store-ref or rerun interactively")
	}
	if len(plan.Profiles) == 0 {
		_, _ = fmt.Fprintln(r.out, "\nNothing to save.")
		return 0
	}
	if !args.yes {
		if !r.canPrompt() {
			return r.fail("setup workstation requires an interactive terminal or -yes")
		}
		ok, err := r.promptConfirm(ctx, "Save workstation setup?", true)
		if err != nil {
			return r.fail("Failed to confirm workstation setup: %v", err)
		}
		if !ok {
			_, _ = fmt.Fprintln(r.out, "Workstation setup cancelled.")
			return 0
		}
	}

	result, err := cloudstic.ApplyWorkstationSetupPlan(cfg, plan)
	if err != nil {
		return r.fail("Failed to apply workstation setup plan: %v", err)
	}
	if err := cloudstic.SaveProfilesFile(args.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "\nSaved %d profile(s) in %s", len(result.ProfileNames), args.profilesFile)
	if result.ProfilesCreated > 0 || result.ProfilesUpdated > 0 {
		_, _ = fmt.Fprintf(r.out, " (%d created, %d updated)", result.ProfilesCreated, result.ProfilesUpdated)
	}
	_, _ = fmt.Fprintln(r.out)
	return 0
}

func printWorkstationSetupPlan(out io.Writer, plan *cloudstic.WorkstationSetupPlan, dryRun bool) {
	if dryRun {
		_, _ = fmt.Fprintln(out, "Workstation setup plan (dry-run)")
	} else {
		_, _ = fmt.Fprintln(out, "Workstation setup plan")
	}
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
