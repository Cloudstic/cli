package store

import (
	"context"
	"io"

	"github.com/cloudstic/cli/internal/core"
)

// ObjectStore is the interface for content-addressable object storage.
// Keys are slash-separated paths like "chunk/<hash>" or "snapshot/<hash>".
type ObjectStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Size(ctx context.Context, key string) (int64, error)
	TotalSize(ctx context.Context) (int64, error)
	Flush(ctx context.Context) error
}

// ConcurrencyHinter is an optional interface that ObjectStore implementations
// can implement to indicate the optimal number of concurrent operations.
// Remote stores (S3) benefit from high concurrency; local stores do not.
type ConcurrencyHinter interface {
	ConcurrencyHint() int
}

// Unwrapper is an optional interface for wrapper stores (CompressedStore,
// EncryptedStore, etc.) to expose their inner store for introspection.
type Unwrapper interface {
	Unwrap() ObjectStore
}

// GetConcurrencyHint walks the store wrapper chain and returns the first
// ConcurrencyHint it finds, defaulting to defaultConcurrency if none exists.
func GetConcurrencyHint(s ObjectStore, defaultConcurrency int) int {
	for s != nil {
		if h, ok := s.(ConcurrencyHinter); ok {
			return h.ConcurrencyHint()
		}
		if u, ok := s.(Unwrapper); ok {
			s = u.Unwrap()
		} else {
			break
		}
	}
	return defaultConcurrency
}

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

// IncrementalSource extends Source with delta-based walking using a change
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
