package source

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/internal/core"

	"google.golang.org/api/drive/v3"
)

// GDriveChangeSource is an IncrementalSource backed by the Google Drive
// Changes API. It embeds GDriveSource to reuse authentication, full Walk,
// GetFileStream, and metadata conversion.
type GDriveChangeSource struct {
	GDriveSource
}

func NewGDriveChangeSource(ctx context.Context, opts ...GDriveOption) (*GDriveChangeSource, error) {
	base, err := NewGDriveSource(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &GDriveChangeSource{GDriveSource: *base}, nil
}

func (s *GDriveChangeSource) Info() core.SourceInfo {
	info := s.GDriveSource.Info()
	info.Type = "gdrive-changes"
	return info
}

// GetStartPageToken returns the token representing the current head of the
// Google Drive change stream.
func (s *GDriveChangeSource) GetStartPageToken() (string, error) {
	call := s.service.Changes.GetStartPageToken()
	if s.isSharedDrive() {
		call.DriveId(s.driveID).SupportsAllDrives(true)
	}
	resp, err := driveCallWithRetry(context.Background(), func() (*drive.StartPageToken, error) { return call.Do() })
	if err != nil {
		return "", fmt.Errorf("get start page token: %w", err)
	}
	return resp.StartPageToken, nil
}

// WalkChanges iterates over all changes since the given page token. Folder
// changes are emitted before file changes so that the engine can resolve
// parent references incrementally.
func (s *GDriveChangeSource) WalkChanges(ctx context.Context, token string, callback func(FileChange) error) (string, error) {
	s.mimeTypes = make(map[string]string)

	var folderChanges, fileChanges []FileChange

	pageToken := token
	for {
		call := s.service.Changes.List(pageToken).
			Fields("nextPageToken, newStartPageToken, changes(fileId, removed, file(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum, headRevisionId))").
			PageSize(1000).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.driveID).
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
		}

		resp, err := driveCallWithRetry(ctx, func() (*drive.ChangeList, error) { return call.Do() })
		if err != nil {
			return "", fmt.Errorf("list changes: %w", err)
		}

		for _, ch := range resp.Changes {
			fc := s.changeToFileChange(ch)

			// Skip native files when the user opted out.
			if s.skipNativeFiles && fc.Type == ChangeUpsert && ch.File != nil && isGoogleNativeMimeType(ch.File.MimeType) {
				continue
			}

			if fc.Type == ChangeUpsert && fc.Meta.Type == core.FileTypeFolder {
				folderChanges = append(folderChanges, fc)
			} else {
				fileChanges = append(fileChanges, fc)
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			// Topologically sort folder changes so parents are emitted
			// before children, ensuring resolveParents sees up-to-date refs.
			folderChanges = topoSortFolderChanges(folderChanges)

			// Resolve paths and apply exclude filtering.
			pathMap := make(map[string]string)
			excludedIDs := make(map[string]bool)
			hasExclude := !s.exclude.Empty()

			var err error
			folderChanges, err = s.processChanges(ctx, folderChanges, pathMap, true)
			if err != nil {
				return "", err
			}
			fileChanges, err = s.processChanges(ctx, fileChanges, pathMap, false)
			if err != nil {
				return "", err
			}

			for _, fc := range folderChanges {
				if hasExclude && fc.Type == ChangeUpsert && s.shouldExcludeChange(fc, excludedIDs) {
					continue
				}
				if err := callback(fc); err != nil {
					return "", err
				}
			}
			for _, fc := range fileChanges {
				if hasExclude && fc.Type == ChangeUpsert && s.shouldExcludeChange(fc, excludedIDs) {
					continue
				}
				if err := callback(fc); err != nil {
					return "", err
				}
			}
			return resp.NewStartPageToken, nil
		}
	}
}

func (s *GDriveChangeSource) processChanges(ctx context.Context, changes []FileChange, pathMap map[string]string, isFolder bool) ([]FileChange, error) {
	// Fast-path: if no root folder is specified, we don't need to filter anything out,
	// and we can update the slice in-place without allocating a new one.
	if s.rootFolderID == "" {
		for i := range changes {
			fc := &changes[i]
			if fc.Type != ChangeUpsert {
				continue
			}
			p, err := s.resolveChangePath(ctx, fc.Meta, pathMap)
			if err != nil {
				return nil, err
			}
			if p != "" {
				fc.Meta.Paths = []string{p}
				if isFolder {
					pathMap[fc.Meta.FileID] = p
				}
			}
		}
		return changes, nil
	}

	var validChanges []FileChange
	for i := range changes {
		fc := changes[i]
		if fc.Type != ChangeUpsert {
			validChanges = append(validChanges, fc)
			continue
		}
		// Skip changes strictly outside our root folder
		if !s.isDescendantOfRoot(ctx, fc.Meta) {
			continue
		}
		p, err := s.resolveChangePath(ctx, fc.Meta, pathMap)
		if err != nil {
			return nil, err
		}
		// If p == "" and we have a rootFolderID, it means resolveChangePath
		// determined this is not a descendant of rootFolderID.
		if p == "" {
			continue
		}
		if p != "" {
			fc.Meta.Paths = []string{p}
			if isFolder {
				pathMap[fc.Meta.FileID] = p
			}
		}
		validChanges = append(validChanges, fc)
	}
	return validChanges, nil
}

