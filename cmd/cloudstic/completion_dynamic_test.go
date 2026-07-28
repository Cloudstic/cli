package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cloudstic/cli/pkg/profile"
)

func TestCompletionCandidates_ProfileAndAuthNames(t *testing.T) {
	profilesPath := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := profile.Save(profilesPath, &profile.Config{
		Version: 1,
		Profiles: map[string]profile.Profile{
			"laptop": {},
			"server": {},
		},
		Auth: map[string]profile.Auth{
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
	completionLoadProfilesFile = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version: 1,
			Profiles: map[string]profile.Profile{
				"work": {},
			},
		}, nil
	}
	t.Cleanup(func() { completionLoadProfilesFile = oldLoad })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	queryRunner := &runner{out: w}
	queryArgs := &completionQueryArgs{values: []string{"profile-names", "", "backup"}}
	if code := runCompletionQuery(queryRunner, context.Background(), queryArgs); code != 0 {
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

func TestCompleteCommand_AcceptsForwardedFlags(t *testing.T) {
	oldLoad := completionLoadProfilesFile
	completionLoadProfilesFile = func(string) (*profile.Config, error) {
		return &profile.Config{
			Version:  1,
			Profiles: map[string]profile.Profile{"work": {}},
		}, nil
	}
	t.Cleanup(func() { completionLoadProfilesFile = oldLoad })

	var out bytes.Buffer
	r := &runner{
		args:   []string{"--", "profile-names", "", "backup", "-profile"},
		out:    &out,
		errOut: io.Discard,
	}
	if code := completeCommand().execute(r, context.Background(), "__complete"); code != 0 {
		t.Fatalf("completeCommand() exit code = %d, want 0", code)
	}
	if got, want := out.String(), "work\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
