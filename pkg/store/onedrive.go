package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/retry"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

// OneDriveSourceConfig holds configuration for a OneDrive source.
type OneDriveSourceConfig struct {
	ClientID        string // empty uses built-in OAuth client
	TokenPath       string // where the OAuth token is cached
	ExcludePatterns []string
}

type OneDriveSource struct {
	Client  *http.Client
	account string // cached user principal name; populated lazily by Info()
	exclude *ExcludeMatcher
}

// NewOneDriveSource creates a new OneDriveSource from the given config.
func NewOneDriveSource(cfg OneDriveSourceConfig) (*OneDriveSource, error) {
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = defaultOneDriveClientID
	}

	ctx := context.Background()
	conf := &oauth2.Config{
		ClientID: clientID,
		Scopes:   []string{"Files.Read", "Files.Read.All", "User.Read", "offline_access"},
		Endpoint: microsoft.AzureADEndpoint("common"),
	}

	token, err := loadToken(cfg.TokenPath)
	if err != nil {
		token, err = exchangeWithLocalServer(conf, oauth2.AccessTypeOffline)
		if err != nil {
			return nil, fmt.Errorf("onedrive auth: %w", err)
		}
		_ = saveTokenJSON(cfg.TokenPath, token)
	}

	client := conf.Client(ctx, token)
	return &OneDriveSource{Client: client, exclude: NewExcludeMatcher(cfg.ExcludePatterns)}, nil
}

func (s *OneDriveSource) Info() core.SourceInfo {
	if s.account == "" {
		s.account = s.fetchAccount()
	}
	return core.SourceInfo{
		Type:    "onedrive",
		Account: s.account,
		Path:    "onedrive://",
	}
}

func (s *OneDriveSource) fetchAccount() string {
	req, err := http.NewRequestWithContext(context.Background(), "GET",
		"https://graph.microsoft.com/v1.0/me?$select=userPrincipalName", nil)
	if err != nil {
		return ""
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var me struct {
		UPN string `json:"userPrincipalName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return ""
	}
	return me.UPN
}

func loadToken(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var tok oauth2.Token
	err = json.NewDecoder(f).Decode(&tok)
	return &tok, err
}

func saveTokenJSON(file string, token *oauth2.Token) error {
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
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
	Package              *graphPackage   `json:"package"`
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
	ID   string `json:"id"`
	Path string `json:"path"` // e.g. "/drive/root:/Documents/Reports"
}

type graphPackage struct {
	Type string `json:"type"`
}

// isDownloadable returns true for regular files and folders.
// Package items (e.g. OneNote notebooks/sections) are not downloadable via /content.
func (item graphItem) isDownloadable() bool {
	if item.Package != nil {
		return false
	}
	return item.File != nil || item.Folder != nil
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

	var rootItem graphItem
	err := retry.Do(ctx, retry.DefaultPolicy(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", rootURL, nil)
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
		return json.Unmarshal(body, &rootItem)
	})
	if err != nil {
		return err
	}

	// pathMap tracks itemID → full path for all emitted entries.
	pathMap := make(map[string]string)

	// Iterative DFS using an explicit stack instead of recursion.
	type stackEntry struct {
		folderID string
		url      string
	}
	stack := []stackEntry{{folderID: rootItem.ID, url: fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/children", rootItem.ID)}}

	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		url := top.url
		for {
			listResp, err := s.fetchPage(ctx, url)
			if err != nil {
				return err
			}

			var childFolders []stackEntry
			for _, item := range listResp.Value {
				if !item.isDownloadable() {
					continue
				}
				meta := s.toFileMeta(item)

				// Compute full path from parent path map.
				p := meta.Name
				if item.ParentReference != nil && item.ParentReference.ID != "" {
					if parentPath, ok := pathMap[item.ParentReference.ID]; ok {
						p = parentPath + "/" + meta.Name
					}
				}
				meta.Paths = []string{p}
				pathMap[item.ID] = p

				// Apply exclude patterns.
				if !s.exclude.Empty() {
					isDir := meta.Type == core.FileTypeFolder
					if s.exclude.Excludes(p, isDir) {
						continue // skip entry; excluded dirs won't be pushed onto stack
					}
				}

				if err := callback(meta); err != nil {
					return err
				}

				if meta.Type == core.FileTypeFolder {
					childFolders = append(childFolders, stackEntry{
						folderID: item.ID,
						url:      fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/children", item.ID),
					})
				}
			}

			// Push child folders onto the stack (reverse order for DFS consistency).
			for i := len(childFolders) - 1; i >= 0; i-- {
				stack = append(stack, childFolders[i])
			}

			if listResp.NextLink == "" {
				break
			}
			url = listResp.NextLink
		}
	}
	return nil
}

func (s *OneDriveSource) fetchPage(ctx context.Context, url string) (*graphListResponse, error) {
	var listResp graphListResponse
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
		return json.Unmarshal(body, &listResp)
	})
	return &listResp, err
}

// Size returns the total storage usage for the OneDrive account by calling
// the /me/drive endpoint which includes quota information.
func (s *OneDriveSource) Size(ctx context.Context) (*SourceSize, error) {
	var result struct {
		Quota struct {
			Used      int64 `json:"used"`
			Total     int64 `json:"total"`
			FileCount int64 `json:"fileCount"`
		} `json:"quota"`
	}

	err := retry.Do(ctx, retry.DefaultPolicy(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/me/drive?$select=quota", nil)
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
		return json.Unmarshal(body, &result)
	})
	if err != nil {
		return nil, err
	}

	return &SourceSize{Bytes: result.Quota.Used, Files: result.Quota.FileCount}, nil
}

func (s *OneDriveSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", fileID)

	var body io.ReadCloser
	err := retry.Do(context.Background(), retry.DefaultPolicy(), func() error {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}

		// The /content endpoint returns a 302 redirect to a pre-authenticated
		// download URL on a different domain. We must NOT follow the redirect with
		// the oauth2 client, because it would forward the Graph API Bearer token to
		// SharePoint, which rejects it with 401.
		noFollow := *s.Client
		noFollow.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}

		resp, err := noFollow.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode/100 == 3 {
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if location == "" {
				return fmt.Errorf("redirect without Location header")
			}
			resp, err = http.Get(location)
			if err != nil {
				return &retry.RetryableError{Err: err}
			}
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return retry.ClassifyHTTPResponse(resp, respBody)
		}

		body = resp.Body
		return nil
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}
