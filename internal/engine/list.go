package engine

import (
	"context"

	"github.com/cloudstic/cli/pkg/store"
)

// ListOption configures a list operation.
type ListOption func(*listConfig)

type listConfig struct{}

// ListResult holds the snapshots returned by a list operation.
type ListResult struct {
	Snapshots []SnapshotEntry
}

// ListManager enumerates all available snapshots.
type ListManager struct {
	store store.ObjectStore
}

func NewListManager(s store.ObjectStore) *ListManager {
	return &ListManager{store: s}
}

// Run lists every snapshot in the store.
func (lm *ListManager) Run(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	entries, err := LoadSnapshotCatalog(lm.store)
	if err != nil {
		return nil, err
	}
	return &ListResult{Snapshots: entries}, nil
}
