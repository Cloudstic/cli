package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/pathmatch"
)

// findPredicate is a compiled FindQuery, split into the part that can be
// decided from a metadata object alone and the part that needs a reconstructed
// path.
//
// The split is the whole point. Paths are not stored (RFC 0015); reconstructing
// one costs a walk up the parent chain, so the scan resolves a path only for
// entries that already survived everything cheaper.
type findPredicate struct {
	name        *pathmatch.Pattern
	path        *pathmatch.Pattern
	pathBase    *pathmatch.Pattern // cheap basename prefilter derived from path
	regex       *regexp.Regexp
	fileID      string
	contentHash string
	ref         string
	ftype       core.FileType
	size        *SizeCompare
	newer       *time.Time
	older       *time.Time
	ignoreCase  bool
}

func compileFindPredicate(q FindQuery) (*findPredicate, error) {
	p := &findPredicate{
		fileID:      q.FileID,
		contentHash: strings.ToLower(q.ContentHash),
		ref:         normalizeFindRef(q.Ref),
		ftype:       q.Type,
		size:        q.Size,
		ignoreCase:  q.IgnoreCase,
	}

	var err error
	if q.Name != "" {
		if p.name, err = pathmatch.Compile(q.Name, q.IgnoreCase); err != nil {
			return nil, fmt.Errorf("invalid -name pattern: %w", err)
		}
	}
	if q.Path != "" {
		if p.path, err = pathmatch.Compile(q.Path, q.IgnoreCase); err != nil {
			return nil, fmt.Errorf("invalid -path pattern: %w", err)
		}
		if base, ok := p.path.BaseSegment(); ok {
			p.pathBase = base
		}
	}
	if q.Regex != "" {
		expr := q.Regex
		if q.IgnoreCase {
			expr = "(?i)" + expr
		}
		// Compiling before the scan starts means a bad expression is reported
		// immediately rather than after a repository-wide walk.
		if p.regex, err = regexp.Compile(expr); err != nil {
			return nil, fmt.Errorf("invalid -regex pattern: %w", err)
		}
	}
	if q.Type != "" && q.Type != core.FileTypeFile && q.Type != core.FileTypeFolder {
		return nil, fmt.Errorf("invalid -type %q: use %q or %q", q.Type, core.FileTypeFile, core.FileTypeFolder)
	}
	if p.newer, err = parseFindTime(q.Newer, "-newer"); err != nil {
		return nil, err
	}
	if p.older, err = parseFindTime(q.Older, "-older"); err != nil {
		return nil, err
	}

	if p.isEmpty() {
		return nil, fmt.Errorf(
			"find needs at least one predicate: give a pattern, or one of " +
				"-name, -path, -regex, -id, -content-hash, -ref, -type, -size, -newer, -older")
	}
	return p, nil
}

// isEmpty reports whether the query would match every entry in the repository.
// Running that is never what a user meant, and printing a million rows is a
// worse answer than an error.
func (p *findPredicate) isEmpty() bool {
	return p.name == nil && p.path == nil && p.regex == nil &&
		p.fileID == "" && p.contentHash == "" && p.ref == "" &&
		p.ftype == "" && p.size == nil && p.newer == nil && p.older == nil
}

// needsPath reports whether deciding a match requires a reconstructed path.
func (p *findPredicate) needsPath() bool { return p.path != nil || p.regex != nil }

// prefilterWarning describes the case where no cheap prefilter exists, so every
// entry's path has to be reconstructed. That is correct but slow, and saying so
// beats appearing to hang.
func (p *findPredicate) prefilterWarning() string {
	if !p.needsPath() {
		return ""
	}
	if p.regex != nil {
		return "-regex matches full paths, so every entry's path must be reconstructed; this is slower than a -name query"
	}
	if p.pathBase == nil && p.name == nil {
		return "this -path pattern ends in ** and offers no basename prefilter, so every entry's path must be reconstructed; this is slower than a -name query"
	}
	return ""
}

// matchKey applies the one predicate decidable from the HAMT key alone. The key
// is the source FileID, so this is the cheapest question the repository can
// answer: it needs no object read at all.
func (p *findPredicate) matchKey(key string) bool {
	return p.fileID == "" || p.fileID == key
}

// matchMeta applies every predicate decidable from the metadata object itself,
// including the basename prefilter derived from a path pattern. An entry that
// fails here never has its path reconstructed.
//
// It deliberately does not consider the HAMT key, which is what makes the result
// a pure function of the ref — and therefore safe to memoize per ref across
// every snapshot and lineage in the scan. The key predicate is matchKey's job.
func (p *findPredicate) matchMeta(ref string, meta *core.FileMeta) bool {
	if p.ref != "" && ref != p.ref {
		return false
	}
	if p.contentHash != "" && !strings.EqualFold(p.contentHash, meta.ContentHash) {
		return false
	}
	if p.ftype != "" && metaFileType(meta) != p.ftype {
		return false
	}
	if p.size != nil && !p.size.matches(meta.Size) {
		return false
	}
	if p.newer != nil && !time.Unix(meta.Mtime, 0).After(*p.newer) {
		return false
	}
	if p.older != nil && !time.Unix(meta.Mtime, 0).Before(*p.older) {
		return false
	}
	if p.name != nil && !p.name.Match(meta.Name) {
		return false
	}
	if p.pathBase != nil && !p.pathBase.Match(meta.Name) {
		return false
	}
	return true
}

