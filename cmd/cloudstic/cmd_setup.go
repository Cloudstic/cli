package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/cloudstic/cli/internal/onboarding"
	"github.com/cloudstic/cli/internal/workstation"

	"github.com/jedib0t/go-pretty/v6/table"
)

var planWorkstationSetup = workstation.Plan

type setupWorkstationArgs struct {
	*globalFlags
	dryRun       bool
	yes          bool
	jsonOutput   bool
	profilesFile string
	storeRef     string
}

func declareSetupWorkstationArgs(g *globalFlags) (*setupWorkstationArgs, commandInput) {
	a := &setupWorkstationArgs{globalFlags: g}
	return a, commandInput{flags: []flagSpec{
		boolFlag(&a.dryRun, "dry-run", false, "Preview generated profiles without writing configuration"),
		boolFlag(&a.yes, "yes", false, "Accept default selections without prompting"),
		boolFlag(&a.jsonOutput, "json", false, "Write onboarding plan as JSON"),
		profilesFileFlag(&a.profilesFile, g),
		stringFlag(&a.storeRef, "store-ref", "", "Existing store reference to attach to generated profiles",
			withPlaceholder("<name>")),
	}}
}

func runSetupWorkstation(r *runner, ctx context.Context, a *setupWorkstationArgs) int {
	a.storeRef = strings.TrimSpace(a.storeRef)
	cfg, err := profile.LoadOrEmpty(a.profilesFile)
	if err != nil {
		return r.fail("Failed to load profiles: %v", err)
	}
	profile.EnsureMaps(cfg)

	if !a.dryRun && a.storeRef == "" {
		if len(cfg.Stores) == 0 {
			if !r.canPrompt() || a.yes {
				return r.fail("No store is configured; create one first with 'cloudstic store new' or rerun interactively")
			}
			ref, created, selErr := onboarding.SelectStore(ctx, prompterFor(r), cfg)
			if selErr != nil {
				return r.fail("Failed to %v", selErr)
			}
			a.storeRef = ref
			if created {
				s := cfg.Stores[a.storeRef]
				if !onboarding.HasExplicitEncryption(s) {
					onboarding.ConfigureEncryption(ctx, prompterFor(r), cfg, a.storeRef, a.profilesFile, newSecretResolver(a.configDir), r.out, r.errOut)
				}
				if err := checkOrInitStoreWithRecovery(r, ctx, cfg, a.storeRef, a.profilesFile, checkOrInitOptions{
					configDir:            a.configDir,
					allowMissingSecrets:  true,
					warnOnMissingSecrets: true,
					offerInit:            true,
				}, true); err != nil {
					_, _ = fmt.Fprintf(r.errOut, "%v\n", err)
				}
			}
		} else if len(cfg.Stores) > 1 {
			if !r.canPrompt() || a.yes {
				return r.fail("Multiple stores are configured; pass -store-ref or rerun interactively")
			}
			ref, _, selErr := onboarding.SelectStore(ctx, prompterFor(r), cfg)
			if selErr != nil {
				return r.fail("Failed to %v", selErr)
			}
			a.storeRef = ref
		}
	}

	opts := []workstation.SetupOption{workstation.WithProfiles(cfg)}
	if a.storeRef != "" {
		opts = append(opts, workstation.WithStoreRef(a.storeRef))
	}
	plan, err := planWorkstationSetup(ctx, opts...)
	if err != nil {
		return r.fail("Failed to plan workstation setup: %v", err)
	}

	if a.jsonOutput {
		return r.writeJSON(plan)
	}

	if !a.dryRun && !a.yes {
		if !r.canPrompt() {
			return r.fail("setup workstation requires an interactive terminal or -yes")
		}
		if err := reviewWorkstationPlan(ctx, cfg, (*workstation.SetupPlan)(plan), workstationReviewPrompts{
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

	printWorkstationSetupPlan(r.out, plan, a.dryRun)
	if a.dryRun {
		return 0
	}

	if plan.StoreRef == "" {
		return r.fail("Store selection is still unresolved; pass -store-ref or rerun interactively")
	}
	if countSelectedWorkstationProfiles(plan) == 0 {
		_, _ = fmt.Fprintln(r.out, "\nNothing to save.")
		return 0
	}
	if !a.yes {
		ok, err := r.promptConfirm(ctx, "Save workstation setup?", true)
		if err != nil {
			return r.fail("Failed to confirm workstation setup: %v", err)
		}
		if !ok {
			_, _ = fmt.Fprintln(r.out, "Workstation setup cancelled.")
			return 0
		}
	}

	result, err := workstation.Apply(cfg, plan)
	if err != nil {
		return r.fail("Failed to apply workstation setup plan: %v", err)
	}
	if err := profile.Save(a.profilesFile, cfg); err != nil {
		return r.fail("Failed to save profiles: %v", err)
	}
	_, _ = fmt.Fprintf(r.out, "\nSaved %d profile(s) in %s", len(result.ProfileNames), a.profilesFile)
	if result.ProfilesCreated > 0 || result.ProfilesUpdated > 0 {
		_, _ = fmt.Fprintf(r.out, " (%d created, %d updated)", result.ProfilesCreated, result.ProfilesUpdated)
	}
	_, _ = fmt.Fprintln(r.out)
	return 0
}

func printWorkstationSetupPlan(out io.Writer, plan *workstation.SetupPlan, dryRun bool) {
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
				firstNonEmpty(profile.StoreRef, "(none)"),
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

func printWorkstationCoverage(out io.Writer, plan *workstation.SetupPlan) {
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

type workstationReviewPrompts struct {
	confirm   func(context.Context, string, bool) (bool, error)
	selectOne func(context.Context, string, []string) (string, error)
	input     func(context.Context, string, string, func(string) error) (string, error)
}

func reviewWorkstationPlan(ctx context.Context, cfg *profile.Config, plan *workstation.SetupPlan, prompts workstationReviewPrompts) error {
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

func promptWorkstationProfileName(ctx context.Context, prompts workstationReviewPrompts, cfg *profile.Config, plan *workstation.SetupPlan, index int, defaultName string) (string, error) {
	return prompts.input(ctx, "Profile name", defaultName, func(v string) error {
		if v == "" {
			return fmt.Errorf("profile name is required")
		}
		if err := onboarding.ValidateRefName("profile", v); err != nil {
			return err
		}
		if nameTakenInWorkstationPlan(cfg, plan, index, v) {
			return fmt.Errorf("profile %q already exists", v)
		}
		return nil
	})
}

func nameTakenInWorkstationPlan(cfg *profile.Config, plan *workstation.SetupPlan, index int, name string) bool {
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

func nextAvailableWorkstationProfileName(cfg *profile.Config, plan *workstation.SetupPlan, base string) string {
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

func refreshWorkstationCoverage(plan *workstation.SetupPlan) {
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
		label := firstNonEmpty(draft.DisplayLabel, draft.SourceURI)
		if draft.Selected {
			plan.Coverage.ProtectedNow = append(plan.Coverage.ProtectedNow, label)
		} else {
			plan.Coverage.SkippedIntentionally = append(plan.Coverage.SkippedIntentionally, label)
		}
	}
}

func workstationDraftDecisionLabel(draft workstation.ProfileDraft) string {
	if !draft.Selected {
		return "skip"
	}
	return draft.Action
}

func countSelectedWorkstationProfiles(plan *workstation.SetupPlan) int {
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

// setupCommand declares the `setup` command group.
func setupCommand() command {
	return group("setup", "Guided setup and onboarding flows",
		leaf("workstation", "Guide workstation onboarding and profile scaffolding", nil, declareSetupWorkstationArgs, runSetupWorkstation),
	)
}
