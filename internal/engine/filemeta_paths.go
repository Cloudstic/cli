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

func fileMetaPath(meta core.FileMeta, lookupParent func(string) (core.FileMeta, bool)) string {
	if p := normalizeMetaPath(firstMetaPath(meta)); p != "" {
		return p
	}

	const maxDepth = 50
	parts := []string{meta.Name}
	cur := meta
	for i := 0; i < maxDepth && len(cur.Parents) > 0; i++ {
		parent, ok := lookupParent(cur.Parents[0])
		if !ok {
			break
		}
		if p := normalizeMetaPath(firstMetaPath(parent)); p != "" {
			return path.Join(append([]string{p}, reverseParts(parts)...)...)
		}
		parts = append(parts, parent.Name)
		cur = parent
	}
	return path.Join(reverseParts(parts)...)
}

func firstMetaPath(meta core.FileMeta) string {
	if len(meta.Paths) == 0 {
		return ""
	}
	return meta.Paths[0]
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

func reverseParts(parts []string) []string {
	reversed := make([]string, len(parts))
	for i := range parts {
		reversed[len(parts)-1-i] = parts[i]
	}
	return reversed
}
