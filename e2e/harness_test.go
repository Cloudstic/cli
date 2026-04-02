package e2e

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestMain sets up a shared GOCOVERDIR for coverage tracking.
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

// ---------------------------------------------------------------------------
// Fluent Harness — forces correct call order via types
// ---------------------------------------------------------------------------

// harness represents a fresh test environment that must be initialized.
type harness struct {
	t           *testing.T
	bin         string
	source      TestSource
	store       TestStore
	sourceArgs  []string
	storeArgs   []string
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
		restoreRoot: t.TempDir(),
	}
}

func (h *harness) writeFile(relPath, content string) {
	h.t.Helper()
	h.source.WriteFile(h.t, relPath, content)
}

func (h *harness) Run(args ...string) *commandResult {
	h.t.Helper()
	return newCommandResult(h.t, run(h.t, h.bin, args...))
}

func (h *harness) RunExpectFail(args ...string) *commandResult {
	h.t.Helper()
	return newCommandResult(h.t, runExpectFail(h.t, h.bin, args...))
}

func (h *harness) WithFile(relPath, content string) *harness {
	h.writeFile(relPath, content)
	return h
}

func (h *harness) RemoveFile(relPath string) *harness {
	h.t.Helper()
	hostSource, ok := h.source.(hostPathSource)
	if !ok {
		h.t.Fatalf("source %T does not support host-path file removal", h.source)
	}
	if err := os.Remove(hostSource.HostPath(relPath)); err != nil {
		h.t.Fatalf("remove %s: %v", relPath, err)
	}
	return h
}

// InitEncrypted initializes the repo with encryption and returns an active repo handle and output.
func (h *harness) InitEncrypted(extraArgs ...string) (*repo, string) {
	h.t.Helper()
	password := "test-matrix-passphrase"
	args := append([]string{"init"}, h.storeArgs...)
	args = append(args, "-password", password)
	args = append(args, extraArgs...)
	out := run(h.t, h.bin, args...)
	authArgs := append(append([]string{}, h.storeArgs...), "-password", password)
	return &repo{h: h, authArgs: authArgs}, out
}

func (h *harness) MustInitEncrypted(extraArgs ...string) *repo {
	h.t.Helper()
	r, _ := h.InitEncrypted(extraArgs...)
	return r
}

// InitUnencrypted initializes the repo without encryption and returns an active repo handle and output.
func (h *harness) InitUnencrypted() (*repo, string) {
	h.t.Helper()
	args := append([]string{"init", "--no-encryption"}, h.storeArgs...)
	out := run(h.t, h.bin, args...)
	return &repo{h: h, authArgs: append([]string{}, h.storeArgs...)}, out
}

func (h *harness) MustInitUnencrypted() *repo {
	h.t.Helper()
	r, _ := h.InitUnencrypted()
	return r
}

// Compatibility methods removed. Use r := h.InitEncrypted() or h.InitUnencrypted() to get a repo handle.

// repo represents an initialized repository ready for operations.
type repo struct {
	h        *harness
	authArgs []string
}

type commandResult struct {
	t   *testing.T
	out string
}

func newCommandResult(t *testing.T, out string) *commandResult {
	t.Helper()
	return &commandResult{t: t, out: out}
}

func (r *commandResult) Raw() string {
	r.t.Helper()
	return r.out
}

func (r *commandResult) MustContain(substr string) *commandResult {
	r.t.Helper()
	if !strings.Contains(r.out, substr) {
		r.t.Fatalf("expected output to contain %q, got:\n%s", substr, r.out)
	}
	return r
}

func (r *commandResult) MustNotContain(substr string) *commandResult {
	r.t.Helper()
	if strings.Contains(r.out, substr) {
		r.t.Fatalf("expected output not to contain %q, got:\n%s", substr, r.out)
	}
	return r
}

func (r *commandResult) MustNotContainFold(substr string) *commandResult {
	r.t.Helper()
	if strings.Contains(strings.ToLower(r.out), strings.ToLower(substr)) {
		r.t.Fatalf("expected output not to contain %q (case-insensitive), got:\n%s", substr, r.out)
	}
	return r
}

func (r *commandResult) MustContainAnyFold(parts ...string) *commandResult {
	r.t.Helper()
	outLower := strings.ToLower(r.out)
	for _, part := range parts {
		if strings.Contains(outLower, strings.ToLower(part)) {
			return r
		}
	}
	r.t.Fatalf("expected output to contain one of %q, got:\n%s", parts, r.out)
	return r
}