// matchPaths applies the predicates that need a reconstructed path. An entry
// reachable by several paths matches when any one of them does — the file is
// genuinely there under each.
func (p *findPredicate) matchPaths(paths []string) bool {
	if !p.needsPath() {
		return true
	}
	for _, path := range paths {
		if p.path != nil && !p.path.Match(path) {
			continue
		}
		if p.regex != nil && !p.regex.MatchString(path) {
			continue
		}
		return true
	}
	return false
}

// metaFileType reports an entry's type, defaulting to file. Metadata written
// before the field was always populated leaves it empty, and reading that as
// "neither a file nor a folder" would hide those entries from every -type query.
func metaFileType(meta *core.FileMeta) core.FileType {
	if meta.Type == core.FileTypeFolder {
		return core.FileTypeFolder
	}
	return core.FileTypeFile
}

func (s SizeCompare) matches(size int64) bool {
	switch s.Op {
	case SizeAtLeast:
		return size >= s.Bytes
	case SizeAtMost:
		return size <= s.Bytes
	default:
		return size == s.Bytes
	}
}

// normalizeFindRef accepts either "filemeta/<hash>" or a bare hash.
func normalizeFindRef(ref string) string {
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, "/") {
		return ref
	}
	return "filemeta/" + ref
}

// pathPatternLooksLikePath reports whether a positional pattern constrains a
// path rather than a basename.
func pathPatternLooksLikePath(pattern string) bool {
	return pathmatch.IsPathPattern(pattern)
}

// ---------------------------------------------------------------------------
// Size and time parsing
// ---------------------------------------------------------------------------

var sizeSuffixes = map[string]int64{
	"":  1,
	"b": 1,
	"k": 1 << 10,
	"m": 1 << 20,
	"g": 1 << 30,
	"t": 1 << 40,
}

// ParseSizeCompare parses find(1)'s size syntax: an optional "+" (at least) or
// "-" (at most), a number, and an optional binary suffix (k, M, G, T).
func ParseSizeCompare(spec string) (SizeCompare, error) {
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return SizeCompare{}, fmt.Errorf("empty size")
	}

	cmp := SizeCompare{Op: SizeExactly}
	switch raw[0] {
	case '+':
		cmp.Op = SizeAtLeast
		raw = raw[1:]
	case '-':
		cmp.Op = SizeAtMost
		raw = raw[1:]
	}

	digits := strings.TrimRight(raw, "bBkKmMgGtT")
	suffix := strings.ToLower(raw[len(digits):])
	if len(suffix) > 1 {
		return SizeCompare{}, fmt.Errorf("invalid size %q: unknown suffix %q", spec, suffix)
	}
	multiplier, ok := sizeSuffixes[suffix]
	if !ok {
		return SizeCompare{}, fmt.Errorf("invalid size %q: unknown suffix %q", spec, suffix)
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return SizeCompare{}, fmt.Errorf("invalid size %q: expected a number, optionally suffixed with k, M, G, or T", spec)
	}
	if n < 0 {
		return SizeCompare{}, fmt.Errorf("invalid size %q: must not be negative", spec)
	}
	cmp.Bytes = n * multiplier
	return cmp, nil
}

// findTimeNow is indirected so tests can pin relative durations.
var findTimeNow = time.Now

var durationSpecPattern = regexp.MustCompile(`^(\d+)([smhdwy])$`)

// ParseFindTime parses a time specification: an RFC3339 timestamp, a plain
// date ("2026-01-01"), or a duration back from now ("7d", "12h").
func ParseFindTime(spec string) (time.Time, error) {
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if m := durationSpecPattern.FindStringSubmatch(raw); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid duration %q", spec)
		}
		var unit time.Duration
		switch m[2] {
		case "s":
			unit = time.Second
		case "m":
			unit = time.Minute
		case "h":
			unit = time.Hour
		case "d":
			unit = 24 * time.Hour
		case "w":
			unit = 7 * 24 * time.Hour
		case "y":
			unit = 365 * 24 * time.Hour
		}
		return findTimeNow().Add(-time.Duration(n) * unit), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf(
		"invalid time %q: use RFC3339 (2026-01-31T09:00:00Z), a date (2026-01-31), or a duration back from now (7d)", spec)
}

func parseFindTime(spec, flagName string) (*time.Time, error) {
	if spec == "" {
		return nil, nil
	}
	t, err := ParseFindTime(spec)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", flagName, err)
	}
	return &t, nil
}
