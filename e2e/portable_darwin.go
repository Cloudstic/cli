//go:build darwin

package e2e

import (
"crypto/rand"
"fmt"
"os"
"os/exec"
"path/filepath"
"strings"
"testing"
)

// portableDriveSource is a TestSource backed by a real GPT-formatted RAM disk.
// It exercises the full volume UUID auto-detection pipeline (Statfs → diskutil
// info -plist → DiskUUID parsing) without any manual -volume-uuid flag.
type portableDriveSource struct {
mountPoint string
rawDev     string
volName    string
}

func newPortableDriveSource(_ *testing.T) *portableDriveSource {
	// Generate a unique volume name so parallel tests don't collide.
	var b [3]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("CST%X", b)
	return &portableDriveSource{volName: name}
}

func (s *portableDriveSource) Name() string { return "portable" }
func (s *portableDriveSource) Env() TestEnv { return Hermetic }

// Setup creates a GPT-formatted RAM disk. Called inside the subtest, so
// t.Skip gracefully skips just this matrix entry if RAM disks aren't available.
func (s *portableDriveSource) Setup(t *testing.T) []string {
t.Helper()

// Create a 10 MB RAM disk (20480 × 512-byte sectors).
out, err := exec.Command("hdiutil", "attach", "-nomount", "ram://20480").CombinedOutput()
if err != nil {
t.Skipf("cannot create RAM disk (needs disk access): %v\n%s", err, out)
}
s.rawDev = strings.TrimSpace(string(out))
t.Cleanup(func() { _ = exec.Command("diskutil", "eject", s.rawDev).Run() })

// Partition as GPT with ExFAT — this gives us a real GPT partition UUID.
out, err = exec.Command("diskutil", "partitionDisk", s.rawDev,
"1", "GPT", "ExFAT", s.volName, "100%").CombinedOutput()
if err != nil {
_ = exec.Command("diskutil", "eject", s.rawDev).Run()
t.Skipf("cannot partition RAM disk: %v\n%s", err, out)
}

s.mountPoint = "/Volumes/" + s.volName
if _, err := os.Stat(s.mountPoint); os.IsNotExist(err) {
t.Fatalf("expected mount point %s after partitioning", s.mountPoint)
}

return []string{"-source", "local:" + s.mountPoint}
}

func (s *portableDriveSource) WriteFile(t *testing.T, relPath, content string) {
t.Helper()
fullPath := filepath.Join(s.mountPoint, relPath)
if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
t.Fatal(err)
}
if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
t.Fatal(err)
}
}
