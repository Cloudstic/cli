package e2e

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

// TestMain sets up the coverage directory tree. Each child process gets its own
// directory beneath it — see cleanEnv for why sharing one is not safe.
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
	}
	coverBase = coverDir

	code := m.Run()

	if outFile := os.Getenv("E2E_COVERAGE_OUT"); outFile != "" {
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+coverInputs(coverDir), "-o="+outFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: coverage conversion failed: %v\n%s\n", err, out)
		}
	}

	if ownsDir {
		_ = os.RemoveAll(coverDir)
	}
	// The object cache is bounded, not small: a full suite left 176 MB across
	// 274 entries, and it would accumulate a fresh copy per run.
	if dir := objectCacheDir(); dir != "" {
		_ = os.RemoveAll(dir)
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
		// Deliberately not t.TempDir(): this runs once and the binary is shared
		// by every test in the package, but t.TempDir() is scoped to whichever
		// test happened to win the race into buildOnce — it would delete the
		// binary the moment that test finished, and every test scheduled after
		// it would exec a path that no longer exists.
		//nolint:usetesting // see above; lifetime must outlive the calling test
		dir, err := os.MkdirTemp("", "cloudstic-e2e-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "cloudstic")
		// -tags crashinject links in the CLOUDSTIC_TEST_CRASH_AFTER_PUTS knob
		// (see cmd/cloudstic/crashinject.go), which a plain production build
		// never includes.
		cmd := exec.Command("go", "build", "-buildvcs=false", "-cover", "-tags", "crashinject", "-o", bin, "../cmd/cloudstic")
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

// coverBase is the directory the per-process coverage directories live under,
// and coverSeq names them apart. Both are set by TestMain.
var (
	coverBase string
	coverSeq  atomic.Int64
)

// cleanEnv builds the environment for a child cloudstic process: the test
// process's own, minus anything that would leak configuration into it, plus a
// coverage directory belonging to that child alone.
//
// The private coverage directory is what stops a Go runtime race from failing
// unrelated tests. A -cover binary rewrites covmeta.<hash> on every exit —
// it does not skip the write when the file is already there — via a temporary
// file named for the meta hash and the clock, with no pid in it. macOS
// quantizes UnixNano to microseconds, so two of these processes exiting in the
// same microsecond choose the same temporary path; one renames it away and the
// other fails:
//
//	coverage meta-data emit failed: ... rename ...: no such file or directory
//
// The binary writes that to stderr, and the run helpers fold stderr into the
// string a test asserts on, so it surfaced as an unrelated assertion failing
// with output the command never produced — a diff test reporting a coverage
// error. It reproduced in 5 of 25 attempts at 24 concurrent processes sharing
// one directory, and CI runs e2e with -parallel 8.
//
// Giving each process its own directory removes the shared path the race needs.
//
// Only the failure goes away, not a gap in the data: counter files are named
// with the pid and never collided, and every process writes identical meta
// content, so one winner was always enough. Measured before and after, the
// suite reports the same 38.4%.
// objectCacheDir is where the binary under test keeps its local object cache.
//
// It has to be set. The cache defaults to os.UserCacheDir(), so a run that
// said nothing would write into the developer's own ~/Library/Caches — and,
// worse, would carry entries between runs, so a test could be served an object
// that the repository it is testing never contained.
//
// One directory for the whole run rather than one per command, on purpose:
// e2e commands run in parallel, so this exercises the case the budget is
// derived from the directory for — several processes sharing a cache.
var objectCacheDir = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "cloudstic-e2e-objcache-")
	if err != nil {
		return ""
	}
	return dir
})

func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CLOUDSTIC_") || strings.HasPrefix(e, "GOCOVERDIR=") {
			continue
		}
		env = append(env, e)
	}
	if dir := objectCacheDir(); dir != "" {
		env = append(env, "CLOUDSTIC_OBJECT_CACHE_DIR="+dir)
	}
	if coverBase == "" {
		return env
	}
	dir := filepath.Join(coverBase, strconv.FormatInt(coverSeq.Add(1), 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Coverage is reporting, not correctness: a test must not fail because
		// its counters could not be placed.
		return env
	}
	return append(env, "GOCOVERDIR="+dir)
}

