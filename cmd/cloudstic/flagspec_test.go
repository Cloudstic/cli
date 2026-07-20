package main

import (
	"bytes"
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
	b := commandFlags{set: fs, own: []flagSpec{spec}}
	if err := b.parse(nil); err != nil {
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
	b := commandFlags{set: fs, own: []flagSpec{spec}}
	if err := b.parse(nil); err != nil {
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
	b := commandFlags{set: fs, own: []flagSpec{spec}}
	if err := b.parse([]string{"-thing", "from-flag"}); err != nil {
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
			b := commandFlags{set: fs, own: []flagSpec{spec}}
			if err := b.parse(nil); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if target != tc.want {
				t.Errorf("env %q -> %v, want %v", tc.env, target, tc.want)
			}
		})
	}
}

func TestBoolFlagRejectsUnparseableEnv(t *testing.T) {
	withFakeEnv(t, map[string]string{"CLOUDSTIC_TEST_BOOL": "banana"})
	var target bool
	spec := boolFlag(&target, "thing", true, "usage", withEnv("CLOUDSTIC_TEST_BOOL"))
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	bindFlags(fs, []flagSpec{spec})
	b := commandFlags{set: fs, own: []flagSpec{spec}}

	err := b.parse(nil)
	if err == nil {
		t.Fatal("expected an error for an unparseable boolean environment value")
	}
	// The message must name the variable and say what is acceptable.
	for _, want := range []string{"CLOUDSTIC_TEST_BOOL", "banana", "true or false"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
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

	backupFlags := lookupCommandForTest(t, "backup").flags().names()
	for _, name := range sourceOnly {
		if !slices.Contains(backupFlags, name) {
			t.Errorf("backup should offer source flag -%s", name)
		}
	}

	for _, tc := range []struct {
		name string
		fn   func() commandFlags
	}{
		{"list", func() commandFlags { return lookupCommandForTest(t, "list").flags() }},
		{"check", func() commandFlags { return lookupCommandForTest(t, "check").flags() }},
		{"prune", func() commandFlags { return lookupCommandForTest(t, "prune").flags() }},
		{"cat", func() commandFlags { return lookupCommandForTest(t, "cat").flags() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := tc.fn().names()
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
	walk(commandRegistry(), func(path string, c command) {
		if c.flags == nil {
			return
		}
		cf := c.flags()
		for _, spec := range cf.specs() {
			if cf.lookup(spec.name) == nil {
				t.Errorf("command %q declares spec -%s but never registers it", path, spec.name)
			}
		}
	})
}

// TestSecretEnvValuesNeverAppearInHelp is the regression test for the leak this
// change fixes: exporting a credential must not put its value in -h output.
func TestSecretEnvValuesNeverAppearInHelp(t *testing.T) {
	const sentinel = "SUPER-SECRET-SENTINEL"

	env := map[string]string{}
	for _, s := range globalSpecs() {
		if s.secret && s.env != "" {
			env[s.env] = sentinel
		}
	}
	if len(env) == 0 {
		t.Fatal("no secret flags declare an environment variable")
	}
	withFakeEnv(t, env)

	walk(commandRegistry(), func(path string, c command) {
		if c.flags == nil {
			return
		}
		var buf bytes.Buffer
		printCommandHelp(&buf, c, path)
		if strings.Contains(buf.String(), sentinel) {
			t.Errorf("help output for %q leaks a secret environment value", path)
		}
	})
}

// TestHelpNamesEnvironmentVariables ensures users can still discover which
// variable feeds a flag, even though the value is never shown.
func TestHelpNamesEnvironmentVariables(t *testing.T) {
	var buf bytes.Buffer
	printCommandHelp(&buf, lookupCommandForTest(t, "backup"), "backup")
	out := buf.String()

	for _, want := range []string{"[env: CLOUDSTIC_PASSWORD]", "[env: CLOUDSTIC_STORE]", "[env: CLOUDSTIC_SOURCE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output should name the environment variable %s", want)
		}
	}
}

func lookupCommandForTest(t *testing.T, name string) command {
	t.Helper()
	c, ok := lookupCommand(name)
	if !ok {
		t.Fatalf("command %q not in registry", name)
	}
	return c
}
