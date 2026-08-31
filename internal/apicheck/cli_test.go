package apicheck

import (
	"strings"
	"testing"
)

// cliPackage is the CLI binary, which is this module's own reference consumer
// of the public API.
const cliPackage = "github.com/cloudstic/cli/cmd/cloudstic"

// cliPrivateInternals are the internal packages cmd/cloudstic may import,
// because they are parts of the CLI itself rather than of the library.
//
// The distinction is what the package would mean to a library caller. A
// terminal dashboard, a console progress reporter, a workstation onboarding
// wizard and a config-directory resolver are things *this program* is; nobody
// embedding Cloudstic wants them, and re-exporting them would enlarge the
// public surface to no one's benefit. The repository format, the operation
// engine and the store decorator chain are the opposite: they are what the
// library is for, and every one of them is already reachable through the root
// package.
var cliPrivateInternals = map[string]bool{
	"github.com/cloudstic/cli/internal/app":         true,
	"github.com/cloudstic/cli/internal/logger":      true,
	"github.com/cloudstic/cli/internal/paths":       true,
	"github.com/cloudstic/cli/internal/tui":         true,
	"github.com/cloudstic/cli/internal/tui/forms":   true,
	"github.com/cloudstic/cli/internal/ui":          true,
	"github.com/cloudstic/cli/internal/workstation": true,
}

// TestCLIUsesOnlyPublicAPI pins the CLI to the same API an external caller
// gets, for everything the library actually exposes.
//
// The rest of this package proves the public surface is *complete*
// (TestEveryEngineOptionIsReExported), that it carries no unaliased internal
// type (TestPublicAPIHasNoUnaliasedInternalTypes), and that a separate module
// can build against it (TestExternalModuleImplementsPublicContracts). None of
// them proves it is *used*: the CLI is the only real consumer in this
// repository, and it had quietly opted out — importing internal/core and
// internal/engine in eleven files to spell FileType, SourceInfo,
// WithRestorePath and ErrRepoLocked, every one of which already had a public
// alias. A public option that was awkward, or missing, or wrong would not have
// been noticed by anything.
//
// Test files are covered too, deliberately. A result fixture assembled from
// engine types is the same bypass, and is where it tends to start.
func TestCLIUsesOnlyPublicAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; skipped under -short")
	}
	requireGoToolchain(t)

	out, err := runGo(t, ".",
		"list", "-f", "{{join .Imports \"\\n\"}}{{\"\\n\"}}{{join .TestImports \"\\n\"}}",
		cliPackage)
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", cliPackage, err, out)
	}

	seen := map[string]bool{}
	for imp := range strings.SplitSeq(out, "\n") {
		imp = strings.TrimSpace(imp)
		if imp == "" || seen[imp] || !isInternalPath(imp) || cliPrivateInternals[imp] {
			continue
		}
		seen[imp] = true
		t.Errorf("cmd/cloudstic imports %q directly.\n"+
			"The CLI is the reference consumer of this module's public API and must reach "+
			"library concerns the way any other caller does — through the root package or pkg/. "+
			"If what it needs is genuinely missing, re-export it (see repo.go, query.go, restore.go "+
			"for the alias pattern) rather than reaching past the boundary. If the package is part "+
			"of the CLI rather than of the library, add it to cliPrivateInternals with the reason.",
			imp)
	}
}
