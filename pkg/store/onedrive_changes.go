package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/cloudstic/cli/pkg/core"
	"github.com/cloudstic/cli/pkg/retry"
)

// OneDriveChangeSource is an IncrementalSource backed by the Microsoft Graph
// delta API. It embeds OneDriveSource to reuse authentication, full Walk,
// GetFileStream, and metadata conversion.
type OneDriveChangeSource struct {
	OneDriveSource
}

func NewOneDriveChangeSource(clientID, tokenPath string) (*OneDriveChangeSource, error) {
	base, err := NewOneDriveSource(clientID, tokenPath)
	if err != nil {
		return nil, err
	}
	return &OneDriveChangeSource{OneDriveSource: *base}, nil
}

func (s *OneDriveChangeSource) Info() core.SourceInfo {
	info := s.OneDriveSource.Info()
	info.Type = "onedrive-changes"
	return info
}

// GetStartPageToken returns the current head of the OneDrive delta stream by
// requesting a "latest" delta token. The returned string is a full deltaLink URL.
func (s *OneDriveChangeSource) GetStartPageToken() (string, error) {
	resp, err := s.fetchDeltaPage(context.Background(), "https://graph.microsoft.com/v1.0/me/drive/root/delta?token=latest")
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
func (s *OneDriveChangeSource) WalkChanges(ctx context.Context, token string, callback func(FileChange) error) (string, error) {
	var folderChanges, fileChanges []FileChange

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
			if fc.Type == ChangeUpsert && fc.Meta.Type == core.FileTypeFolder {
				folderChanges = append(folderChanges, fc)
			} else {
				fileChanges = append(fileChanges, fc)
			}
		}

		if resp.NextLink != "" {
			url = resp.NextLink
			continue
		}

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
		return resp.DeltaLink, nil
	}
}

func (s *OneDriveChangeSource) itemToFileChange(item graphItem) FileChange {
	if item.Deleted != nil {
		return FileChange{
			Type: ChangeDelete,
			Meta: core.FileMeta{FileID: item.ID},
		}
	}
	return FileChange{
		Type: ChangeUpsert,
		Meta: s.toFileMeta(item),
	}
}

type graphDeltaResponse struct {
	Value     []graphItem `json:"value"`
	NextLink  string      `json:"@odata.nextLink"`
	DeltaLink string      `json:"@odata.deltaLink"`
}

func (s *OneDriveChangeSource) fetchDeltaPage(ctx context.Context, url string) (*graphDeltaResponse, error) {
	var deltaResp graphDeltaResponse
	err := retry.Do(ctx, retry.DefaultPolicy(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := s.Client.Do(req)
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
