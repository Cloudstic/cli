package apicheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// goPackageArg matches a ./-relative Go package pattern as it appears in a
// workflow's shell command, e.g. "./pkg/secretref/..." in
//
//	run: go test ./pkg/secretref/... ./cmd/cloudstic
var goPackageArg = regexp.MustCompile(`\./[\w./-]+`)

// TestWorkflowGoPackagePathsExist verifies that every ./-relative Go package
// path named in a GitHub workflow still resolves to a directory.
//
// Workflows pin package paths as plain strings, so moving a package leaves
// them stale. Some of those jobs are the only thing exercising a code path:
// verify-platform-build-paths runs the secretref backends on macOS and Windows
// precisely because the Keychain and Credential Manager implementations are
// build-tagged out everywhere else. A stale path there means that coverage
// silently stops happening on the platforms it was written for.
//
// Promoting internal/secretref to pkg/secretref broke exactly this, in both
// ci.yml and release.yml. It failed loudly in CI — but only after a push, and
// release.yml would not have failed until a release.
func TestWorkflowGoPackagePathsExist(t *testing.T) {
	root := moduleRoot(t)
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	if len(workflows) == 0 {
		t.Fatal("no workflows found; the glob is wrong")
	}

	for _, wf := range workflows {
		data, err := os.ReadFile(wf)
		if err != nil {
			t.Fatalf("read %s: %v", wf, err)
		}
		name := filepath.Base(wf)

		for _, line := range strings.Split(string(data), "\n") {
			// Only lines that actually invoke the go tool on packages.
			if !strings.Contains(line, "go test") && !strings.Contains(line, "go build") &&
				!strings.Contains(line, "go vet") {
				continue
			}
			for _, pkg := range uniq(goPackageArg.FindAllString(line, -1)) {
				dir := filepath.Join(root, strings.TrimSuffix(strings.TrimSuffix(pkg, "..."), "/"))
				info, err := os.Stat(dir)
				if err != nil || !info.IsDir() {
					t.Errorf("%s names Go package path %q, which does not exist "+
						"(looked for %s).\nA package was probably moved without updating the "+
						"workflow.\n  line: %s", name, pkg, dir, strings.TrimSpace(line))
				}
			}
		}
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