func (r *commandResult) MustUnmarshalJSON(v any) *commandResult {
	r.t.Helper()
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.out)), v); err != nil {
		r.t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, r.out)
	}
	return r
}

type listResult struct {
	*commandResult
}

type snapshotRow struct {
	Seq     int
	Created string
	Hash    string
	Source  string
	Account string
	Path    string
	Tags    []string
}

type diffChange struct {
	Type string
	Path string
}

type diffResult struct {
	*commandResult
	Ref1    string
	Ref2    string
	Changes []diffChange
}

type lsResult struct {
	*commandResult
	SnapshotRef string
	Entries     []string
}

type forgetResult struct {
	*commandResult
	DryRun           bool
	RemovedCount     int
	WouldRemoveCount int
}

type restoreZipResult struct {
	t       *testing.T
	zipPath string
}

type restoreDirResult struct {
	t       *testing.T
	dirPath string
}

func snapshotCountLabel(n int) string {
	if n == 1 {
		return "1 snapshot"
	}
	return fmt.Sprintf("%d snapshots", n)
}

func (r *listResult) MustHaveSnapshotCount(n int) *listResult {
	r.t.Helper()
	want := snapshotCountLabel(n)
	if !strings.Contains(r.out, want) {
		r.t.Fatalf("expected list output to contain %q, got:\n%s", want, r.out)
	}
	return r
}

func (r *listResult) SnapshotRows() []snapshotRow {
	r.t.Helper()
	return parseSnapshotRows(r.t, r.out)
}

func (r *listResult) FirstSnapshotID() string {
	r.t.Helper()
	rows := r.SnapshotRows()
	if len(rows) == 0 {
		r.t.Fatalf("expected at least one snapshot row, got:\n%s", r.out)
	}
	return rows[0].Hash
}

func (r *listResult) MustHaveTag(tag string) *listResult {
	r.t.Helper()
	for _, row := range r.SnapshotRows() {
		for _, existing := range row.Tags {
			if existing == tag {
				return r
			}
		}
	}
	r.t.Fatalf("expected list output to contain tag %q, got:\n%s", tag, r.out)
	return r
}

func (r *repo) WithFile(relPath, content string) *repo {
	r.h.WithFile(relPath, content)
	return r
}

func (r *repo) RemoveFile(relPath string) *repo {
	r.h.RemoveFile(relPath)
	return r
}

func (r *repo) run(args ...string) *commandResult {
	r.h.t.Helper()
	return newCommandResult(r.h.t, run(r.h.t, r.h.bin, args...))
}

func (r *repo) Backup(extraArgs ...string) *commandResult {
	r.h.t.Helper()
	args := append([]string{"backup"}, r.h.sourceArgs...)
	args = append(args, r.authArgs...)
	args = append(args, extraArgs...)
	return r.run(args...)
}

func (r *repo) List(extraArgs ...string) *listResult {
	r.h.t.Helper()
	args := append([]string{"list"}, r.authArgs...)
	args = append(args, extraArgs...)
	return &listResult{commandResult: r.run(args...)}
}

func (r *repo) Check(extraArgs ...string) *commandResult {
	r.h.t.Helper()
	args := append([]string{"check"}, r.authArgs...)
	args = append(args, extraArgs...)
	return r.run(args...)
}

func (r *repo) RestoreZip(name string, extraArgs ...string) *restoreZipResult {
	r.h.t.Helper()
	zipPath := filepath.Join(r.h.restoreRoot, name)
	args := append([]string{"restore"}, r.authArgs...)
	args = append(args, "-output", zipPath)
	args = append(args, extraArgs...)
	run(r.h.t, r.h.bin, args...)
	return &restoreZipResult{t: r.h.t, zipPath: zipPath}
}

func (r *repo) RestoreDir(name string, extraArgs ...string) *restoreDirResult {
	r.h.t.Helper()
	dirPath := filepath.Join(r.h.restoreRoot, name)
	args := append([]string{"restore"}, r.authArgs...)
	args = append(args, "-format", "dir", "-output", dirPath)
	args = append(args, extraArgs...)
	run(r.h.t, r.h.bin, args...)
	return &restoreDirResult{t: r.h.t, dirPath: dirPath}
}

func (r *repo) Forget(extraArgs ...string) *forgetResult {
	r.h.t.Helper()
	args := append([]string{"forget"}, r.authArgs...)
	args = append(args, extraArgs...)
	out := r.run(args...)
	return parseForgetResult(r.h.t, out)
}

