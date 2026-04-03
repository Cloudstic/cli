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
	ActivityLines   []string
	Profiles        []ProfileCard
}

type ProfileCard struct {
	Name       string
	Source     string
	StoreRef   string
	AuthRef    string
	Enabled    bool
	Status     string
	StatusNote string
	LastBackup string
	LastRef    string
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
		lastBackup, lastRef := latestBackup(profile.Source, probes[profile.Store].Snapshots)
		d.Profiles = append(d.Profiles, ProfileCard{
			Name:       name,
			Source:     profile.Source,
			StoreRef:   profile.Store,
			AuthRef:    profile.AuthRef,
			Enabled:    profile.IsEnabled(),
			Status:     status,
			StatusNote: note,
			LastBackup: lastBackup,
			LastRef:    lastRef,
		})
	}
	return d
}

func profileStatus(cfg *engine.ProfilesConfig, p engine.BackupProfile, probe StoreProbe) (string, string) {
	if !p.IsEnabled() {
		return "disabled", "profile disabled"
	}
	if p.Store == "" {
		return "error", "no store ref"
	}
	if _, ok := cfg.Stores[p.Store]; !ok {
		return "error", "missing store"
	}
	if p.AuthRef != "" {
		auth, ok := cfg.Auth[p.AuthRef]
		if !ok {
			return "error", "missing auth ref"
		}
		if provider := profileProviderFromSource(p.Source); provider != "" && auth.Provider != "" && auth.Provider != provider {
			return "error", "provider mismatch"
		}
	}
	if provider := profileProviderFromSource(p.Source); provider != "" && p.AuthRef == "" {
		return "error", "missing auth"
	}
	switch probe.Status {
	case "error":
		if probe.Error != "" {
			return "warning", normalizeProbeError(probe.Error)
		}
		return "warning", "store unavailable"
	case "ok":
		if latest, _ := latestBackup(p.Source, probe.Snapshots); latest == "" {
			return "ready", "never backed up"
		}
	}
	return "ready", ""
}

func latestBackup(sourceURI string, entries []engine.SnapshotEntry) (string, string) {
	want := sourceKeyFromURI(sourceURI)
	if want.Type == "" {
		return "", ""
	}
	for _, entry := range entries {
		if snapshotMatchesSource(entry.Snap.Source, want) {
			if entry.Created.IsZero() {
				return "unknown time", entry.Ref
			}
			return entry.Created.Local().Format("2006-01-02 15:04"), entry.Ref
		}
	}
	return "", ""
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
