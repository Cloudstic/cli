package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudstic/cli/internal/retry"
	"github.com/cloudstic/cli/pkg/source"
)

// ChangeSource is an IncrementalSource backed by the Microsoft Graph
// delta API. It embeds Source to reuse authentication, full Walk,
// GetFileStream, and metadata conversion.
type ChangeSource struct {
	Source
}

func NewChangeSource(ctx context.Context, opts ...Option) (*ChangeSource, error) {
	base, err := New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &ChangeSource{Source: *base}, nil
}

func (s *ChangeSource) Info() source.SourceInfo {
	info := s.Source.Info()
	info.Type = "onedrive-changes"
	return info
}

// GetStartPageToken returns the current head of the OneDrive delta stream by
// requesting a "latest" delta token. The returned string is a full deltaLink URL.
func (s *ChangeSource) GetStartPageToken() (string, error) {
	url := s.getRootURL()
	if normalizeOneDriveRootPath(s.rootPath) != "/" {
		url += ":/delta?token=latest"
	} else {
		url += "/delta?token=latest"
	}
	resp, err := s.fetchDeltaPage(context.Background(), url)
	if err != nil {
		return "", fmt.Errorf("get latest delta token: %w", err)
	}
	if resp.DeltaLink == "" {
		return "", fmt.Errorf("no delta link in latest token response")
	}
	return resp.DeltaLink, nil
}

// WalkChanges iterates over all changes since the given delta token. Folder
// changes are emitted before file changes so that the engine can resolve
// parent references incrementally. Returns the new delta token for the next run.
func (s *ChangeSource) WalkChanges(ctx context.Context, token string, callback func(source.FileChange) error) (string, error) {
	var folderChanges, fileChanges []source.FileChange

	url := token
	for {
		resp, err := s.fetchDeltaPage(ctx, url)
		if err != nil {
			return "", fmt.Errorf("list delta changes: %w", err)
		}

		for _, item := range resp.Value {
			if item.Deleted == nil && !item.isDownloadable() {
				continue
			}
			fc := s.itemToFileChange(item)
			if fc.Type == source.ChangeUpsert && fc.Meta.Type == source.FileTypeFolder {
				folderChanges = append(folderChanges, fc)
			} else {
				fileChanges = append(fileChanges, fc)
			}
		}

		if resp.NextLink != "" {
			url = resp.NextLink
			continue
		}

		folderChanges = source.TopoSortFolderChanges(folderChanges)

		// Resolve paths and apply exclude filtering.
		hasExclude := !s.exclude.Empty()
		excludedIDs := make(map[string]bool)

		folderChanges = s.filterChangesByRootPath(folderChanges)
		fileChanges = s.filterChangesByRootPath(fileChanges)

		for _, fc := range folderChanges {
			if hasExclude && fc.Type == source.ChangeUpsert && shouldExcludeOneDriveChange(s.exclude, fc, excludedIDs) {
				continue
			}
			if err := callback(fc); err != nil {
				return "", err
			}
		}

		for _, fc := range fileChanges {
			if hasExclude && fc.Type == source.ChangeUpsert && shouldExcludeOneDriveChange(s.exclude, fc, excludedIDs) {
				continue
			}
			if err := callback(fc); err != nil {
				return "", err
			}
		}
		return resp.DeltaLink, nil
	}
}

func (s *ChangeSource) filterChangesByRootPath(changes []source.FileChange) []source.FileChange {
	normalizedRoot := normalizeOneDriveRootPath(s.rootPath)
	if normalizedRoot == "/" {
		return changes
	}
	var valid []source.FileChange
	trimmedRoot := strings.TrimPrefix(normalizedRoot, "/")
	for _, fc := range changes {
		if len(fc.Meta.Paths) > 0 {
			p := fc.Meta.Paths[0]
			if p == trimmedRoot {
				continue // Skip root folder itself; emit descendants only.
			}
			if !strings.HasPrefix(p, trimmedRoot+"/") {
				continue // Outside of root path
			}
			// Adjust path relative to root
			fc.Meta.Paths = []string{strings.TrimPrefix(p, trimmedRoot+"/")}
		} else if fc.Type == source.ChangeUpsert {
			continue
		}
		valid = append(valid, fc)
	}
	return valid
}

func (s *ChangeSource) itemToFileChange(item graphItem) source.FileChange {
	if item.Deleted != nil {
		return source.FileChange{
			Type: source.ChangeDelete,
			Meta: source.FileMeta{FileID: item.ID},
		}
	}
	meta := s.toFileMeta(item)
	// Resolve full path from parentReference.path (provided by the delta API).
	if item.ParentReference != nil && item.ParentReference.Path != "" {
		parentPath := stripOneDriveRootPrefix(item.ParentReference.Path)
		if parentPath != "" {
			meta.Paths = []string{parentPath + "/" + meta.Name}
		} else {
			meta.Paths = []string{meta.Name}
		}
	}
	return source.FileChange{
		Type: source.ChangeUpsert,
		Meta: meta,
	}
}

// stripOneDriveRootPrefix strips the "/drive/root:" or "/drive/root:/" prefix
// from a OneDrive parentReference.path, returning the relative path.
func stripOneDriveRootPrefix(p string) string {
	// The path format is "/drive/root:" for items directly under root,
	// or "/drive/root:/path/to/folder" for nested items.
	if idx := strings.Index(p, ":/"); idx >= 0 {
		return p[idx+2:]
	}
	// "/drive/root:" means directly under root.
	if strings.HasSuffix(p, ":") {
		return ""
	}
	return p
}

// shouldExcludeOneDriveChange checks whether a change entry should be excluded.
// For excluded directories, their ID is added to excludedIDs so children
// are also suppressed.
func shouldExcludeOneDriveChange(m *source.ExcludeMatcher, fc source.FileChange, excludedIDs map[string]bool) bool {
	// Check if parent is excluded.
	if len(fc.Meta.Parents) > 0 && excludedIDs[fc.Meta.Parents[0]] {
		if fc.Meta.Type == source.FileTypeFolder {
			excludedIDs[fc.Meta.FileID] = true
		}
		return true
	}
	if len(fc.Meta.Paths) == 0 {
		return false
	}
	isDir := fc.Meta.Type == source.FileTypeFolder
	if m.Excludes(fc.Meta.Paths[0], isDir) {
		if isDir {
			excludedIDs[fc.Meta.FileID] = true
		}
		return true
	}
	return false
}

type graphDeltaResponse struct {
	Value     []graphItem `json:"value"`
	NextLink  string      `json:"@odata.nextLink"`
	DeltaLink string      `json:"@odata.deltaLink"`
}

func (s *ChangeSource) fetchDeltaPage(ctx context.Context, url string) (*graphDeltaResponse, error) {
	var deltaResp graphDeltaResponse
	err := retry.Do(ctx, retry.DefaultPolicy(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(resp.Body)
		if apiErr := retry.ClassifyHTTPResponse(resp, body); apiErr != nil {
			return apiErr
		}
		return json.Unmarshal(body, &deltaResp)
	})
	return &deltaResp, err
}
