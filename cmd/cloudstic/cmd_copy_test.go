package main

import (
	"strings"
	"testing"
	"time"
)

// The mirrors are generated from the repository flag groups so the two sets
// cannot drift. This is the assertion that makes that guarantee real: add a
// repository flag and it is mirrored automatically, or this fails.
func TestCopyMirrorsEveryRepositoryFlag(t *testing.T) {
	dest := &globalFlags{}
	var want []string
	for _, group := range []flagGroup{repoFlagSpecs, storeSFTPFlagSpecs, encryptionFlagSpecs} {
		for _, spec := range group(dest) {
			if spec.name == "profiles-file" {
				continue // one profiles file serves both repositories
			}
			want = append(want, spec.name)
		}
	}

	mirrored := map[string]flagSpec{}
	for _, spec := range copyFromFlagSpecs(&globalFlags{}) {
		mirrored[spec.name] = spec
	}

	for _, name := range want {
		if _, ok := mirrored[fromFlagPrefix+name]; !ok {
			t.Errorf("-%s has no -%s%s mirror", name, fromFlagPrefix, name)
		}
	}
	if len(mirrored) != len(want) {
		t.Errorf("mirrored %d flags, expected %d", len(mirrored), len(want))
	}
}

// An ambient CLOUDSTIC_* value means "the repository I am operating on".
// Letting one bind to both sides of a two-repository command is how an operator
// unlocks the wrong one, or believes they did.
func TestCopyFromFlagsCarryNoEnvironmentBindings(t *testing.T) {
	for _, spec := range copyFromFlagSpecs(&globalFlags{}) {
		if spec.env != "" {
			t.Errorf("-%s reads %s; mirrored flags must have no environment binding", spec.name, spec.env)
		}
	}
}

// Every mirrored credential flag must stay marked secret, or it would start
// rendering live values into help output.
func TestCopyFromCredentialFlagsStaySecret(t *testing.T) {
	secretByName := map[string]bool{}
	dest := &globalFlags{}
	for _, group := range []flagGroup{repoFlagSpecs, storeSFTPFlagSpecs, encryptionFlagSpecs} {
		for _, spec := range group(dest) {
			secretByName[spec.name] = spec.secret
		}
	}

	for _, spec := range copyFromFlagSpecs(&globalFlags{}) {
		original := strings.TrimPrefix(spec.name, fromFlagPrefix)
		if want := secretByName[original]; want != spec.secret {
			t.Errorf("-%s secret=%v, but -%s is secret=%v", spec.name, spec.secret, original, want)
		}
	}
}

// The mirrored usage text has to say which repository it configures: left
// alone, the mirror of -store-sftp-password reads "SFTP store password" beside
// a -source-* flag that means something else entirely.
func TestCopyFromFlagsAreDescribedAsTheSource(t *testing.T) {
	for _, spec := range copyFromFlagSpecs(&globalFlags{}) {
		if !strings.Contains(spec.usage, "source repository") {
			t.Errorf("-%s usage does not say which repository it configures: %q", spec.name, spec.usage)
		}
	}
}

func parseCopy(t *testing.T, args ...string) (*copyArgs, error) {
	t.Helper()
	return parseInto("copy", repoCommandGroups, declareCopyArgs, args)
}

// -store defaults to a local path, which is sensible for the one repository a
// command usually has. Inheriting that default for the *source* would make a
// bare `cloudstic copy` read whatever happens to be in ./backup_store.
func TestCopyRequiresAnExplicitSource(t *testing.T) {
	a, err := parseCopy(t, "-store", "local:/tmp/dst")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = prepareCopyArgs(a)
	if err == nil {
		t.Fatal("copy accepted an unspecified source repository")
	}
	if !strings.Contains(err.Error(), "-from-store") || !strings.Contains(err.Error(), "-from-profile") {
		t.Errorf("error does not say how to name the source: %v", err)
	}
}

func TestCopyAcceptsSourceByStoreOrProfile(t *testing.T) {
	for _, args := range [][]string{
		{"-store", "local:/tmp/dst", "-from-store", "local:/tmp/src"},
		{"-store", "local:/tmp/dst", "-from-profile", "seed"},
	} {
		a, err := parseCopy(t, args...)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		if err := prepareCopyArgs(a); err != nil {
			t.Errorf("prepare %v: %v", args, err)
		}
	}
}

