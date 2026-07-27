package engine

import (
	"path"
	"strings"

	"github.com/cloudstic/cli/internal/core"
)

func persistedFileMeta(meta core.FileMeta) core.FileMeta {
	persisted := meta
	persisted.Paths = nil
	return persisted
}

// maxParentDepth bounds a walk up the parent chain. A chain longer than this is
// a corrupt or cyclic tree rather than a deep directory.
const maxParentDepth = 50

// maxResolvedPaths bounds how many distinct paths one entry may resolve to.
// Multi-parent entries multiply: an entry with two parents that each have two
// parents has four paths, and the product grows with depth. The cap keeps a
// pathological tree from turning one lookup into an unbounded expansion.
const maxResolvedPaths = 32

func fileMetaPath(meta core.FileMeta, lookupParent func(string) (core.FileMeta, bool)) string {
	paths := fileMetaPaths(meta, lookupParent)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// fileMetaPaths returns every path by which meta can be reached, in
// Parents-declaration order — so the first element is always what the
// single-path fileMetaPath returns.
//
// core.FileMeta.Parents is a list, and for Google Drive it genuinely holds more
// than one entry: a single Drive file lives in two folders at once. Callers that
// report where a file is (rather than picking somewhere to write it) need every
// answer, not the first one.
func fileMetaPaths(meta core.FileMeta, lookupParent func(string) (core.FileMeta, bool)) []string {
	return collectMetaPaths(meta, lookupParent, 0, map[string]bool{})
}

// collectMetaPaths walks up every parent chain, returning one path per chain.
//
// A parent that cannot be resolved ends its chain rather than discarding it: the
// result is then relative to the deepest ancestor actually present, which is the
// most that can honestly be said about that entry's location.
func collectMetaPaths(
	meta core.FileMeta,
	lookupParent func(string) (core.FileMeta, bool),
	depth int,
	visiting map[string]bool,
) []string {
	// Snapshots predating RFC 0015 persist Paths. A stored path is what the
	// source itself reported, so it wins over anything reconstructed.
	if stored := storedMetaPaths(meta); len(stored) > 0 {
		return stored
	}
	if depth >= maxParentDepth || len(meta.Parents) == 0 || visiting[meta.FileID] {
		return []string{path.Join(meta.Name)}
	}

	visiting[meta.FileID] = true
	defer delete(visiting, meta.FileID)

	var out []string
	seen := make(map[string]bool)
	for _, parentID := range meta.Parents {
		parent, ok := lookupParent(parentID)
		if !ok {
			continue
		}
		for _, prefix := range collectMetaPaths(parent, lookupParent, depth+1, visiting) {
			p := path.Join(prefix, meta.Name)
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
			if len(out) >= maxResolvedPaths {
				return out
			}
		}
	}
	if len(out) == 0 {
		return []string{path.Join(meta.Name)}
	}
	return out
}

// storedMetaPaths returns meta's persisted paths, normalized, dropping any that
// normalize away to nothing.
func storedMetaPaths(meta core.FileMeta) []string {
	var out []string
	for _, p := range meta.Paths {
		if n := normalizeMetaPath(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func normalizeMetaPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return ""
	}
	clean := path.Clean("/" + p)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." {
		return ""
	}
	return clean
}
