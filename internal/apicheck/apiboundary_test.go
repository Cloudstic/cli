package apicheck

import (
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestPublicAPIHasNoUnaliasedInternalTypes walks the exported API surface of
// this module's public packages (the root package plus everything under
// pkg/) and fails if it finds a type defined in an internal/ package that
// has no public alias anywhere in that surface.
//
// An internal type reachable from a public signature but without a public
// alias cannot be named, constructed, or used to implement an interface
// (e.g. pkg/source.Source) from a separate Go module — see RFC 0022. This
// test is the regression guard for that invariant: it does not forbid
// referencing internal types (the alias pattern used throughout client.go,
// e.g. "type RepoConfig = core.RepoConfig", is exactly how they should be
// exposed) — it only forbids an *unaliased* one leaking through.
func TestPublicAPIHasNoUnaliasedInternalTypes(t *testing.T) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
	}
	pkgs, err := packages.Load(cfg, "github.com/cloudstic/cli", "github.com/cloudstic/cli/pkg/...")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("errors loading public packages, see above")
	}

	// covered records every internal type that is reachable through a
	// public alias declaration somewhere in the public API surface.
	covered := map[types.Object]bool{}
	for _, p := range pkgs {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || !tn.Exported() || !tn.IsAlias() {
				continue
			}
			if named, ok := types.Unalias(tn.Type()).(*types.Named); ok {
				covered[named.Obj()] = true
			}
		}
	}

	var leaks []string
	seenNamed := map[types.Object]bool{}

	var walk func(typ types.Type, path string)
	walk = func(typ types.Type, path string) {
		switch tt := types.Unalias(typ).(type) {
		case *types.Named:
			obj := tt.Obj()
			if seenNamed[obj] {
				return
			}
			seenNamed[obj] = true
			// Unexported types can never be spelled from another package,
			// module-internal or not, so a defined type's choice to use one
			// as a signature detail (the func(*xConfig) functional-options
			// pattern, e.g. BackupOption) is intentional encapsulation, not
			// a boundary leak — there is no alias that could even be
			// written for an unexported identifier.
			pkg := obj.Pkg()
			if obj.Exported() && pkg != nil && isInternalPath(pkg.Path()) && !covered[obj] {
				leaks = append(leaks, path+" uses unaliased internal type "+tt.String())
			}
			walk(tt.Underlying(), path)
		case *types.Pointer:
			walk(tt.Elem(), path)
		case *types.Slice:
			walk(tt.Elem(), path)
		case *types.Array:
			walk(tt.Elem(), path)
		case *types.Map:
			walk(tt.Key(), path)
			walk(tt.Elem(), path)
		case *types.Chan:
			walk(tt.Elem(), path)
		case *types.Signature:
			if params := tt.Params(); params != nil {
				for i := 0; i < params.Len(); i++ {
					walk(params.At(i).Type(), path)
				}
			}
			if results := tt.Results(); results != nil {
				for i := 0; i < results.Len(); i++ {
					walk(results.At(i).Type(), path)
				}
			}
		case *types.Struct:
			for i := 0; i < tt.NumFields(); i++ {
				f := tt.Field(i)
				if f.Exported() {
					walk(f.Type(), path+"."+f.Name())
				}
			}
		case *types.Interface:
			for i := 0; i < tt.NumMethods(); i++ {
				m := tt.Method(i)
				walk(m.Type(), path+"."+m.Name()+"()")
			}
		}
	}

	for _, p := range pkgs {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			switch o := obj.(type) {
			case *types.Func:
				walk(o.Type(), p.PkgPath+"."+name+"()")
			case *types.Var:
				walk(o.Type(), p.PkgPath+"."+name)
			case *types.TypeName:
				walk(o.Type(), p.PkgPath+"."+name)
				// Method sets aren't reachable by walking the type's own
				// declaration (methods aren't part of a struct's field
				// list), so check them explicitly.
				named, ok := types.Unalias(o.Type()).(*types.Named)
				if !ok {
					continue
				}
				for _, recv := range []types.Type{named, types.NewPointer(named)} {
					mset := types.NewMethodSet(recv)
					for i := 0; i < mset.Len(); i++ {
						fn := mset.At(i).Obj().(*types.Func)
						if fn.Exported() {
							walk(fn.Type(), p.PkgPath+"."+name+"."+fn.Name()+"()")
						}
					}
				}
			}
		}
	}

	if len(leaks) > 0 {
		t.Errorf("public API exposes internal types with no public alias (RFC 0022) — "+
			"add a \"type X = <pkg>.<Type>\" alias in the appropriate public package:\n%s",
			strings.Join(leaks, "\n"))
	}
}

// isInternalPath reports whether path is an internal/ package of this
// module specifically — not a dependency's own internal/ packages, which
// are that dependency's business and are already reachable by anyone who
// imports it directly.
func isInternalPath(path string) bool {
	return strings.HasPrefix(path, "github.com/cloudstic/cli/internal/")
}
