package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestCompletionCandidates_ProfileAndAuthNames(t *testing.T) {
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := cloudstic.SaveProfilesFile(profilesPath, &cloudstic.ProfilesConfig{
		Version: 1,
		Profiles: map[string]cloudstic.BackupProfile{
			"laptop": {},
			"server": {},
		},
		Auth: map[string]cloudstic.ProfileAuth{
			"google-work": {},
			"ms-personal": {},
		},
	}); err != nil {
		t.Fatalf("SaveProfilesFile: %v", err)
	}

	profiles, err := completionCandidates(context.Background(), "profile-names", "", []string{"backup", "-profiles-file", profilesPath})
	if err != nil {
		t.Fatalf("completionCandidates(profile-names): %v", err)
	}
	if want := []string{"laptop", "server"}; !reflect.DeepEqual(profiles, want) {
		t.Fatalf("profile names = %#v, want %#v", profiles, want)
	}

	auth, err := completionCandidates(context.Background(), "auth-names", "", []string{"backup", "-profiles-file", profilesPath})
	if err != nil {
		t.Fatalf("completionCandidates(auth-names): %v", err)
	}
	if want := []string{"google-work", "ms-personal"}; !reflect.DeepEqual(auth, want) {
		t.Fatalf("auth names = %#v, want %#v", auth, want)
	}
}

func TestCompletionCandidates_MissingProfilesFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	got, err := completionCandidates(context.Background(), "profile-names", "", []string{"backup", "-profiles-file", path})
	if err != nil {
		t.Fatalf("completionCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("profile names = %#v, want empty", got)
	}
}

func TestRunCompletionQuery_WritesCandidates(t *testing.T) {
	oldLoad := completionLoadProfilesFile
	completionLoadProfilesFile = func(string) (*cloudstic.ProfilesConfig, error) {
		return &cloudstic.ProfilesConfig{
			Version: 1,
			Profiles: map[string]cloudstic.BackupProfile{
				"work": {},
			},
		}, nil
	}
	t.Cleanup(func() { completionLoadProfilesFile = oldLoad })

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"cloudstic", "__complete", "profile-names", "", "backup"}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	oldStdout := os.Stdout
	t.Cleanup(func() { os.Stdout = oldStdout })
	os.Stdout = w

	if code := runCompletionQuery(context.Background()); code != 0 {
		t.Fatalf("runCompletionQuery code = %d, want 0", code)
	}
	_ = w.Close()

	data, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll: %v", readErr)
	}
	if string(data) != "work\n" {
		t.Fatalf("stdout = %q, want %q", string(data), "work\n")
	}
}
