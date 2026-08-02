package apicheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The documentation site is a separate repository, so nothing in this module's
// build breaks when a rename leaves its code samples describing an API that no
// longer exists. That is not hypothetical: the store and source packages were
// split into per-backend subpackages, and the docs kept calling
// store.NewLocalStore and source.NewGDriveSource for two releases — long enough
// that the Quick Start could not compile for anyone following it. Two doc pull
// requests touching that same page did not notice.
//
// These tests close that gap from this side, where the rename happens. Point
// CLOUDSTIC_DOCS_DIR at a checkout of the docs repository and they check every
// Go sample against the real API; without it they skip, so the ordinary build
// needs no second checkout.
//
//	CLOUDSTIC_DOCS_DIR=../cloudstic-doc go test ./internal/apicheck -run TestDocs
//
// What they do *not* do is compile the samples. Of the Go blocks on the API
// page, most are statement fragments referencing variables established in
// prose, and the rest are type declarations and bodiless signatures that either
// would not compile or would compile vacuously as fresh local declarations.
// Assembling them would take guesswork per block and fail for reasons that are
// not drift. Checking the names and the signatures catches what actually rots.

// docPackages maps the import alias the documentation uses to the package it
// means. The aliases are conventional rather than declared per snippet, which is
// exactly why a rename goes unnoticed: `store.NewLocalStore` reads fine long
// after the constructor moved to pkg/store/local.
// alsoSearch widens the lookup for aliases that read equally well as variable
// names. `source.GetFileStream(id)` is a method on a Source value and
// `store.NewWriter(ctx, key)` a method on a B2 store — neither is a package
// reference, and nothing in the surrounding text says so.
//
// Rather than guess from the symbol's name, which fails the moment a method is
// called NewWriter, a reference is accepted when the symbol exists anywhere in
// the package's family, methods included. A name that appears nowhere in the
// family is drift whichever way it was meant.
var alsoSearch = map[string][]string{
	"store": {
		"github.com/cloudstic/cli/pkg/store/local",
		"github.com/cloudstic/cli/pkg/store/s3",
		"github.com/cloudstic/cli/pkg/store/b2",
		"github.com/cloudstic/cli/pkg/store/sftp",
	},
	"source": {
		"github.com/cloudstic/cli/pkg/source/local",
		"github.com/cloudstic/cli/pkg/source/sftp",
		"github.com/cloudstic/cli/pkg/source/gdrive",
		"github.com/cloudstic/cli/pkg/source/onedrive",
	},
	"client":    {"github.com/cloudstic/cli"},
	"cloudstic": {"github.com/cloudstic/cli/internal/engine"},
}

var docPackages = map[string]string{
	"cloudstic":   "github.com/cloudstic/cli",
	"config":      "github.com/cloudstic/cli/pkg/config",
	"open":        "github.com/cloudstic/cli/pkg/open",
	"profile":     "github.com/cloudstic/cli/pkg/profile",
	"source":      "github.com/cloudstic/cli/pkg/source",
	"store":       "github.com/cloudstic/cli/pkg/store",
	"keychain":    "github.com/cloudstic/cli/pkg/keychain",
	"crypto":      "github.com/cloudstic/cli/pkg/crypto",
	"secretref":   "github.com/cloudstic/cli/pkg/secretref",
	"backends":    "github.com/cloudstic/cli/pkg/secretref/backends",
	"localsource": "github.com/cloudstic/cli/pkg/source/local",
	"sftpsource":  "github.com/cloudstic/cli/pkg/source/sftp",
	"gdrive":      "github.com/cloudstic/cli/pkg/source/gdrive",
	"onedrive":    "github.com/cloudstic/cli/pkg/source/onedrive",
	"localstore":  "github.com/cloudstic/cli/pkg/store/local",
	"s3store":     "github.com/cloudstic/cli/pkg/store/s3",
	"b2store":     "github.com/cloudstic/cli/pkg/store/b2",
	"sftpstore":   "github.com/cloudstic/cli/pkg/store/sftp",
}

