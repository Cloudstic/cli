package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/secretref"
	"github.com/cloudstic/cli/internal/tui/forms"
)

type testWritableSecretBackend struct {
	scheme      string
	displayName string
	defaultRef  string
	storedRef   string
	storedValue string
}

func (b *testWritableSecretBackend) Resolve(context.Context, secretref.Ref) (string, error) {
	return "", nil
}
func (b *testWritableSecretBackend) Scheme() string                   { return b.scheme }
func (b *testWritableSecretBackend) DisplayName() string              { return b.displayName }
func (b *testWritableSecretBackend) WriteSupported() bool             { return true }
func (b *testWritableSecretBackend) DefaultRef(string, string) string { return b.defaultRef }
func (b *testWritableSecretBackend) Exists(context.Context, secretref.Ref) (bool, error) {
	return false, nil
}
func (b *testWritableSecretBackend) Store(_ context.Context, ref secretref.Ref, value string) error {
	b.storedRef = ref.Raw
	b.storedValue = value
	return nil
}

func TestTUIProfileSourceCompose(t *testing.T) {
	tests := []struct {
		name string
		src  tuiProfileSource
		want string
	}{
		{name: "local", src: tuiProfileSource{Type: "local", Value: "/docs"}, want: "local:/docs"},
		{name: "sftp", src: tuiProfileSource{Type: "sftp", Value: "backup@host/data"}, want: "sftp://backup@host/data"},
		{name: "gdrive root", src: tuiProfileSource{Type: "gdrive", Value: ""}, want: "gdrive"},
		{name: "gdrive path", src: tuiProfileSource{Type: "gdrive", Value: "/Team"}, want: "gdrive:/Team"},
		{name: "gdrive drive name", src: tuiProfileSource{Type: "gdrive", Value: "Shared Drive/Finance"}, want: "gdrive://Shared Drive/Finance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.src.Compose(); got != tt.want {
				t.Fatalf("Compose()=%q want %q", got, tt.want)
			}
		})
	}
}

// newTestFormsBackend builds the dashboard's forms backend over a temporary
// profiles file, returning the backend and the file's path.
func newTestFormsBackend(t *testing.T, cfg *cloudstic.ProfilesConfig) (*tuiFormsBackend, string) {
	t.Helper()
	path := t.TempDir() + "/profiles.yaml"
	if err := cloudstic.SaveProfilesFile(path, cfg); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}
	loaded, err := tuiLoadConfig(path)
	if err != nil {
		t.Fatalf("tuiLoadConfig: %v", err)
	}
	return newTUIFormsBackend(&runner{out: io.Discard, errOut: io.Discard}, path, loaded), path
}

func TestInitialStoreValues_PopulatesExistingSecretFields(t *testing.T) {
	backend, _ := newTestFormsBackend(t, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {
				URI:               "s3:bucket/prod",
				S3Region:          "us-east-1",
				S3Profile:         "work",
				S3Endpoint:        "https://s3.example.com",
				S3AccessKeySecret: "env://S3_ACCESS_KEY",
				S3SecretKeySecret: "keychain://cloudstic/store/remote/s3-secret",
				PasswordSecret:    "keychain://cloudstic/store/remote/password",
				KMSKeyARN:         "arn:aws:kms:us-east-1:123:key/abc",
				KMSRegion:         "us-east-1",
				KMSEndpoint:       "https://kms.example.com",
			},
		},
	})

	values := backend.InitialStoreValues("remote")
	want := map[string]string{
		forms.FieldStoreType:      "s3",
		forms.FieldStoreValue:     "bucket/prod",
		forms.FieldS3Region:       "us-east-1",
		forms.FieldS3Profile:      "work",
		forms.FieldS3AccessKey:    "env://S3_ACCESS_KEY",
		forms.FieldS3SecretKey:    "keychain://cloudstic/store/remote/s3-secret",
		forms.FieldEncryptionMode: string(tuiStoreEncryptionKMS),
		forms.FieldKMSKeyARN:      "arn:aws:kms:us-east-1:123:key/abc",
	}
	for key, expected := range want {
		if got := values[key]; got != expected {
			t.Fatalf("%s=%q want %q", key, got, expected)
		}
	}
}

func TestSaveStore_PersistsSecretRefsAndClearsUnusedModes(t *testing.T) {
	backend, path := newTestFormsBackend(t, &cloudstic.ProfilesConfig{Version: 1})

	uri, err := backend.ComposeStore("s3", "bucket/prod")
	if err != nil {
		t.Fatalf("ComposeStore: %v", err)
	}
	err = backend.SaveStore("remote", map[string]string{
		forms.FieldStoreURI:       uri,
		forms.FieldStoreType:      "s3",
		forms.FieldS3Region:       "us-east-1",
		forms.FieldS3Endpoint:     "https://s3.example.com",
		forms.FieldS3AccessKey:    "env://S3_ACCESS_KEY",
		forms.FieldS3SecretKey:    "keychain://cloudstic/store/remote/s3-secret",
		forms.FieldEncryptionMode: forms.EncPassword,
		forms.FieldPasswordSecret: "keychain://cloudstic/store/remote/password",
		// Set but irrelevant to the selected mode: must not be persisted.
		forms.FieldKMSKeyARN: "arn:aws:kms:us-east-1:123:key/abc",
	}, false)
	if err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	cfg, err := cloudstic.LoadProfilesFile(path)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	store := cfg.Stores["remote"]
	if store.URI != "s3:bucket/prod" {
		t.Fatalf("uri=%q want s3:bucket/prod", store.URI)
	}
	if store.S3Region != "us-east-1" || store.S3Endpoint != "https://s3.example.com" {
		t.Fatalf("unexpected s3 config: %+v", store)
	}
	if store.S3AccessKeySecret != "env://S3_ACCESS_KEY" || store.S3SecretKeySecret != "keychain://cloudstic/store/remote/s3-secret" {
		t.Fatalf("unexpected s3 secret refs: %+v", store)
	}
	if store.PasswordSecret != "keychain://cloudstic/store/remote/password" {
		t.Fatalf("password secret=%q", store.PasswordSecret)
	}
	if store.RecoveryKeySecret != "" {
		t.Fatalf("expected no recovery secret in store form, got %q", store.RecoveryKeySecret)
	}
	if store.KMSKeyARN != "" {
		t.Fatalf("expected kms config to be cleared: %+v", store)
	}
}

