package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

var (
	workstationDiscoverSourcesFunc = DiscoverSources
	workstationUserHomeDirFunc     = os.UserHomeDir
	workstationHostnameFunc        = os.Hostname
	workstationPathExistsFunc      = func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	workstationGOOS = runtime.GOOS
)

type WorkstationSetupOption func(*workstationSetupOptions)

type workstationSetupOptions struct {
	profiles *ProfilesConfig
	storeRef string
}

func WithWorkstationProfiles(cfg *ProfilesConfig) WorkstationSetupOption {
	return func(o *workstationSetupOptions) { o.profiles = cfg }
}

func WithWorkstationStoreRef(name string) WorkstationSetupOption {
	return func(o *workstationSetupOptions) { o.storeRef = strings.TrimSpace(name) }
}

type WorkstationFolderCandidate struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	Path     string `json:"path"`
	Selected bool   `json:"selected"`
}

type WorkstationProfileDraft struct {
	Name         string   `json:"name"`
	SourceURI    string   `json:"source_uri"`
	StoreRef     string   `json:"store_ref,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Action       string   `json:"action"`
	DisplayLabel string   `json:"display_label,omitempty"`
	Selected     bool     `json:"selected"`
}

type WorkstationCoverageSummary struct {
	ProtectedNow         []string `json:"protected_now,omitempty"`
	SkippedIntentionally []string `json:"skipped_intentionally,omitempty"`
	NotAvailableNow      []string `json:"not_available_now,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
}

type WorkstationApplyResult struct {
	ProfilesCreated int      `json:"profiles_created"`
	ProfilesUpdated int      `json:"profiles_updated"`
	ProfileNames    []string `json:"profile_names,omitempty"`
}

type WorkstationSetupPlan struct {
	Hostname        string                       `json:"hostname"`
	StoreRef        string                       `json:"store_ref,omitempty"`
	StoreAction     string                       `json:"store_action"`
	Folders         []WorkstationFolderCandidate `json:"folders,omitempty"`
	PortableSources []DiscoveredSource           `json:"portable_sources,omitempty"`
	Profiles        []WorkstationProfileDraft    `json:"profiles,omitempty"`
	Coverage        WorkstationCoverageSummary   `json:"coverage"`
}

type workstationFolderSpec struct {
	key      string
	label    string
	category string
	path     string
}

func PlanWorkstationSetup(ctx context.Context, opts ...WorkstationSetupOption) (*WorkstationSetupPlan, error) {
	options := workstationSetupOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	cfg := normalizeProfilesConfig(options.profiles)
	hostname, err := workstationHostnameFunc()
	if err != nil {
		return nil, fmt.Errorf("resolve hostname: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		hostname = "workstation"
	}

	folders, skipped, err := discoverWorkstationFolders()
	if err != nil {
		return nil, err
	}

	discovered, err := workstationDiscoverSourcesFunc(ctx)
	if err != nil {
		return nil, err
	}
	portable := make([]DiscoveredSource, 0, len(discovered))
	for _, result := range discovered {
		if result.Portable {
			portable = append(portable, result)
		}
	}

	storeRef, storeAction, warnings := resolveWorkstationStore(cfg, options.storeRef)

	plan := &WorkstationSetupPlan{
		Hostname:        hostname,
		StoreRef:        storeRef,
		StoreAction:     storeAction,
		Folders:         folders,
		PortableSources: portable,
	}

	usedNames := make(map[string]struct{}, len(cfg.Profiles))
	for name := range cfg.Profiles {
		usedNames[name] = struct{}{}
	}

	for _, folder := range folders {
		if !folder.Selected {
			continue
		}
		name, action := nextWorkstationProfileName(cfg, usedNames, folder.Key, hostname, "local:"+folder.Path)
		plan.Profiles = append(plan.Profiles, WorkstationProfileDraft{
			Name:         name,
			SourceURI:    "local:" + folder.Path,
			StoreRef:     storeRef,
			Tags:         []string{"workstation"},
			Action:       action,
			DisplayLabel: folder.Label + " (" + folder.Path + ")",
			Selected:     true,
		})
		plan.Coverage.ProtectedNow = append(plan.Coverage.ProtectedNow, folder.Label+" ("+folder.Path+")")
	}

	for _, src := range portable {
		base := sanitizeWorkstationName(firstNonEmpty(src.DriveName, src.DisplayName, filepath.Base(src.MountPoint), "portable"))
		name, action := nextWorkstationProfileName(cfg, usedNames, base, hostname, src.SourceURI)
		plan.Profiles = append(plan.Profiles, WorkstationProfileDraft{
			Name:         name,
			SourceURI:    src.SourceURI,
			StoreRef:     storeRef,
			Tags:         []string{"portable", "workstation"},
			Action:       action,
			DisplayLabel: src.DisplayName + " (" + src.MountPoint + ")",
			Selected:     true,
		})
		plan.Coverage.ProtectedNow = append(plan.Coverage.ProtectedNow, src.DisplayName+" ("+src.MountPoint+")")
	}

	plan.Coverage.SkippedIntentionally = append(plan.Coverage.SkippedIntentionally, skipped...)
	plan.Coverage.Warnings = append(plan.Coverage.Warnings, warnings...)
	return plan, nil
}

func ApplyWorkstationSetupPlan(cfg *ProfilesConfig, plan *WorkstationSetupPlan) (*WorkstationApplyResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("workstation setup plan is required")
	}
	cfg = normalizeProfilesConfig(cfg)

	result := &WorkstationApplyResult{
		ProfileNames: make([]string, 0, len(plan.Profiles)),
	}

	for _, draft := range plan.Profiles {
		if !draft.Selected {
			continue
		}
		if strings.TrimSpace(draft.Name) == "" {
			return nil, fmt.Errorf("workstation setup plan contains a draft with no profile name")
		}
		if strings.TrimSpace(draft.SourceURI) == "" {
			return nil, fmt.Errorf("workstation setup plan contains an empty source URI for profile %q", draft.Name)
		}

		if _, ok := cfg.Profiles[draft.Name]; ok {
			result.ProfilesUpdated++
		} else {
			result.ProfilesCreated++
		}

		cfg.Profiles[draft.Name] = BackupProfile{
			Source: draft.SourceURI,
			Store:  draft.StoreRef,
			Tags:   slices.Clone(draft.Tags),
		}
		result.ProfileNames = append(result.ProfileNames, draft.Name)
	}

	slices.Sort(result.ProfileNames)
	return result, nil
}

