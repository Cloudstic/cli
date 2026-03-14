package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/retry"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

// oneDriveOptions holds configuration for a OneDrive source.
type oneDriveOptions struct {
	clientID        string
	tokenPath       string
	driveName       string
	rootPath        string
	excludePatterns []string
}

// OneDriveOption configures a OneDrive source.
type OneDriveOption func(*oneDriveOptions)

// WithOneDriveClientID sets the OAuth client ID. If empty, uses the built-in default.
func WithOneDriveClientID(id string) OneDriveOption {
	return func(o *oneDriveOptions) {
		o.clientID = id
	}
}

// WithOneDriveDriveName sets the shared drive name.
func WithOneDriveDriveName(name string) OneDriveOption {
	return func(o *oneDriveOptions) {
		o.driveName = name
	}
}

// WithOneDriveRootPath sets the path to the root folder.
func WithOneDriveRootPath(path string) OneDriveOption {
	return func(o *oneDriveOptions) {
		o.rootPath = path
	}
}

// WithOneDriveTokenPath sets the path where the OAuth token is cached.
func WithOneDriveTokenPath(path string) OneDriveOption {
	return func(o *oneDriveOptions) {
		o.tokenPath = path
	}
}

// WithOneDriveExcludePatterns sets the patterns used to exclude files and folders.
func WithOneDriveExcludePatterns(patterns []string) OneDriveOption {
	return func(o *oneDriveOptions) {
		o.excludePatterns = patterns
	}
}

type OneDriveSource struct {
	client    *http.Client
	accountID string // cached stable account identity; populated lazily by Info()
	account   string // cached user principal name; populated lazily by Info()
	driveID   string // The resolved Drive ID
	driveName string // The Drive Name (from config)
	rootPath  string // The string path the user specified, or "/"
	rootID    string // stable selected root folder/item ID
	exclude   *ExcludeMatcher
}

// NewOneDriveSource creates a new OneDriveSource from the given config.
func NewOneDriveSource(ctx context.Context, opts ...OneDriveOption) (*OneDriveSource, error) {
	var cfg oneDriveOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	clientID := cfg.clientID
	if clientID == "" {
		clientID = defaultOneDriveClientID
	}

	conf := &oauth2.Config{
		ClientID: clientID,
		Scopes:   []string{"Files.Read", "Files.Read.All", "User.Read", "offline_access"},
		Endpoint: microsoft.AzureADEndpoint("common"),
	}

	token, err := loadToken(cfg.tokenPath)
	if err != nil {
		token, err = exchangeWithLocalServer(conf, oauth2.AccessTypeOffline)
		if err != nil {
			return nil, fmt.Errorf("onedrive auth: %w", err)
		}
		_ = saveTokenJSON(cfg.tokenPath, token)
	}

	client := conf.Client(ctx, token)

	rootPath := cfg.rootPath
	if rootPath == "" {
		rootPath = "/"
	}
	src := &OneDriveSource{
		client:    client,
		driveName: cfg.driveName,
		rootPath:  rootPath,
		exclude:   NewExcludeMatcher(cfg.excludePatterns),
	}

	if src.driveName != "" {
		err = src.resolveDriveName(ctx)
		if err != nil {
			return nil, err
		}
	}

	return src, nil
}

func (s *OneDriveSource) resolveDriveName(ctx context.Context) error {
	// Try fetching by ID first
	req, err := http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/drives/"+s.driveName+"?$select=id,name", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			var drive struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&drive); err == nil {
				s.driveID = drive.ID
				s.driveName = drive.Name
				return nil
			}
		}
	}

	// Fetch all drives and find by name
	req, err = http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/me/drives?$select=id,name", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err = s.client.Do(req)
	if err != nil {
		return fmt.Errorf("list drives: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("list drives returned status %d", resp.StatusCode)
	}

	var result struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode drives: %w", err)
	}

	var matchedID string
	var matchedName string
	matches := 0
	for _, d := range result.Value {
		if d.Name == s.driveName {
			matchedID = d.ID
			matchedName = d.Name
			matches++
		}
	}

	if matches == 0 {
		return fmt.Errorf("drive %q not found", s.driveName)
	}
	if matches > 1 {
		return fmt.Errorf("ambiguous drive name: multiple drives named %q found", s.driveName)
	}

	s.driveID = matchedID
	s.driveName = matchedName
	return nil
}

func (s *OneDriveSource) Info() core.SourceInfo {
	if s.client != nil && (s.account == "" || s.accountID == "") {
		id, upn := s.fetchAccountInfo()
		if s.accountID == "" {
			s.accountID = id
		}
		if s.account == "" {
			s.account = upn
		}
	}
	if s.client != nil && s.rootID == "" {
		s.rootID = s.resolveRootID(context.Background())
	}
	info := core.SourceInfo{
		Type:      "onedrive",
		Account:   s.account,
		Path:      s.rootPath,
		PathID:    s.rootID,
		DriveName: "My Drive",
	}
	if s.driveID != "" {
		info.Identity = s.driveID
		info.DriveName = s.driveName
	} else if s.accountID != "" {
		info.Identity = s.accountID
	} else {
		info.Identity = s.account
	}
	if info.PathID == "" {
		info.PathID = s.rootPath
	}
	return info
}

func (s *OneDriveSource) fetchAccountInfo() (id, upn string) {
	req, err := http.NewRequestWithContext(context.Background(), "GET",
		"https://graph.microsoft.com/v1.0/me?$select=id,userPrincipalName", nil)
	if err != nil {
		return "", ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var me struct {
		ID  string `json:"id"`
		UPN string `json:"userPrincipalName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return "", ""
	}
	return me.ID, me.UPN
}

func (s *OneDriveSource) resolveRootID(ctx context.Context) string {
	rootURL := s.getRootURL()
	req, err := http.NewRequestWithContext(ctx, "GET", rootURL, nil)
	if err != nil {
		return ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var item struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return ""
	}
	if item.ID == "" {
		return ""
	}
	return item.ID
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
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
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

func (s *OneDriveSource) getRootURL() string {
	base := "https://graph.microsoft.com/v1.0/me/drive/root"
	if s.driveID != "" {
		base = fmt.Sprintf("https://graph.microsoft.com/v1.0/drives/%s/root", s.driveID)
	}
	if s.rootPath != "" && s.rootPath != "/" {
		return fmt.Sprintf("%s:%s", base, s.rootPath)
	}
	return base
}

func (s *OneDriveSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	rootURL := s.getRootURL()

	var rootItem graphItem
	err := retry.Do(ctx, retry.DefaultPolicy(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", rootURL, nil)
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
		return json.Unmarshal(body, &rootItem)
	})
	if err != nil {
		return err
	}

	// pathMap tracks itemID → full path for all emitted entries.
	pathMap := make(map[string]string)
	pathMap[rootItem.ID] = "" // Root folder has empty path relative to itself

	// Iterative DFS using an explicit stack instead of recursion.
	type stackEntry struct {
		folderID string
		url      string
	}
	childrenURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/children", rootItem.ID)
	stack := []stackEntry{{folderID: rootItem.ID, url: childrenURL}}

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
						if parentPath != "" {
							p = parentPath + "/" + meta.Name
						}
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

		resp, err := s.client.Do(req)
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
		resp, err := s.client.Do(req)
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
		noFollow := *s.client
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
