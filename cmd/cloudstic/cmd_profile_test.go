package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunProfileList_Success(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  home-s3:
    uri: s3:my-bucket/cloudstic
    s3_region: us-east-1
    s3_profile: prod
auth:
  google-work:
    provider: google
    google_token_file: /tmp/google-work.json
profiles:
  photos:
    source: local:/Volumes/Photos
    store: home-s3
  work-drive:
    source: gdrive-changes://Company Data/Engineering
    store: home-s3
    auth_ref: google-work
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "list", "-profiles-file", profilesPath}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}

	got := out.String()
	for _, want := range []string{"Stores", "Auth", "Profiles", "home-s3", "google-work", "photos", "work-drive"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestRunProfileShow_Success(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  home-s3:
    uri: s3:my-bucket/cloudstic
    s3_region: us-east-1
    s3_profile: prod
auth:
  google-work:
    provider: google
    google_token_file: /tmp/google-work.json
profiles:
  work-drive:
    source: gdrive-changes://Company Data/Engineering
    store: home-s3
    auth_ref: google-work
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "show", "-profiles-file", profilesPath, "work-drive"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Profile work-drive") || !strings.Contains(got, "Resolved References") || !strings.Contains(got, "s3:my-bucket/cloudstic") || !strings.Contains(got, "google") {
		t.Fatalf("unexpected show output:\n%s", got)
	}
	if !strings.Contains(got, "Options") || !strings.Contains(got, "aws-shared-profile") || !strings.Contains(got, "prod") {
		t.Fatalf("expected store auth details in show output:\n%s", got)
	}
}

func TestRunProfileShow_UnknownProfile(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	if err := os.WriteFile(profilesPath, []byte("version: 1\nprofiles: {}\n"), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "show", "-profiles-file", profilesPath, "missing"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Unknown profile") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileList_MissingFile(t *testing.T) {
	os.Args = []string{"cloudstic", "profile", "list", "-profiles-file", filepath.Join(t.TempDir(), "missing.yaml")}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("expected zero exit code, got=%d err=%s", code, errOut.String())
	}
	if out.String() != "" {
		t.Fatalf("expected empty output for missing file, got: %q", out.String())
	}
	if errOut.String() != "" {
		t.Fatalf("expected empty stderr for missing file, got: %q", errOut.String())
	}
}

func TestRunProfile_UnknownSubcommand(t *testing.T) {
	os.Args = []string{"cloudstic", "profile", "unknown"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatalf("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Unknown profile subcommand") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "photos",
		"-source", "local:/Volumes/Photos",
		"-store-ref", "home-s3",
		"-store", "s3:my-bucket/cloudstic",
		"-tag", "daily",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "photos:") || !strings.Contains(got, "source: local:/Volumes/Photos") {
		t.Fatalf("profiles file missing profile content:\n%s", got)
	}
	if !strings.Contains(got, "home-s3:") || !strings.Contains(got, "uri: s3:my-bucket/cloudstic") {
		t.Fatalf("profiles file missing store content:\n%s", got)
	}
}

func TestRunProfileNew_PrefillsExistingProfile(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")

	// Create initial profile.
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "photos",
		"-source", "local:/Volumes/Photos",
		"-store-ref", "main", "-store", "s3:my-bucket/backup",
		"-tag", "daily",
		"-exclude", "*.tmp",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("initial create: code=%d err=%s", code, errOut.String())
	}

	// Re-run with same name, only override source.
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "photos",
		"-source", "local:/Volumes/NewPhotos",
	}
	out.Reset()
	errOut.Reset()
	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("update: code=%d err=%s", code, errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	got := string(raw)

	// Source should be updated.
	if !strings.Contains(got, "source: local:/Volumes/NewPhotos") {
		t.Fatalf("expected updated source, got:\n%s", got)
	}
	// Store should be preserved from original.
	if !strings.Contains(got, "store: main") {
		t.Fatalf("expected preserved store ref, got:\n%s", got)
	}
	// Tags should be preserved.
	if !strings.Contains(got, "daily") {
		t.Fatalf("expected preserved tags, got:\n%s", got)
	}
	// Excludes should be preserved.
	if !strings.Contains(got, "*.tmp") {
		t.Fatalf("expected preserved excludes, got:\n%s", got)
	}
}

