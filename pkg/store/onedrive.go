package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/cloudstic/cli/pkg/core"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

type OneDriveSource struct {
	Client *http.Client
}

func NewOneDriveSource(clientID, clientSecret, tokenPath string) (*OneDriveSource, error) {
	ctx := context.Background()
	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"Files.Read", "Files.Read.All", "User.Read"},
		Endpoint:     microsoft.AzureADEndpoint("common"),
		RedirectURL:  "http://localhost:9999/callback",
	}

	// Load token
	token, err := loadToken(tokenPath)
	if err != nil {
		// If no token, we might need a way to trigger auth flow.
		// For now, assuming we fail if not present or print URL?
		// Copying gdrive pattern somewhat.
		url := conf.AuthCodeURL("state", oauth2.AccessTypeOffline)
		fmt.Printf("Visit the URL for the auth dialog: %v\n", url)
		fmt.Printf("Paste the code here: ")

		var code string
		if _, err := fmt.Scan(&code); err != nil {
			return nil, err
		}

		token, err = conf.Exchange(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange token: %w", err)
		}
		saveTokenJSON(tokenPath, token)
	}

	client := conf.Client(ctx, token)
	return &OneDriveSource{Client: client}, nil
}

func (s *OneDriveSource) Info() core.SourceInfo {
	return core.SourceInfo{Type: "onedrive"}
}

func loadToken(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var tok oauth2.Token
	err = json.NewDecoder(f).Decode(&tok)
	return &tok, err
}

func saveTokenJSON(file string, token *oauth2.Token) error {
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// Graph API Models
type graphItem struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Size                 int64           `json:"size"`
	LastModifiedDateTime string          `json:"lastModifiedDateTime"`
	File                 *graphFile      `json:"file"`
	Folder               *graphFolder    `json:"folder"`
	Deleted              *graphDeleted   `json:"deleted"`
	ParentReference      *graphParentRef `json:"parentReference"`
	DownloadURL          string          `json:"@microsoft.graph.downloadUrl"`
}

type graphDeleted struct {
	State string `json:"state"`
}

type graphFile struct {
	MimeType string `json:"mimeType"`
}

type graphFolder struct {
	ChildCount int `json:"childCount"`
}

type graphParentRef struct {
	ID string `json:"id"`
}

type graphListResponse struct {
	Value    []graphItem `json:"value"`
	NextLink string      `json:"@odata.nextLink"`
}

func (s *OneDriveSource) toFileMeta(item graphItem) core.FileMeta {
	mtime := int64(0)
	t, err := time.Parse(time.RFC3339, item.LastModifiedDateTime)
	if err == nil {
		mtime = t.Unix()
	}

	fileType := core.FileTypeFile
	if item.Folder != nil {
		fileType = core.FileTypeFolder
	}

	parents := []string{}
	if item.ParentReference != nil && item.ParentReference.ID != "" {
		parents = append(parents, item.ParentReference.ID)
	}

	return core.FileMeta{
		Version: 1,
		FileID:  item.ID,
		Name:    item.Name,
		Type:    fileType,
		Parents: parents,
		Size:    item.Size,
		Mtime:   mtime,
		Extra:   map[string]interface{}{"downloadUrl": item.DownloadURL},
	}
}

func (s *OneDriveSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	rootURL := "https://graph.microsoft.com/v1.0/me/drive/root"
	req, err := http.NewRequestWithContext(ctx, "GET", rootURL, nil)
	if err != nil {
		return err
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("graph api error fetching root: %s %s", resp.Status, string(body))
	}

	var rootItem graphItem
	if err := json.NewDecoder(resp.Body).Decode(&rootItem); err != nil {
		return err
	}

	return s.walkRecursive(ctx, rootItem.ID, callback)
}

func (s *OneDriveSource) walkRecursive(ctx context.Context, folderID string, callback func(core.FileMeta) error) error {
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/children", folderID)

	for {
		listResp, err := s.fetchPage(ctx, url)
		if err != nil {
			return err
		}

		for _, item := range listResp.Value {
			meta := s.toFileMeta(item)

			if err := callback(meta); err != nil {
				return err
			}

			if meta.Type == core.FileTypeFolder {
				if err := s.walkRecursive(ctx, item.ID, callback); err != nil {
					return err
				}
			}
		}

		if listResp.NextLink == "" {
			break
		}
		url = listResp.NextLink
	}
	return nil
}

func (s *OneDriveSource) fetchPage(ctx context.Context, url string) (*graphListResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("graph api error: %s %s", resp.Status, string(body))
	}

	var listResp graphListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, err
	}
	return &listResp, nil
}

// Size returns the total storage usage for the OneDrive account by calling
// the /me/drive endpoint which includes quota information.
func (s *OneDriveSource) Size(ctx context.Context) (*SourceSize, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/me/drive?$select=quota", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph drive request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("graph drive error: %s %s", resp.Status, string(body))
	}

	var result struct {
		Quota struct {
			Used      int64 `json:"used"`
			Total     int64 `json:"total"`
			FileCount int64 `json:"fileCount"`
		} `json:"quota"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode drive quota: %w", err)
	}

	return &SourceSize{Bytes: result.Quota.Used, Files: result.Quota.FileCount}, nil
}

func (s *OneDriveSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	// We can use the downloadUrl from metadata if valid, or request content directly
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", fileID)

	// Note: Client.Get will follow redirects, which is what we want for /content
	resp, err := s.Client.Get(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to download file: %s", resp.Status)
	}

	return resp.Body, nil
}
