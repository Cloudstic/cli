package source

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

func TestDetectVolumeIdentity_TempDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudstic-volume-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	uuid, label, mountPoint := detectVolumeIdentity(tmpDir)

	// On macOS and Linux, the temp directory lives on a real filesystem
	// that should have a volume UUID. On stub platforms, both are empty.
	t.Logf("UUID=%q, Label=%q, MountPoint=%q for %s", uuid, label, mountPoint, tmpDir)

	if uuid != "" {
		// Validate UUID format: hex characters with dashes.
		uuidPattern := regexp.MustCompile(`^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`)
		if !uuidPattern.MatchString(uuid) {
			t.Errorf("UUID %q does not match expected format", uuid)
		}
	}
}

func TestDetectVolumeIdentity_ReturnsUppercaseUUID(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("UUID detection only implemented on darwin and linux")
	}

	tmpDir, err := os.MkdirTemp("", "cloudstic-case-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	uuid, _, _ := detectVolumeIdentity(tmpDir)
	if uuid == "" {
		t.Skip("no UUID detected for temp dir filesystem")
	}

	// UUIDs should be uppercase for consistent cross-platform matching.
	for _, c := range uuid {
		if c >= 'a' && c <= 'f' {
			t.Errorf("UUID %q contains lowercase hex; expected uppercase for consistency", uuid)
			break
		}
	}
}

func TestLocalSource_WithVolumeUUID_Override(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudstic-uuid-override-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	explicitUUID := "CUSTOM-UUID-1234-5678-ABCD-EF0123456789"
	src := NewLocalSource(tmpDir, WithVolumeUUID(explicitUUID))

	info := src.Info()
	if info.VolumeUUID != explicitUUID {
		t.Errorf("expected VolumeUUID=%q, got %q", explicitUUID, info.VolumeUUID)
	}
}

func TestLocalSource_Info_PopulatesVolumeFields(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudstic-info-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	src := NewLocalSource(tmpDir)
	info := src.Info()

	if info.Type != "local" {
		t.Errorf("expected Type=local, got %s", info.Type)
	}
	if info.Account == "" {
		t.Error("expected non-empty Account (hostname)")
	}

	// VolumeUUID and VolumeLabel are populated via the platform-specific
	// detectVolumeIdentity. We just verify they're set correctly on the
	// Info output (they may be empty on stub platforms).
	if info.VolumeUUID != src.VolumeUUID() {
		t.Errorf("Info().VolumeUUID=%q != VolumeUUID()=%q", info.VolumeUUID, src.VolumeUUID())
	}
	if info.VolumeLabel != src.VolumeLabel() {
		t.Errorf("Info().VolumeLabel=%q != VolumeLabel()=%q", info.VolumeLabel, src.VolumeLabel())
	}

	// When VolumeUUID is set, Path should be relative to the volume mount
	// point (not an absolute path).
	if info.VolumeUUID != "" && len(info.Path) > 0 && info.Path[0] == '/' {
		t.Errorf("Path should be volume-relative when VolumeUUID is set, got absolute: %q", info.Path)
	}
}

func TestLocalSource_Walk_NormalizesPathSeparators(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudstic-walk-sep-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create nested structure.
	writeTestFile(t, tmpDir, "docs/notes/file.txt", "hello")

	src := NewLocalSource(tmpDir)
	var metas []struct{ id, parent, path string }
	err = src.Walk(t.Context(), func(fm core.FileMeta) error {
		var parent string
		if len(fm.Parents) > 0 {
			parent = fm.Parents[0]
		}
		var p string
		if len(fm.Paths) > 0 {
			p = fm.Paths[0]
		}
		metas = append(metas, struct{ id, parent, path string }{fm.FileID, parent, p})
		return nil
	})
	if err != nil {
		t.Fatalf("Walk failed: %v", err)
	}

	for _, m := range metas {
		for _, field := range []struct{ name, val string }{
			{"FileID", m.id}, {"Parent", m.parent}, {"Path", m.path},
		} {
			if strings.Contains(field.val, "\\") {
				t.Errorf("%s=%q contains backslash; expected forward slashes only", field.name, field.val)
			}
		}
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
