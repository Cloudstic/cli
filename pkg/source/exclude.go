package source

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudstic/cli/internal/pathmatch"
)

// excludeRule is a single parsed exclude pattern.
type excludeRule struct {
	pattern  *pathmatch.Pattern // the compiled glob
	negate   bool               // true if the original line started with '!'
	dirOnly  bool               // true if the original line ended with '/'
	hasSlash bool               // true if the pattern contains a '/' (anchored to path)
}

// ExcludeMatcher evaluates gitignore-style exclude patterns against relative
// file paths. Patterns are evaluated in order; the last matching rule wins.
type ExcludeMatcher struct {
	rules []excludeRule
}

// NewExcludeMatcher compiles the given pattern strings into a matcher.
// Supported syntax (subset of gitignore):
//
//   - Blank lines and lines starting with '#' are ignored.
//   - A trailing '/' matches only directories.
//   - A leading '!' negates the pattern (re-includes a previously excluded path).
//   - '*' matches anything except '/'.
//   - '**' matches zero or more path segments.
//   - Patterns without '/' match against the file/dir name in any directory.
//   - Patterns with '/' are anchored to the root of the walk.
//
// The glob syntax itself is internal/pathmatch's: path.Match per segment, plus
// '**' for zero or more segments. Only the gitignore layer — negation,
// directory-only, anchoring, last-rule-wins — lives here.
func NewExcludeMatcher(patterns []string) *ExcludeMatcher {
	var rules []excludeRule
	for _, raw := range patterns {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		r := excludeRule{}

		// Negation.
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}

		// Directory-only.
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		// Leading '/' just anchors — strip it but mark as anchored.
		line = strings.TrimPrefix(line, "/")

		// If the pattern contains a slash, it is anchored to the root.
		r.hasSlash = strings.Contains(line, "/")

		// A pattern that will not compile is dropped rather than reported: this
		// constructor has never returned an error, and the matching it replaces
		// discarded path.Match's error and treated a malformed pattern as one
		// that matches nothing. Dropping the rule is that same outcome, reached
		// once at compile time instead of on every candidate.
		compiled, err := pathmatch.Compile(line, false)
		if err != nil {
			continue
		}
		r.pattern = compiled
		rules = append(rules, r)
	}
	return &ExcludeMatcher{rules: rules}
}

// ParseExcludeFile reads patterns from a file (one per line) and returns them.
// Comment lines (#) and blank lines are preserved for NewExcludeMatcher to handle.
func ParseExcludeFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		patterns = append(patterns, scanner.Text())
	}
	return patterns, scanner.Err()
}

// ExcludeHash is the canonical fingerprint of a set of exclude patterns, as
// recorded on a snapshot.
//
// It is repository format, not a convenience. A backup writes it, and the next
// backup compares its own against the previous snapshot's to decide whether the
// exclude set changed: when it did, the incremental change feed cannot be
// trusted — files newly excluded must be dropped and newly included files found
// — so the engine falls back to a full rescan. Two callers computing it
// differently therefore do not merely disagree about a string; one of them
// silently skips that rescan, or forces one on every run.
//
// The patterns are joined in the order given, so this is a hash of the exclude
// *list*, not of the set: reordering the same patterns reads as a change. That
// is deliberate and cheap — a spurious full rescan is safe, a skipped one is
// not.
func ExcludeHash(patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(patterns, "\n")))
	return hex.EncodeToString(sum[:])
}

// Empty returns true if the matcher has no rules.
func (m *ExcludeMatcher) Empty() bool {
	return len(m.rules) == 0
}

// Excludes reports whether the given relative path should be excluded.
// isDir must be true when the path refers to a directory.
// relPath must use forward slashes as separators.
func (m *ExcludeMatcher) Excludes(relPath string, isDir bool) bool {
	// Normalise to forward slash for consistent matching.
	relPath = filepath.ToSlash(relPath)

	excluded := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if matchRule(r, relPath) {
			excluded = !r.negate
		}
	}
	return excluded
}

// matchRule checks whether a single rule matches relPath.
func matchRule(r excludeRule, relPath string) bool {
	if r.hasSlash {
		// Anchored pattern: match against the full relative path.
		return r.pattern.Match(relPath)
	}

	// Unanchored: match against the basename, but also try the full path
	// so that e.g. "vendor" matches both "vendor" and "a/vendor".
	if r.pattern.Match(baseName(relPath)) {
		return true
	}
	return r.pattern.Match(relPath)
}

// IsUnderExcludedDir reports whether true if relPath falls under any of the excluded
// directory prefixes. Each entry in excludedDirs must end with '/'.
func IsUnderExcludedDir(relPath string, excludedDirs []string) bool {
	for _, prefix := range excludedDirs {
		if strings.HasPrefix(relPath, prefix) {
			return true
		}
	}
	return false
}

// baseName returns the last component of a slash-separated path.
func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
