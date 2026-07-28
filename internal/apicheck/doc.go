// Package apicheck holds repository-hygiene tests that inspect the module
// rather than exercise it: the public API boundary (RFC 0022) and the
// goreleaser ldflags wiring.
//
// They live here, outside the packages they analyze, for two reasons. They
// work by loading packages by import path, so they never needed to be inside
// the package under test. And keeping golang.org/x/tools out of the root
// package's test binary keeps that package's type-check graph small — folding
// it in grew the graph by ~50 packages and was enough to make staticcheck
// lose the fact that testing.T.Fatal never returns, producing a spurious
// SA5011 in an unrelated file.
package apicheck
