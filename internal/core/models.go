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
	Version     int                    `json:"version"`
	FileID      string                 `json:"fileId"` // Google Drive file ID (HAMT key)
	Name        string                 `json:"name"`
	Type        FileType               `json:"type"`    // "file" or "folder"
	Parents     []string               `json:"parents"` // List of "filemeta/<sha256>" refs (NOT raw IDs)
	Paths       []string               `json:"paths"`
	ContentHash string                 `json:"content_hash"`          // SHA256 of the file content
	ContentRef  string                 `json:"content_ref,omitempty"` // HMAC(dedupKey, ContentHash) for secure backend lookup
	Size        int64                  `json:"size"`
	Mtime       int64                  `json:"mtime"` // Unix timestamp
	Owner       string                 `json:"owner"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
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

// LeafEntry represents an entry in a Leaf node
type LeafEntry struct {
	Key      string `json:"key"`                // FileID
	PathKey  string `json:"path_key,omitempty"` // AffinityKey routing key; falls back to SHA256(Key) if empty
	FileMeta string `json:"filemeta"`           // "filemeta/<sha256>"
}

// SourceInfo describes the origin of a backup snapshot. It is stored as a
// first-class field on the snapshot so that forget policies can group by
// source identity (Type + Account + Path).
type SourceInfo struct {
	Type    string `json:"type"`              // e.g. "gdrive", "local"
	Account string `json:"account,omitempty"` // Google account email, hostname, etc.
	Path    string `json:"path,omitempty"`    // root folder ID, filesystem path, etc.
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
	HAMTVersion int               `json:"hamt_version,omitempty"` // 1 = legacy, 2 = affinity keys
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
type RepoConfig struct {
	Version   int    `json:"version"`
	Created   string `json:"created"` // ISO8601
	Encrypted bool   `json:"encrypted"`
}
