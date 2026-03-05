package source

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
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// gDriveOptions holds configuration for a Google Drive source.
type gDriveOptions struct {
	httpClient      *http.Client
	credsPath       string
	tokenPath       string
	driveID         string
	rootFolderID    string
	accountEmail    string
	excludePatterns []string
}

// GDriveOption configures a Google Drive source.
type GDriveOption func(*gDriveOptions)

// WithHTTPClient sets a custom HTTP client for OAuth.
func WithHTTPClient(client *http.Client) GDriveOption {
	return func(o *gDriveOptions) {
		o.httpClient = client
	}
}

// WithCredsPath sets the path to the credentials JSON file.
// If empty, uses the built-in OAuth client.
func WithCredsPath(path string) GDriveOption {
	return func(o *gDriveOptions) {
		o.credsPath = path
	}
}

// WithTokenPath sets the path where the OAuth token is cached.
func WithTokenPath(path string) GDriveOption {
	return func(o *gDriveOptions) {
		o.tokenPath = path
	}
}

// WithDriveID sets the shared drive ID. If empty, defaults to "My Drive".
func WithDriveID(id string) GDriveOption {
	return func(o *gDriveOptions) {
		o.driveID = id
	}
}

// WithRootFolderID sets the root folder ID.
// If empty, defaults to the root of the specified drive.
func WithRootFolderID(id string) GDriveOption {
	return func(o *gDriveOptions) {
		o.rootFolderID = id
	}
}

// WithAccountEmail explicitly sets the account email instead of calling the API.
func WithAccountEmail(email string) GDriveOption {
	return func(o *gDriveOptions) {
		o.accountEmail = email
	}
}

// WithGDriveExcludePatterns sets the patterns used to exclude files and folders.
func WithGDriveExcludePatterns(patterns []string) GDriveOption {
	return func(o *gDriveOptions) {
		o.excludePatterns = patterns
	}
}

// GDriveSource implements Source for Google Drive. By default it backs up the
// entire "My Drive" root. Set DriveID in GDriveSourceConfig to back up a
// shared drive instead, and/or set RootFolderID to restrict to a specific
// folder within the selected drive.
type GDriveSource struct {
	service      *drive.Service
	driveID      string // shared drive ID; empty means "My Drive"
	rootFolderID string // if empty, defaults to "root" (entire drive)
	account      string // Google account email; populated automatically
	exclude      *ExcludeMatcher
}

// NewGDriveSource creates a new GDriveSource from the given options.
func NewGDriveSource(ctx context.Context, opts ...GDriveOption) (*GDriveSource, error) {
	var cfg gDriveOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	var srv *drive.Service
	var err error

	if cfg.httpClient != nil {
		srv, err = drive.NewService(ctx, option.WithHTTPClient(cfg.httpClient))
		if err != nil {
			return nil, fmt.Errorf("create drive client (custom http client): %w", err)
		}
	} else if cfg.credsPath != "" {
		b, err := os.ReadFile(cfg.credsPath)
		if err != nil {
			return nil, fmt.Errorf("read credentials file: %w", err)
		}
		config, err := google.ConfigFromJSON(b, drive.DriveReadonlyScope)
		if err == nil {
			client, err := oauthClient(config, cfg.tokenPath)
			if err != nil {
				return nil, err
			}
			srv, err = drive.NewService(ctx, option.WithHTTPClient(client))
			if err != nil {
				return nil, fmt.Errorf("create drive client (user auth): %w", err)
			}
		} else {
			srv, err = drive.NewService(ctx, option.WithCredentialsFile(cfg.credsPath))
			if err != nil {
				return nil, fmt.Errorf("create drive client: %w", err)
			}
		}
	} else {
		config := &oauth2.Config{
			ClientID:     defaultGoogleClientID,
			ClientSecret: defaultGoogleClientSecret,
			Scopes:       []string{drive.DriveReadonlyScope},
			Endpoint:     google.Endpoint,
		}
		client, err := oauthClient(config, cfg.tokenPath)
		if err != nil {
			return nil, err
		}
		srv, err = drive.NewService(ctx, option.WithHTTPClient(client))
		if err != nil {
			return nil, fmt.Errorf("create drive client: %w", err)
		}
	}

	return &GDriveSource{
		service:      srv,
		driveID:      cfg.driveID,
		rootFolderID: cfg.rootFolderID,
		account:      cfg.accountEmail,
		exclude:      NewExcludeMatcher(cfg.excludePatterns),
	}, nil
}

