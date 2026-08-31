package config

import "testing"

// TestBackupFieldSpecsCoverEveryBackupFieldConst is the completeness check the
// three parallel lists never had: every Field the const block declares as a
// backup field must be in the table, since BackupFields and backupFieldKeys are
// both derived from it now.
//
// Declaring a const and forgetting the table used to leave the field settable
// in YAML, absent from BackupFields, and therefore silently outside the CLI's
// decided-field set (issue #569).
//
// This test is internal because the table is: the point of the exercise is that
// nothing outside this package needs to know the field list.
func TestBackupFieldSpecsCoverEveryBackupFieldConst(t *testing.T) {
	inTable := map[Field]bool{}
	for _, s := range backupFieldSpecs() {
		inTable[s.field] = true
		if s.key == "" {
			t.Errorf("%s: no profiles-file key, so an error about it cannot name the YAML entry", s.field)
		}
		if s.str.dest == nil && s.bl.dest == nil && s.sl.dest == nil {
			t.Errorf("%s: no destination accessor, so the field can never be applied", s.field)
		}
	}

	for _, f := range []Field{
		FieldSourceURI, FieldTags, FieldExcludes, FieldExcludeFile, FieldIgnoreEmpty,
		FieldSkipNativeFiles, FieldVolumeUUID,
		FieldGoogleCreds, FieldGoogleCredsRef, FieldGoogleCredsJSON,
		FieldGoogleTokenFile, FieldGoogleTokenRef,
		FieldOneDriveClientID, FieldOneDriveTokenFile, FieldOneDriveTokenRef,
		FieldAuthRef,
	} {
		if !inTable[f] {
			t.Errorf("backup field %s is declared but absent from backupFieldSpecs; "+
				"BackupFields and backupFieldKeys derive from that table, so it would be "+
				"invisible to every caller building a FieldSet", f)
		}
		if backupFieldKeys[f] == "" {
			t.Errorf("backup field %s has no profiles-file key", f)
		}
	}
	if got, want := len(BackupFields()), len(inTable); got != want {
		t.Errorf("BackupFields returned %d fields, table has %d", got, want)
	}
}
