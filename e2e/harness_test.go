package e2e

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// TestMain sets up a shared GOCOVERDIR so that every instrumented binary
// spawned during the e2e suite writes its coverage data to a single directory.
// After all tests complete, the binary coverage data is converted to the
// standard textfmt profile (compatible with go tool cover / Codecov) and
// written to the path in E2E_COVERAGE_OUT, if set.
func TestMain(m *testing.M) {
	coverDir := os.Getenv("GOCOVERDIR")
	ownsDir := coverDir == ""
	if ownsDir {
		var err error
		coverDir, err = os.MkdirTemp("", "e2e-cover-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e: failed to create GOCOVERDIR: %v\n", err)
			os.Exit(1)
		}
		if err := os.Setenv("GOCOVERDIR", coverDir); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: failed to set GOCOVERDIR: %v\n", err)
			os.Exit(1)
		}
	}

	code := m.Run()

	if outFile := os.Getenv("E2E_COVERAGE_OUT"); outFile != "" {
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+coverDir, "-o="+outFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: coverage conversion failed: %v\n%s\n", err, out)
		}
	}

	if ownsDir {
		_ = os.RemoveAll(coverDir)
	}

	os.Exit(code)
}

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cloudstic-e2e-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "cloudstic")
		cmd := exec.Command("go", "build", "-buildvcs=false", "-cover", "-o", bin, "../cmd/cloudstic")
		cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(dir, "gocache"))
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build failed: %w\n%s", err, out)
			return
		}
		buildPath = bin
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

// cleanEnv returns the current environment with all CLOUDSTIC_ variables removed
// so that tests don't inherit credentials from the host.
func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLOUDSTIC_") {
			env = append(env, e)
		}
	}
	return env
}

func run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func runExpectFail(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected command %v to fail, but it succeeded:\n%s", args, out)
	}
	return string(out)
}

func runWithEnv(t *testing.T, bin string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(cleanEnv(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

type harness struct {
	t           *testing.T
	bin         string
	source      TestSource
	store       TestStore
	sourceArgs  []string
	storeArgs   []string
	password    string
	restoreRoot string
}

func newHarness(t *testing.T, bin string, source TestSource, store TestStore) *harness {
	t.Helper()
	return &harness{
		t:           t,
		bin:         bin,
		source:      source,
		store:       store,
		sourceArgs:  source.Setup(t),
		storeArgs:   store.Setup(t),
		password:    "test-matrix-passphrase",
		restoreRoot: t.TempDir(),
	}
}

func (h *harness) encryptedArgs() []string {
	return append([]string{}, append(h.storeArgs, "-password", h.password)...)
}

func (h *harness) writeFile(relPath, content string) {
	h.t.Helper()
	h.source.WriteFile(h.t, relPath, content)
}

func (h *harness) initEncrypted(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"init"}, h.encryptedArgs()...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) initUnencrypted() string {
	h.t.Helper()
	args := append([]string{"init", "--no-encryption"}, h.storeArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) backup(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"backup"}, h.sourceArgs...)
	args = append(args, h.encryptedArgs()...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) backupUnencrypted(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"backup"}, h.sourceArgs...)
	args = append(args, h.storeArgs...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) list(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"list"}, h.encryptedArgs()...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) listUnencrypted(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"list"}, h.storeArgs...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) check(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"check"}, h.encryptedArgs()...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) checkUnencrypted(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"check"}, h.storeArgs...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) restoreZip(name string, extraArgs ...string) string {
	h.t.Helper()
	zipPath := filepath.Join(h.restoreRoot, name)
	args := append([]string{"restore"}, h.encryptedArgs()...)
	args = append(args, "-output", zipPath)
	args = append(args, extraArgs...)
	run(h.t, h.bin, args...)
	return zipPath
}

func (h *harness) restoreDir(name string, extraArgs ...string) string {
	h.t.Helper()
	dirPath := filepath.Join(h.restoreRoot, name)
	args := append([]string{"restore"}, h.encryptedArgs()...)
	args = append(args, "-format", "dir", "-output", dirPath)
	args = append(args, extraArgs...)
	run(h.t, h.bin, args...)
	return dirPath
}

func (h *harness) restoreZipUnencrypted(name string, extraArgs ...string) string {
	h.t.Helper()
	zipPath := filepath.Join(h.restoreRoot, name)
	args := append([]string{"restore", "--output", zipPath}, h.storeArgs...)
	args = append(args, extraArgs...)
	run(h.t, h.bin, args...)
	return zipPath
}

func (h *harness) forget(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"forget"}, h.encryptedArgs()...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func (h *harness) forgetUnencrypted(extraArgs ...string) string {
	h.t.Helper()
	args := append([]string{"forget"}, h.storeArgs...)
	args = append(args, extraArgs...)
	return run(h.t, h.bin, args...)
}

func zipFileExists(t *testing.T, zipPath, name string) bool {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func readZipFile(t *testing.T, zipPath, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open zip entry %s: %v", name, err)
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read zip entry %s: %v", name, err)
			}
			return string(data)
		}
	}
	t.Fatalf("file %s not found in zip %s", name, zipPath)
	return ""
}

func assertZipMissing(t *testing.T, zipPath, name string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == name {
			t.Errorf("file %s should not be present in zip %s", name, zipPath)
			return
		}
	}
}

func extractMnemonic(t *testing.T, output string) string {
	t.Helper()
	re := regexp.MustCompile(`║\s{2}((?:\w+\s+){23}\w+)`)
	m := re.FindStringSubmatch(output)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}