func TestCopySourceInheritsSharedSettingsButNoCredentials(t *testing.T) {
	a, err := parseCopy(t,
		"-store", "local:/tmp/dst",
		"-password", "destination-secret",
		"-config-dir", "/tmp/cfg",
		"-quiet",
		"-from-store", "local:/tmp/src",
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := prepareCopyArgs(a); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if a.from.configDir != "/tmp/cfg" {
		t.Errorf("source config dir = %q, want the destination's", a.from.configDir)
	}
	if !a.from.quiet {
		t.Error("source did not inherit -quiet, so the two halves would report differently")
	}
	// The whole reason the two structs are kept apart.
	if a.from.password == "destination-secret" {
		t.Error("the destination password leaked into the source repository's flags")
	}
}

func TestCopySourceCredentialsAreIndependent(t *testing.T) {
	a, err := parseCopy(t,
		"-store", "local:/tmp/dst", "-password", "dst-pw",
		"-from-store", "local:/tmp/src", "-from-password", "src-pw",
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := prepareCopyArgs(a); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if a.password != "dst-pw" {
		t.Errorf("destination password = %q", a.password)
	}
	if a.from.password != "src-pw" {
		t.Errorf("source password = %q", a.from.password)
	}
}

func TestCopyParsesSinceAsDateOrTimestamp(t *testing.T) {
	tests := map[string]time.Time{
		"2026-04-01":           time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		"2026-04-01T20:15:03Z": time.Date(2026, 4, 1, 20, 15, 3, 0, time.UTC),
	}
	for raw, want := range tests {
		got, err := parseSinceTime(raw)
		if err != nil {
			t.Errorf("parseSinceTime(%q): %v", raw, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseSinceTime(%q) = %s, want %s", raw, got, want)
		}
	}

	if _, err := parseSinceTime("last tuesday"); err == nil {
		t.Error("parseSinceTime accepted an unparseable value")
	}
}

func TestCopySourceFilterSplitsURI(t *testing.T) {
	a, err := parseCopy(t,
		"-store", "local:/tmp/dst", "-from-store", "local:/tmp/src",
		"-source", "local:./Documents",
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := prepareCopyArgs(a); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if a.filterSource != "local" {
		t.Errorf("filterSource = %q, want local", a.filterSource)
	}
	if a.filterPath != "./Documents" {
		t.Errorf("filterPath = %q, want ./Documents", a.filterPath)
	}
}

func TestCopySourceFilterAcceptsBareType(t *testing.T) {
	a, err := parseCopy(t,
		"-store", "local:/tmp/dst", "-from-store", "local:/tmp/src", "-source", "gdrive",
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := prepareCopyArgs(a); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if a.filterSource != "gdrive" || a.filterPath != "" {
		t.Errorf("filterSource=%q filterPath=%q, want gdrive with no path", a.filterSource, a.filterPath)
	}
}

func TestCopyCollectsPositionalSnapshotIDs(t *testing.T) {
	a, err := parseCopy(t,
		"-store", "local:/tmp/dst", "-from-store", "local:/tmp/src",
		"410b18a2", "4e5d5487", "latest",
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(a.snapshotIDs) != 3 {
		t.Fatalf("snapshotIDs = %v, want three selectors", a.snapshotIDs)
	}
	if a.snapshotIDs[2] != "latest" {
		t.Errorf("last selector = %q, want latest", a.snapshotIDs[2])
	}
}

func TestSameStoreTargetIgnoresUnsetURIs(t *testing.T) {
	// Two repositories that both resolve to no URI are not thereby the same
	// one; the guard must not fire on absence.
	if sameStoreTarget(storeConfig{}, storeConfig{}) {
		t.Error("two empty store configurations were reported as the same repository")
	}
	if !sameStoreTarget(storeConfig{URI: "local:/a"}, storeConfig{URI: "local:/a"}) {
		t.Error("identical URIs were not recognised as the same repository")
	}
	if sameStoreTarget(storeConfig{URI: "local:/a"}, storeConfig{URI: "local:/b"}) {
		t.Error("different URIs were reported as the same repository")
	}
}
