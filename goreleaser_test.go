package cloudstic

import (
	"go/types"
	"os"
	"regexp"
	"testing"

	"golang.org/x/tools/go/packages"
)

// ldflagXPattern matches a goreleaser "-X <importpath>.<Name>=<value>" entry,
// capturing the import path and the symbol name.
var ldflagXPattern = regexp.MustCompile(`-X\s+([\w./-]+)\.(\w+)=`)

// TestGoreleaserLdflagsTargetRealSymbols verifies that every "-X" symbol in
// .goreleaser.yml actually exists as a settable string variable.
//
// This guards a silent failure mode: `go build -X` writes to a symbol only if
// it exists, and the linker reports nothing when it does not. Renaming or
// moving one of the OAuth default variables without updating .goreleaser.yml
// therefore produces release binaries with empty Google/OneDrive client IDs —
// cloud auth breaks at runtime while every build and test stays green.
//
// See internal/sourceoauth/defaults.go for the declarations.
func TestGoreleaserLdflagsTargetRealSymbols(t *testing.T) {
	data, err := os.ReadFile(".goreleaser.yml")
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}

	matches := ldflagXPattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatal("no -X ldflags found in .goreleaser.yml; if they were removed " +
			"intentionally, delete this test with them")
	}

	// Group symbol names by the package that should declare them, so each
	// package is loaded once.
	wanted := map[string][]string{}
	for _, m := range matches {
		wanted[m[1]] = append(wanted[m[1]], m[2])
	}

	paths := make([]string, 0, len(wanted))
	for p := range wanted {
		paths = append(paths, p)
	}

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes}
	pkgs, err := packages.Load(cfg, paths...)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("errors loading packages named by -X ldflags, see above")
	}

	loaded := map[string]*packages.Package{}
	for _, p := range pkgs {
		loaded[p.PkgPath] = p
	}

	for path, names := range wanted {
		p, ok := loaded[path]
		if !ok || p.Types == nil {
			t.Errorf("-X names package %q, which does not exist", path)
			continue
		}
		for _, name := range names {
			obj := p.Types.Scope().Lookup(name)
			if obj == nil {
				t.Errorf("-X names %s.%s, which is not declared in that package "+
					"(the linker would silently ignore it)", path, name)
				continue
			}
			v, ok := obj.(*types.Var)
			if !ok {
				t.Errorf("-X names %s.%s, which is a %T, not a variable", path, name, obj)
				continue
			}
			// -X can only set variables of type string.
			if b, ok := v.Type().(*types.Basic); !ok || b.Kind() != types.String {
				t.Errorf("-X names %s.%s of type %s; -X can only set string variables",
					path, name, v.Type())
			}
		}
	}
}