func (r *repo) Diff(left, right string, extraArgs ...string) *diffResult {
	r.h.t.Helper()
	args := []string{"diff", left, right}
	args = append(args, r.authArgs...)
	args = append(args, extraArgs...)
	out := r.run(args...)
	return parseDiffResult(r.h.t, out)
}

func (r *repo) Ls(extraArgs ...string) *lsResult {
	r.h.t.Helper()
	args := append([]string{"ls"}, r.authArgs...)
	args = append(args, extraArgs...)
	out := r.run(args...)
	return parseLsResult(r.h.t, out)
}

func (r *repo) BreakLock() *commandResult {
	r.h.t.Helper()
	args := append([]string{"break-lock"}, r.authArgs...)
	return r.run(args...)
}

func (r *repo) Cat(extraArgs ...string) *commandResult {
	r.h.t.Helper()
	args := append([]string{"cat"}, r.authArgs...)
	args = append(args, extraArgs...)
	return r.run(args...)
}

func parseSnapshotRows(t *testing.T, out string) []snapshotRow {
	t.Helper()
	var rows []snapshotRow
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cols := strings.Split(trimmed, "|")
		if len(cols) < 8 {
			continue
		}
		seqText := strings.TrimSpace(cols[1])
		if seqText == "" || seqText == "SEQ" || strings.ContainsAny(seqText, "-+") {
			continue
		}
		seq, err := strconv.Atoi(seqText)
		if err != nil {
			t.Fatalf("parse snapshot seq %q: %v\noutput:\n%s", seqText, err, out)
		}
		row := snapshotRow{
			Seq:     seq,
			Created: strings.TrimSpace(cols[2]),
			Hash:    strings.TrimSpace(cols[3]),
			Source:  strings.TrimSpace(cols[4]),
			Account: strings.TrimSpace(cols[5]),
			Path:    strings.TrimSpace(cols[6]),
		}
		tagsText := strings.TrimSpace(cols[7])
		if tagsText != "" {
			for _, tag := range strings.Split(tagsText, ",") {
				row.Tags = append(row.Tags, strings.TrimSpace(tag))
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func parseDiffResult(t *testing.T, result *commandResult) *diffResult {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(result.out), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected diff output, got empty output")
	}
	dr := &diffResult{commandResult: result}
	if header := lines[0]; strings.HasPrefix(header, "Diffing ") && strings.Contains(header, " vs ") {
		parts := strings.SplitN(strings.TrimPrefix(header, "Diffing "), " vs ", 2)
		if len(parts) == 2 {
			dr.Ref1 = strings.TrimSpace(parts[0])
			dr.Ref2 = strings.TrimSpace(parts[1])
		}
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		changeType := strings.TrimSpace(parts[0])
		switch changeType {
		case "A":
			changeType = "added"
		case "M":
			changeType = "modified"
		case "D":
			changeType = "removed"
		}
		dr.Changes = append(dr.Changes, diffChange{Type: changeType, Path: strings.TrimSpace(parts[1])})
	}
	return dr
}

func (r *diffResult) MustHaveChange(changeType, path string) *diffResult {
	r.t.Helper()
	for _, change := range r.Changes {
		if change.Type == changeType && change.Path == path {
			return r
		}
	}
	r.t.Fatalf("expected diff output to contain %s %s, got:\n%s", changeType, path, r.out)
	return r
}

func (r *diffResult) MustHaveNoChanges() *diffResult {
	r.t.Helper()
	if len(r.Changes) != 0 {
		r.t.Fatalf("expected diff output to contain no changes, got:\n%s", r.out)
	}
	return r
}

var lsEntrySuffixRE = regexp.MustCompile(` \([^)]*\)$`)

func parseLsResult(t *testing.T, result *commandResult) *lsResult {
	t.Helper()
	lines := strings.Split(result.out, "\n")
	if len(lines) == 0 {
		t.Fatalf("expected ls output, got empty output")
	}
	lr := &lsResult{commandResult: result}
	if header := strings.TrimSpace(lines[0]); strings.HasPrefix(header, "Listing files for snapshot: ") {
		rest := strings.TrimPrefix(header, "Listing files for snapshot: ")
		if idx := strings.Index(rest, " (Created: "); idx >= 0 {
			lr.SnapshotRef = rest[:idx]
		}
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "entries listed in ") {
			continue
		}
		label := strings.TrimLeft(trimmed, "*-│├└─ ")
		label = lsEntrySuffixRE.ReplaceAllString(label, "")
		if label != "" {
			lr.Entries = append(lr.Entries, label)
		}
	}
	return lr
}

func (r *lsResult) MustContainEntry(name string) *lsResult {
	r.t.Helper()
	for _, entry := range r.Entries {
		if entry == name {
			return r
		}
	}
	r.t.Fatalf("expected ls output to contain entry %q, got entries %v\nraw output:\n%s", name, r.Entries, r.out)
	return r
}

func (r *lsResult) MustNotContainEntry(name string) *lsResult {
	r.t.Helper()
	for _, entry := range r.Entries {
		if entry == name {
			r.t.Fatalf("expected ls output not to contain entry %q, got entries %v\nraw output:\n%s", name, r.Entries, r.out)
		}
	}
	return r
}

func parseForgetResult(t *testing.T, result *commandResult) *forgetResult {
	t.Helper()
	fr := &forgetResult{commandResult: result}
	for _, line := range strings.Split(result.out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "snapshots would be removed") {
			fr.DryRun = true
			if n, ok := leadingInt(line); ok {
				fr.WouldRemoveCount = n
			}
			continue
		}
		if strings.Contains(line, "snapshots have been removed") {
			if n, ok := leadingInt(line); ok {
				fr.RemovedCount = n
			}
			continue
		}
	}
	return fr
}

