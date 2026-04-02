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
	"github.com/cloudstic/cli/internal/engine"
	"github.com/jedib0t/go-pretty/v6/table"
)

var planWorkstationSetup = cloudstic.PlanWorkstationSetup

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
	plan, err := planWorkstationSetup(ctx, opts...)
	if err != nil {
		return r.fail("Failed to plan workstation setup: %v", err)
	}

	if args.jsonOutput {
		return r.writeJSON(plan)
	}

	if !args.dryRun && !args.yes {
		if !r.canPrompt() {
			return r.fail("setup workstation requires an interactive terminal or -yes")
		}
		if err := reviewWorkstationPlan(ctx, cfg, (*engine.WorkstationSetupPlan)(plan), workstationReviewPrompts{
			confirm: func(ctx context.Context, label string, defaultYes bool) (bool, error) {
				return r.promptConfirm(ctx, label, defaultYes)
			},
			selectOne: func(ctx context.Context, label string, options []string) (string, error) {
				return r.promptSelect(ctx, label, options)
			},
			input: func(ctx context.Context, label, defaultValue string, validate func(string) error) (string, error) {
				return r.promptValidatedLine(ctx, label, defaultValue, validate)
			},
		}); err != nil {
			return r.fail("Failed to review workstation setup: %v", err)
		}
	}

	printWorkstationSetupPlan(r.out, plan, args.dryRun)
	if args.dryRun {
		return 0
	}

	if plan.StoreRef == "" {
		return r.fail("Store selection is still unresolved; pass -store-ref or rerun interactively")
	}
	if countSelectedWorkstationProfiles(plan) == 0 {
		_, _ = fmt.Fprintln(r.out, "\nNothing to save.")
		return 0
	}
	if !args.yes {
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
				workstationDraftDecisionLabel(profile),
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

type workstationReviewPrompts struct {
	confirm   func(context.Context, string, bool) (bool, error)
	selectOne func(context.Context, string, []string) (string, error)
	input     func(context.Context, string, string, func(string) error) (string, error)
}

func reviewWorkstationPlan(ctx context.Context, cfg *cloudstic.ProfilesConfig, plan *engine.WorkstationSetupPlan, prompts workstationReviewPrompts) error {
	if plan == nil {
		return nil
	}
	for i := range plan.Profiles {
		draft := &plan.Profiles[i]
		switch draft.Action {
		case "create":
			ok, err := prompts.confirm(ctx, fmt.Sprintf("Create profile %q for %s?", draft.Name, draft.DisplayLabel), true)
			if err != nil {
				return err
			}
			draft.Selected = ok
		case "update":
			choice, err := prompts.selectOne(ctx,
				fmt.Sprintf("Profile %q already exists for %s", draft.Name, draft.DisplayLabel),
				[]string{
					fmt.Sprintf("Update existing profile %q", draft.Name),
					"Create renamed profile",
					"Skip this source",
				},
			)
			if err != nil {
				return err
			}
			switch choice {
			case fmt.Sprintf("Update existing profile %q", draft.Name):
				draft.Selected = true
			case "Create renamed profile":
				name, err := promptWorkstationProfileName(ctx, prompts, cfg, plan, i, nextAvailableWorkstationProfileName(cfg, plan, draft.Name))
				if err != nil {
					return err
				}
				draft.Name = name
				draft.Action = "rename"
				draft.Selected = true
			default:
				draft.Selected = false
				draft.Action = "skip"
			}
		case "rename":
			choice, err := prompts.selectOne(ctx,
				fmt.Sprintf("Profile name collision for %s", draft.DisplayLabel),
				[]string{
					fmt.Sprintf("Create renamed profile %q", draft.Name),
					"Use a different name",
					"Skip this source",
				},
			)
			if err != nil {
				return err
			}
			switch choice {
			case fmt.Sprintf("Create renamed profile %q", draft.Name):
				draft.Selected = true
			case "Use a different name":
				name, err := promptWorkstationProfileName(ctx, prompts, cfg, plan, i, draft.Name)
				if err != nil {
					return err
				}
				draft.Name = name
				draft.Selected = true
			default:
				draft.Selected = false
				draft.Action = "skip"
			}
		default:
			draft.Selected = true
		}
	}
	refreshWorkstationCoverage(plan)
	return nil
}

func promptWorkstationProfileName(ctx context.Context, prompts workstationReviewPrompts, cfg *cloudstic.ProfilesConfig, plan *engine.WorkstationSetupPlan, index int, defaultName string) (string, error) {
	return prompts.input(ctx, "Profile name", defaultName, func(v string) error {
		if v == "" {
			return fmt.Errorf("profile name is required")
		}
		if err := validateRefName("profile", v); err != nil {
			return err
		}
		if nameTakenInWorkstationPlan(cfg, plan, index, v) {
			return fmt.Errorf("profile %q already exists", v)
		}
		return nil
	})
}

func nameTakenInWorkstationPlan(cfg *cloudstic.ProfilesConfig, plan *engine.WorkstationSetupPlan, index int, name string) bool {
	if existing, ok := cfg.Profiles[name]; ok {
		if index >= 0 {
			current := plan.Profiles[index]
			if current.Action == "update" && current.Name == name && existing.Source == current.SourceURI {
				return false
			}
		}
		return true
	}
	for i, draft := range plan.Profiles {
		if i == index || !draft.Selected {
			continue
		}
		if draft.Name == name {
			return true
		}
	}
	return false
}

func nextAvailableWorkstationProfileName(cfg *cloudstic.ProfilesConfig, plan *engine.WorkstationSetupPlan, base string) string {
	base = sanitizeWorkstationProfileName(base)
	if base == "" {
		base = "workstation"
	}
	candidate := base
	for i := 2; ; i++ {
		if !nameTakenInWorkstationPlan(cfg, plan, -1, candidate) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func refreshWorkstationCoverage(plan *engine.WorkstationSetupPlan) {
	if plan == nil {
		return
	}
	profileLabels := map[string]struct{}{}
	for _, draft := range plan.Profiles {
		if draft.DisplayLabel != "" {
			profileLabels[draft.DisplayLabel] = struct{}{}
		}
	}

	preservedSkipped := make([]string, 0, len(plan.Coverage.SkippedIntentionally))
	for _, item := range plan.Coverage.SkippedIntentionally {
		if _, ok := profileLabels[item]; !ok {
			preservedSkipped = append(preservedSkipped, item)
		}
	}

	plan.Coverage.ProtectedNow = nil
	plan.Coverage.SkippedIntentionally = preservedSkipped
	for _, draft := range plan.Profiles {
		label := firstNonEmptyCLI(draft.DisplayLabel, draft.SourceURI)
		if draft.Selected {
			plan.Coverage.ProtectedNow = append(plan.Coverage.ProtectedNow, label)
		} else {
			plan.Coverage.SkippedIntentionally = append(plan.Coverage.SkippedIntentionally, label)
		}
	}
}

func workstationDraftDecisionLabel(draft cloudstic.WorkstationProfileDraft) string {
	if !draft.Selected {
		return "skip"
	}
	return draft.Action
}

func countSelectedWorkstationProfiles(plan *cloudstic.WorkstationSetupPlan) int {
	if plan == nil {
		return 0
	}
	count := 0
	for _, draft := range plan.Profiles {
		if draft.Selected {
			count++
		}
	}
	return count
}

func sanitizeWorkstationProfileName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '.', r == '_', r == '-':
			if b.Len() == 0 || prevDash {
				continue
			}
			b.WriteRune(r)
			prevDash = r == '-'
		default:
			if b.Len() == 0 || prevDash {
				continue
			}
			b.WriteRune('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-._")
}
