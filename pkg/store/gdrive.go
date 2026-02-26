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
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
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

// NewGDriveSource creates a new GDriveSource. credsPath is the path to the
// Google credentials JSON file, and tokenPath is where the OAuth token will
// be cached (e.g. "~/.config/cloudstic/google_token.json").
func NewGDriveSource(credsPath, tokenPath string) (*GDriveSource, error) {
	ctx := context.Background()

	b, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}

	var srv *drive.Service

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
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the authorization code:\n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("read authorization code: %w", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		return nil, fmt.Errorf("exchange token: %w", err)
	}
	return tok, nil
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

// ---------------------------------------------------------------------------
// Walk
// ---------------------------------------------------------------------------

func (s *GDriveSource) Walk(ctx context.Context, callback func(core.FileMeta) error) error {
	var folders, files []*drive.File
	pageToken := ""

	for {
		call := s.Service.Files.List().
			Q("trashed = false").
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum)").
			PageSize(1000)
		if s.isSharedDrive() {
			call.DriveId(s.DriveID).
				Corpora("drive").
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
		}
		if pageToken != "" {
			call.PageToken(pageToken)
		}

		r, err := call.Do()
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}

		for _, f := range r.Files {
			if f.MimeType == "application/vnd.google-apps.folder" {
				folders = append(folders, f)
			} else {
				files = append(files, f)
			}
		}

		pageToken = r.NextPageToken
		if pageToken == "" {
			break
		}
	}

	// Topologically sort folders so parents are always emitted before children.
	folders = topoSortFolders(folders)

	for _, f := range folders {
		if err := s.visitEntry(f, callback); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := s.visitEntry(f, callback); err != nil {
			return err
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
		about, err := s.Service.About.Get().Fields("storageQuota").Do()
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
			PageSize(1000)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		result, err := call.Do()
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
	call := s.Service.Files.Get(fileID).SupportsAllDrives(true)
	resp, err := call.Download()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