func leadingInt(s string) (int, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return n, true
}

func (r *forgetResult) MustBeDryRun() *forgetResult {
	r.t.Helper()
	if !r.DryRun {
		r.t.Fatalf("expected forget output to be a dry run, got:\n%s", r.out)
	}
	return r
}

func (r *forgetResult) MustWouldRemove(n int) *forgetResult {
	r.t.Helper()
	if r.WouldRemoveCount != n {
		r.t.Fatalf("expected forget output to report %d snapshots would be removed, got %d\nraw output:\n%s", n, r.WouldRemoveCount, r.out)
	}
	return r
}

func (r *forgetResult) MustRemove(n int) *forgetResult {
	r.t.Helper()
	if r.RemovedCount != n {
		r.t.Fatalf("expected forget output to report %d snapshots removed, got %d\nraw output:\n%s", n, r.RemovedCount, r.out)
	}
	return r
}

func (r *restoreZipResult) Path() string {
	r.t.Helper()
	return r.zipPath
}

func (r *restoreZipResult) MustContainFile(name string) *restoreZipResult {
	r.t.Helper()
	if !zipFileExists(r.t, r.zipPath, name) {
		r.t.Fatalf("expected zip restore to contain %s in %s", name, r.zipPath)
	}
	return r
}

func (r *restoreZipResult) MustNotContainFile(name string) *restoreZipResult {
	r.t.Helper()
	assertZipMissing(r.t, r.zipPath, name)
	return r
}

func (r *restoreZipResult) MustHaveFileContent(name, want string) *restoreZipResult {
	r.t.Helper()
	if got := readZipFile(r.t, r.zipPath, name); got != want {
		r.t.Fatalf("restore zip content mismatch for %s: got %q, want %q", name, got, want)
	}
	return r
}

func (r *restoreDirResult) Path() string {
	r.t.Helper()
	return r.dirPath
}

func (r *restoreDirResult) MustContainFile(relPath string) *restoreDirResult {
	r.t.Helper()
	fullPath := filepath.Join(r.dirPath, filepath.FromSlash(relPath))
	if _, err := os.Stat(fullPath); err != nil {
		r.t.Fatalf("expected restore dir to contain %s: %v", relPath, err)
	}
	return r
}

func (r *restoreDirResult) MustNotContainFile(relPath string) *restoreDirResult {
	r.t.Helper()
	fullPath := filepath.Join(r.dirPath, filepath.FromSlash(relPath))
	if _, err := os.Stat(fullPath); err == nil {
		r.t.Fatalf("expected restore dir not to contain %s", relPath)
	}
	return r
}

func (r *restoreDirResult) MustHaveFileContent(relPath, want string) *restoreDirResult {
	r.t.Helper()
	fullPath := filepath.Join(r.dirPath, filepath.FromSlash(relPath))
	b, err := os.ReadFile(fullPath)
	if err != nil {
		r.t.Fatalf("restore dir missing %s: %v", relPath, err)
	}
	if got := string(b); got != want {
		r.t.Fatalf("restore dir content mismatch for %s: got %q, want %q", relPath, got, want)
	}
	return r
}

// Lowercase methods for backward compatibility removed.

// ---------------------------------------------------------------------------
// ZIP helpers
// ---------------------------------------------------------------------------

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
