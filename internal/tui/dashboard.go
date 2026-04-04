package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

type Dashboard struct {
	ProfileCount    int
	StoreCount      int
	AuthCount       int
	SelectedProfile string
	Activity        ActivityPanel
	Modal           *Modal
	Profiles        []ProfileCard
}

type ModalKind string

const (
	ModalKindProfileForm ModalKind = "profile_form"
	ModalKindConfirm     ModalKind = "confirm"
)

type ModalFieldKind string

const (
	ModalFieldText   ModalFieldKind = "text"
	ModalFieldSelect ModalFieldKind = "select"
)

type Modal struct {
	Kind        ModalKind
	Title       string
	Subtitle    string
	Error       string
	ErrorField  string
	Hint        string
	Message     []string
	Fields      []ModalField
	Selected    int
	SubmitLabel string
	CancelLabel string
}

type ModalField struct {
	Key      string
	Label    string
	Kind     ModalFieldKind
	Value    string
	Options  []string
	Required bool
	Disabled bool
}

type ActivityStatus string

const (
	ActivityStatusIdle    ActivityStatus = ""
	ActivityStatusRunning ActivityStatus = "running"
	ActivityStatusSuccess ActivityStatus = "success"
	ActivityStatusError   ActivityStatus = "error"
)

type ActivityPanel struct {
	Status     ActivityStatus
	ActionKind ActionKind
	Action     string
	Target     string
	Phase      string
	Current    int64
	Total      int64
	IsBytes    bool
	Summary    string
	UpdatedAt  string
	Lines      []string
}

type ActionKind string

const (
	ActionKindInit   ActionKind = "init"
	ActionKindBackup ActionKind = "backup"
	ActionKindCheck  ActionKind = "check"
)

type ProfileAction struct {
	Kind    ActionKind
	Key     string
	Label   string
	Enabled bool
	Reason  string
}

type ProfileStatus string

const (
	ProfileStatusReady    ProfileStatus = "ready"
	ProfileStatusDisabled ProfileStatus = "disabled"
	ProfileStatusWarning  ProfileStatus = "warning"
	ProfileStatusError    ProfileStatus = "error"
)

type StoreHealth string

const (
	StoreHealthReady            StoreHealth = "ready"
	StoreHealthPending          StoreHealth = "pending"
	StoreHealthDisabled         StoreHealth = "disabled"
	StoreHealthMissingStore     StoreHealth = "missing_store"
	StoreHealthMissingAuth      StoreHealth = "missing_auth"
	StoreHealthProviderMismatch StoreHealth = "provider_mismatch"
	StoreHealthUnavailable      StoreHealth = "unavailable"
	StoreHealthNotInitialized   StoreHealth = "not_initialized"
	StoreHealthUnknown          StoreHealth = "unknown"
)

type StoreReachability string

const (
	StoreReachabilityUnknown     StoreReachability = "unknown"
	StoreReachabilityPending     StoreReachability = "pending"
	StoreReachabilityReachable   StoreReachability = "reachable"
	StoreReachabilityUnavailable StoreReachability = "unavailable"
)

type RepositoryState string

const (
	RepositoryStateUnknown        RepositoryState = "unknown"
	RepositoryStateInitialized    RepositoryState = "initialized"
	RepositoryStateNotInitialized RepositoryState = "not_initialized"
)

type BackupFreshness string

const (
	BackupFreshnessUnknown BackupFreshness = ""
	BackupFreshnessNever   BackupFreshness = "never"
	BackupFreshnessRecent  BackupFreshness = "recent"
	BackupFreshnessStale   BackupFreshness = "stale"
)

type ProfileCard struct {
	Name         string
	Source       string
	StoreRef     string
	AuthRef      string
	Enabled      bool
	Status       ProfileStatus
	StatusNote   string
	StoreHealth  StoreHealth
	Reachability StoreReachability
	Repository   RepositoryState
	BackupState  BackupFreshness
	LastBackup   string
	LastRef      string
	Actions      []ProfileAction
}

type StoreProbe struct {
	Status    string
	Error     string
	Snapshots []engine.SnapshotEntry
}

type SnapshotLoader func(context.Context, string, engine.ProfileStore) ([]engine.SnapshotEntry, error)

func BuildDashboardFromConfig(ctx context.Context, cfg *engine.ProfilesConfig, load SnapshotLoader) Dashboard {
	if cfg == nil {
		cfg = &engine.ProfilesConfig{}
	}

	probes := map[string]StoreProbe{}
	if load != nil {
		for name, storeCfg := range cfg.Stores {
			snapshots, err := load(ctx, name, storeCfg)
			if err != nil {
				probes[name] = StoreProbe{
					Status: "error",
					Error:  err.Error(),
				}
				continue
			}
			probes[name] = StoreProbe{
				Status:    "ok",
				Snapshots: snapshots,
			}
		}
	}

	return BuildDashboard(cfg, probes)
}

