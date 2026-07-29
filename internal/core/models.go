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
	// RepoFormatVersion is stamped into every repository this build creates.
	//
	// It tracks MaxSupportedRepoFormat deliberately. The version is not only a
	// claim about the bytes currently present — it is the signal that tells
	// other machines sharing this repository to upgrade. A heterogeneous fleet
	// is the dangerous state, so a repository touched by a build that can seal
	// says so, and older builds are told to catch up rather than left writing
	// alongside it.
	RepoFormatVersion = 2

	// MaxSupportedRepoFormat is the highest version this build can read. A
	// repository above it is refused rather than misread.
	//
	// 2 covers a sealed pack index and a sharded one. Builds before that read
	// the sealed catalog as unparseable and, without the fixes released in
	// v1.15.0, as empty — which is how a prune deletes a live repository. They
	// would also read the pre-shard monolithic catalog as complete when it is
	// merely stale, which is the same failure by a different route.
	//
	// Both changes ship in the same release, so one version covers them.
	MaxSupportedRepoFormat = 2

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
