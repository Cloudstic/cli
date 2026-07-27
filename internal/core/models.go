package core

// ObjectType defines the type of the object in the system
type ObjectType string

const (
	ObjectTypeContent  ObjectType = "content"
	ObjectTypeInternal ObjectType = "internal"
	ObjectTypeLeaf     ObjectType = "leaf"
)

// FileType defines the generic type of the file (e.g. generic file, folder, symlink)
type FileType string

const (
	FileTypeFile   FileType = "file"
	FileTypeFolder FileType = "folder"
)

// Content represents a file's content as a list of chunks
// Object key: content/<sha256>
type Content struct {
	Type          ObjectType `json:"type"` // "content"
	Size          int64      `json:"size"`
	Chunks        []string   `json:"chunks,omitempty"`          // List of "chunk/<sha256>"
	DataInlineB64 []byte     `json:"data_inline_b64,omitempty"` // For small files
}

// FileMeta represents immutable file metadata
// Object key: filemeta/<sha256>
type FileMeta struct {
	Version int      `json:"version"`
	FileID  string   `json:"fileId"` // Google Drive file ID (HAMT key)
	Name    string   `json:"name"`
	Type    FileType `json:"type"` // "file" or "folder"
	// Parents holds raw source FileIDs — the same values used as HAMT keys —
	// not "filemeta/<sha256>" refs. Resolving one therefore means a lookup by
	// key, not a Get. See internal/engine/backup_scan.go and restore.go.
	Parents     []string               `json:"parents"`
	Paths       []string               `json:"paths,omitempty"`
	ContentHash string                 `json:"content_hash"`          // SHA256 of the file content
	ContentRef  string                 `json:"content_ref,omitempty"` // HMAC(dedupKey, ContentHash) for secure backend lookup
	Size        int64                  `json:"size"`
	Mtime       int64                  `json:"mtime"` // Unix timestamp
	Owner       string                 `json:"owner"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
	Mode        uint32                 `json:"mode,omitempty"`   // POSIX permission bits (st_mode & 0xFFF)
	Uid         uint32                 `json:"uid,omitempty"`    // POSIX user ID
	Gid         uint32                 `json:"gid,omitempty"`    // POSIX group ID
	Btime       int64                  `json:"btime,omitempty"`  // birth/creation time, Unix seconds; 0 = not available
	Flags       uint32                 `json:"flags,omitempty"`  // per-file flags (chflags / FS_IOC_GETFLAGS)
	Xattrs      map[string][]byte      `json:"xattrs,omitempty"` // extended attributes: name → raw bytes
}

func (f *FileMeta) Ref() (string, []byte, error) {
	hash, data, err := ComputeJSONHash(f)
	if err != nil {
		return "", data, err
	}
	return "filemeta/" + hash, data, nil
}

// HAMTNode represents a node in the Merkle-HAMT
// Object key: node/<sha256>
type HAMTNode struct {
	Type     ObjectType  `json:"type"` // "internal" or "leaf"
	Bitmap   uint32      `json:"bitmap,omitempty"`
	Children []string    `json:"children,omitempty"` // ["node/<sha256>", ...]
	Entries  []LeafEntry `json:"entries,omitempty"`
}

// LeafEntry represents an entry in a Leaf node.
// The HAMT treats Value as opaque; the backup engine stores filemeta refs in
// it, which is why the wire tag is "filemeta".
type LeafEntry struct {
	Key     string `json:"key"`                // caller's entry key (the source file ID)
	PathKey string `json:"path_key,omitempty"` // routing key; falls back to SHA256(Key) if empty
	Value   string `json:"filemeta"`           // "filemeta/<sha256>"
}

// SourceInfo describes the origin of a backup snapshot. It is stored as a
// first-class field on the snapshot so that forget policies can group by
// source identity (Type + Account + Path).
type SourceInfo struct {
	Type      string `json:"type"`                 // e.g. "gdrive", "local"
	Account   string `json:"account,omitempty"`    // friendly account/host label for display
	Path      string `json:"path,omitempty"`       // display path within the source container
	Identity  string `json:"identity,omitempty"`   // stable container identity for lineage matching
	PathID    string `json:"path_id,omitempty"`    // stable selected-root identity within container
	DriveName string `json:"drive_name,omitempty"` // human-readable container label (e.g. "My Drive")
	FsType    string `json:"fs_type,omitempty"`    // source filesystem type (e.g. "apfs", "ext4", "sftp")
}

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