// coverInputs lists the per-process directories for `go tool covdata`, which
// takes a comma-separated set rather than a tree to walk.
//
// A full run produces around 400 of them: roughly 74 KB of argument, against an
// ARG_MAX near 1 MB, and about 50 MB of temporary files that TestMain removes.
// Both have an order of magnitude of headroom; past that this would need to
// merge in batches rather than pass every directory at once.
func coverInputs(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return base
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return base
	}
	return strings.Join(dirs, ",")
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

// runStdoutOnly runs bin and returns stdout alone, discarding stderr. Tests use
// this to check that a command's -json output is not contaminated by anything
// written to stderr — CombinedOutput, which every other helper here uses, would
// interleave the two and hide exactly that bug.
func runStdoutOnly(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	var stdout strings.Builder
	cmd.Stdout = &stdout
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("command %v failed: %v\nstderr:\n%s", args, err, stderr.String())
	}
	return stdout.String()
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

type findResult struct {
	*commandResult
	Result cloudstic.FindResult
}

// MustMatchPath returns the match reported at path, failing if there is none.
func (r *findResult) MustMatchPath(path string) cloudstic.FileMatch {
	r.t.Helper()
	for _, m := range r.Result.Matches {
		if m.Path() == path {
			return m
		}
	}
	r.t.Fatalf("expected a match at %q, got %v\nraw output:\n%s", path, r.matchedPaths(), r.out)
	return cloudstic.FileMatch{}
}

func (r *findResult) MustNotMatchPath(path string) *findResult {
	r.t.Helper()
	for _, m := range r.Result.Matches {
		if m.Path() == path {
			r.t.Fatalf("expected no match at %q, got %v", path, r.matchedPaths())
		}
	}
	return r
}

func (r *findResult) MustMatchPaths(want ...string) *findResult {
	r.t.Helper()
	got := r.matchedPaths()
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		r.t.Fatalf("matched paths = %v, want %v\nraw output:\n%s", got, wantSorted, r.out)
	}
	return r
}

func (r *findResult) MustHaveVersions(path string, n int) *findResult {
	r.t.Helper()
	m := r.MustMatchPath(path)
	if len(m.Versions) != n {
		r.t.Fatalf("match %q has %d versions, want %d\nraw output:\n%s", path, len(m.Versions), n, r.out)
	}
	return r
}

// MustHaveVersionInSnapshots asserts that the version at index i of the match at
// path appears in exactly n snapshots. Index 0 is the newest version.
func (r *findResult) MustHaveVersionInSnapshots(path string, version, n int) *findResult {
	r.t.Helper()
	m := r.MustMatchPath(path)
	if version >= len(m.Versions) {
		r.t.Fatalf("match %q has %d versions, no v%d\nraw output:\n%s", path, len(m.Versions), version+1, r.out)
	}
	if got := len(m.Versions[version].Snapshots); got != n {
		r.t.Fatalf("match %q v%d spans %d snapshots, want %d\nraw output:\n%s",
			path, version+1, got, n, r.out)
	}
	return r
}

func (r *findResult) MustHaveSearchedSnapshots(n int) *findResult {
	r.t.Helper()
	if r.Result.SnapshotsSearched != n {
		r.t.Fatalf("searched %d snapshots, want %d\nraw output:\n%s", r.Result.SnapshotsSearched, n, r.out)
	}
	return r
}

func (r *findResult) matchedPaths() []string {
	paths := make([]string, 0, len(r.Result.Matches))
	for _, m := range r.Result.Matches {
		paths = append(paths, m.Path())
	}
	return paths
}

type forgetResult struct {
	*commandResult
	DryRun           bool
	RemovedCount     int
	WouldRemoveCount int
}