// TestDocsReferenceOnlyRealSymbols fails when a code sample names an exported
// symbol the API does not have.
//
// This is the check that would have caught every stale reference found so far:
// nine per-operation verbose options, twenty-one find options, two renamed
// forget options, and fifteen constructors left behind by the backend split.
func TestDocsReferenceOnlyRealSymbols(t *testing.T) {
	dir := docsDir(t)

	known := map[string]map[string]bool{}
	for alias, pkg := range docPackages {
		syms := exportedSymbols(t, pkg)
		for _, extra := range alsoSearch[alias] {
			for name := range exportedSymbols(t, extra) {
				syms[name] = true
			}
		}
		known[alias] = syms
	}

	var problems []string
	forEachGoBlock(t, dir, func(file string, line int, block string) {
		for _, ref := range symbolRefs(block) {
			syms, watched := known[ref.alias]
			if !watched {
				continue
			}
			if !syms[ref.symbol] {
				problems = append(problems, fmt.Sprintf(
					"%s:%d: %s.%s exists nowhere in %s",
					file, line+ref.line, ref.alias, ref.symbol, docPackages[ref.alias]))
			}
		}
	})

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("the documentation names %d symbol(s) the API does not have:\n\t%s",
			len(problems), strings.Join(problems, "\n\t"))
	}
}

// TestDocsSignaturesMatch fails when a sample states a function signature that
// differs from the real one.
//
// Symbol existence alone would not have caught Find losing its variadic options
// for a query value: the name survived while every documented call became
// wrong. A block holding nothing but a signature is the documentation asserting
// that exact shape, so it is checkable directly.
func TestDocsSignaturesMatch(t *testing.T) {
	dir := docsDir(t)

	// Keyed by receiver and name, because a bare name collides: Client.Backup
	// and open.Backup are different functions, as are Client.List and
	// Store.List. Comparing the wrong pair reports drift that is not there.
	real := map[string]string{}
	for _, pkg := range docPackages {
		for key, sig := range exportedSignatures(t, pkg) {
			if _, seen := real[key]; !seen {
				real[key] = sig
			}
		}
	}

	var problems []string
	forEachGoBlock(t, dir, func(file string, line int, block string) {
		key, sig, ok := bodilessSignature(block)
		if !ok {
			return
		}
		want, known := real[key]
		if !known {
			return // covered by the symbol test, which names it better
		}
		if normalizeSig(sig) != normalizeSig(want) {
			problems = append(problems, fmt.Sprintf(
				"%s:%d: documented as\n\t\t%s\n\t  but the API declares\n\t\t%s",
				file, line, sig, want))
		}
	})

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("%d documented signature(s) do not match the API:\n\t%s",
			len(problems), strings.Join(problems, "\n\t"))
	}
}

func docsDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("CLOUDSTIC_DOCS_DIR")
	if dir == "" {
		t.Skip("set CLOUDSTIC_DOCS_DIR to a docs checkout to check its code samples")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("CLOUDSTIC_DOCS_DIR=%q: %v", dir, err)
	}
	requireGoToolchain(t)
	return dir
}

var goBlock = regexp.MustCompile("(?s)```go\n(.*?)```")

// ignoreMarker opts a block out, for illustrative pseudocode that names an API
// on purpose rather than by accident:
//
//	{/* apicheck:ignore explaining the idea, not calling the API */}
//
// It takes a reason so the exemption stays arguable. Without an escape hatch the
// first conceptual snippet forces a choice between rewriting prose that was
// never meant to compile and switching the whole check off.
var ignoreMarker = regexp.MustCompile(`\{/\*\s*apicheck:ignore`)

