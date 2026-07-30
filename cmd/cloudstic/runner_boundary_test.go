package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
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

// TestRunnerMethodsAreIOPrimitivesOnly pins the runner's method set to the I/O
// primitives AGENTS.md describes it as holding.
//
// The list had drifted: four profiles-domain workflows had accumulated as
// methods — two of them mutating a profile config and returning a process exit
// code, one of them writing the profiles file. Being methods on the runner made
// them look like runner capabilities and made command flow inseparable from
// prompting. They are free functions taking the runner now, the same shape the
// commands and the print helpers already use.
//
// Adding a genuine primitive here is fine; adding a domain workflow is what this
// catches. If a new entry needs a profile.Config, writes a file, or returns an
// exit code, it belongs outside this list.
func TestRunnerMethodsAreIOPrimitivesOnly(t *testing.T) {
	allowed := map[string]bool{
		// Output and process result.
		"fail": true, "parseError": true, "writeJSON": true,
		"failJSONFlagConflict": true, "jsonEnabled": true, "printUsage": true,
		// Input.
		"canPrompt": true, "lineReader": true, "promptLine": true,
		"promptValidatedLine": true, "promptConfirm": true, "promptSelect": true,
		"promptSecret": true,
		// Connection and dispatch plumbing.
		"openClient": true, "withArgs": true, "withUsage": true,
	}

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	fset := token.NewFileSet()
	seen := 0
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "runner" {
				continue
			}
			seen++
			if !allowed[fn.Name.Name] {
				t.Errorf("%s: runner must not have method %q; make it a free function "+
					"taking the runner, as the commands and print helpers do",
					path, fn.Name.Name)
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no runner methods; this test is not checking anything")
	}
}