func TestRunProfileNew_RequiresNameAndSource(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{"cloudstic", "profile", "new", "-profiles-file", profilesPath, "-name", "x"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "-source is required") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_RequiresStore(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "photos",
		"-source", "local:/Volumes/Photos",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "-store-ref is required") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_RejectsUnknownStoreRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "google-drive",
		"-source", "gdrive-changes:/Test Folder 2",
		"-store-ref", "home-s3",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Unknown store reference") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_CloudSourceRequiresAuthRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "drive-backup",
		"-source", "gdrive:/",
		"-store-ref", "s", "-store", "s3:bucket",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "-auth-ref is required for cloud sources") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_RejectsUnknownAuthRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "work-drive",
		"-source", "gdrive-changes:/Team",
		"-store-ref", "s", "-store", "s3:bucket",
		"-auth-ref", "google-work",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Unknown auth reference") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_AuthRefRequiresCloudSource(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "docs",
		"-source", "local:/Users/me/Documents",
		"-store-ref", "s", "-store", "local:/backup",
		"-auth-ref", "google-work",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "-auth-ref requires a cloud source") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestProfileStoreAuthMode(t *testing.T) {
	tests := []struct {
		name string
		s    cloudstic.ProfileStore
		want string
	}{
		{"s3_access_key", cloudstic.ProfileStore{S3AccessKey: "x"}, "static-keys"},
		{"s3_profile", cloudstic.ProfileStore{S3Profile: "x"}, "aws-shared-profile"},
		{"sftp_password", cloudstic.ProfileStore{StoreSFTPPassword: "x"}, "sftp"},
		{"sftp_key", cloudstic.ProfileStore{StoreSFTPKey: "x"}, "sftp"},
		{"empty", cloudstic.ProfileStore{}, "default-chain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := profileStoreAuthMode(tt.s)
			if got != tt.want {
				t.Fatalf("profileStoreAuthMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProfileProviderFromSource(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"gdrive:/Docs", "google"},
		{"gdrive-changes:/Docs", "google"},
		{"onedrive:/Files", "onedrive"},
		{"onedrive-changes:/Files", "onedrive"},
		{"local:/tmp", ""},
		{"sftp://host/path", ""},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			got := profileProviderFromSource(tt.source)
			if got != tt.want {
				t.Fatalf("profileProviderFromSource(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestRunProfileShow_WithOneDriveAuth(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  local-store:
    uri: local:/backup
auth:
  od-work:
    provider: onedrive
    onedrive_token_file: /tmp/od-token.json
profiles:
  my-onedrive:
    source: onedrive:/Documents
    store: local-store
    auth_ref: od-work
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "show", "-profiles-file", profilesPath, "my-onedrive"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Auth Provider") || !strings.Contains(got, "onedrive") {
		t.Fatalf("expected auth provider details in output:\n%s", got)
	}
	if !strings.Contains(got, "/tmp/od-token.json") {
		t.Fatalf("expected onedrive_token_file in output:\n%s", got)
	}
}

func TestRunProfileShow_MissingStoreRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores: {}
profiles:
  broken:
    source: local:/data
    store: nonexistent-store
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "show", "-profiles-file", profilesPath, "broken"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Store URI") || !strings.Contains(got, "<missing>") {
		t.Fatalf("expected missing store marker in output:\n%s", got)
	}
}

func TestRunProfileShow_MissingAuthRef(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  s:
    uri: local:/backup
auth: {}
profiles:
  broken:
    source: local:/data
    store: s
    auth_ref: nonexistent-auth
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "show", "-profiles-file", profilesPath, "broken"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Auth Provider") || !strings.Contains(got, "<missing>") {
		t.Fatalf("expected missing auth marker in output:\n%s", got)
	}
}

func TestRunProfileNew_WithExcludesAndTags(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "multi",
		"-source", "local:/data",
		"-store-ref", "s", "-store", "local:/backup",
		"-exclude", "*.log",
		"-exclude", "*.tmp",
		"-exclude-file", "/etc/excludes.txt",
		"-tag", "daily",
		"-tag", "important",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}

	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles file: %v", err)
	}
	got := string(raw)
	for _, want := range []string{"*.log", "*.tmp", "/etc/excludes.txt", "daily", "important"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in profiles YAML:\n%s", want, got)
		}
	}
}

func TestRunProfileNew_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "bad name!",
		"-source", "local:/data",
		"-store-ref", "s", "-store", "local:/backup",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "invalid profile name") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_InvalidSource(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "bad-source",
		"-source", "garbage-no-scheme",
		"-store-ref", "s", "-store", "local:/backup",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Invalid source") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileNew_InvalidStoreURI(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	os.Args = []string{
		"cloudstic", "profile", "new",
		"-profiles-file", profilesPath,
		"-name", "bad-store",
		"-source", "local:/data",
		"-store-ref", "s", "-store", "s3://bucket",
	}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(errOut.String(), "Invalid store URI") {
		t.Fatalf("unexpected error output: %s", errOut.String())
	}
}

func TestRunProfileList_WithOneDriveAuth(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  s:
    uri: local:/backup
auth:
  od-personal:
    provider: onedrive
    onedrive_token_file: /home/user/.config/od-token.json
profiles:
  docs:
    source: onedrive:/Documents
    store: s
    auth_ref: od-personal
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "list", "-profiles-file", profilesPath}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "onedrive") {
		t.Fatalf("expected onedrive provider in list output:\n%s", got)
	}
	if !strings.Contains(got, "/home/user/.config/od-token.json") {
		t.Fatalf("expected onedrive token path in list output:\n%s", got)
	}
}

func TestRunProfileShow_WithExcludes(t *testing.T) {
	tmpDir := t.TempDir()
	profilesPath := filepath.Join(tmpDir, "profiles.yaml")
	content := `version: 1
stores:
  s:
    uri: local:/backup
profiles:
  full:
    source: local:/data
    store: s
    tags:
      - daily
      - critical
    excludes:
      - "*.log"
      - "*.tmp"
    exclude_file: /etc/my-excludes.txt
    skip_native_files: true
    volume_uuid: ABC-123
`
	if err := os.WriteFile(profilesPath, []byte(content), 0600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	os.Args = []string{"cloudstic", "profile", "show", "-profiles-file", profilesPath, "full"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}

	if code := r.runProfile(context.Background()); code != 0 {
		t.Fatalf("runProfile() code=%d err=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Tags") || !strings.Contains(got, "daily, critical") {
		t.Fatalf("expected tags in output:\n%s", got)
	}
	if !strings.Contains(got, "Exclude Patterns") || !strings.Contains(got, "*.log") || !strings.Contains(got, "*.tmp") {
		t.Fatalf("expected excludes in output:\n%s", got)
	}
	if !strings.Contains(got, "/etc/my-excludes.txt") {
		t.Fatalf("expected exclude_file in output:\n%s", got)
	}
}
