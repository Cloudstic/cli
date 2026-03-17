//go:build linux

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// portableDriveSource is a TestSource backed by a real GPT-formatted loopback
// device. It exercises the full volume UUID auto-detection pipeline
// (/proc/mounts → /dev/disk/by-partuuid/ symlinks) without any manual
// -volume-uuid flag. Requires sudo for losetup/mkfs/mount.
type portableDriveSource struct {
	mountPoint string
}

func newPortableDriveSource(_ *testing.T) *portableDriveSource {
	return &portableDriveSource{}
}

func (s *portableDriveSource) Name() string { return "portable" }
func (s *portableDriveSource) Env() TestEnv { return Hermetic }

// Setup creates a GPT-formatted loopback device. Called inside the subtest, so
// t.Skip gracefully skips just this matrix entry if sudo/tools aren't available.
func (s *portableDriveSource) Setup(t *testing.T) []string {
	t.Helper()

	// Check that we have the required tools.
	for _, tool := range []string{"sudo", "sgdisk", "losetup", "mkfs.ext4"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found, skipping portable drive test", tool)
		}
	}

	// Verify passwordless sudo (CI runners have this; local dev may not).
	if out, err := exec.Command("sudo", "-n", "true").CombinedOutput(); err != nil {
		t.Skipf("sudo requires password, skipping portable drive test: %s", out)
	}

	// Create a sparse 20 MB disk image.
	imgPath := filepath.Join(t.TempDir(), "disk.img")
	if out, err := exec.Command("dd", "if=/dev/zero", "of="+imgPath,
		"bs=1M", "count=20").CombinedOutput(); err != nil {
		t.Fatalf("dd failed: %v\n%s", err, out)
	}

	// Create GPT partition table with one partition.
	if out, err := exec.Command("sgdisk", "-n", "1:0:0", imgPath).CombinedOutput(); err != nil {
		t.Fatalf("sgdisk failed: %v\n%s", err, out)
	}

	// Attach as loopback device with partition scanning.
	out, err := exec.Command("sudo", "losetup", "--find", "--show", "--partscan", imgPath).CombinedOutput()
	if err != nil {
		t.Skipf("losetup failed (may need privileges): %v\n%s", err, out)
	}
	loopDev := strings.TrimSpace(string(out)) // e.g. "/dev/loop0"
	t.Cleanup(func() {
		_ = exec.Command("sudo", "umount", s.mountPoint).Run()
		_ = exec.Command("sudo", "losetup", "-d", loopDev).Run()
	})

	// Wait briefly for the partition device to appear.
	partDev := loopDev + "p1"
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(partDev); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(partDev); os.IsNotExist(err) {
		t.Fatalf("partition device %s did not appear", partDev)
	}

	// Format as ext4.
	if out, err := exec.Command("sudo", "mkfs.ext4", "-q", "-F", partDev).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4 failed: %v\n%s", err, out)
	}

	// Mount.
	s.mountPoint = filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(s.mountPoint, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sudo", "mount", partDev, s.mountPoint).CombinedOutput(); err != nil {
		t.Fatalf("mount failed: %v\n%s", err, out)
	}

	// Make the mount point writable by the test user.
	if out, err := exec.Command("sudo", "chmod", "777", s.mountPoint).CombinedOutput(); err != nil {
		t.Fatalf("chmod failed: %v\n%s", err, out)
	}

	// Remove lost+found created by mkfs.ext4 — it's root-owned and causes
	// permission errors during backup scanning.
	_ = exec.Command("sudo", "rm", "-rf", filepath.Join(s.mountPoint, "lost+found")).Run()

	// Verify the partition UUID is detectable via /dev/disk/by-partuuid/.
	found := false
	byPartUUID := "/dev/disk/by-partuuid"
	if entries, err := os.ReadDir(byPartUUID); err == nil {
		for _, e := range entries {
			link, err := os.Readlink(filepath.Join(byPartUUID, e.Name()))
			if err != nil {
				continue
			}
			resolved, err := filepath.Abs(filepath.Join(byPartUUID, link))
			if err != nil {
				continue
			}
			if resolved == partDev {
				found = true
				t.Logf("GPT partition UUID detected: %s → %s", e.Name(), resolved)
				break
			}
		}
	}
	if !found {
		// udev may not have created the symlink yet (rare in CI).
		// Try triggering udev and wait.
		_ = exec.Command("sudo", "udevadm", "trigger", "--subsystem-match=block").Run()
		_ = exec.Command("sudo", "udevadm", "settle", "--timeout=5").Run()
		t.Logf("warning: GPT partition UUID symlink not found for %s; udev triggered", partDev)
	}

	return []string{"-source", "local:" + s.mountPoint}
}

func (s *portableDriveSource) WriteFile(t *testing.T, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(s.mountPoint, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		// Files on ext4 mounted with sudo may need sudo to write.
		// Try direct write first, fall back to sudo tee.
		if out, err2 := exec.Command("sudo", "mkdir", "-p", filepath.Dir(fullPath)).CombinedOutput(); err2 != nil {
			t.Fatalf("mkdir failed: %v\n%s (original: %v)", err2, out, err)
		}
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		// Fall back to sudo tee for permission issues.
		cmd := exec.Command("sudo", "tee", fullPath)
		cmd.Stdin = strings.NewReader(content)
		if out, err2 := cmd.CombinedOutput(); err2 != nil {
			t.Fatalf("write failed: %v\n%s (original: %v)", err2, out, err)
		}
	}
}

func (s *portableDriveSource) HostPath(relPath string) string {
	return filepath.Join(s.mountPoint, relPath)
}
