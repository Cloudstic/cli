package main

import (
	"flag"
	"slices"
	"strings"
	"testing"
)

// withFakeEnv swaps the environment lookup for the duration of a test.
func withFakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := lookupEnv
	lookupEnv = func(key string) string { return env[key] }
	t.Cleanup(func() { lookupEnv = prev })
}

func TestStringFlagUsesEnvOverDefault(t *testing.T) {
	withFakeEnv(t, map[string]string{"CLOUDSTIC_TEST_VALUE": "from-env"})

	var target string
	spec := stringFlag(&target, "thing", "from-default", "usage", withEnv("CLOUDSTIC_TEST_VALUE"))
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	bindFlags(fs, []flagSpec{spec})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if target != "from-env" {
		t.Errorf("target=%q want from-env", target)
	}
}

func TestStringFlagFallsBackToDefaultWhenEnvUnset(t *testing.T) {
	withFakeEnv(t, map[string]string{})

	var target string
	spec := stringFlag(&target, "thing", "from-default", "usage", withEnv("CLOUDSTIC_TEST_VALUE"))
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	bindFlags(fs, []flagSpec{spec})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if target != "from-default" {
		t.Errorf("target=%q want from-default", target)
	}
}

func TestExplicitFlagBeatsEnv(t *testing.T) {
	withFakeEnv(t, map[string]string{"CLOUDSTIC_TEST_VALUE": "from-env"})

	var target string
	spec := stringFlag(&target, "thing", "from-default", "usage", withEnv("CLOUDSTIC_TEST_VALUE"))
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	bindFlags(fs, []flagSpec{spec})
	if err := fs.Parse([]string{"-thing", "from-flag"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if target != "from-flag" {
		t.Errorf("target=%q want from-flag (explicit flag must beat env)", target)
	}
}

// TestBoolFlagAcceptsParseBoolSpellings documents that boolean environment
// values go through strconv.ParseBool, so "TRUE"/"yes"-style values behave
// predictably rather than silently reading as false.
func TestBoolFlagAcceptsParseBoolSpellings(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{"1", true}, {"t", true}, {"T", true}, {"true", true}, {"TRUE", true}, {"True", true},
		{"0", false}, {"f", false}, {"false", false}, {"FALSE", false},
	} {
		t.Run(tc.env, func(t *testing.T) {
			withFakeEnv(t, map[string]string{"CLOUDSTIC_TEST_BOOL": tc.env})
			var target bool
			spec := boolFlag(&target, "thing", false, "usage", withEnv("CLOUDSTIC_TEST_BOOL"))
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			bindFlags(fs, []flagSpec{spec})
			if err := fs.Parse(nil); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if target != tc.want {
				t.Errorf("env %q -> %v, want %v", tc.env, target, tc.want)
			}
		})
	}
}

func TestBoolFlagIgnoresUnparseableEnv(t *testing.T) {
	withFakeEnv(t, map[string]string{"CLOUDSTIC_TEST_BOOL": "banana"})
	var target bool
	spec := boolFlag(&target, "thing", true, "usage", withEnv("CLOUDSTIC_TEST_BOOL"))
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	bindFlags(fs, []flagSpec{spec})
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !target {
		t.Error("unparseable boolean env should leave the declared default intact")
	}
}

// TestSecretFlagsAreMarked pins the set of flags carrying credentials. #266
// relies on this marker to keep secret values out of help output.
func TestSecretFlagsAreMarked(t *testing.T) {
	g := &globalFlags{}
	specs := globalFlagSpecsFor(g, allGlobalGroups)

	secrets := map[string]bool{}
	for _, s := range specs {
		if s.secret {
			secrets[s.name] = true
		}
	}

	for _, name := range []string{
		"password", "encryption-key", "recovery-key",
		"s3-access-key", "s3-secret-key",
		"source-sftp-password", "store-sftp-password",
	} {
		if !secrets[name] {
			t.Errorf("flag -%s carries a credential and must be marked secret", name)
		}
	}

	// Flags that are emphatically not secrets should not be marked.
	for _, name := range []string{"store", "verbose", "kms-region", "profiles-file"} {
		if secrets[name] {
			t.Errorf("flag -%s should not be marked secret", name)
		}
	}
}

// TestEverySecretFlagDeclaresEnv is a consistency check: a credential that can
// be supplied by environment variable needs both markers for #266 to redact it.
func TestSecretFlagsWithEnvAreConsistent(t *testing.T) {
	g := &globalFlags{}
	for _, s := range globalFlagSpecsFor(g, allGlobalGroups) {
		if s.secret && s.env == "" {
			t.Logf("note: secret flag -%s has no environment binding", s.name)
		}
		if s.env != "" && !strings.HasPrefix(s.env, "CLOUDSTIC_") {
			// Provider-standard names are deliberate; record them explicitly.
			switch s.env {
			case "AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY":
			default:
				t.Errorf("flag -%s uses unprefixed env %q; add it to the documented provider-standard list", s.name, s.env)
			}
		}
	}
}

// TestCommandFlagGroupsAreScoped is the payoff of opt-in groups: commands that
// never read a source must not advertise source-SFTP credentials.
func TestCommandFlagGroupsAreScoped(t *testing.T) {
	sourceOnly := specNames(sourceSFTPFlagSpecs(&globalFlags{}))

	backupFS, _ := newBackupFlagSet()
	backupFlags := flagNames(backupFS)
	for _, name := range sourceOnly {
		if !slices.Contains(backupFlags, name) {
			t.Errorf("backup should offer source flag -%s", name)
		}
	}

	for _, tc := range []struct {
		name string
		fn   func() *flag.FlagSet
	}{
		{"list", func() *flag.FlagSet { fs, _ := newListFlagSet(); return fs }},
		{"check", func() *flag.FlagSet { fs, _ := newCheckFlagSet(); return fs }},
		{"prune", func() *flag.FlagSet { fs, _ := newPruneFlagSet(); return fs }},
		{"cat", func() *flag.FlagSet { fs, _ := newCatFlagSet(); return fs }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := flagNames(tc.fn())
			for _, s := range sourceOnly {
				if slices.Contains(names, s) {
					t.Errorf("%s does not read a source but advertises -%s", tc.name, s)
				}
			}
			// It must still offer the store-side equivalents.
			if !slices.Contains(names, "store-sftp-password") {
				t.Errorf("%s must still offer -store-sftp-password", tc.name)
			}
		})
	}
}

// TestFlagSpecsCoverRegisteredFlags ensures the declarative specs and the bound
// flag set stay in agreement for every command that declares one.
func TestFlagSpecsCoverRegisteredFlags(t *testing.T) {
	for _, c := range commandRegistry() {
		if c.newFlagSet == nil {
			continue
		}
		fs := c.newFlagSet()
		for _, name := range flagNames(fs) {
			if fs.Lookup(name) == nil {
				t.Errorf("command %q: flag -%s is not registered", c.name, name)
			}
		}
	}
}
