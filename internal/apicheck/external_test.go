package apicheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// externalModDir is a real Go module, with its own go.mod and a replace
// directive pointing at this checkout. It lives under testdata/ so the go tool
// ignores it during normal builds.
const externalModDir = "testdata/externalmod"

// TestExternalModuleImplementsPublicContracts is the acceptance test for
// RFC 0022: a module outside github.com/cloudstic/cli must be able to implement
// source.Source, source.IncrementalSource and store.ObjectStore.
//
// It compiles the fixture rather than asserting anything about this module's
// own types, because that is the only thing that actually proves the claim —
// Go's internal/ rule is enforced at build time, from the importing module's
// path. The fixture's interface satisfaction is asserted by `var _ Iface =
// (*T)(nil)` declarations inside it, so a compile is a full check.
func TestExternalModuleImplementsPublicContracts(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a separate module; skipped under -short")
	}
	requireGoToolchain(t)

	out, err := runGo(t, externalModDir, "build", "./...")
	if err != nil {
		t.Fatalf("an external module must be able to implement the public contracts, "+
			"but building the fixture failed: %v\n%s", err, out)
	}
}

// TestExternalModuleImportsNoInternalPackage guards the boundary from the
// outside: the fixture must reach the contracts without naming any internal/
// package directly. Transitive internal/ dependencies are fine and expected —
// Go permits them, and they are what the alias re-exports rely on.
func TestExternalModuleImportsNoInternalPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; skipped under -short")
	}
	requireGoToolchain(t)

	out, err := runGo(t, externalModDir, "list", "-f", "{{join .Imports \"\\n\"}}", "./...")
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, imp := range strings.Split(strings.TrimSpace(out), "\n") {
		imp = strings.TrimSpace(imp)
		if imp == "" {
			continue
		}
		if isInternalPath(imp) {
			t.Errorf("the fixture imports %q directly; an external module cannot do that, "+
				"so whatever it needs from there must be re-exported from a public package", imp)
		}
	}
}

// TestExternalContractsPullNoVendorSDK pins the other half of RFC 0022: the
// contract packages stay cheap to depend on. Importing them to write a source
// or a store must not drag in a provider SDK the caller never asked for.
func TestExternalContractsPullNoVendorSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; skipped under -short")
	}
	requireGoToolchain(t)

	// Substrings of import paths that indicate a vendor SDK. Matching on the
	// path keeps this readable when the list grows.
	vendorSDKs := []string{
		"github.com/aws/aws-sdk-go",
		"google.golang.org/api",
		"google.golang.org/grpc",
		"google.golang.org/protobuf",
		"cloud.google.com/go",
		"golang.org/x/oauth2",
	}

	for _, pkg := range []string{
		"github.com/cloudstic/cli/pkg/source",
		"github.com/cloudstic/cli/pkg/store",
	} {
		out, err := runGo(t, ".", "list", "-deps", pkg)
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
		}
		for _, dep := range strings.Split(out, "\n") {
			dep = strings.TrimSpace(dep)
			for _, sdk := range vendorSDKs {
				if strings.HasPrefix(dep, sdk) {
					t.Errorf("%s depends on %s; the contract packages must stay free of "+
						"provider SDKs so implementing one costs nothing extra", pkg, dep)
				}
			}
		}
	}
}

func requireGoToolchain(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
}

// runGo runs the go tool in dir, which is interpreted relative to this
// package's directory.
func runGo(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs %s: %v", dir, err)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = abs
	// Inherit the ambient environment so GOCACHE/GOMODCACHE/GOFLAGS set by CI
	// apply, and keep the network out of it: everything resolves through the
	// replace directive and the module cache.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