func BuildDashboard(cfg *engine.ProfilesConfig, probes map[string]StoreProbe) Dashboard {
	if cfg == nil {
		cfg = &engine.ProfilesConfig{}
	}
	if probes == nil {
		probes = map[string]StoreProbe{}
	}

	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	d := Dashboard{
		ProfileCount: len(cfg.Profiles),
		StoreCount:   len(cfg.Stores),
		AuthCount:    len(cfg.Auth),
		Profiles:     make([]ProfileCard, 0, len(cfg.Profiles)),
	}
	for _, name := range names {
		profile := cfg.Profiles[name]
		status, note := profileStatus(cfg, profile, probes[profile.Store])
		lastBackup, lastRef, lastCreated := latestBackup(profile.Source, probes[profile.Store].Snapshots)
		storeHealth := deriveStoreHealth(cfg, profile, probes[profile.Store])
		reachability := deriveStoreReachability(storeHealth)
		repository := deriveRepositoryState(probes[profile.Store], storeHealth)
		backupState := deriveBackupState(lastCreated)
		if lastBackup == "" {
			backupState = BackupFreshnessNever
		}
		d.Profiles = append(d.Profiles, ProfileCard{
			Name:         name,
			Source:       profile.Source,
			StoreRef:     profile.Store,
			AuthRef:      profile.AuthRef,
			Enabled:      profile.IsEnabled(),
			Status:       status,
			StatusNote:   note,
			StoreHealth:  storeHealth,
			Reachability: reachability,
			Repository:   repository,
			BackupState:  backupState,
			LastBackup:   lastBackup,
			LastRef:      lastRef,
			Actions:      deriveProfileActions(status, storeHealth),
		})
	}
	return d
}

func deriveStoreReachability(health StoreHealth) StoreReachability {
	switch health {
	case StoreHealthPending:
		return StoreReachabilityPending
	case StoreHealthUnavailable:
		return StoreReachabilityUnavailable
	case StoreHealthUnknown:
		return StoreReachabilityUnknown
	default:
		return StoreReachabilityReachable
	}
}

func deriveRepositoryState(probe StoreProbe, health StoreHealth) RepositoryState {
	switch health {
	case StoreHealthNotInitialized:
		return RepositoryStateNotInitialized
	case StoreHealthUnavailable, StoreHealthUnknown, StoreHealthPending:
		return RepositoryStateUnknown
	}
	if probe.Status == "ok" {
		return RepositoryStateInitialized
	}
	return RepositoryStateUnknown
}

func deriveProfileActions(status ProfileStatus, storeHealth StoreHealth) []ProfileAction {
	switch {
	case status == ProfileStatusDisabled:
		return []ProfileAction{{
			Kind:    ActionKindBackup,
			Key:     "b",
			Label:   "No actions available for disabled profiles",
			Enabled: false,
			Reason:  "profile disabled",
		}}
	case status == ProfileStatusError:
		return []ProfileAction{{
			Kind:    ActionKindBackup,
			Key:     "b",
			Label:   "Fix profile configuration before running actions",
			Enabled: false,
			Reason:  "profile configuration error",
		}}
	case storeHealth == StoreHealthNotInitialized:
		return []ProfileAction{{
			Kind:    ActionKindInit,
			Key:     "b",
			Label:   "Press b to initialize the repository",
			Enabled: true,
		}, {
			Kind:    ActionKindCheck,
			Key:     "c",
			Label:   "Repository check unavailable until initialization",
			Enabled: false,
			Reason:  "repository not initialized",
		}}
	default:
		return []ProfileAction{{
			Kind:    ActionKindBackup,
			Key:     "b",
			Label:   "Press b to run backup",
			Enabled: true,
		}, {
			Kind:    ActionKindCheck,
			Key:     "c",
			Label:   "Press c to run repository check",
			Enabled: true,
		}}
	}
}

func profileStatus(cfg *engine.ProfilesConfig, p engine.BackupProfile, probe StoreProbe) (ProfileStatus, string) {
	if !p.IsEnabled() {
		return ProfileStatusDisabled, "profile disabled"
	}
	if p.Store == "" {
		return ProfileStatusError, "no store ref"
	}
	if _, ok := cfg.Stores[p.Store]; !ok {
		return ProfileStatusError, "missing store"
	}
	if p.AuthRef != "" {
		auth, ok := cfg.Auth[p.AuthRef]
		if !ok {
			return ProfileStatusError, "missing auth ref"
		}
		if provider := profileProviderFromSource(p.Source); provider != "" && auth.Provider != "" && auth.Provider != provider {
			return ProfileStatusError, "provider mismatch"
		}
	}
	if provider := profileProviderFromSource(p.Source); provider != "" && p.AuthRef == "" {
		return ProfileStatusError, "missing auth"
	}
	switch probe.Status {
	case "error":
		if probe.Error != "" {
			return ProfileStatusWarning, normalizeProbeError(probe.Error)
		}
		return ProfileStatusWarning, "store unavailable"
	case "ok":
		if latest, _, _ := latestBackup(p.Source, probe.Snapshots); latest == "" {
			return ProfileStatusReady, "never backed up"
		}
	}
	return ProfileStatusReady, ""
}

