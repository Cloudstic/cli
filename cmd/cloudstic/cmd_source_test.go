package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/source"
)

func TestRunSourceDiscover(t *testing.T) {
	oldDiscover := discoverSources
	t.Cleanup(func() { discoverSources = oldDiscover })
	discoverSources = func() ([]source.DiscoveredSource, error) {
		return []source.DiscoveredSource{
			{DisplayName: "System", SourceURI: "local:/", MountPoint: "/", Identity: "HOST-1", FsType: "apfs", Portable: false},
			{DisplayName: "Photos", SourceURI: "local:/Volumes/Photos", MountPoint: "/Volumes/Photos", Identity: "UUID-1", FsType: "exfat", Portable: true},
		}, nil
	}

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "source", "discover"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runSource(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Photos") || !strings.Contains(got, "local:/Volumes/Photos") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRunSourceDiscover_PortableOnly(t *testing.T) {
	oldDiscover := discoverSources
	t.Cleanup(func() { discoverSources = oldDiscover })
	discoverSources = func() ([]source.DiscoveredSource, error) {
		return []source.DiscoveredSource{
			{DisplayName: "System", SourceURI: "local:/", MountPoint: "/", Portable: false},
			{DisplayName: "Photos", SourceURI: "local:/Volumes/Photos", MountPoint: "/Volumes/Photos", Portable: true},
		}, nil
	}

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "source", "discover", "-portable-only"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runSource(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if strings.Contains(got, "System") {
		t.Fatalf("expected portable-only output, got:\n%s", got)
	}
	if !strings.Contains(got, "Photos") {
		t.Fatalf("missing portable source:\n%s", got)
	}
}

func TestRunSourceDiscover_JSON(t *testing.T) {
	oldDiscover := discoverSources
	t.Cleanup(func() { discoverSources = oldDiscover })
	discoverSources = func() ([]source.DiscoveredSource, error) {
		return []source.DiscoveredSource{
			{DisplayName: "Photos", SourceURI: "local:/Volumes/Photos", MountPoint: "/Volumes/Photos", Portable: true},
		}, nil
	}

	osArgs := os.Args
	t.Cleanup(func() { os.Args = osArgs })
	os.Args = []string{"cloudstic", "source", "discover", "-json"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runSource(context.Background()); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "\"source_uri\": \"local:/Volumes/Photos\"") {
		t.Fatalf("unexpected json output:\n%s", got)
	}
}
