package source

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudstic/cli/internal/core"
)

var filepathAbs = filepath.Abs

func normalizeVolumeUUID(uuid string) string {
	return strings.ToUpper(strings.TrimSpace(uuid))
}

func (s *LocalSource) Info() core.SourceInfo {
	hostname, _ := os.Hostname()
	absPath, _ := filepathAbs(s.rootPath)

	// When volume UUID is present, store the path relative to the volume
	// mount point instead of the absolute path. This makes the path
	// stable across machines where mount points differ.
	infoPath := absPath
	if s.volumeUUID != "" && s.volumeMountPoint != "" {
		// Resolve symlinks so the relative path is correct (e.g. on macOS
		// /var → /private/var but the mount point is /System/Volumes/Data).
		realAbs, errA := filepath.EvalSymlinks(absPath)
		realMount, errM := filepath.EvalSymlinks(s.volumeMountPoint)
		if errA == nil && errM == nil {
			if pathWithinRoot(realMount, realAbs) {
				if rel, err := filepath.Rel(realMount, realAbs); err == nil {
					infoPath = filepath.ToSlash(rel)
				}
			} else if pathWithinRoot(s.volumeMountPoint, absPath) {
				if rel, err := filepath.Rel(s.volumeMountPoint, absPath); err == nil {
					infoPath = filepath.ToSlash(rel)
				}
			}
		} else if pathWithinRoot(s.volumeMountPoint, absPath) {
			if rel, err := filepath.Rel(s.volumeMountPoint, absPath); err == nil {
				infoPath = filepath.ToSlash(rel)
			}
		}
	}

	pathID := infoPath
	displayPath := infoPath
	if s.volumeUUID != "" {
		clean := strings.TrimPrefix(pathID, "./")
		switch clean {
		case "", ".":
			displayPath = "/"
		default:
			displayPath = "/" + strings.TrimPrefix(clean, "/")
		}
		pathID = displayPath
	}

	return core.SourceInfo{
		Type:      "local",
		Account:   hostname,
		Path:      displayPath,
		PathID:    pathID,
		DriveName: s.volumeLabel,
		FsType:    s.fsType,

		Identity: func() string {
			if s.volumeUUID != "" {
				return s.volumeUUID
			}
			return hostname
		}(),
	}
}

func pathWithinRoot(root, target string) bool {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)
	if rootClean == "." || rootClean == "" {
		return false
	}
	if targetClean == rootClean {
		return true
	}
	return strings.HasPrefix(targetClean, rootClean+string(filepath.Separator))
}

// localOptions holds configuration for a local filesystem source.
type localOptions struct {
	excludePatterns []string
	volumeUUID      string   // explicit override for volume UUID
	skipMode        bool     // skip Mode, Uid, Gid, Btime, Flags collection
	skipFlags       bool     // skip Flags ioctl only (Linux); no-op on macOS
	skipXattrs      bool     // skip extended attribute collection
	xattrNamespaces []string // restrict xattr collection to these prefixes
}

// LocalOption configures a local filesystem source.
type LocalOption func(*localOptions)

// WithLocalExcludePatterns sets the patterns used to exclude files and folders.
func WithLocalExcludePatterns(patterns []string) LocalOption {
	return func(o *localOptions) {
		o.excludePatterns = patterns
	}
}

// WithVolumeUUID overrides the auto-detected volume UUID for the source.
// Use this for filesystems where UUID detection is unsupported or to
// explicitly tie backups to a specific snapshot lineage.
func WithVolumeUUID(uuid string) LocalOption {
	return func(o *localOptions) {
		o.volumeUUID = uuid
	}
}

// WithSkipMode disables collection of POSIX mode, uid, gid, btime, and flags.
func WithSkipMode() LocalOption {
	return func(o *localOptions) { o.skipMode = true }
}

// WithSkipFlags disables file flags collection.
func WithSkipFlags() LocalOption {
	return func(o *localOptions) { o.skipFlags = true }
}

// WithSkipXattrs disables extended attribute collection.
func WithSkipXattrs() LocalOption {
	return func(o *localOptions) { o.skipXattrs = true }
}

// WithXattrNamespaces restricts xattr collection to attributes whose name
// starts with one of the given prefixes (e.g. "user.", "com.apple.").
// An empty slice (default) collects all readable attributes.
func WithXattrNamespaces(prefixes []string) LocalOption {
	return func(o *localOptions) { o.xattrNamespaces = prefixes }
}

// LocalSource implements Source for local filesystem.
type LocalSource struct {
	rootPath         string
	exclude          *ExcludeMatcher
	volumeUUID       string
	volumeLabel      string
	volumeMountPoint string
	fsType           string
	skipMode         bool
	skipFlags        bool
	skipXattrs       bool
	xattrNamespaces  []string
}

// NewLocalSource creates a local filesystem source rooted at rootPath.
func NewLocalSource(rootPath string, opts ...LocalOption) *LocalSource {
	var cfg localOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	uuid, label, mountPoint := detectVolumeIdentity(rootPath)
	if cfg.volumeUUID != "" {
		uuid = cfg.volumeUUID
	}

	return &LocalSource{
		rootPath:         rootPath,
		exclude:          NewExcludeMatcher(cfg.excludePatterns),
		volumeUUID:       uuid,
		volumeLabel:      label,
		volumeMountPoint: mountPoint,
		fsType:           detectFsType(rootPath),
		skipMode:         cfg.skipMode,
		skipFlags:        cfg.skipFlags,
		skipXattrs:       cfg.skipXattrs,
		xattrNamespaces:  cfg.xattrNamespaces,
	}
}

func (s *LocalSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	return filepath.Walk(s.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(s.rootPath, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		// Apply exclude patterns.
		if !s.exclude.Empty() && s.exclude.Excludes(filepath.ToSlash(relPath), info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		var fileType core.FileType
		if info.IsDir() {
			fileType = core.FileTypeFolder
		} else {
			fileType = core.FileTypeFile
		}

		// Normalize to forward slashes so backup trees are portable across OS.
		normalizedPath := filepath.ToSlash(relPath)

		var parents []string
		if dir := filepath.ToSlash(filepath.Dir(relPath)); dir != "." {
			parents = []string{dir}
		}

		meta := core.FileMeta{
			FileID:  normalizedPath,
			Name:    filepath.Base(path),
			Type:    fileType,
			Parents: parents,
			Paths:   []string{normalizedPath},
			Size:    info.Size(),
			Mtime:   info.ModTime().Unix(),
		}

		readExtendedMeta(path, &meta, s.skipMode, s.skipFlags, s.skipXattrs, s.xattrNamespaces)

		return callback(meta)
	})
}

func (s *LocalSource) Size(ctx context.Context) (*SourceSize, error) {
	var totalBytes, totalFiles int64
	err := filepath.Walk(s.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !s.exclude.Empty() {
			relPath, relErr := filepath.Rel(s.rootPath, path)
			if relErr == nil && relPath != "." {
				if s.exclude.Excludes(filepath.ToSlash(relPath), info.IsDir()) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}
		if !info.IsDir() {
			totalBytes += info.Size()
			totalFiles++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &SourceSize{Bytes: totalBytes, Files: totalFiles}, nil
}

func (s *LocalSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	// fileID is relPath
	fullPath := filepath.Join(s.rootPath, fileID)
	return os.Open(fullPath)
}
