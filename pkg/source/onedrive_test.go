package source

import "testing"

func TestOneDriveInfo(t *testing.T) {
	s := &OneDriveSource{account: "user@outlook.com"}
	info := s.Info()

	if info.Type != "onedrive" {
		t.Errorf("Type = %q, want onedrive", info.Type)
	}
	if info.Account != "user@outlook.com" {
		t.Errorf("Account = %q, want user@outlook.com", info.Account)
	}
	if info.Path != "/" {
		t.Errorf("Path = %q, want /", info.Path)
	}
	if info.VolumeUUID != "" {
		t.Errorf("VolumeUUID = %q, want empty", info.VolumeUUID)
	}
	if info.VolumeLabel != "My Drive" {
		t.Errorf("VolumeLabel = %q, want My Drive", info.VolumeLabel)
	}
}

func TestOneDriveChangesInfo_Type(t *testing.T) {
	s := &OneDriveChangeSource{
		OneDriveSource: OneDriveSource{account: "user@outlook.com"},
	}
	info := s.Info()

	if info.Type != "onedrive-changes" {
		t.Errorf("Type = %q, want onedrive-changes", info.Type)
	}
	if info.VolumeLabel != "My Drive" {
		t.Errorf("VolumeLabel = %q, want My Drive", info.VolumeLabel)
	}
	if info.Path != "/" {
		t.Errorf("Path = %q, want /", info.Path)
	}
}