// isDescendantOfRoot checks if a changed file belongs to the rootFolderID tree.
func (s *GDriveChangeSource) isDescendantOfRoot(ctx context.Context, meta core.FileMeta) bool {
	if s.rootFolderID == "" {
		return true // No root folder specified, everything is a descendant
	}
	if len(meta.Parents) == 0 {
		return false // It's in the drive root, not inside rootFolderID
	}
	for _, pid := range meta.Parents {
		if pid == s.rootFolderID {
			return true
		}
		// We need to resolve the path up to the root to verify.
		// s.resolveChangePath naturally stops at rootFolderID because
		// we check it, and returns errNotDescendant if it goes past it.
		// We'll rely on resolveChangePath for the full strict check.
	}
	return true // Optimistic check, rely on resolveChangePath for definitive answer
}

// topoSortFolderChanges orders folder upsert changes so that every parent
// appears before its children, using raw Drive parent IDs in Meta.Parents.
func topoSortFolderChanges(changes []FileChange) []FileChange {
	byID := make(map[string]int, len(changes))
	for i, fc := range changes {
		byID[fc.Meta.FileID] = i
	}

	visited := make(map[string]bool, len(changes))
	sorted := make([]FileChange, 0, len(changes))

	var visit func(idx int)
	visit = func(idx int) {
		fc := changes[idx]
		if visited[fc.Meta.FileID] {
			return
		}
		visited[fc.Meta.FileID] = true
		for _, pid := range fc.Meta.Parents {
			if pidx, ok := byID[pid]; ok {
				visit(pidx)
			}
		}
		sorted = append(sorted, fc)
	}

	for i := range changes {
		visit(i)
	}
	return sorted
}

// resolveChangePath computes the full path for a changed entry by looking up
// its parent in pathMap. If the parent is not in the map, it walks up the
// Drive hierarchy via API calls and caches every resolved segment.
func (s *GDriveChangeSource) resolveChangePath(ctx context.Context, meta core.FileMeta, pathMap map[string]string) (string, error) {
	if len(meta.Parents) == 0 {
		if s.rootFolderID != "" {
			return "", nil // Not in our root folder
		}
		return meta.Name, nil
	}
	parentPath, err := s.resolveDrivePath(ctx, meta.Parents[0], pathMap)
	if err != nil {
		if err == errNotDescendant {
			return "", nil
		}
		return "", err
	}
	if parentPath == "" {
		return meta.Name, nil
	}
	return parentPath + "/" + meta.Name, nil
}

var errNotDescendant = fmt.Errorf("not a descendant of root folder")

// resolveDrivePath resolves a Drive folder ID to its full path by walking
// up the parent chain via the Files.Get API. Results are cached in pathMap.
func (s *GDriveChangeSource) resolveDrivePath(ctx context.Context, folderID string, pathMap map[string]string) (string, error) {
	if s.rootFolderID != "" && folderID == s.rootFolderID {
		return "", nil // Base of the tree
	}
	if p, ok := pathMap[folderID]; ok {
		return p, nil
	}

	call := s.service.Files.Get(folderID).
		Fields("id, name, parents").
		SupportsAllDrives(true)
	f, err := driveCallWithRetry(ctx, func() (*drive.File, error) { return call.Do() })
	if err != nil {
		return "", fmt.Errorf("resolve drive path for %s: %w", folderID, err)
	}

	p := f.Name
	if len(f.Parents) > 0 {
		if s.rootFolderID != "" && f.Parents[0] == s.rootFolderID {
			pathMap[folderID] = p
			return p, nil
		}
		parentPath, err := s.resolveDrivePath(ctx, f.Parents[0], pathMap)
		if err != nil {
			return "", err
		}
		if parentPath != "" {
			p = parentPath + "/" + f.Name
		}
	} else if s.rootFolderID != "" {
		// Reached the root of the drive, but it wasn't our rootFolderID
		return "", errNotDescendant
	}
	pathMap[folderID] = p
	return p, nil
}

// shouldExcludeChange checks whether a change entry should be excluded.
// For excluded directories, their ID is added to excludedIDs so children
// are also suppressed.
func (s *GDriveChangeSource) shouldExcludeChange(fc FileChange, excludedIDs map[string]bool) bool {
	// Check if parent is excluded.
	if len(fc.Meta.Parents) > 0 && excludedIDs[fc.Meta.Parents[0]] {
		if fc.Meta.Type == core.FileTypeFolder {
			excludedIDs[fc.Meta.FileID] = true
		}
		return true
	}
	if len(fc.Meta.Paths) == 0 {
		return false // can't evaluate without a path
	}
	isDir := fc.Meta.Type == core.FileTypeFolder
	if s.exclude.Excludes(fc.Meta.Paths[0], isDir) {
		if isDir {
			excludedIDs[fc.Meta.FileID] = true
		}
		return true
	}
	return false
}

func (s *GDriveChangeSource) changeToFileChange(ch *drive.Change) FileChange {
	if ch.Removed || (ch.File != nil && ch.File.Trashed) {
		return FileChange{
			Type: ChangeDelete,
			Meta: core.FileMeta{FileID: ch.FileId},
		}
	}

	// Record MIME type for GetFileStream export routing.
	if ch.File.MimeType != "application/vnd.google-apps.folder" {
		s.mimeTypes[ch.File.Id] = ch.File.MimeType
	}

	return FileChange{
		Type: ChangeUpsert,
		Meta: s.toFileMeta(ch.File),
	}
}