// forEachGoBlock visits every fenced Go block in the docs, with the file and the
// 1-indexed line the block's body starts on.
func forEachGoBlock(t *testing.T, dir string, fn func(file string, line int, block string)) {
	t.Helper()
	var found int
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".mdx" && filepath.Ext(path) != ".md" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		rel, _ := filepath.Rel(dir, path)
		for _, loc := range goBlock.FindAllStringSubmatchIndex(text, -1) {
			body := text[loc[2]:loc[3]]
			line := strings.Count(text[:loc[2]], "\n") + 1
			found++
			if precededByIgnore(text, loc[0]) {
				continue
			}
			fn(rel, line, body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found == 0 {
		t.Fatalf("no Go code blocks under %s; this test would pass vacuously", dir)
	}
}

// precededByIgnore reports whether the marker sits in the few lines before a
// fence, close enough to be about that block rather than an earlier one.
func precededByIgnore(text string, fenceStart int) bool {
	from := fenceStart - 200
	if from < 0 {
		from = 0
	}
	window := text[from:fenceStart]
	if i := strings.LastIndex(window, "```"); i >= 0 {
		window = window[i:] // do not read past the previous block
	}
	return ignoreMarker.MatchString(window)
}

type symbolRef struct {
	alias  string
	symbol string
	line   int
}

// qualifiedRef matches pkg.Symbol where Symbol is exported. The leading
// boundary keeps it off selectors on a value (cfg.Store.URI): a match must
// start the expression or follow an operator, bracket, or space.
var qualifiedRef = regexp.MustCompile(`(^|[^\w.])([a-z][a-z0-9]*)\.([A-Z][A-Za-z0-9_]*)`)

func symbolRefs(block string) []symbolRef {
	var refs []symbolRef
	for i, l := range strings.Split(block, "\n") {
		line := l
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx] // comments describe, they do not call
		}
		for _, m := range qualifiedRef.FindAllStringSubmatch(line, -1) {
			refs = append(refs, symbolRef{alias: m[2], symbol: m[3], line: i})
		}
	}
	return refs
}

// bodilessSignature reports a block that is exactly one function signature with
// no body, which is how the docs state an API shape.
func bodilessSignature(block string) (key, sig string, ok bool) {
	trimmed := strings.TrimSpace(block)
	if strings.Contains(trimmed, "\n") || !strings.HasPrefix(trimmed, "func ") {
		return "", "", false
	}
	if strings.HasSuffix(trimmed, "{") {
		return "", "", false
	}
	m := regexp.MustCompile(`^func (?:\([\w ]*\*?(\w+)\) )?([A-Z][A-Za-z0-9_]*)\(`).FindStringSubmatch(trimmed)
	if m == nil {
		return "", "", false
	}
	return sigKey(m[1], m[2]), trimmed, true
}

// sigKey identifies a function by receiver type and name, or by name alone for
// a package-level function.
func sigKey(recv, name string) string {
	if recv == "" {
		return name
	}
	return recv + "." + name
}

// normalizeSig collapses the differences that are formatting rather than
// meaning: `go doc` renders receivers and spacing its own way.
func normalizeSig(s string) string {
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`^func \([^)]*\) `).ReplaceAllString(s, "func ")
	return strings.TrimSpace(s)
}

// exportedSymbols returns every exported name a package declares.
func exportedSymbols(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out := goDoc(t, pkg)
	syms := map[string]bool{}
	re := regexp.MustCompile(`(?m)^(?:func|type|const|var)\s+(?:\([^)]*\)\s*)?([A-Z][A-Za-z0-9_]*)`)
	for _, m := range re.FindAllStringSubmatch(out, -1) {
		syms[m[1]] = true
	}
	// Grouped const/var blocks declare their names on continuation lines.
	block := regexp.MustCompile(`(?m)^\t([A-Z][A-Za-z0-9_]*)\s`)
	for _, m := range block.FindAllStringSubmatch(out, -1) {
		syms[m[1]] = true
	}
	if len(syms) == 0 {
		t.Fatalf("go doc %s returned no exported symbols", pkg)
	}
	return syms
}

// exportedSignatures returns each exported function's declared signature.
func exportedSignatures(t *testing.T, pkg string) map[string]string {
	t.Helper()
	sigs := map[string]string{}
	re := regexp.MustCompile(`(?m)^func (?:\([\w ]*\*?(\w+)\) )?([A-Z][A-Za-z0-9_]*)\(.*$`)
	for _, m := range re.FindAllStringSubmatch(goDoc(t, pkg), -1) {
		sigs[sigKey(m[1], m[2])] = m[0]
	}
	return sigs
}

func goDoc(t *testing.T, pkg string) string {
	t.Helper()
	cmd := exec.Command("go", "doc", "-all", pkg)
	cmd.Dir = ".."
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go doc -all %s: %v\n%s", pkg, err, out)
	}
	return string(out)
}
