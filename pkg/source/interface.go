package source

import (
	"context"
	"io"

	"github.com/cloudstic/cli/internal/core"
)

// SourceSize holds the total size of a source.
type SourceSize struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}

// Source is the interface for a backup data source (local filesystem, Google
// Drive, OneDrive, etc.). Implementations MUST ensure that parent folders are
// visited before their children during Walk.
type Source interface {
	Walk(ctx context.Context, callback func(core.FileMeta) error) error
	GetFileStream(fileID string) (io.ReadCloser, error)
	Info() core.SourceInfo
	Size(ctx context.Context) (*SourceSize, error)
}

// ChangeType describes the kind of change reported by an IncrementalSource.
type ChangeType string

const (
	ChangeUpsert ChangeType = "upsert"
	ChangeDelete ChangeType = "delete"
)

// FileChange pairs a change type with file metadata. For deletions only
// Meta.FileID is required.
type FileChange struct {
	Type ChangeType
	Meta core.FileMeta
}

// IncrementalSource
// token stored in the snapshot. On the first run (empty token) the engine
// falls back to the full Walk; on subsequent runs only changed entries are
// emitted.
type IncrementalSource interface {
	Source
	// GetStartPageToken returns the token representing the current head of
	// the change stream. Call this before a full Walk to capture the baseline.
	GetStartPageToken() (string, error)
	// WalkChanges emits only the entries that changed since token.
	// It returns the new token to persist for the next run.
	WalkChanges(ctx context.Context, token string, callback func(FileChange) error) (newToken string, err error)
}
