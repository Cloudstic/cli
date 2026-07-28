package engine

import (
	"io"

	"context"
	"fmt"
	"github.com/cloudstic/cli/internal/logger"
	"os"

	"github.com/cloudstic/cli/pkg/store"
)

// ListOption configures a list operation.
type ListOption func(*listConfig)

type listConfig struct {
	verbose bool
}

// WithListVerbose enables verbose output for the list operation.
func WithListVerbose() ListOption {
	return func(cfg *listConfig) { cfg.verbose = true }
}

// ListResult holds the snapshots returned by a list operation.
type ListResult struct {
	Snapshots []SnapshotEntry
}

// ListManager enumerates all available snapshots.
type ListManager struct {
	store store.ObjectStore
	// log is the snapshot-catalog sink this manager passes to the free
	// functions in snapshots.go.
	log *logger.Logger
}

func NewListManager(s store.ObjectStore, logWriter io.Writer) *ListManager {
	return &ListManager{store: s, log: SnapshotLogger(logWriter)}
}

// Run lists every snapshot in the store.
func (lm *ListManager) Run(ctx context.Context, opts ...ListOption) (*ListResult, error) {
	var cfg listConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "Loading snapshot catalog...\n")
	}
	entries, err := LoadSnapshotCatalog(lm.store, lm.log)
	if err != nil {
		return nil, err
	}
	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "Found %d snapshots\n", len(entries))
		for _, e := range entries {
			source := ""
			if e.Snap.Source != nil {
				source = fmt.Sprintf(" source=%s account=%s path=%s", e.Snap.Source.Type, e.Snap.Source.Account, e.Snap.Source.Path)
				if e.Snap.Source.DriveName != "" {
					source += fmt.Sprintf(" drive=%s", e.Snap.Source.DriveName)
				}
				if e.Snap.Source.Identity != "" {
					source += fmt.Sprintf(" identity=%s", e.Snap.Source.Identity)
				}
				if e.Snap.Source.PathID != "" {
					source += fmt.Sprintf(" path_id=%s", e.Snap.Source.PathID)
				}
			}
			fmt.Fprintf(os.Stderr, "  %s seq=%d created=%s%s\n", e.Ref, e.Snap.Seq, e.Snap.Created, source)
		}
	}
	return &ListResult{Snapshots: entries}, nil
}
