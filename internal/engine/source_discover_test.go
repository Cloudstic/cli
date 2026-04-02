package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

func TestDiscoverSources_Error(t *testing.T) {
	oldDiscover := discoverLocalCandidatesFunc
	oldInfo := discoverLocalSourceInfoFunc
	t.Cleanup(func() {
		discoverLocalCandidatesFunc = oldDiscover
		discoverLocalSourceInfoFunc = oldInfo
	})

	discoverLocalCandidatesFunc = func() ([]discoverCandidate, error) {
		return nil, errors.New("boom")
	}

	_, err := DiscoverSources(context.Background())
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestDiscoverSources_NormalizesAndMergesCandidates(t *testing.T) {
	oldDiscover := discoverLocalCandidatesFunc
	oldInfo := discoverLocalSourceInfoFunc
	t.Cleanup(func() {
		discoverLocalCandidatesFunc = oldDiscover
		discoverLocalSourceInfoFunc = oldInfo
	})

	discoverLocalCandidatesFunc = func() ([]discoverCandidate, error) {
		return []discoverCandidate{
			{mountPoint: "/Volumes/Photos", portable: false},
			{mountPoint: "/Volumes/Photos/", portable: true},
			{mountPoint: "/", portable: false},
			{mountPoint: "", portable: true},
		}, nil
	}
	discoverLocalSourceInfoFunc = func(mountPoint string) core.SourceInfo {
		switch mountPoint {
		case "/":
			return core.SourceInfo{DriveName: "", Identity: "HOST-1", PathID: "/", FsType: "apfs"}
		case "/Volumes/Photos":
			return core.SourceInfo{DriveName: "Photos", Identity: "UUID-1", PathID: "/", FsType: "exfat"}
		default:
			t.Fatalf("unexpected mountPoint %q", mountPoint)
			return core.SourceInfo{}
		}
	}

	got, err := DiscoverSources(context.Background())
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d want 2", len(got))
	}
	if got[0].MountPoint != "/" || got[0].DisplayName != "System" || got[0].Portable {
		t.Fatalf("root result = %+v", got[0])
	}
	if got[1].MountPoint != "/Volumes/Photos" || got[1].DisplayName != "Photos" || !got[1].Portable {
		t.Fatalf("photos result = %+v", got[1])
	}
	if got[1].SourceURI != "local:/Volumes/Photos" {
		t.Fatalf("SourceURI=%q want local:/Volumes/Photos", got[1].SourceURI)
	}
}

func TestDiscoverDisplayName(t *testing.T) {
	tests := []struct {
		name       string
		mountPoint string
		driveName  string
		want       string
	}{
		{name: "drive name wins", mountPoint: "/Volumes/Photos", driveName: "Portable SSD", want: "Portable SSD"},
		{name: "root", mountPoint: "/", want: "System"},
		{name: "base name", mountPoint: "/Volumes/Photos", want: "Photos"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discoverDisplayName(tt.mountPoint, tt.driveName); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
