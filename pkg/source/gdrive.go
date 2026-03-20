package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/internal/retry"
	"github.com/cloudstic/cli/internal/secretref"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// gDriveOptions holds configuration for a Google Drive source.
type gDriveOptions struct {
	service         *drive.Service // pre-built service; highest priority
	httpClient      *http.Client
	resolver        *secretref.Resolver
	credsPath       string
	credsRef        string
	credsJSON       []byte // inline credential JSON (OAuth client or service-account)
	tokenPath       string
	tokenRef        string
	driveID         string
	driveName       string
	rootFolderID    string
	rootPath        string
	accountEmail    string
	excludePatterns []string
	skipNativeFiles bool
}

// GDriveOption configures a Google Drive source.
type GDriveOption func(*gDriveOptions)

// WithDriveService injects a fully-constructed *drive.Service.
// When set, all credential/token options are ignored — the caller is
// responsible for scopes, token refresh, etc.
func WithDriveService(srv *drive.Service) GDriveOption {
	return func(o *gDriveOptions) {
		o.service = srv
	}
}

// WithHTTPClient sets a custom HTTP client for OAuth.
func WithHTTPClient(client *http.Client) GDriveOption {
	return func(o *gDriveOptions) {
		o.httpClient = client
	}
}

// WithResolver sets the secret resolver for ref-based auth.
func WithResolver(r *secretref.Resolver) GDriveOption {
	return func(o *gDriveOptions) {
		o.resolver = r
	}
}

// WithCredsPath sets the path to the credentials JSON file.
// If empty, uses the built-in OAuth client.
func WithCredsPath(path string) GDriveOption {
	return func(o *gDriveOptions) {
		o.credsPath = path
	}
}

// WithCredsRef sets the secret reference to the credentials JSON.
func WithCredsRef(ref string) GDriveOption {
	return func(o *gDriveOptions) {
		o.credsRef = ref
	}
}

// WithCredsJSON provides credentials as raw JSON bytes (inline).
// Supports both OAuth-client and service-account credential formats.
// This mirrors rclone's service_account_credentials option.
func WithCredsJSON(data []byte) GDriveOption {
	return func(o *gDriveOptions) {
		o.credsJSON = data
	}
}

// WithTokenPath sets the path where the OAuth token is cached.
func WithTokenPath(path string) GDriveOption {
	return func(o *gDriveOptions) {
		o.tokenPath = path
	}
}

// WithTokenRef sets the secret reference where the OAuth token is cached.
func WithTokenRef(ref string) GDriveOption {
	return func(o *gDriveOptions) {
		o.tokenRef = ref
	}
}

// WithDriveName sets the shared drive name to use. It will be resolved to a Drive ID.
func WithDriveName(name string) GDriveOption {
	return func(o *gDriveOptions) {
		if name != "" {
			o.driveName = name
		}
	}
}

// WithDriveID sets the shared drive ID. If empty, defaults to "My Drive".
func WithDriveID(id string) GDriveOption {
	return func(o *gDriveOptions) {
		o.driveID = id
	}
}

// WithRootFolderID sets the root folder ID directly (for client API).
func WithRootFolderID(id string) GDriveOption {
	return func(o *gDriveOptions) {
		o.rootFolderID = id
	}
}

