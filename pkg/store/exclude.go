package store

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// excludeRule is a single parsed exclude pattern.
type excludeRule struct {
	pattern  string // the cleaned glob pattern
	negate   bool   // true if the original line started with '!'
	dirOnly  bool   // true if the original line ended with '/'
	hasSlash bool   // true if the pattern contains a '/' (anchored to path)
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
		r.pattern = line
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
	pattern := r.pattern

	if r.hasSlash {
		// Anchored pattern: match against the full relative path.
		return globMatch(pattern, relPath)
	}

	// Unanchored: match against the basename, but also try the full path
	// so that e.g. "vendor" matches both "vendor" and "a/vendor".
	base := baseName(relPath)
	if globMatch(pattern, base) {
		return true
	}
	// Also try matching against every suffix of the path.
	return globMatch(pattern, relPath)
}

// globMatch matches pattern against name, supporting '*' (no slash) and '**'
// (zero or more path segments).
func globMatch(pattern, name string) bool {
	// Fast path: no '**' → use filepath.Match (which handles '*' and '?').
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, name)
		return ok
	}

	// Split on '**' and match each segment.
	return matchDoublestar(pattern, name)
}

// matchDoublestar handles patterns containing '**'.
func matchDoublestar(pattern, name string) bool {
	// Split on "**" — each part must match a contiguous section of the path.
	parts := strings.Split(pattern, "**")

	if len(parts) == 1 {
		// No '**' — should not reach here, but handle gracefully.
		ok, _ := filepath.Match(pattern, name)
		return ok
	}

	// First part must match a prefix.
	first := parts[0]
	if first != "" {
		// First must match beginning of name.
		first = strings.TrimSuffix(first, "/")
		if first != "" {
			segments := strings.Split(name, "/")
			firstSegs := strings.Split(first, "/")
			if len(segments) < len(firstSegs) {
				return false
			}
			for i, fp := range firstSegs {
				ok, _ := filepath.Match(fp, segments[i])
				if !ok {
					return false
				}
			}
			name = strings.Join(segments[len(firstSegs):], "/")
		}
	}

	// Last part must match a suffix.
	last := parts[len(parts)-1]
	if last != "" {
		last = strings.TrimPrefix(last, "/")
		if last != "" {
			segments := strings.Split(name, "/")
			lastSegs := strings.Split(last, "/")
			if len(segments) < len(lastSegs) {
				return false
			}
			offset := len(segments) - len(lastSegs)
			for i, lp := range lastSegs {
				ok, _ := filepath.Match(lp, segments[offset+i])
				if !ok {
					return false
				}
			}
			name = strings.Join(segments[:offset], "/")
		}
	}

	// Middle parts (if any) must each match somewhere in the remaining path.
	for i := 1; i < len(parts)-1; i++ {
		mid := strings.Trim(parts[i], "/")
		if mid == "" {
			continue
		}
		idx := indexGlob(name, mid)
		if idx < 0 {
			return false
		}
		midSegs := strings.Split(mid, "/")
		segments := strings.Split(name, "/")
		name = strings.Join(segments[idx+len(midSegs):], "/")
	}

	return true
}

// indexGlob finds the first position in name (split by '/') where sub matches.
func indexGlob(name, sub string) int {
	segments := strings.Split(name, "/")
	subSegs := strings.Split(sub, "/")
	for i := 0; i <= len(segments)-len(subSegs); i++ {
		match := true
		for j, sp := range subSegs {
			ok, _ := filepath.Match(sp, segments[i+j])
			if !ok {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// isUnderExcludedDir returns true if relPath falls under any of the excluded
// directory prefixes. Each entry in excludedDirs must end with '/'.
func isUnderExcludedDir(relPath string, excludedDirs []string) bool {
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