type pruneResult struct {
	*commandResult
	ObjectsDeleted int
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

// BackupWithEnv runs backup with additional environment variables, without
// failing the test on a non-zero exit — used by tests that expect the
// process to exit abnormally, such as simulated crashes.
func (r *repo) BackupWithEnv(extraEnv []string, extraArgs ...string) (out string, exitCode int) {
	r.h.t.Helper()
	args := append([]string{"backup"}, r.h.sourceArgs...)
	args = append(args, r.authArgs...)
	args = append(args, extraArgs...)
	cmd := exec.Command(r.h.bin, args...)
	cmd.Env = append(cleanEnv(), extraEnv...)
	outBytes, err := cmd.CombinedOutput()
	out = string(outBytes)
	if err == nil {
		return out, 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return out, exitErr.ExitCode()
	}
	r.h.t.Fatalf("backup command failed to start: %v\n%s", err, out)
	return out, -1
}

// BackupPutCount runs `backup -debug -quiet` and returns the number of
// physical store Put calls it made, read off DebugStore's "PUT" log lines.
// Tests use this to find the last possible crash point in a backup without
// hardcoding an assumption about how many objects a given change produces.
func (r *repo) BackupPutCount(extraArgs ...string) int {
	r.h.t.Helper()
	args := append([]string{"backup", "-debug", "-quiet"}, r.h.sourceArgs...)
	args = append(args, r.authArgs...)
	args = append(args, extraArgs...)
	out := run(r.h.t, r.h.bin, args...)
	return strings.Count(out, "PUT   ")
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

// ForgetSnapshot forgets one named snapshot. That is a different code path from
// Forget's retention policy, which removes a batch and reports a count; this
// one names its target and either removes it or fails.
func (r *repo) ForgetSnapshot(id string, extraArgs ...string) *commandResult {
	r.h.t.Helper()
	args := append([]string{"forget", id}, r.authArgs...)
	args = append(args, extraArgs...)
	return r.run(args...)
}

func (r *repo) Prune(extraArgs ...string) *pruneResult {
	r.h.t.Helper()
	args := append([]string{"prune"}, r.authArgs...)
	args = append(args, extraArgs...)
	return parsePruneResult(r.h.t, r.run(args...))
}

func (r *repo) Diff(left, right string, extraArgs ...string) *diffResult {
	r.h.t.Helper()
	args := []string{"diff", left, right}
	args = append(args, r.authArgs...)
	args = append(args, extraArgs...)
	out := r.run(args...)
	return parseDiffResult(r.h.t, out)
}

// Find runs `cloudstic find` and returns its rendered output.
func (r *repo) Find(extraArgs ...string) *commandResult {
	r.h.t.Helper()
	args := append([]string{"find"}, r.authArgs...)
	args = append(args, extraArgs...)
	return r.run(args...)
}

// FindExpectFail runs a find that is expected to be rejected.
func (r *repo) FindExpectFail(extraArgs ...string) *commandResult {
	r.h.t.Helper()
	args := append([]string{"find"}, r.authArgs...)
	args = append(args, extraArgs...)
	return r.h.RunExpectFail(args...)
}

// FindJSON runs `cloudstic find -json` and decodes the result, so assertions
// can be made against the structure rather than against rendered text.
func (r *repo) FindJSON(extraArgs ...string) *findResult {
	r.h.t.Helper()
	args := append([]string{"find", "-json"}, r.authArgs...)
	args = append(args, extraArgs...)
	// find can write a warning to stderr on the same run that succeeds and
	// prints a JSON result to stdout (a -regex query with no cheap prefilter,
	// for instance). run()'s CombinedOutput would interleave the two and
	// corrupt the JSON, which is exactly the separation -json promises callers
	// — so this reads stdout alone, the same way a real script consuming the
	// output would.
	stdout := runStdoutOnly(r.h.t, r.h.bin, args...)
	out := newCommandResult(r.h.t, stdout)

	fr := &findResult{commandResult: out}
	out.MustUnmarshalJSON(&fr.Result)
	return fr
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

func parsePruneResult(t *testing.T, result *commandResult) *pruneResult {
	t.Helper()
	pr := &pruneResult{commandResult: result}
	for _, line := range strings.Split(result.out, "\n") {
		_, count, ok := strings.Cut(strings.TrimSpace(line), "Objects deleted:")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(count)); err == nil {
			pr.ObjectsDeleted = n
		}
	}
	return pr
}

// MustDeleteObjects asserts prune reclaimed something. The count itself is not
// pinned: it is a property of how the tree happened to be laid out, while
// "collected nothing at all" is the symptom of a forget that unlinked nothing.
func (r *pruneResult) MustDeleteObjects() *pruneResult {
	r.t.Helper()
	if r.ObjectsDeleted <= 0 {
		r.t.Fatalf("expected prune to delete objects, got %d\nraw output:\n%s", r.ObjectsDeleted, r.out)
	}
	return r
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
