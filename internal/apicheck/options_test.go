package apicheck

import (
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// rootReExport matches a root-package re-export of an engine option, i.e. the
//
//	WithBackupDryRun = engine.WithBackupDryRun
//
// lines in client.go's var blocks.
var rootReExport = regexp.MustCompile(`=\s*engine\.(With\w+)`)

// TestEveryEngineOptionIsReExported checks that each exported With* option
// constructor in internal/engine is reachable from the root package.
//
// The root package is a hand-maintained facade over internal/engine: adding an
// option to the engine does not surface it to library callers until someone
// remembers to mirror it. The failure is silent and asymmetric — a public
// function keeps compiling while one of the options it advertises cannot be
// constructed from outside the module.
//
// That is not hypothetical. WithWorkstationDryRun existed and worked, but
// PlanWorkstationSetup — which is public — could not be given it.
//
// If an engine option is deliberately not for callers, unexport it rather than
// listing it below; an exported option nobody can reach is what this guards.
func TestEveryEngineOptionIsReExported(t *testing.T) {
	// Exported engine options that are deliberately not re-exported, with the
	// reason. Keep this empty if you can.
	internalOnly := map[string]string{}

	engineOpts := exportedOptionCtors(t, "github.com/cloudstic/cli/internal/engine")
	if len(engineOpts) == 0 {
		t.Fatal("found no exported With* constructors in internal/engine; the loader is wrong")
	}

	// Scan every file in the root package, not just client.go: the facade is
	// split across per-domain files (backup.go, retention.go, ...), and a test
	// pinned to one filename silently stops checking the rest.
	rootFiles, err := filepath.Glob(filepath.Join(moduleRoot(t), "*.go"))
	if err != nil {
		t.Fatalf("glob root package: %v", err)
	}
	reExported := map[string]bool{}
	for _, path := range rootFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range rootReExport.FindAllStringSubmatch(string(src), -1) {
			reExported[m[1]] = true
		}
	}

	var missing []string
	for _, opt := range engineOpts {
		if reExported[opt] {
			continue
		}
		if _, ok := internalOnly[opt]; ok {
			continue
		}
		missing = append(missing, opt)
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("internal/engine exports %d option constructor(s) the root package does not "+
			"re-export, so external callers cannot pass them:\n  %s\n\n"+
			"Add `%s = engine.%s` to the matching var block in the root package, or unexport the "+
			"option if it is not meant for callers.",
			len(missing), strings.Join(missing, "\n  "), missing[0], missing[0])
	}
}

// exportedOptionCtors returns every exported function in pkgPath named With*
// that returns exactly one value — the functional-options shape used
// throughout internal/engine.
func exportedOptionCtors(t *testing.T, pkgPath string) []string {
	t.Helper()

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes}
	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil {
		t.Fatalf("packages.Load %s: %v", pkgPath, err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("errors loading %s, see above", pkgPath)
	}

	var out []string
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			fn, ok := scope.Lookup(name).(*types.Func)
			if !ok || !fn.Exported() || !strings.HasPrefix(name, "With") {
				continue
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok || sig.Results().Len() != 1 {
				continue
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
