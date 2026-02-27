package store

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

func NewGDriveChangeSource(credsPath, tokenPath string) (*GDriveChangeSource, error) {
	base, err := NewGDriveSource(credsPath, tokenPath)
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
	call := s.Service.Changes.GetStartPageToken()
	if s.isSharedDrive() {
		call.DriveId(s.DriveID).SupportsAllDrives(true)
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
	var folderChanges, fileChanges []FileChange

	pageToken := token
	for {
		call := s.Service.Changes.List(pageToken).
			Fields("nextPageToken, newStartPageToken, changes(fileId, removed, file(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum))").
			PageSize(1000).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.DriveID).
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
		}

		resp, err := driveCallWithRetry(ctx, func() (*drive.ChangeList, error) { return call.Do() })
		if err != nil {
			return "", fmt.Errorf("list changes: %w", err)
		}

		for _, ch := range resp.Changes {
			fc := s.changeToFileChange(ch)
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

			for _, fc := range folderChanges {
				if err := callback(fc); err != nil {
					return "", err
				}
			}
			for _, fc := range fileChanges {
				if err := callback(fc); err != nil {
					return "", err
				}
			}
			return resp.NewStartPageToken, nil
		}
	}
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

func (s *GDriveChangeSource) changeToFileChange(ch *drive.Change) FileChange {
	if ch.Removed || (ch.File != nil && ch.File.Trashed) {
		return FileChange{
			Type: ChangeDelete,
			Meta: core.FileMeta{FileID: ch.FileId},
		}
	}
	return FileChange{
		Type: ChangeUpsert,
		Meta: s.toFileMeta(ch.File),
	}
}
