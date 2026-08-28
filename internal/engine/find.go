package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/pkg/store"
)

var defaultFindLog = logger.New("find", logger.ColorCyan)

// defaultFindMaxResults bounds how many distinct files one query reports.
// Scanning continues past the cap so the counters stay accurate; only
// accumulation stops.
//
// Which files survive the cap is the arbitrary part: it is the first N a
// scanner happens to encounter, and the delta scan and the -no-delta walk visit
// entries in different orders. Each is deterministic on its own, so a repeated
// query gives a repeated answer, but a truncated result is a sample rather than
// a ranking — which is why Truncated is reported rather than left implicit.
const defaultFindMaxResults = 1000

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

// SizeOp is the comparison a size predicate applies, in find(1)'s vocabulary:
// "+10M" is at least, "-10M" is at most, a bare "10M" is exactly.
type SizeOp string

const (
	SizeAtLeast SizeOp = "+"
	SizeAtMost  SizeOp = "-"
	SizeExactly SizeOp = "="
)

// SizeCompare is a parsed size predicate.
type SizeCompare struct {
	Op    SizeOp `json:"op"`
	Bytes int64  `json:"bytes"`
}

func (s SizeCompare) String() string {
	switch s.Op {
	case SizeAtLeast:
		return fmt.Sprintf("+%d", s.Bytes)
	case SizeAtMost:
		return fmt.Sprintf("-%d", s.Bytes)
	default:
		return fmt.Sprintf("%d", s.Bytes)
	}
}

