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
	"github.com/cloudstic/cli/pkg/retry"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// GDriveSource implements Source for Google Drive. By default it backs up the
// entire "My Drive" root. Set DriveID to back up a shared drive instead, and/or
// set RootFolderID to restrict to a specific folder within the selected drive.
type GDriveSource struct {
	Service      *drive.Service
	DriveID      string // shared drive ID; empty means "My Drive"
	RootFolderID string // if empty, defaults to "root" (entire drive)
	Account      string // Google account email; populated automatically if empty
}

// NewGDriveSource creates a new GDriveSource. If credsPath is non-empty it is
// used as a Google credentials JSON file (user OAuth or service-account). When
// credsPath is empty the built-in OAuth client credentials are used instead.
// tokenPath is where the OAuth token will be cached.
func NewGDriveSource(credsPath, tokenPath string) (*GDriveSource, error) {
	ctx := context.Background()

	var srv *drive.Service

	if credsPath != "" {
		b, err := os.ReadFile(credsPath)
		if err != nil {
			return nil, fmt.Errorf("read credentials file: %w", err)
		}
		config, err := google.ConfigFromJSON(b, drive.DriveReadonlyScope)
		if err == nil {
			client, err := oauthClient(config, tokenPath)
			if err != nil {
				return nil, err
			}
			srv, err = drive.NewService(ctx, option.WithHTTPClient(client))
			if err != nil {
				return nil, fmt.Errorf("create drive client (user auth): %w", err)
			}
		} else {
			srv, err = drive.NewService(ctx, option.WithCredentialsFile(credsPath))
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
		client, err := oauthClient(config, tokenPath)
		if err != nil {
			return nil, err
		}
		srv, err = drive.NewService(ctx, option.WithHTTPClient(client))
		if err != nil {
			return nil, fmt.Errorf("create drive client: %w", err)
		}
	}

	return &GDriveSource{Service: srv}, nil
}

func (s *GDriveSource) Info() core.SourceInfo {
	account := s.Account
	if account == "" {
		if about, err := s.Service.About.Get().Fields("user(emailAddress)").Do(); err == nil && about.User != nil {
			account = about.User.EmailAddress
		}
	}
	return core.SourceInfo{
		Type:    "gdrive",
		Account: account,
		Path:    drivePath(s.DriveID, s.RootFolderID),
	}
}

func (s *GDriveSource) isSharedDrive() bool {
	return s.DriveID != ""
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
		call := s.Service.Files.List().
			Q("trashed = false").
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum)").
			PageSize(1000).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.DriveID).
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
			if f.MimeType == "application/vnd.google-apps.folder" {
				folders = append(folders, f)
			}
		}

		pageToken = r.NextPageToken
		if pageToken == "" {
			break
		}
	}

	// Emit folders first (topo-sorted so parents before children).
	folders = topoSortFolders(folders)
	for _, f := range folders {
		if err := s.visitEntry(f, callback); err != nil {
			return err
		}
	}

	// Second pass: emit files page-by-page to avoid holding them all in memory.
	pageToken = ""
	for {
		call := s.Service.Files.List().
			Q("trashed = false AND mimeType != 'application/vnd.google-apps.folder'").
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum)").
			PageSize(1000).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.DriveID).
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
			if err := s.visitEntry(f, callback); err != nil {
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

func (s *GDriveSource) visitEntry(f *drive.File, callback func(core.FileMeta) error) error {
	meta := s.toFileMeta(f)

	// Filter out parents that are outside the backed-up set (e.g. the Drive
	// root folder). Only keep parents whose ID is a real folder we've seen.
	// Since toFileMeta already sets Parents to raw Drive IDs, we just emit
	// as-is. The consumer resolves FileIDs when building the hierarchy.
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
			return s.Service.About.Get().Fields("storageQuota").Context(ctx).Do()
		})
		if err != nil {
			return nil, fmt.Errorf("drive about: %w", err)
		}
		return &SourceSize{Bytes: about.StorageQuota.UsageInDrive}, nil
	}

	var totalBytes, totalFiles int64
	pageToken := ""
	for {
		call := s.Service.Files.List().
			Corpora("drive").
			DriveId(s.DriveID).
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
		call := s.Service.Files.Get(fileID).SupportsAllDrives(true)
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
