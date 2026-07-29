// Package pathmatch matches slash-separated paths against glob patterns.
//
// The syntax is path.Match applied per segment, plus "**" for "zero or more
// path segments". path.Match alone cannot express "**" — its "*" never crosses
// a separator — so the segment walk lives here rather than being open-coded by
// each caller.
//
// The package is deliberately independent of the repository model: it matches
// strings. `find` uses it for -name/-path patterns, and pkg/source's
// ExcludeMatcher compiles backup exclude patterns through it — the gitignore
// layer (negation, directory-only, anchoring) stays there, the glob lives here,
// so the two cannot drift apart.
package pathmatch

import (
	"fmt"
	"path"
	"strings"
)

// Pattern is a compiled glob.
type Pattern struct {
	raw        string
	segments   []string
	ignoreCase bool
}

// Compile parses a glob pattern. When ignoreCase is set, both the pattern and
// every candidate are folded to lower case before matching — which is a
// property of the *match*, not a claim about the case sensitivity of the
// filesystem the files came from.
func Compile(pattern string, ignoreCase bool) (*Pattern, error) {
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	raw := pattern
	if ignoreCase {
		pattern = strings.ToLower(pattern)
	}

	segments := splitSegments(pattern)
	for _, seg := range segments {
		if seg == "**" {
			continue
		}
		// path.Match reports a bad pattern independently of the name it is
		// given, so any candidate serves to validate the syntax. Rejecting here
		// means a malformed pattern is an error before the scan starts rather
		// than a silent non-match during it.
		if _, err := path.Match(seg, ""); err != nil {
			return nil, fmt.Errorf("bad pattern %q: %w", raw, err)
		}
	}
	return &Pattern{raw: raw, segments: collapseDoubleStars(segments), ignoreCase: ignoreCase}, nil
}

// String returns the pattern as written.
func (p *Pattern) String() string { return p.raw }

// Match reports whether s matches the pattern. s is treated as a
// slash-separated path; leading and trailing slashes are ignored.
func (p *Pattern) Match(s string) bool {
	if p == nil {
		return true
	}
	if p.ignoreCase {
		s = strings.ToLower(s)
	}
	return matchSegments(p.segments, splitSegments(s))
}

// BaseSegment returns the pattern's last segment when that segment can serve as
// a cheap basename prefilter — that is, when it is not "**", which matches any
// number of segments and therefore constrains the basename not at all.
//
// This is what makes "Documents/**/*.pdf" affordable: the caller evaluates
// "*.pdf" against a name it already holds and resolves the full path only for
// the entries that survive. A pattern ending in "**" has no such prefilter, and
// the caller has to resolve every path.
func (p *Pattern) BaseSegment() (*Pattern, bool) {
	if p == nil || len(p.segments) == 0 {
		return nil, false
	}
	last := p.segments[len(p.segments)-1]
	if last == "**" {
		return nil, false
	}
	return &Pattern{raw: last, segments: []string{last}, ignoreCase: p.ignoreCase}, true
}

// IsPathPattern reports whether the pattern constrains more than a basename.
// A caller deciding between matching a name and matching a full path uses this
// to make that choice from the pattern alone.
func IsPathPattern(pattern string) bool { return strings.Contains(pattern, "/") }

func splitSegments(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "/")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// collapseDoubleStars folds runs of "**" into one. Consecutive "**" segments
// match exactly what a single one does, and collapsing them keeps the
// backtracking in matchSegments from exploring the same split repeatedly.
func collapseDoubleStars(segments []string) []string {
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg == "**" && len(out) > 0 && out[len(out)-1] == "**" {
			continue
		}
		out = append(out, seg)
	}
	return out
}

func matchSegments(pattern, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0] == "**" {
		// "**" matches zero or more segments; try every split.
		for i := 0; i <= len(parts); i++ {
			if matchSegments(pattern[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], parts[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], parts[1:])
}