// FindQuery is the complete, serializable description of one find. It is echoed
// back on FindResult so a JSON consumer can tell what produced the matches
// without re-deriving it from the command line.
//
// Entry predicates (Name through Older) select files. Snapshot selectors
// (Snapshots through Until) select which snapshots are searched. The two are
// deliberately separate vocabularies: -newer/-older filter by a file's Mtime,
// -since/-until filter by a snapshot's creation time.
type FindQuery struct {
	// Entry predicates. All given predicates must match (conjunction).
	Name        string        `json:"name,omitempty"`
	Path        string        `json:"path,omitempty"`
	Regex       string        `json:"regex,omitempty"`
	IgnoreCase  bool          `json:"ignore_case,omitempty"`
	FileID      string        `json:"file_id,omitempty"`
	ContentHash string        `json:"content_hash,omitempty"`
	Ref         string        `json:"ref,omitempty"`
	Type        core.FileType `json:"type,omitempty"`
	Size        *SizeCompare  `json:"size,omitempty"`
	Newer       string        `json:"newer,omitempty"` // RFC3339 or a duration like "7d"
	Older       string        `json:"older,omitempty"`

	// Snapshot selectors.
	Snapshots []string `json:"snapshots,omitempty"`
	Source    string   `json:"source,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Latest    int      `json:"latest,omitempty"`
	Since     string   `json:"since,omitempty"`
	Until     string   `json:"until,omitempty"`

	// Presentation and execution.
	GroupByContent bool `json:"group_by_content,omitempty"`
	MaxResults     int  `json:"max_results,omitempty"`
	NoDelta        bool `json:"no_delta,omitempty"`
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// SnapshotRef identifies one snapshot a version was found in.
type SnapshotRef struct {
	Ref     string `json:"ref"`
	Seq     int    `json:"seq"`
	Created string `json:"created"` // ISO8601
}

// FileVersion is one immutable state of a file: exactly the metadata object at
// Ref, together with every snapshot that holds it.
//
// Paths is a slice because core.FileMeta.Parents is a list — a Google Drive file
// can live in two folders at once. It is also why two versions can share a Ref:
// renaming an ancestor folder changes a file's path without changing its own
// metadata object, so the same Ref legitimately appears at different paths in
// different snapshots.
type FileVersion struct {
	Ref         string        `json:"ref"`
	FileID      string        `json:"file_id"`
	Name        string        `json:"name"`
	Paths       []string      `json:"paths"`
	ContentHash string        `json:"content_hash,omitempty"`
	Type        core.FileType `json:"type,omitempty"`
	Size        int64         `json:"size"`
	Mtime       int64         `json:"mtime"`
	Mode        uint32        `json:"mode,omitempty"`
	Snapshots   []SnapshotRef `json:"snapshots"`
	FirstSeen   string        `json:"first_seen"` // ISO8601, earliest containing snapshot
	LastSeen    string        `json:"last_seen"`  // ISO8601, latest containing snapshot
}

// FileMatch is one file, with every version of it the query matched.
//
// The grouping key is the source FileID, which is stable across renames and
// moves within a source — so a renamed file is one match whose versions carry
// different names, rather than two unrelated results. Under GroupByContent the
// key is ContentHash instead and FileID is empty, since the group then spans
// several distinct files that happen to hold identical bytes.
type FileMatch struct {
	FileID      string           `json:"file_id,omitempty"`
	ContentHash string           `json:"content_hash,omitempty"`
	Source      *core.SourceInfo `json:"source,omitempty"`
	Type        core.FileType    `json:"type,omitempty"`
	Versions    []FileVersion    `json:"versions"` // newest first
}

// Path returns the match's most representative path: the first path of its
// newest version.
func (m FileMatch) Path() string {
	if len(m.Versions) == 0 || len(m.Versions[0].Paths) == 0 {
		return ""
	}
	return m.Versions[0].Paths[0]
}

// LatestSnapshot returns the newest snapshot holding the newest version, which
// is the one a follow-up restore would name.
func (m FileMatch) LatestSnapshot() (SnapshotRef, bool) {
	if len(m.Versions) == 0 || len(m.Versions[0].Snapshots) == 0 {
		return SnapshotRef{}, false
	}
	return m.Versions[0].Snapshots[0], true
}

// FindResult is the outcome of one query.
type FindResult struct {
	Query             FindQuery   `json:"query"`
	SnapshotsSearched int         `json:"snapshots_searched"`
	EntriesScanned    int         `json:"entries_scanned"`
	MetaFetched       int         `json:"meta_fetched"` // filemeta objects actually read
	Matches           []FileMatch `json:"matches"`
	Truncated         bool        `json:"truncated"`
	Warnings          []string    `json:"warnings,omitempty"`
	GroupedBy         string      `json:"grouped_by"` // "file" or "content"
	Elapsed           string      `json:"elapsed,omitempty"`
}

// TotalVersions counts versions across every match.
func (r *FindResult) TotalVersions() int {
	var n int
	for _, m := range r.Matches {
		n += len(m.Versions)
	}
	return n
}

// TotalSnapshots counts the distinct snapshots any match was found in.
func (r *FindResult) TotalSnapshots() int {
	seen := make(map[string]struct{})
	for _, m := range r.Matches {
		for _, v := range m.Versions {
			for _, s := range v.Snapshots {
				seen[s.Ref] = struct{}{}
			}
		}
	}
	return len(seen)
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

// SetPattern applies a positional pattern, routing it by shape: a pattern
// containing a separator constrains the full path, one without it constrains
// the basename. This split is what keeps the common case cheap — a basename is
// on the metadata object already, a path has to be reconstructed.
//
// It is a method rather than a plain field because that routing is a decision,
// not a value: a caller who assigned Path directly would silently make every
// basename search pay for path reconstruction.
func (q *FindQuery) SetPattern(pattern string) {
	if pathPatternLooksLikePath(pattern) {
		q.Path = pattern
		return
	}
	q.Name = pattern
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// FindManager locates files across the snapshots of a repository.
//
// It is a pure read path: no lock is taken, nothing is written, and the
// repository format is not stamped.
type FindManager struct {
	store   store.ObjectStore
	tree    *hamt.Tree
	catalog snapshotCatalog
	// log is this manager's own debug sink, handed to the scanner for its
	// progress reporting.
	log *logger.Logger
}

func NewFindManager(d Deps) *FindManager {
	// One Tree serves every snapshot root and shares a single node cache
	// between them, which is what makes the delta scan's structural sharing
	// pay off rather than re-reading the same nodes per snapshot.
	return &FindManager{
		store:   d.Store,
		tree:    hamt.NewTree(d.Store, d.treeOptions()...),
		catalog: newSnapshotCatalog(d.Store, d.LogSink),
		log:     defaultFindLog.To(d.LogSink),
	}
}

// Run executes the query.
//
// A zero FindQuery is a valid search — every file in every snapshot, capped at
// the default result limit. MaxResults is normalized here rather than being a
// required field so that filling in only the predicates you care about behaves
// the way the rest of this module's configuration values do.
func (fm *FindManager) Run(ctx context.Context, q FindQuery) (*FindResult, error) {
	if q.MaxResults <= 0 {
		q.MaxResults = defaultFindMaxResults
	}

	pred, err := compileFindPredicate(q)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	fm.log.Debugf("Loading snapshot catalog...")
	catalog, err := fm.catalog.load()
	if err != nil {
		return nil, fmt.Errorf("load snapshot catalog: %w", err)
	}
	selected, err := selectFindSnapshots(fm.store, catalog, q)
	if err != nil {
		return nil, err
	}

	result := &FindResult{
		Query:             q,
		SnapshotsSearched: len(selected),
		GroupedBy:         "file",
	}
	if q.GroupByContent {
		result.GroupedBy = "content"
	}
	if w := pred.prefilterWarning(); w != "" {
		result.Warnings = append(result.Warnings, w)
	}

	collector := newFindCollector(q.GroupByContent, q.MaxResults)
	scanner := newFindScanner(fm.store, fm.tree, pred, collector, fm.log)

	for _, lineage := range groupFindLineages(selected) {
		fm.log.Debugf("Scanning %d snapshot(s) for %s", len(lineage.snapshots), lineage.key)
		if err := scanner.scanLineage(ctx, lineage, q.NoDelta); err != nil {
			return nil, err
		}
	}

	result.EntriesScanned = scanner.entriesScanned
	result.MetaFetched = scanner.metaFetched
	result.Matches = collector.finish()
	result.Truncated = collector.truncated
	result.Elapsed = time.Since(start).Round(time.Millisecond).String()
	return result, nil
}
