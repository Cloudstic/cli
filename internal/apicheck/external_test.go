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

// vendorSDKPrefixes are import-path prefixes that mark a provider SDK.
// Matching on the path keeps this readable as the list grows.
var vendorSDKPrefixes = []string{
	"github.com/aws/aws-sdk-go",
	"google.golang.org/api",
	"google.golang.org/grpc",
	"google.golang.org/protobuf",
	"cloud.google.com/go",
	"golang.org/x/oauth2",
}

// sdkBearingPackages are the public packages allowed to depend on a provider
// SDK, because carrying one is their entire purpose: each is a single
// provider's implementation of a contract declared elsewhere.
//
// Membership is a deliberate API-design decision, not an exemption for
// convenience. Adding an entry means "importing this package should cost a
// cloud SDK", and every package NOT listed here is a promise that it does not.
var sdkBearingPackages = map[string]bool{
	"github.com/cloudstic/cli/pkg/crypto/kms":      true,
	"github.com/cloudstic/cli/pkg/keychain/kms":    true,
	"github.com/cloudstic/cli/pkg/source/gdrive":   true,
	"github.com/cloudstic/cli/pkg/source/onedrive": true,
	"github.com/cloudstic/cli/pkg/store/s3":        true,
}

// TestPublicPackagesPullNoVendorSDK pins the other half of RFC 0022: the
// public API stays cheap to depend on. Importing a package to write a source,
// implement a store, read a profile, or open a repository must not drag in a
// provider SDK the caller never asked for.
//
// It sweeps every public package rather than a hand-listed few. The earlier
// version checked only pkg/source and pkg/store, which is exactly how
// pkg/crypto came to bundle the AWS KMS client through three stages of the RFC
// that was written to prevent it (RFC 0022 §6): the property held everywhere
// anyone had thought to look. Enumerating from `go list` instead means a new
// package is covered the day it is created, and the allowlist above is the
// only way to opt out.
func TestPublicPackagesPullNoVendorSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; skipped under -short")
	}
	requireGoToolchain(t)

	// "." is the root client package; ./pkg/... is everything else public.
	// internal/ and cmd/ are deliberately out of scope — they are allowed to
	// import anything, and cmd/cloudstic necessarily imports every provider.
	out, err := runGo(t, "../..", "list", ".", "./pkg/...")
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	pkgs := strings.Fields(out)
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages; the sweep would vacuously pass")
	}

	for _, pkg := range pkgs {
		if sdkBearingPackages[pkg] {
			continue
		}
		deps, err := runGo(t, "../..", "list", "-deps", pkg)
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, deps)
		}
		for _, dep := range strings.Fields(deps) {
			for _, sdk := range vendorSDKPrefixes {
				if strings.HasPrefix(dep, sdk) {
					t.Errorf("%s depends on %s.\n"+
						"Public packages must stay free of provider SDKs so that using one "+
						"costs nothing extra. Either move the SDK-bound code into its own "+
						"subpackage (as pkg/crypto/kms and pkg/store/s3 do), or, if carrying "+
						"the SDK really is this package's purpose, add it to "+
						"sdkBearingPackages with a reason.", pkg, dep)
					break
				}
			}
		}
	}
}

// TestEverySDKBearingPackageStillExists keeps the allowlist honest. An entry
// left behind after a package is renamed or removed silently widens the
// exemption for whatever takes its path next.
func TestEverySDKBearingPackageStillExists(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; skipped under -short")
	}
	requireGoToolchain(t)

	for pkg := range sdkBearingPackages {
		if out, err := runGo(t, "../..", "list", pkg); err != nil {
			t.Errorf("sdkBearingPackages names %q, which does not resolve: %v\n%s\n"+
				"Remove the stale entry rather than leaving the exemption in place.",
				pkg, err, out)
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
