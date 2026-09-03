package core

import "github.com/cloudstic/cli/pkg/source"

// ObjectType defines the type of the object in the system
type ObjectType string

const (
	ObjectTypeContent ObjectType = "content"
)

// Content represents a file's content as a list of chunks
// Object key: content/<sha256>
type Content struct {
	Type          ObjectType `json:"type"` // "content"
	Size          int64      `json:"size"`
	Chunks        []string   `json:"chunks,omitempty"`          // List of "chunk/<sha256>"
	DataInlineB64 []byte     `json:"data_inline_b64,omitempty"` // For small files
}

// FileMetaRef returns the object key and encoded bytes for a file's metadata.
//
// It is a function rather than a method on FileMeta because FileMeta is now
// defined in pkg/source: "filemeta/<hash>" is repository-format naming, which
// a source implementation has no business knowing about.
func FileMetaRef(f *FileMeta) (string, []byte, error) {
	hash, data, err := ComputeJSONHash(f)
	if err != nil {
		return "", data, err
	}
	return "filemeta/" + hash, data, nil
}

// The node objects at "node/<sha256>" are defined in internal/hamt, which is
// the only package that reads or writes them. Their encoding is part of the
// repository format all the same — see internal/hamt/node.go.

// Snapshot represents a backup checkpoint
// Object key: snapshot/<sha256>
type Snapshot struct {
	Version     int               `json:"version"`
	Created     string            `json:"created"` // ISO8601
	Root        string            `json:"root"`    // "node/<sha256>"
	Seq         int               `json:"seq"`
	Source      *SourceInfo       `json:"source,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	ChangeToken string            `json:"change_token,omitempty"`
	ExcludeHash string            `json:"exclude_hash,omitempty"`
}

// Index represents a pointer to the latest snapshot
// Key: index/latest
type Index struct {
	LatestSnapshot string `json:"latest_snapshot"` // "snapshot/<sha256>"
	Seq            int    `json:"seq"`
}

// SnapshotSummary is a lightweight representation of a snapshot stored in the
// snapshot catalog index. It contains enough metadata for listing, filtering,
// and finding the previous snapshot without having to fetch the full object.
type SnapshotSummary struct {
	Ref         string      `json:"ref"` // "snapshot/<hash>"
	Seq         int         `json:"seq"`
	Created     string      `json:"created"` // ISO8601
	Root        string      `json:"root"`    // "node/<hash>"
	Source      *SourceInfo `json:"source,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	ChangeToken string      `json:"change_token,omitempty"`
	ExcludeHash string      `json:"exclude_hash,omitempty"`

	// CopiedFrom denormalizes CopyProvenance out of the snapshot's Meta so
	// that `copy` can decide what it has already copied by reading the catalog
	// alone. The authoritative record stays in Meta, which the catalog does not
	// carry; without this field the skip check would have to fetch every
	// destination snapshot object on every run, which is the cost the catalog
	// exists to avoid.
	//
	// It is a cache, and is rebuilt from Meta whenever the catalog is
	// reconciled — so a build predating this field that rebuilds the catalog
	// drops the value rather than corrupting it, and the next `copy` recovers
	// it. See RFC 0017 §5.3.
	CopiedFrom string `json:"copied_from,omitempty"` // CopyProvenance.String()
}

