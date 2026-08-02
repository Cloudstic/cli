package engine

import (
	"io"

	"context"
	"fmt"
	"time"

	"github.com/cloudstic/cli/internal/logger"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

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

// FindOption configures a find operation.
type FindOption func(*findConfig)

type findConfig struct {
	query FindQuery
}

// WithFindPattern applies the positional pattern, routing it by shape: a
// pattern containing a separator constrains the full path, one without it
// constrains the basename. This split is what keeps the common case cheap —
// a basename is on the metadata object already, a path has to be reconstructed.
func WithFindPattern(pattern string) FindOption {
	return func(c *findConfig) {
		if pathPatternLooksLikePath(pattern) {
			c.query.Path = pattern
			return
		}
		c.query.Name = pattern
	}
}

func WithFindName(pattern string) FindOption {
	return func(c *findConfig) { c.query.Name = pattern }
}

func WithFindPath(pattern string) FindOption {
	return func(c *findConfig) { c.query.Path = pattern }
}

func WithFindRegex(expr string) FindOption {
	return func(c *findConfig) { c.query.Regex = expr }
}

func WithFindIgnoreCase() FindOption {
	return func(c *findConfig) { c.query.IgnoreCase = true }
}

func WithFindFileID(id string) FindOption {
	return func(c *findConfig) { c.query.FileID = id }
}

func WithFindContentHash(hash string) FindOption {
	return func(c *findConfig) { c.query.ContentHash = hash }
}

func WithFindRef(ref string) FindOption {
	return func(c *findConfig) { c.query.Ref = ref }
}

func WithFindType(t core.FileType) FindOption {
	return func(c *findConfig) { c.query.Type = t }
}

func WithFindSize(cmp SizeCompare) FindOption {
	return func(c *findConfig) { c.query.Size = &cmp }
}

// WithFindNewer and WithFindOlder filter by a file's Mtime. They accept RFC3339
// or a duration such as "7d", which is read relative to now.
func WithFindNewer(spec string) FindOption {
	return func(c *findConfig) { c.query.Newer = spec }
}

func WithFindOlder(spec string) FindOption {
	return func(c *findConfig) { c.query.Older = spec }
}

// WithFindSnapshots restricts the search to the named snapshots. Refs may be
// full ("snapshot/<hash>"), bare hashes, unambiguous prefixes, or "latest".
func WithFindSnapshots(refs ...string) FindOption {
	return func(c *findConfig) { c.query.Snapshots = append(c.query.Snapshots, refs...) }
}

func WithFindSource(uri string) FindOption {
	return func(c *findConfig) { c.query.Source = uri }
}

func WithFindTags(tags ...string) FindOption {
	return func(c *findConfig) { c.query.Tags = append(c.query.Tags, tags...) }
}

// WithFindLatest restricts the search to the n newest selected snapshots.
func WithFindLatest(n int) FindOption {
	return func(c *findConfig) { c.query.Latest = n }
}

// WithFindSince and WithFindUntil filter by a snapshot's creation time, not by
// any file's Mtime — that is what WithFindNewer and WithFindOlder do.
func WithFindSince(spec string) FindOption {
	return func(c *findConfig) { c.query.Since = spec }
}

func WithFindUntil(spec string) FindOption {
	return func(c *findConfig) { c.query.Until = spec }
}

// WithFindGroupByContent regroups the same matches by content hash instead of
// by file identity, which is how duplicate content is found. It changes
// grouping only, never which entries matched.
func WithFindGroupByContent() FindOption {
	return func(c *findConfig) { c.query.GroupByContent = true }
}

func WithFindMaxResults(n int) FindOption {
	return func(c *findConfig) { c.query.MaxResults = n }
}

// WithFindNoDelta forces the straightforward per-snapshot walk instead of the
// delta scan. It exists so a suspected delta-scan bug can be confirmed against
// an implementation with nowhere to hide, and so the two can be compared in
// tests.
func WithFindNoDelta() FindOption {
	return func(c *findConfig) { c.query.NoDelta = true }
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// FindManager locates files across the snapshots of a repository.
//
// It is a pure read path: no lock is taken, nothing is written, and the
// repository format is not stamped.
type FindManager struct {
	store store.ObjectStore
	tree  *hamt.Tree
	// log is the snapshot-catalog sink this manager passes to the free
	// functions in snapshots.go.
	log *logger.Logger
}

func NewFindManager(s store.ObjectStore, logWriter io.Writer) *FindManager {
	// One Tree serves every snapshot root and shares a single node cache
	// between them, which is what makes the delta scan's structural sharing
	// pay off rather than re-reading the same nodes per snapshot.
	return &FindManager{store: s, tree: hamt.NewTree(s), log: SnapshotLogger(logWriter)}
}

// QueryFromOptions resolves a set of options into the query they describe,
// defaults filled in. Run uses it; callers that want to show or record what a
// query will do before running it can too.
func QueryFromOptions(opts ...FindOption) FindQuery {
	return newFindConfig(opts...).query
}

func newFindConfig(opts ...FindOption) findConfig {
	cfg := findConfig{query: FindQuery{MaxResults: defaultFindMaxResults}}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.query.MaxResults <= 0 {
		cfg.query.MaxResults = defaultFindMaxResults
	}
	return cfg
}

// Run executes the query.
func (fm *FindManager) Run(ctx context.Context, opts ...FindOption) (*FindResult, error) {
	cfg := newFindConfig(opts...)

	pred, err := compileFindPredicate(cfg.query)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	fm.log.Debugf("Loading snapshot catalog...")
	catalog, err := LoadSnapshotCatalog(fm.store, fm.log)
	if err != nil {
		return nil, fmt.Errorf("load snapshot catalog: %w", err)
	}
	selected, err := selectFindSnapshots(fm.store, catalog, cfg.query)
	if err != nil {
		return nil, err
	}

	result := &FindResult{
		Query:             cfg.query,
		SnapshotsSearched: len(selected),
		GroupedBy:         "file",
	}
	if cfg.query.GroupByContent {
		result.GroupedBy = "content"
	}
	if w := pred.prefilterWarning(); w != "" {
		result.Warnings = append(result.Warnings, w)
	}

	collector := newFindCollector(cfg.query.GroupByContent, cfg.query.MaxResults)
	scanner := newFindScanner(fm.store, fm.tree, pred, collector, fm.log)

	for _, lineage := range groupFindLineages(selected) {
		fm.log.Debugf("Scanning %d snapshot(s) for %s", len(lineage.snapshots), lineage.key)
		if err := scanner.scanLineage(ctx, lineage, cfg.query.NoDelta); err != nil {
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
