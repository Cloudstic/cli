package pathmatch

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Single segment.
		{"*.pdf", "report.pdf", true},
		{"*.pdf", "report.txt", false},
		{"report.pdf", "report.pdf", true},
		{"repor?.pdf", "report.pdf", true},
		{"[rs]eport.pdf", "report.pdf", true},

		// A single-segment pattern does not cross separators.
		{"*.pdf", "Documents/report.pdf", false},

		// Multi-segment.
		{"Documents/report.pdf", "Documents/report.pdf", true},
		{"Documents/*.pdf", "Documents/report.pdf", true},
		{"Documents/*.pdf", "Documents/2026/report.pdf", false},

		// "**" matches zero or more segments.
		{"Documents/**/report.pdf", "Documents/report.pdf", true},
		{"Documents/**/report.pdf", "Documents/2026/report.pdf", true},
		{"Documents/**/report.pdf", "Documents/2026/q1/report.pdf", true},
		{"Documents/**/report.pdf", "Archive/2026/report.pdf", false},
		{"**/report.pdf", "report.pdf", true},
		{"**/report.pdf", "a/b/c/report.pdf", true},
		{"Documents/**", "Documents", true},
		{"Documents/**", "Documents/a/b", true},
		{"Documents/**", "Archive/a", false},
		{"**", "anything/at/all", true},

		// Consecutive "**" collapse rather than requiring extra segments.
		{"a/**/**/b", "a/b", true},
		{"a/**/**/b", "a/x/b", true},

		// Leading and trailing slashes are insignificant.
		{"/Documents/report.pdf", "Documents/report.pdf", true},
		{"Documents/", "Documents", true},
	}

	for _, tc := range cases {
		p, err := Compile(tc.pattern, false)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.pattern, err)
		}
		if got := p.Match(tc.path); got != tc.want {
			t.Errorf("Compile(%q).Match(%q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestMatchIgnoreCase(t *testing.T) {
	p, err := Compile("Documents/*.PDF", true)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !p.Match("documents/report.pdf") {
		t.Error("case-insensitive pattern did not match differing case")
	}

	sensitive, err := Compile("Documents/*.PDF", false)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if sensitive.Match("documents/report.pdf") {
		t.Error("case-sensitive pattern matched differing case")
	}
}

func TestCompileRejectsBadPattern(t *testing.T) {
	if _, err := Compile("[unterminated", false); err == nil {
		t.Fatal("expected a compile error for an unterminated character class")
	}
	if _, err := Compile("", false); err == nil {
		t.Fatal("expected a compile error for an empty pattern")
	}
}

// TestBaseSegmentPrefilterAgreesWithFullMatch is the guard on the two-stage
// path match: every path the full pattern accepts must also survive the cheap
// basename prefilter, or find would discard real matches before ever resolving
// their paths.
func TestBaseSegmentPrefilterAgreesWithFullMatch(t *testing.T) {
	paths := []string{
		"report.pdf",
		"Documents/report.pdf",
		"Documents/2026/report.pdf",
		"Documents/2026/q1/notes.txt",
		"Archive/report.pdf",
		"Documents/report.pdf.bak",
	}
	patterns := []string{
		"Documents/**/report.pdf",
		"Documents/*.pdf",
		"**/*.pdf",
		"**/notes.txt",
	}

	for _, pattern := range patterns {
		full, err := Compile(pattern, false)
		if err != nil {
			t.Fatalf("Compile(%q): %v", pattern, err)
		}
		base, ok := full.BaseSegment()
		if !ok {
			t.Fatalf("Compile(%q).BaseSegment(): no prefilter available", pattern)
		}
		for _, p := range paths {
			if !full.Match(p) {
				continue
			}
			name := p
			if i := lastSlash(p); i >= 0 {
				name = p[i+1:]
			}
			if !base.Match(name) {
				t.Errorf("pattern %q matches %q but its prefilter rejects basename %q", pattern, p, name)
			}
		}
	}
}

func TestBaseSegmentAbsentForTrailingDoubleStar(t *testing.T) {
	p, err := Compile("Documents/**", false)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, ok := p.BaseSegment(); ok {
		t.Fatal("a pattern ending in ** must report no basename prefilter")
	}
}

func TestIsPathPattern(t *testing.T) {
	if IsPathPattern("*.pdf") {
		t.Error("a pattern without a separator is not a path pattern")
	}
	if !IsPathPattern("Documents/*.pdf") {
		t.Error("a pattern with a separator is a path pattern")
	}
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