func TestSaveStore_ClearsConnectionFieldsOfOtherTypes(t *testing.T) {
	backend, path := newTestFormsBackend(t, &cloudstic.ProfilesConfig{
		Version: 1,
		Stores: map[string]cloudstic.ProfileStore{
			"remote": {
				URI:               "s3:bucket/prod",
				S3Region:          "us-east-1",
				S3AccessKeySecret: "env://S3_ACCESS_KEY",
			},
		},
	})

	uri, err := backend.ComposeStore("local", "/tmp/backups")
	if err != nil {
		t.Fatalf("ComposeStore: %v", err)
	}
	if err := backend.SaveStore("remote", map[string]string{
		forms.FieldStoreURI:  uri,
		forms.FieldStoreType: "local",
	}, true); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	cfg, err := cloudstic.LoadProfilesFile(path)
	if err != nil {
		t.Fatalf("LoadProfilesFile: %v", err)
	}
	store := cfg.Stores["remote"]
	if store.URI != "local:/tmp/backups" {
		t.Fatalf("uri=%q want local:/tmp/backups", store.URI)
	}
	if store.S3Region != "" || store.S3AccessKeySecret != "" {
		t.Fatalf("s3 fields not cleared for a local store: %+v", store)
	}
}

func TestStoreSecret_WritesThroughWritableBackend(t *testing.T) {
	secretBackend := &testWritableSecretBackend{
		scheme:      "test",
		displayName: "Test Backend",
		defaultRef:  "test://cloudstic/store/remote/password",
	}
	oldResolver := tuiSecretResolver
	t.Cleanup(func() { tuiSecretResolver = oldResolver })
	tuiSecretResolver = secretref.NewResolver(map[string]secretref.Backend{
		"env":  secretref.NewEnvBackend(nil),
		"test": secretBackend,
	})

	backend, _ := newTestFormsBackend(t, &cloudstic.ProfilesConfig{Version: 1})

	ref := backend.DefaultRef("test", "remote", "password")
	if ref != "test://cloudstic/store/remote/password" {
		t.Fatalf("default ref=%q", ref)
	}
	if err := backend.StoreSecret(ref, "super-secret"); err != nil {
		t.Fatalf("StoreSecret: %v", err)
	}
	if secretBackend.storedRef != ref || secretBackend.storedValue != "super-secret" {
		t.Fatalf("unexpected stored secret: ref=%q value=%q", secretBackend.storedRef, secretBackend.storedValue)
	}
}

func TestProfileAuthOptions_FiltersByProvider(t *testing.T) {
	cfg := &cloudstic.ProfilesConfig{
		Auth: map[string]cloudstic.ProfileAuth{
			"work-gdrive": {Provider: "gdrive"},
			"home-gdrive": {Provider: "gdrive"},
			"onedrive":    {Provider: "onedrive"},
		},
	}
	got := profileAuthOptions(cfg, "gdrive")
	if len(got) != 2 || got[0] != "home-gdrive" || got[1] != "work-gdrive" {
		t.Fatalf("auth options = %v want sorted gdrive refs", got)
	}
	if got := profileAuthOptions(cfg, "sftp"); len(got) != 0 {
		t.Fatalf("auth options = %v want none", got)
	}
}

func TestRunTUI_Help(t *testing.T) {
	args := []string{"--help"}

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	if code := tuiCommand().execute(r.withArgs(args), context.Background(), "tui"); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "cloudstic tui") ||
		!strings.Contains(out.String(), "-profiles-file <path>") {
		t.Fatalf("unexpected help output:\n%s", out.String())
	}
}

func TestRunTUI_RequiresInteractiveTerminal(t *testing.T) {
	oldIsTerminal := isTerminalFunc
	t.Cleanup(func() { isTerminalFunc = oldIsTerminal })
	isTerminalFunc = func(uintptr) bool { return false }
	args := []string{}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = readEnd.Close() }()
	_ = writeEnd.Close()

	var out strings.Builder
	var errOut strings.Builder
	r := &runner{
		out:    &out,
		errOut: &errOut,
		stdin:  readEnd,
		lineIn: bufio.NewReader(readEnd),
	}
	if code := tuiCommand().execute(r.withArgs(args), context.Background(), "tui"); code == 0 {
		t.Fatalf("expected failure for non-interactive terminal")
	}
	if !strings.Contains(errOut.String(), "requires an interactive terminal") {
		t.Fatalf("unexpected stderr:\n%s", errOut.String())
	}
}

func TestCaptureTUIRunnerOutput_RestoresRunnerState(t *testing.T) {
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut}
	log := newTUIActionState(5)

	restore := captureTUIRunnerOutput(r, log)
	if _, err := io.WriteString(r.out, "hello\n"); err != nil {
		t.Fatalf("write captured output: %v", err)
	}
	restore()

	if got := strings.Join(log.Lines(), "\n"); !strings.Contains(got, "hello") {
		t.Fatalf("captured log missing output: %q", got)
	}
	if r.out != &out || r.errOut != &errOut {
		t.Fatalf("runner outputs not restored")
	}
}