func latestBackup(sourceURI string, entries []engine.SnapshotEntry) (string, string, time.Time) {
	want := sourceKeyFromURI(sourceURI)
	if want.Type == "" {
		return "", "", time.Time{}
	}
	for _, entry := range entries {
		if snapshotMatchesSource(entry.Snap.Source, want) {
			if entry.Created.IsZero() {
				return "unknown time", entry.Ref, time.Time{}
			}
			return entry.Created.Local().Format("2006-01-02 15:04"), entry.Ref, entry.Created
		}
	}
	return "", "", time.Time{}
}

func deriveStoreHealth(cfg *engine.ProfilesConfig, p engine.BackupProfile, probe StoreProbe) StoreHealth {
	if !p.IsEnabled() {
		return StoreHealthDisabled
	}
	if p.Store == "" {
		return StoreHealthMissingStore
	}
	if _, ok := cfg.Stores[p.Store]; !ok {
		return StoreHealthMissingStore
	}
	if provider := profileProviderFromSource(p.Source); provider != "" && p.AuthRef == "" {
		return StoreHealthMissingAuth
	}
	if p.AuthRef != "" {
		auth, ok := cfg.Auth[p.AuthRef]
		if !ok {
			return StoreHealthMissingAuth
		}
		if provider := profileProviderFromSource(p.Source); provider != "" && auth.Provider != "" && auth.Provider != provider {
			return StoreHealthProviderMismatch
		}
	}
	switch probe.Status {
	case "error":
		switch normalizeProbeError(probe.Error) {
		case "repository not initialized":
			return StoreHealthNotInitialized
		case "":
			return StoreHealthUnavailable
		default:
			return StoreHealthUnavailable
		}
	case "ok":
		return StoreHealthReady
	default:
		return StoreHealthPending
	}
}

func deriveBackupState(created time.Time) BackupFreshness {
	if created.IsZero() {
		return BackupFreshnessUnknown
	}
	age := time.Since(created)
	if age <= 7*24*time.Hour {
		return BackupFreshnessRecent
	}
	return BackupFreshnessStale
}

type sourceKey struct {
	Type      string
	Path      string
	DriveName string
}

func sourceKeyFromURI(raw string) sourceKey {
	scheme, rest, ok := strings.Cut(raw, ":")
	if !ok {
		switch raw {
		case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
			return sourceKey{Type: raw, Path: "/"}
		default:
			return sourceKey{}
		}
	}

	switch scheme {
	case "local", "sftp":
		return sourceKey{Type: scheme, Path: rest}
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		if strings.HasPrefix(rest, "//") {
			remainder := strings.TrimPrefix(rest, "//")
			host, path, _ := strings.Cut(remainder, "/")
			return sourceKey{Type: scheme, DriveName: host, Path: ensureLeadingSlash(path)}
		}
		if rest == "" {
			return sourceKey{Type: scheme, Path: "/"}
		}
		return sourceKey{Type: scheme, Path: ensureLeadingSlash(rest)}
	default:
		return sourceKey{}
	}
}

func snapshotMatchesSource(src *core.SourceInfo, want sourceKey) bool {
	if src == nil || src.Type != want.Type {
		return false
	}
	if want.DriveName != "" && src.DriveName != "" && src.DriveName != want.DriveName {
		return false
	}
	if want.Path != "" && src.Path != want.Path {
		return false
	}
	return true
}

func profileProviderFromSource(sourceURI string) string {
	switch sourceKeyFromURI(sourceURI).Type {
	case "gdrive", "gdrive-changes":
		return "google"
	case "onedrive", "onedrive-changes":
		return "onedrive"
	default:
		return ""
	}
}

func ensureLeadingSlash(path string) string {
	if path == "" {
		return "/"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func normalizeProbeError(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if _, rest, ok := strings.Cut(raw, ": "); ok {
		raw = rest
	}
	switch {
	case strings.Contains(raw, "repository not initialized"):
		return "repository not initialized"
	default:
		return raw
	}
}

func ProbeStatusLabel(kind string) string {
	switch kind {
	case "ready":
		return "ready"
	case "disabled":
		return "disabled"
	case "warning":
		return "warning"
	case "error":
		return "error"
	default:
		return fmt.Sprintf("unknown:%s", kind)
	}
}

func SnapshotAgeLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}