// RepoConfig is the repository marker written by "init". It is stored as
// plaintext at key "config" so it can be read without the encryption key.
// Key: config
// Repository format versioning. See docs/compatibility.md for the contract
// these constants enforce.
//
// RepoFormatVersion is stamped into every repository this build creates.
// MaxSupportedRepoFormat is the highest version this build can read; a
// repository above it is refused rather than misread.
//
// Raise both together when a change makes a repository unreadable by earlier
// builds. Do not raise them for a change earlier builds can still read: a
// needless bump locks users out of their own data for no benefit.
const (
	// RepoFormatVersion is stamped into every repository this build creates by
	// default. The version is not only a claim about the bytes currently
	// present — it is the signal that tells other machines sharing this
	// repository to upgrade. A heterogeneous fleet is the dangerous state, so
	// a repository touched by a build that can seal says so, and older builds
	// are told to catch up rather than left writing alongside it.
	//
	// It equals MaxSupportedRepoFormat: `init` creates the newest format this
	// build understands, which is what makes v3 the default (RFC 0026, #517).
	// It stayed at 2 while v3 was opt-in, so that a repository became v3 only
	// when asked for; that gate is now lifted.
	//
	// Raising this does not touch an existing repository. A packfile
	// repository stays packfile, is read and written as one forever, and is
	// converted only by an explicit `cloudstic migrate`. What changes is what
	// `init` creates when it is not told otherwise, and `init -format 2` still
	// creates a packfile repository for anyone who needs one an older build
	// can read.
	RepoFormatVersion = 3

	// RepoFormatV2 is the packfile format: small objects bundled into packs,
	// with a sealed, sharded index. Still created on request by
	// `init -format 2`, for a repository a build older than v3 support can
	// read, and still read and written forever wherever it already exists.
	RepoFormatV2 = 2

	// MaxInPlaceUpgradeFormat is the highest format an *existing* repository
	// is raised to by an ordinary write.
	//
	// It is not RepoFormatVersion, and the difference is the whole reason this
	// constant exists. Format upgrades are in place and opportunistic
	// (docs/compatibility.md): a mutation stamps the marker so other machines
	// know to catch up. That works for a change earlier builds merely need to
	// be warned about, and v1 to v2 was one.
	//
	// v2 to v3 is not. A v3 repository holds fat leaves and blobs where a v2
	// one holds packs, so the version cannot be raised without rewriting every
	// object — which is what `cloudstic migrate` does, explicitly and under
	// the user's control. Stamping 3 onto a packfile repository would claim a
	// layout that is not there: older builds would refuse a repository they
	// can in fact read, and this build would open it as v3, build its chain
	// without PackStore, and fail to read the packs it is made of.
	//
	// So a backup onto a packfile repository leaves it at 2 forever, and the
	// only route to v3 is init or migrate.
	MaxInPlaceUpgradeFormat = 2

	// RepoFormatV3 is the fat-leaf, packless repository format (RFC 0026).
	// Created only by an explicit init; a v3 repository contains only v3
	// structures, and a v3 client builds its store chain without PackStore.
	RepoFormatV3 = 3

	// MaxSupportedRepoFormat is the highest version this build can read. A
	// repository above it is refused rather than misread.
	//
	// 2 covers a sealed pack index and a sharded one. Builds before that read
	// the sealed catalog as unparseable and, without the fixes released in
	// v1.15.0, as empty — which is how a prune deletes a live repository. They
	// would also read the pre-shard monolithic catalog as complete when it is
	// merely stale, which is the same failure by a different route.
	//
	// 3 is RepoFormatV3. Builds before it refuse a v3 repository at open —
	// the clean failure RFC 0026 relies on.
	MaxSupportedRepoFormat = 3

	// FramedCompressionFormat is the lowest recorded format at which the
	// compression layer may write framed objects (see pkg/store/compressed.go).
	//
	// It gates writes rather than reads: a framed object is always readable,
	// but a build predating the frame reads one as opaque bytes and returns
	// them, which is a misread rather than a clean refusal. The version gate
	// cannot prevent that on its own, because the stamp is applied after a
	// mutation completes — so a repository still recording format 1 would
	// otherwise be handed framed objects that an older build sails straight
	// into. Framing only once the repository already records this version
	// closes that window without stamping repositories speculatively.
	FramedCompressionFormat = 2
)

type RepoConfig struct {
	Version   int    `json:"version"`
	Created   string `json:"created"` // ISO8601
	Encrypted bool   `json:"encrypted"`

	// ID names this repository, so that an operation spanning two of them can
	// say which one a piece of data came from. `copy` records it as snapshot
	// provenance (RFC 0017 §5.1); nothing else reads it.
	//
	// It is random rather than derived, because every derivable candidate
	// moves: the sealed marker is re-encrypted with a fresh nonce on each
	// UpgradeRepoFormat, Version moves on upgrade, and Created is reset by
	// `init --adopt`. A provenance key that changes underneath a repository is
	// worse than none — it fails by silently duplicating history rather than by
	// declining to skip — so repositories written before this field simply do
	// not have one, and callers must handle "" rather than invent a value.
	//
	// Adding it needed no format bump: the marker is JSON decoded with
	// json.Unmarshal and is not content-addressed, so older builds ignore it.
	// Sealing covers it automatically, being applied to whatever JSON it wraps.
	ID string `json:"id,omitempty"`
}

// The source-facing domain types are defined in pkg/source and aliased here.
//
// The definitions moved so that the public Source contract does not depend on
// an internal package; the aliases stay so that the engine, the HAMT and the
// stored JSON keep spelling them core.FileMeta and core.SourceInfo. A Go alias
// denotes the identical type, so nothing about the on-disk format changes.
type (
	FileMeta   = source.FileMeta
	SourceInfo = source.SourceInfo
	FileType   = source.FileType
)

// FileType values.
const (
	FileTypeFile   = source.FileTypeFile
	FileTypeFolder = source.FileTypeFolder
)