func discoverWorkstationFolders() ([]WorkstationFolderCandidate, []string, error) {
	home, err := workstationUserHomeDirFunc()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve home directory: %w", err)
	}

	specs := make([]workstationFolderSpec, 0, 8)
	addSpec := func(key, label, category string, elems ...string) {
		path := filepath.Join(append([]string{home}, elems...)...)
		specs = append(specs, workstationFolderSpec{
			key:      key,
			label:    label,
			category: category,
			path:     path,
		})
	}

	addSpec("documents", "Documents", "core documents", "Documents")
	addSpec("desktop", "Desktop", "desktop and workspace", "Desktop")
	addSpec("pictures", "Pictures", "media libraries", "Pictures")
	addSpec("videos", "Videos", "media libraries", "Videos")
	if workstationGOOS != "windows" {
		addSpec("music", "Music", "media libraries", "Music")
	}
	for _, name := range []string{"Projects", "projects", "code", "src"} {
		addSpec(sanitizeWorkstationName(name), name, "developer projects", name)
	}

	folders := make([]WorkstationFolderCandidate, 0, len(specs))
	seenPaths := map[string]struct{}{}
	for _, spec := range specs {
		if !workstationPathExistsFunc(spec.path) {
			continue
		}
		cleanPath := filepath.Clean(spec.path)
		if _, ok := seenPaths[cleanPath]; ok {
			continue
		}
		seenPaths[cleanPath] = struct{}{}
		folders = append(folders, WorkstationFolderCandidate{
			Key:      spec.key,
			Label:    spec.label,
			Category: spec.category,
			Path:     cleanPath,
			Selected: true,
		})
	}
	slices.SortFunc(folders, func(a, b WorkstationFolderCandidate) int {
		if v := strings.Compare(a.Category, b.Category); v != 0 {
			return v
		}
		return strings.Compare(a.Path, b.Path)
	})

	skipped := []string{}
	downloads := filepath.Join(home, "Downloads")
	if workstationPathExistsFunc(downloads) {
		skipped = append(skipped, "Downloads ("+filepath.Clean(downloads)+")")
	}

	return folders, skipped, nil
}

func resolveWorkstationStore(cfg *ProfilesConfig, requested string) (string, string, []string) {
	if requested != "" {
		if _, ok := cfg.Stores[requested]; ok {
			return requested, "use-existing", nil
		}
		return "", "missing", []string{fmt.Sprintf("Store %q was requested but is not defined in profiles.yaml.", requested)}
	}

	storeNames := sortedProfileNames(cfg.Stores)
	switch len(storeNames) {
	case 0:
		return "", "missing", []string{"No store is configured yet. Create one with `cloudstic store new` or rerun with `-store-ref`."}
	case 1:
		return storeNames[0], "use-existing", nil
	default:
		return "", "choose-existing", []string{"Multiple stores are configured. Rerun with `-store-ref <name>` to attach one to the generated profiles."}
	}
}

func nextWorkstationProfileName(cfg *ProfilesConfig, used map[string]struct{}, base, hostname, sourceURI string) (string, string) {
	base = sanitizeWorkstationName(base)
	if base == "" {
		base = "workstation"
	}
	if existing, ok := cfg.Profiles[base]; ok && existing.Source == sourceURI {
		return base, "update"
	}
	if _, ok := used[base]; !ok {
		used[base] = struct{}{}
		return base, "create"
	}

	prefixed := sanitizeWorkstationName(hostname + "-" + base)
	if prefixed == "" {
		prefixed = "workstation-" + base
	}
	if existing, ok := cfg.Profiles[prefixed]; ok && existing.Source == sourceURI {
		return prefixed, "update"
	}
	if _, ok := used[prefixed]; !ok {
		used[prefixed] = struct{}{}
		return prefixed, "rename"
	}

	for i := 2; ; i++ {
		candidate := prefixed + "-" + strconv.Itoa(i)
		if existing, ok := cfg.Profiles[candidate]; ok && existing.Source == sourceURI {
			return candidate, "update"
		}
		if _, ok := used[candidate]; ok {
			continue
		}
		used[candidate] = struct{}{}
		return candidate, "rename"
	}
}

func sanitizeWorkstationName(value string) string {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortedProfileNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
