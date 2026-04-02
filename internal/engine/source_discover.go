package engine

import (
	"context"
	"github.com/cloudstic/cli/internal/core"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/cloudstic/cli/pkg/source"
)

var (
	discoverLocalCandidatesFunc = discoverLocalCandidates
	discoverLocalSourceInfoFunc = func(mountPoint string) core.SourceInfo {
		return source.NewLocalSource(mountPoint).Info()
	}
)

// DiscoveredSource describes a local source candidate that can be used for
// onboarding and source selection flows.
type DiscoveredSource struct {
	SourceURI   string `json:"source_uri"`
	DisplayName string `json:"display_name"`
	MountPoint  string `json:"mount_point"`
	DriveName   string `json:"drive_name,omitempty"`
	Identity    string `json:"identity,omitempty"`
	PathID      string `json:"path_id,omitempty"`
	FsType      string `json:"fs_type,omitempty"`
	Portable    bool   `json:"portable"`
}

type discoverCandidate struct {
	mountPoint string
	portable   bool
}

// DiscoverSources returns local source candidates suitable for workstation
// onboarding and source-selection UX.
func DiscoverSources(_ context.Context) ([]DiscoveredSource, error) {
	candidates, err := discoverLocalCandidatesFunc()
	if err != nil {
		return nil, err
	}

	byMount := make(map[string]discoverCandidate, len(candidates))
	for _, candidate := range candidates {
		if candidate.mountPoint == "" {
			continue
		}
		mountPoint := filepath.Clean(candidate.mountPoint)
		if existing, ok := byMount[mountPoint]; ok {
			existing.portable = existing.portable || candidate.portable
			byMount[mountPoint] = existing
			continue
		}
		candidate.mountPoint = mountPoint
		byMount[mountPoint] = candidate
	}

	mounts := make([]string, 0, len(byMount))
	for mountPoint := range byMount {
		mounts = append(mounts, mountPoint)
	}
	slices.Sort(mounts)

	results := make([]DiscoveredSource, 0, len(mounts))
	for _, mountPoint := range mounts {
		candidate := byMount[mountPoint]
		info := discoverLocalSourceInfoFunc(mountPoint)
		results = append(results, DiscoveredSource{
			SourceURI:   "local:" + mountPoint,
			DisplayName: discoverDisplayName(mountPoint, info.DriveName),
			MountPoint:  mountPoint,
			DriveName:   info.DriveName,
			Identity:    info.Identity,
			PathID:      info.PathID,
			FsType:      info.FsType,
			Portable:    candidate.portable,
		})
	}

	return results, nil
}

func discoverDisplayName(mountPoint, driveName string) string {
	if driveName != "" {
		return driveName
	}
	if runtime.GOOS == "windows" {
		trimmed := strings.TrimRight(mountPoint, `\/`)
		if trimmed != "" {
			return trimmed
		}
	}
	if mountPoint == "/" {
		return "System"
	}
	base := filepath.Base(mountPoint)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return mountPoint
	}
	return base
}