func (s *GDriveSource) Info() core.SourceInfo {
	account := s.account
	if account == "" {
		if about, err := s.service.About.Get().Fields("user(emailAddress)").Do(); err == nil && about.User != nil {
			account = about.User.EmailAddress
		}
	}
	return core.SourceInfo{
		Type:    "gdrive",
		Account: account,
		Path:    drivePath(s.driveID, s.rootFolderID),
	}
}

func (s *GDriveSource) isSharedDrive() bool {
	return s.driveID != ""
}

// drivePath builds a URI-like path that uniquely identifies the drive and
// optional root folder: "my-drive://" or "<driveID>://<rootFolderID>".
func drivePath(driveID, rootFolderID string) string {
	drive := "my-drive"
	if driveID != "" {
		drive = driveID
	}
	return drive + "://" + rootFolderID
}

// ---------------------------------------------------------------------------
// OAuth helpers
// ---------------------------------------------------------------------------

func oauthClient(config *oauth2.Config, tokFile string) (*http.Client, error) {
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok, err = tokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokFile, tok); err != nil {
			return nil, err
		}
	}
	return config.Client(context.Background(), tok), nil
}

func tokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	return exchangeWithLocalServer(config, oauth2.AccessTypeOffline)
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func saveToken(path string, token *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(token)
}

// driveCallWithRetry wraps a Google API call with retry logic for transient errors.
func driveCallWithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var result T
	err := retry.Do(ctx, retry.DefaultPolicy(), func() error {
		var err error
		result, err = fn()
		if err != nil {
			if isRetryableGoogleErr(err) {
				return &retry.RetryableError{Err: err}
			}
			return err
		}
		return nil
	})
	return result, err
}

func isRetryableGoogleErr(err error) bool {
	if apiErr, ok := err.(*googleapi.Error); ok {
		return apiErr.Code == 429 || apiErr.Code >= 500
	}
	return false
}

// ---------------------------------------------------------------------------
// Walk
// ---------------------------------------------------------------------------

// Walk lists all files from Drive. Folders are accumulated in memory (necessary
// for topological sort) but files are streamed page-by-page to avoid holding
// the full file list in memory.
func (s *GDriveSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	var folders []*drive.File
	pageToken := ""

	for {
		call := s.service.Files.List().
			Q("trashed = false AND mimeType = 'application/vnd.google-apps.folder'").
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum)").
			PageSize(1000).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.driveID).
				Corpora("drive").
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
		}
		if pageToken != "" {
			call.PageToken(pageToken)
		}

		r, err := driveCallWithRetry(ctx, func() (*drive.FileList, error) { return call.Do() })
		if err != nil {
			return fmt.Errorf("list folders: %w", err)
		}

		folders = append(folders, r.Files...)

		pageToken = r.NextPageToken
		if pageToken == "" {
			break
		}
	}

	// pathMap tracks fileID → full path for all emitted entries.
	// Folders are topo-sorted (parents before children) so the parent
	// path is always known when we compute the child path.
	pathMap := make(map[string]string, len(folders))
	// excludedPaths tracks Drive file IDs of excluded directories so
	// their children are also skipped.
	excludedPaths := make(map[string]bool)

	// Emit folders first (topo-sorted so parents before children).
	folders = topoSortFolders(folders)
	for _, f := range folders {
		if err := s.visitEntryWithPath(f, pathMap, excludedPaths, callback); err != nil {
			return err
		}
	}

	// Second pass: emit files page-by-page to avoid holding them all in memory.
	pageToken = ""
	for {
		call := s.service.Files.List().
			Q("trashed = false AND mimeType != 'application/vnd.google-apps.folder'").
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum)").
			PageSize(1000).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.driveID).
				Corpora("drive").
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
		}
		if pageToken != "" {
			call.PageToken(pageToken)
		}

		r, err := driveCallWithRetry(ctx, func() (*drive.FileList, error) { return call.Do() })
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}

		for _, f := range r.Files {
			if err := s.visitEntryWithPath(f, pathMap, excludedPaths, callback); err != nil {
				return err
			}
		}

		pageToken = r.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return nil
}

