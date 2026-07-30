package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRunnerHoldsNoConfigurationResolution keeps the runner to the I/O
// primitives it is meant to hold.
//
// It used to take a *globalFlags and resolve it internally, which meant a call
// reading as "open the client" also read the profiles file and applied
// flag-versus-profile precedence — a file read and a precedence decision
// invisible at all eleven call sites. Each command now resolves first and passes
// the result, which is what `init` and the `key` subcommands always did.
//
// This is a structural guard in the same spirit as
// TestGlobalFlagsHasNoConstructionMethods: it fails if runner.go regains a
// reference to the flag struct or to configuration resolution, since either
// would pull that hidden work back in.
func TestRunnerHoldsNoConfigurationResolution(t *testing.T) {
	banned := map[string]string{
		"globalFlags":           "the runner must not see the flag struct; take a resolved clientConfig instead",
		"resolveClientConfig":   "the runner must not resolve configuration; the command resolves and passes the result",
		"resolveBackupConfig":   "the runner must not resolve configuration; the command resolves and passes the result",
		"loadProfileStore":      "the runner must not read the profiles file",
		"newSecretResolver":     "the runner must not build a secret resolver",
		"backupConfigFromFlags": "the runner must not translate flags",
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "runner.go", nil, 0)
	if err != nil {
		t.Fatalf("ParseFile runner.go: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if reason, bad := banned[ident.Name]; bad {
			t.Errorf("runner.go references %q at %s: %s",
				ident.Name, fset.Position(ident.Pos()), reason)
		}
		return true
	})

	// Guard against the guard being vacuous: runner.go must still be the file
	// this test thinks it is.
	if len(file.Decls) == 0 {
		t.Fatal("runner.go parsed to no declarations; this test is not checking anything")
	}
	var sawOpenClient bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "openClient" && fn.Recv != nil {
			sawOpenClient = true
		}
	}
	if !sawOpenClient {
		t.Error("runner.go no longer declares (*runner).openClient; if it moved, move this guard with it")
	}
}
