package source

// The types the Source contract is written in.
//
// They live here rather than in internal/core so the dependency points the way
// it should: the public contract owns its own types, and the repository format
// consumes them. internal/core aliases these back, so the engine and the HAMT
// keep spelling them core.FileMeta and nothing there had to change.
//
// The practical consequence is that pkg/source depends on nothing outside the
// standard library — implementing a Source costs no Cloudstic dependency at
// all beyond this package.

// FileType defines the generic type of the file (e.g. generic file, folder, symlink)
type FileType string

const (
	FileTypeFile   FileType = "file"
	FileTypeFolder FileType = "folder"
)

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