// WithRootPath sets the path to the root folder.
// This is resolved to a folder ID during initialization.
func WithRootPath(path string) GDriveOption {
	return func(o *gDriveOptions) {
		o.rootPath = path
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

// WithSkipNativeFiles excludes Google-native files (Docs, Sheets, Slides, etc.)
// from the backup. They will not appear in the snapshot at all.
func WithSkipNativeFiles() GDriveOption {
	return func(o *gDriveOptions) {
		o.skipNativeFiles = true
	}
}

// GDriveSource implements Source for Google Drive. By default it backs up the
// entire "My Drive" root. Set DriveID in GDriveSourceConfig to back up a
// shared drive instead, and/or set RootFolderID to restrict to a specific
// folder within the selected drive.
type GDriveSource struct {
	service         *drive.Service
	driveID         string // shared drive ID; empty means "My Drive"
	rootFolderID    string // if empty, defaults to "root" (entire drive)
	rootPath        string // The string path the user specified, or "/"
	account         string // Google account email; populated automatically
	accountID       string // stable Google account identity; populated automatically
	driveName       string // shared drive name; populated during construction
	exclude         *ExcludeMatcher
	skipNativeFiles bool
	mimeTypes       map[string]string // fileID → mimeType; populated during Walk/WalkChanges
}

// NewGDriveSource creates a new GDriveSource from the given options.
func NewGDriveSource(ctx context.Context, opts ...GDriveOption) (*GDriveSource, error) {
	var cfg gDriveOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	srv, err := buildDriveService(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if cfg.driveName != "" && cfg.driveID == "" {
		if err := resolveDriveName(ctx, srv, &cfg); err != nil {
			return nil, err
		}
	}

	src := &GDriveSource{
		service:         srv,
		driveID:         cfg.driveID,
		rootFolderID:    cfg.rootFolderID,
		rootPath:        cfg.rootPath,
		account:         cfg.accountEmail,
		driveName:       cfg.driveName,
		exclude:         NewExcludeMatcher(cfg.excludePatterns),
		skipNativeFiles: cfg.skipNativeFiles,
	}

	// Resolve the shared drive name for VolumeLabel if driveID was set directly.
	if cfg.driveID != "" && src.driveName == "" {
		if d, err := srv.Drives.Get(cfg.driveID).Fields("name").Do(); err == nil {
			src.driveName = d.Name
		}
	}

	if src.rootPath != "" && src.rootPath != "/" {
		id, err := src.resolvePathToFolderID(ctx, src.rootPath)
		if err != nil {
			return nil, fmt.Errorf("invalid source path %q: %w", src.rootPath, err)
		}
		src.rootFolderID = id
	} else if src.rootPath == "" {
		src.rootPath = "/"
	}

	return src, nil
}

// ---------------------------------------------------------------------------
// Service construction helpers
// ---------------------------------------------------------------------------

// buildDriveService creates a *drive.Service from the supplied options.
// Auth strategies are tried in priority order with early returns:
//  1. Pre-built service (WithDriveService)
//  2. Custom HTTP client (WithHTTPClient)
//  3. Explicit credentials (WithCredsJSON / WithCredsRef / WithCredsPath)
//  4. Built-in default OAuth client
func buildDriveService(ctx context.Context, cfg gDriveOptions) (*drive.Service, error) {
	// 1. Pre-built service — caller manages auth entirely.
	if cfg.service != nil {
		return cfg.service, nil
	}

	// 2. Custom HTTP client.
	if cfg.httpClient != nil {
		srv, err := drive.NewService(ctx, option.WithHTTPClient(cfg.httpClient))
		if err != nil {
			return nil, fmt.Errorf("create drive client (custom http client): %w", err)
		}
		return srv, nil
	}

	// 3. Explicit credentials: inline JSON, secret ref, or file path.
	if b, ok, err := loadCredsBytes(ctx, cfg); err != nil {
		return nil, err
	} else if ok {
		return serviceFromCredsBytes(ctx, cfg, b)
	}

	// 4. Default built-in OAuth client.
	config := &oauth2.Config{
		ClientID:     defaultGoogleClientID,
		ClientSecret: defaultGoogleClientSecret,
		Scopes:       []string{drive.DriveReadonlyScope},
		Endpoint:     google.Endpoint,
	}
	client, err := oauthClient(ctx, config, cfg.resolver, cfg.tokenRef, cfg.tokenPath)
	if err != nil {
		return nil, err
	}
	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("create drive client: %w", err)
	}
	return srv, nil
}

// loadCredsBytes resolves credential JSON from the first available source:
// inline JSON > secret ref > file path. Returns (nil, false, nil) when no
// credential source is configured.
func loadCredsBytes(ctx context.Context, cfg gDriveOptions) ([]byte, bool, error) {
	if len(cfg.credsJSON) > 0 {
		return cfg.credsJSON, true, nil
	}
	if cfg.credsRef != "" && cfg.resolver != nil {
		b, err := cfg.resolver.LoadBlob(ctx, cfg.credsRef)
		if err != nil {
			return nil, false, fmt.Errorf("load credentials from ref %q: %w", cfg.credsRef, err)
		}
		return b, true, nil
	}
	if cfg.credsPath != "" {
		b, err := os.ReadFile(cfg.credsPath)
		if err != nil {
			return nil, false, fmt.Errorf("read credentials file: %w", err)
		}
		return b, true, nil
	}
	return nil, false, nil
}

// serviceFromCredsBytes builds a *drive.Service from raw credential JSON.
// It first tries to interpret the JSON as an OAuth user-credential config;
// if that fails it falls back to service-account authentication.
func serviceFromCredsBytes(ctx context.Context, cfg gDriveOptions, b []byte) (*drive.Service, error) {
	// Try OAuth user config first.
	oauthCfg, err := google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err == nil {
		client, err := oauthClient(ctx, oauthCfg, cfg.resolver, cfg.tokenRef, cfg.tokenPath)
		if err != nil {
			return nil, err
		}
		srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
		if err != nil {
			return nil, fmt.Errorf("create drive client (user auth): %w", err)
		}
		return srv, nil
	}

	// Fall back to service-account credentials.
	if cfg.credsPath != "" {
		srv, err := drive.NewService(ctx, option.WithAuthCredentialsFile(option.ServiceAccount, cfg.credsPath))
		if err != nil {
			return nil, fmt.Errorf("create drive client (service account): %w", err)
		}
		return srv, nil
	}
	srv, err := drive.NewService(ctx, option.WithAuthCredentialsJSON(option.ServiceAccount, b))
	if err != nil {
		return nil, fmt.Errorf("create drive client (service account): %w", err)
	}
	return srv, nil
}

// resolveDriveName resolves a shared drive name (or ID) to an actual driveID.
func resolveDriveName(ctx context.Context, srv *drive.Service, cfg *gDriveOptions) error {
	// Try to see if the provided name is actually an ID.
	if d, err := srv.Drives.Get(cfg.driveName).Fields("id, name").Do(); err == nil {
		cfg.driveID = d.Id
		cfg.driveName = d.Name
		return nil
	}

	// Search by name.
	query := fmt.Sprintf("name = '%s'", strings.ReplaceAll(cfg.driveName, "'", "\\'"))
	call := srv.Drives.List().Q(query).Fields("drives(id, name)").Context(ctx)
	r, err := driveCallWithRetry(ctx, func() (*drive.DriveList, error) { return call.Do() })
	if err != nil {
		return fmt.Errorf("resolve drive %q: %w", cfg.driveName, err)
	}
	if len(r.Drives) == 0 {
		return fmt.Errorf("shared drive %q not found", cfg.driveName)
	}
	if len(r.Drives) > 1 {
		return fmt.Errorf("ambiguous shared drive name: multiple drives named %q found", cfg.driveName)
	}
	cfg.driveID = r.Drives[0].Id
	cfg.driveName = r.Drives[0].Name
	return nil
}

func (s *GDriveSource) Info() core.SourceInfo {
	account := s.account
	accountID := s.accountID
	if s.service != nil && (account == "" || accountID == "") {
		if about, err := s.service.About.Get().Fields("user(emailAddress,permissionId)").Do(); err == nil && about.User != nil {
			if account == "" {
				account = about.User.EmailAddress
				s.account = account
			}
			if accountID == "" {
				accountID = about.User.PermissionId
				s.accountID = accountID
			}
		}
	}

	info := core.SourceInfo{
		Type:      "gdrive",
		Account:   account,
		Path:      s.rootPath,
		PathID:    s.selectedRootID(),
		DriveName: "My Drive",
		FsType:    "google-drive",
	}

	if s.isSharedDrive() {
		info.Identity = s.driveID
		info.DriveName = s.driveName
	} else if accountID != "" {
		info.Identity = accountID
	} else {
		info.Identity = account
	}

	return info
}

func (s *GDriveSource) isSharedDrive() bool {
	return s.driveID != ""
}

func (s *GDriveSource) selectedRootID() string {
	if s.rootFolderID != "" {
		return s.rootFolderID
	}
	if s.isSharedDrive() {
		return s.driveID
	}
	return "root"
}

// resolvePathToFolderID resolves a string path (e.g. "/foo/bar") to a Drive folder ID.
func (s *GDriveSource) resolvePathToFolderID(ctx context.Context, path string) (string, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	currentParent := "root"
	if s.isSharedDrive() {
		currentParent = s.driveID
	}

	for _, part := range parts {
		if part == "" {
			continue
		}
		query := fmt.Sprintf("trashed = false and mimeType = 'application/vnd.google-apps.folder' and name = '%s' and '%s' in parents",
			strings.ReplaceAll(part, "'", "\\'"), currentParent)
		call := s.service.Files.List().
			Q(query).
			Fields("files(id)").
			PageSize(2).
			Context(ctx)
		if s.isSharedDrive() {
			call.DriveId(s.driveID).
				Corpora("drive").
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
		}

		r, err := driveCallWithRetry(ctx, func() (*drive.FileList, error) { return call.Do() })
		if err != nil {
			return "", fmt.Errorf("resolve path segment %q: %w", part, err)
		}
		if len(r.Files) == 0 {
			return "", fmt.Errorf("folder not found in Drive: %q", part)
		}
		if len(r.Files) > 1 {
			return "", fmt.Errorf("ambiguous path: multiple folders named %q found", part)
		}
		currentParent = r.Files[0].Id
	}
	return currentParent, nil
}

// ---------------------------------------------------------------------------
// OAuth helpers
// ---------------------------------------------------------------------------

func oauthClient(ctx context.Context, config *oauth2.Config, r *secretref.Resolver, tokRef, tokFile string) (*http.Client, error) {
	var tok *oauth2.Token
	var err error
	if tokRef != "" && r == nil {
		return nil, fmt.Errorf("token ref %q requires a resolver", tokRef)
	}

	if tokRef != "" {
		tok, err = tokenFromRef(ctx, r, tokRef)
	} else if tokFile != "" {
		tok, err = tokenFromFile(tokFile)
	} else {
		err = fmt.Errorf("no token storage configured")
	}

	if err != nil {
		tok, err = tokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		if tokRef != "" {
			if err := saveTokenRef(ctx, r, tokRef, tok); err != nil {
				return nil, err
			}
		} else if tokFile != "" {
			if err := saveToken(tokFile, tok); err != nil {
				return nil, err
			}
		}
	}

	// Use a persistent token source so that refreshes are saved.
	ts := oauth2.ReuseTokenSource(tok, config.TokenSource(ctx, tok))
	pts := &persistentTokenSource{
		ts:      ts,
		lastTok: tok,
		save: func(t *oauth2.Token) error {
			if tokRef != "" {
				return saveTokenRef(ctx, r, tokRef, t)
			}
			if tokFile != "" {
				return saveToken(tokFile, t)
			}
			return nil
		},
	}

	return oauth2.NewClient(ctx, pts), nil
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

func tokenFromRef(ctx context.Context, r *secretref.Resolver, ref string) (*oauth2.Token, error) {
	data, err := r.LoadBlob(ctx, ref)
	if err != nil {
		return nil, err
	}
	tok := &oauth2.Token{}
	if err := json.Unmarshal(data, tok); err != nil {
		return nil, fmt.Errorf("decode token from ref: %w", err)
	}
	return tok, nil
}

func saveToken(path string, token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return paths.SaveAtomic(path, data)
}

func saveTokenRef(ctx context.Context, r *secretref.Resolver, ref string, token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return r.SaveBlob(ctx, ref, data)
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
	s.mimeTypes = make(map[string]string)

	var folders []*drive.File
	pageToken := ""

	for {
		call := s.service.Files.List().
			Q("trashed = false AND mimeType = 'application/vnd.google-apps.folder'").
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum, headRevisionId)").
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

	// If rootFolderID is set, we need to filter to only its descendants.
	// We also don't want to yield the root folder itself.
	var filteredFolders []*drive.File
	if s.rootFolderID != "" {
		descendants := make(map[string]bool)
		descendants[s.rootFolderID] = true

		// folders are topo-sorted, so parents always come before children
		topoFolders := topoSortFolders(folders)
		for _, f := range topoFolders {
			if f.Id == s.rootFolderID {
				continue // Skip yielding the root folder itself
			}
			isDescendant := false
			for _, pid := range f.Parents {
				if descendants[pid] {
					isDescendant = true
					break
				}
			}
			if isDescendant {
				descendants[f.Id] = true
				filteredFolders = append(filteredFolders, f)
			}
		}
		folders = filteredFolders
	} else {
		folders = topoSortFolders(folders)
	}

	// pathMap tracks fileID → full path for all emitted entries.
	// Folders are topo-sorted (parents before children) so the parent
	// path is always known when we compute the child path.
	pathMap := make(map[string]string, len(folders))
	// excludedPaths tracks Drive file IDs of excluded directories so
	// their children are also skipped.
	excludedPaths := make(map[string]bool)

	if s.rootFolderID != "" {
		pathMap[s.rootFolderID] = "" // Root folder has empty path relative to itself
	}

	// Emit folders first (topo-sorted so parents before children).
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
			Fields("nextPageToken, files(id, name, parents, mimeType, size, modifiedTime, owners, trashed, sha256Checksum, headRevisionId)").
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
			// If rootFolderID is set, filter files
			if s.rootFolderID != "" {
				isDescendant := false
				for _, pid := range f.Parents {
					// A file's parent must be in pathMap if it's a descendant of rootFolderID
					// because all descendant folders have been processed and added to pathMap
					if _, ok := pathMap[pid]; ok {
						isDescendant = true
						break
					}
				}
				if !isDescendant {
					continue
				}
			}

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
	// Record MIME type for all non-folder entries so GetFileStream can
	// decide between download and export.
	if f.MimeType != "application/vnd.google-apps.folder" {
		s.mimeTypes[f.Id] = f.MimeType
	}

	// Skip native files when the user opted out of exporting them.
	if s.skipNativeFiles && isGoogleNativeMimeType(f.MimeType) {
		return nil
	}

	meta := s.toFileMeta(f)

	// Compute full path from parent path map.
	p := meta.Name
	if len(f.Parents) > 0 {
		if parentPath, ok := pathMap[f.Parents[0]]; ok {
			if parentPath == "" {
				p = meta.Name
			} else {
				p = parentPath + "/" + meta.Name
			}
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

	name := f.Name
	extra := map[string]interface{}{"mimeType": f.MimeType}

	if isGoogleNativeMimeType(f.MimeType) {
		name += nativeExportExtension(f.MimeType)
		extra["exportMimeType"] = nativeExportMimeType(f.MimeType)
	}
	if f.HeadRevisionId != "" {
		extra["headRevisionId"] = f.HeadRevisionId
	}

	return core.FileMeta{
		Version:     1,
		FileID:      f.Id,
		Name:        name,
		Type:        fileType,
		Parents:     f.Parents,
		ContentHash: f.Sha256Checksum,
		Size:        f.Size,
		Mtime:       mtime,
		Owner:       owner,
		Extra:       extra,
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
	if mimeType, ok := s.mimeTypes[fileID]; ok && isGoogleNativeMimeType(mimeType) {
		return s.exportFile(fileID, nativeExportMimeType(mimeType))
	}

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

func (s *GDriveSource) exportFile(fileID, exportMimeType string) (io.ReadCloser, error) {
	var resp *http.Response
	err := retry.Do(context.Background(), retry.DefaultPolicy(), func() error {
		var err error
		resp, err = s.service.Files.Export(fileID, exportMimeType).Download()
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
