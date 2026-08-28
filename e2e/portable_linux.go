//go:build linux

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// portableFixtureMu serialises portable-drive fixture construction. The
// fixtures contend for loop devices — a kernel-wide resource — which is
// exactly what t.Parallel() assumes subtests do not do: concurrent
// `losetup --find` calls race for the same free device, and the udev events
// raised by several attachments interleave. Only construction is serialised;
// once a fixture is mounted the tests using it are independent again.
var portableFixtureMu sync.Mutex

// runFixtureCmd runs one fixture-construction command and returns its trimmed
// combined output. On failure the error carries the command line, the exit
// status and the output on a *single* line: the CI report that prompted this
// (issue #454) showed only the first line of a multi-line t.Fatalf, which is
// why the cause had to be inferred rather than read.
func runFixtureCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w (output: %q)",
			strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

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
// t.Skip gracefully skips just this matrix entry if sudo/tools aren't
// available.
//
// Every step that touches the kernel's loop, udev or mount subsystems —
// losetup, mkfs.ext4, mount, chmod on the mount point — skips rather than
// fails. None of them asserts anything about cloudstic: they build the
// fixture, and they depend on privileges and on a shared resource this
// process does not own. A cloudstic bug still fails the run, because every
// assertion happens after Setup returns. The steps that operate on a plain
// file inside t.TempDir() (dd, sgdisk) keep failing the run, since nothing
// environmental can make them fail once the tools are present.
func (s *portableDriveSource) Setup(t *testing.T) []string {
	t.Helper()

	// Check that we have the required tools.
	for _, tool := range []string{"sudo", "sgdisk", "losetup", "mkfs.ext4"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found, skipping portable drive test", tool)
		}
	}

	// Verify passwordless sudo (CI runners have this; local dev may not).
	if _, err := runFixtureCmd("sudo", "-n", "true"); err != nil {
		t.Skipf("sudo requires password, skipping portable drive test: %v", err)
	}

	portableFixtureMu.Lock()
	defer portableFixtureMu.Unlock()

	// Create a sparse 20 MB disk image.
	imgPath := filepath.Join(t.TempDir(), "disk.img")
	if _, err := runFixtureCmd("dd", "if=/dev/zero", "of="+imgPath,
		"bs=1M", "count=20"); err != nil {
		t.Fatalf("portable fixture: %v", err)
	}

	// Create GPT partition table with one partition.
	if _, err := runFixtureCmd("sgdisk", "-n", "1:0:0", imgPath); err != nil {
		t.Fatalf("portable fixture: %v", err)
	}

	// Attach as loopback device with partition scanning.
	loopDev, err := runFixtureCmd("sudo", "losetup", "--find", "--show", "--partscan", imgPath)
	if err != nil {
		t.Skipf("portable fixture unavailable (losetup may need privileges): %v", err)
	}
	t.Cleanup(func() {
		if s.mountPoint != "" {
			_ = exec.Command("sudo", "umount", s.mountPoint).Run()
		}
		_ = exec.Command("sudo", "losetup", "-d", loopDev).Run()
	})

	partDev := loopDev + "p1"
	if err := waitForPartitionDevice(t, partDev); err != nil {
		t.Skipf("portable fixture unavailable: %v", err)
	}

	// Format as ext4.
	if _, err := runFixtureCmd("sudo", "mkfs.ext4", "-q", "-F", partDev); err != nil {
		t.Skipf("portable fixture unavailable (mkfs.ext4): %v", err)
	}

	// Mount.
	s.mountPoint = filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(s.mountPoint, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := runFixtureCmd("sudo", "mount", partDev, s.mountPoint); err != nil {
		t.Skipf("portable fixture unavailable (mount): %v", err)
	}

	// Make the mount point writable by the test user.
	if _, err := runFixtureCmd("sudo", "chmod", "777", s.mountPoint); err != nil {
		t.Skipf("portable fixture unavailable (chmod): %v", err)
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

// waitForPartitionDevice blocks until the kernel and udev have finished
// attaching partDev.
//
// `udevadm settle` is the check that means something: it waits for the udev
// event queue raised by the attachment to drain, which is the condition
// mkfs.ext4 actually needs. The node simply existing does not imply it — a
// bare os.Stat is what let mkfs.ext4 run against a half-attached device in
// issue #454. Retrying mkfs.ext4 instead was rejected: a retry cannot tell a
// device that is not ready yet from one that is genuinely broken, so a real
// failure would burn the whole retry budget and still report only the last
// attempt.
//
// The Stat loop is kept as a fallback, because udevadm is absent in some
// minimal containers and, where present, returns as soon as the queue is
// empty — which can be before a rule has created the node.
func waitForPartitionDevice(t *testing.T, partDev string) error {
	t.Helper()

	if _, err := exec.LookPath("udevadm"); err == nil {
		if _, err := runFixtureCmd("sudo", "udevadm", "settle", "--timeout=10"); err != nil {
			t.Logf("portable fixture: udevadm settle: %v", err)
		}
	}

	const timeout = 5 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(partDev); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("partition device %s did not appear within %s", partDev, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *portableDriveSource) WriteFile(t *testing.T, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(s.mountPoint, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		// Files on ext4 mounted with sudo may need sudo to write.
		// Try direct write first, fall back to sudo tee.
		if _, err2 := runFixtureCmd("sudo", "mkdir", "-p", filepath.Dir(fullPath)); err2 != nil {
			t.Fatalf("mkdir failed: %v (original: %v)", err2, err)
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
