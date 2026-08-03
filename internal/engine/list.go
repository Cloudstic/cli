package engine

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudstic/cli/pkg/store"
)

// ListOption configures a list operation.
type ListOption func(*listConfig)

type listConfig struct {
}

// ListResult holds the snapshots returned by a list operation.
type ListResult struct {
	Snapshots []SnapshotEntry
}

// ListManager enumerates all available snapshots.
type ListManager struct {
	catalog snapshotCatalog
}

func NewListManager(s store.ObjectStore, logWriter io.Writer) *ListManager {
	return &ListManager{catalog: newSnapshotCatalog(s, logWriter)}
}

// Run lists every snapshot in the store.
func (lm *ListManager) Run(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	var cfg listConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	lm.catalog.debugf("Loading snapshot catalog...")
	entries, err := lm.catalog.load()
	if err != nil {
		return nil, err
	}
	lm.catalog.debugf("Found %d snapshots", len(entries))
	for _, e := range entries {
		source := ""
		if e.Snap.Source != nil {
			source = fmt.Sprintf(" source=%s account=%s path=%s", e.Snap.Source.Type, e.Snap.Source.Account, e.Snap.Source.Path)
			if e.Snap.Source.DriveName != "" {
				source += fmt.Sprintf(" drive=%s", e.Snap.Source.DriveName)
				if e.Snap.Source.Identity != "" {
					source += fmt.Sprintf(" identity=%s", e.Snap.Source.Identity)
				}
				if e.Snap.Source.PathID != "" {
					source += fmt.Sprintf(" path_id=%s", e.Snap.Source.PathID)
				}
			}
			lm.catalog.debugf("  %s seq=%d created=%s%s", e.Ref, e.Snap.Seq, e.Snap.Created, source)
		}
	}
	return &ListResult{Snapshots: entries}, nil
}