// topoSortFolders orders folders so that every parent appears before its
// children. Folders whose parents are outside the set (e.g. the Drive root)
// naturally come first.
func topoSortFolders(folders []*drive.File) []*drive.File {
	byID := make(map[string]*drive.File, len(folders))
	for _, f := range folders {
		byID[f.Id] = f
	}

	visited := make(map[string]bool, len(folders))
	sorted := make([]*drive.File, 0, len(folders))

	var visit func(f *drive.File)
	visit = func(f *drive.File) {
		if visited[f.Id] {
			return
		}
		visited[f.Id] = true
		for _, pid := range f.Parents {
			if parent, ok := byID[pid]; ok {
				visit(parent)
			}
		}
		sorted = append(sorted, f)
	}

	for _, f := range folders {
		visit(f)
	}
	return sorted
}

func (s *GDriveSource) visitEntryWithPath(f *drive.File, pathMap map[string]string, excludedPaths map[string]bool, callback func(core.FileMeta) error) error {
	meta := s.toFileMeta(f)

	// Compute full path from parent path map.
	p := meta.Name
	if len(f.Parents) > 0 {
		if parentPath, ok := pathMap[f.Parents[0]]; ok {
			p = parentPath + "/" + meta.Name
		}
	}
	meta.Paths = []string{p}
	pathMap[f.Id] = p

	// Skip entries under an already-excluded directory.
	if len(f.Parents) > 0 && excludedPaths[f.Parents[0]] {
		if meta.Type == core.FileTypeFolder {
			excludedPaths[f.Id] = true
		}
		return nil
	}

	// Apply exclude patterns.
	if !s.exclude.Empty() {
		isDir := meta.Type == core.FileTypeFolder
		if s.exclude.Excludes(p, isDir) {
			if isDir {
				excludedPaths[f.Id] = true
			}
			return nil
		}
	}

	return callback(meta)
}

func (s *GDriveSource) toFileMeta(f *drive.File) core.FileMeta {
	var mtime int64
	if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
		mtime = t.Unix()
	}

	var owner string
	if len(f.Owners) > 0 {
		owner = f.Owners[0].EmailAddress
	}

	fileType := core.FileTypeFile
	if f.MimeType == "application/vnd.google-apps.folder" {
		fileType = core.FileTypeFolder
	}

	return core.FileMeta{
		Version:     1,
		FileID:      f.Id,
		Name:        f.Name,
		Type:        fileType,
		Parents:     f.Parents,
		ContentHash: f.Sha256Checksum,
		Size:        f.Size,
		Mtime:       mtime,
		Owner:       owner,
		Extra:       map[string]interface{}{"mimeType": f.MimeType},
	}
}

// Size returns the total size of the drive. For My Drive it uses the fast
// about.get endpoint. For shared drives it lists all files and sums sizes.
func (s *GDriveSource) Size(ctx context.Context) (*SourceSize, error) {
	if !s.isSharedDrive() {
		about, err := driveCallWithRetry(ctx, func() (*drive.About, error) {
			return s.service.About.Get().Fields("storageQuota").Context(ctx).Do()
		})
		if err != nil {
			return nil, fmt.Errorf("drive about: %w", err)
		}
		return &SourceSize{Bytes: about.StorageQuota.UsageInDrive}, nil
	}

	var totalBytes, totalFiles int64
	pageToken := ""
	for {
		call := s.service.Files.List().
			Corpora("drive").
			DriveId(s.driveID).
			IncludeItemsFromAllDrives(true).
			SupportsAllDrives(true).
			Q("trashed=false and mimeType!='application/vnd.google-apps.folder'").
			Fields("nextPageToken,files(size)").
			PageSize(1000).
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		result, err := driveCallWithRetry(ctx, func() (*drive.FileList, error) { return call.Do() })
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		for _, f := range result.Files {
			totalBytes += f.Size
			totalFiles++
		}
		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}

	return &SourceSize{Bytes: totalBytes, Files: totalFiles}, nil
}

func (s *GDriveSource) GetFileStream(fileID string) (io.ReadCloser, error) {
	var resp *http.Response
	err := retry.Do(context.Background(), retry.DefaultPolicy(), func() error {
		call := s.service.Files.Get(fileID).SupportsAllDrives(true)
		var err error
		resp, err = call.Download()
		if err != nil {
			if isRetryableGoogleErr(err) {
				return &retry.RetryableError{Err: err}
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
