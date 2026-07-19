package main

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestStaticCompletionValuesComeFromFlagSpecs(t *testing.T) {
	tests := []struct {
		name string
		spec flagSpec
		want []string
	}{
		{"global store", globalFlagByName("store"), []string{"local:", "s3:", "b2:", "sftp://"}},
		{"backup source", mustCommandFlag(t, backupCommandSpec(), "source"), []string{"local:", "sftp://", "gdrive", "gdrive-changes", "onedrive", "onedrive-changes"}},
		{"store new uri", mustCommandFlag(t, storeNewCommandSpec(), "uri"), []string{"local:", "s3:", "b2:", "sftp://"}},
		{"forget source", mustCommandFlag(t, forgetCommandSpec(), "source"), []string{"local:", "sftp://", "gdrive", "gdrive-changes", "onedrive", "onedrive-changes"}},
		{"restore format", mustCommandFlag(t, restoreCommandSpec(), "format"), []string{"zip", "dir"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !slices.Equal(test.spec.completionValues, test.want) {
				t.Fatalf("completion values = %v, want %v", test.spec.completionValues, test.want)
			}
		})
	}
}

func mustCommandFlag(t *testing.T, command *commandSpec, name string) flagSpec {
	t.Helper()
	spec, ok := command.flag(name)
	if !ok {
		t.Fatalf("command %s is missing -%s", command.path(), name)
	}
	return spec
}

func TestRunCompletion(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			r := &runner{args: []string{shell}, out: io.Discard, errOut: io.Discard}
			if code := runCompletion(r); code != 0 {
				t.Fatalf("runCompletion() exit code = %d, want 0", code)
			}
		})
	}

	var errOut bytes.Buffer
	if code := runCompletion(&runner{out: io.Discard, errOut: &errOut}); code != 1 {
		t.Fatalf("runCompletion() without shell exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "Usage: cloudstic completion <shell>") {
		t.Fatalf("unexpected usage output: %q", errOut.String())
	}
}

func TestGeneratedCompletionsContainRegistry(t *testing.T) {
	tests := []struct {
		name     string
		generate func(io.Writer)
		markers  []string
	}{
		{name: "bash", generate: completionBash, markers: []string{"_cloudstic()", "complete -F _cloudstic cloudstic", "profile-names", "auth-names"}},
		{name: "zsh", generate: completionZsh, markers: []string{"#compdef cloudstic", "_cloudstic()", "compdef _cloudstic cloudstic", "_cloudstic_profile_names", "_cloudstic_auth_names"}},
		{name: "fish", generate: completionFish, markers: []string{"complete -c cloudstic -f", "function __fish_cloudstic_query", "profile-names", "auth-names"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			tt.generate(&out)
			script := out.String()
			for _, marker := range tt.markers {
				if !strings.Contains(script, marker) {
					t.Errorf("completion missing marker %q", marker)
				}
			}
			for _, root := range publicRootCommands() {
				if !strings.Contains(script, root.name) {
					t.Errorf("completion missing root command %q", root.name)
				}
			}
			for _, command := range publicLeafCommands() {
				for _, spec := range command.effectiveFlags() {
					if !strings.Contains(script, spec.name) {
						t.Errorf("completion missing %q flag %q", command.path(), spec.name)
					}
				}
			}
			if strings.Contains(script, "__complete:Display") || strings.Contains(script, "--version:Display") {
				t.Error("completion exposes a hidden command or alias")
			}
		})
	}
}

func TestBashCompletionBooleanSFTPFlags(t *testing.T) {
	var out bytes.Buffer
	completionBash(&out)
	script := out.String()
	for _, name := range []string{"source-sftp-insecure", "store-sftp-insecure"} {
		if !strings.Contains(script, "-"+name) {
			t.Fatalf("bash completion missing -%s", name)
		}
		for _, line := range strings.Split(script, "\n") {
			_, valueList, ok := strings.Cut(line, "value_flags=")
			if ok && strings.Contains(valueList, "-"+name) {
				t.Errorf("boolean flag -%s appears in value-taking list: %s", name, line)
			}
		}
	}
	if !strings.Contains(script, `if [[ -z "$root" ]]`) {
		t.Error("bash completion does not handle partial root commands")
	}
	if !strings.Contains(script, "for value_flag in $value_flags") {
		t.Error("bash completion does not skip values using the generated value flag list")
	}
}

func TestStoreNewCompletionOmitsUnsupportedHostKeyFlags(t *testing.T) {
	storeNew := findCommand(rootCommand("store").children, "new")
	for _, spec := range storeNew.effectiveFlags() {
		if spec.name == "store-sftp-known-hosts" || spec.name == "store-sftp-insecure" {
			t.Errorf("store new still advertises unsupported flag %q", spec.name)
		}
	}
}

func TestCompletionProfilesPathUsesSpecEnvironmentAndFlagPrecedence(t *testing.T) {
	t.Setenv("CLOUDSTIC_PROFILES_FILE", "/environment/profiles.yaml")
	if got := completionProfilesPath(nil); got != "/environment/profiles.yaml" {
		t.Fatalf("environment profiles path = %q", got)
	}
	if got := completionProfilesPath([]string{"-profiles-file", "/flag/profiles.yaml"}); got != "/flag/profiles.yaml" {
		t.Fatalf("flag profiles path = %q", got)
	}
}
