package main

import "strings"

type tuiProfileSource struct {
	Type  string
	Value string
}

func newTUIProfileSource(raw string) tuiProfileSource {
	return tuiProfileSource{
		Type:  firstNonEmpty(sourceTypeFromSource(raw), "local"),
		Value: sourceValueFromSource(raw),
	}
}

func (s tuiProfileSource) Compose() string {
	value := strings.TrimSpace(s.Value)
	switch s.Type {
	case "local":
		return "local:" + value
	case "sftp":
		if value == "" {
			return ""
		}
		return "sftp://" + value
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		switch {
		case value == "", value == "/":
			return s.Type
		case strings.HasPrefix(value, "/"):
			return s.Type + ":" + value
		default:
			return s.Type + "://" + value
		}
	default:
		return value
	}
}

func (s tuiProfileSource) Provider() string {
	return profileProviderFromSource(s.Compose())
}

func (s tuiProfileSource) DetailLabel() string {
	switch s.Type {
	case "local":
		return "Path"
	case "sftp":
		return "Target"
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		return "Location"
	default:
		return "Source"
	}
}

func (s tuiProfileSource) DetailRequired() bool {
	switch s.Type {
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		return false
	default:
		return true
	}
}

func (s tuiProfileSource) Description() string {
	switch s.Type {
	case "local":
		return "Back up a local filesystem path."
	case "sftp":
		return "Back up files from an SFTP server."
	case "gdrive":
		return "Back up Google Drive with a full scan."
	case "gdrive-changes":
		return "Back up Google Drive incrementally via the Changes API."
	case "onedrive":
		return "Back up OneDrive with a full scan."
	case "onedrive-changes":
		return "Back up OneDrive incrementally via the delta API."
	default:
		return "Configure the source details below."
	}
}

func (s tuiProfileSource) ExampleText() string {
	switch s.Type {
	case "local":
		return "Example: /Users/me/Documents"
	case "sftp":
		return "Example: backup@host.example.com/data"
	case "gdrive", "gdrive-changes":
		return "Examples: /Team Folder   or   Shared Drive/Finance   (leave empty for the whole drive)"
	case "onedrive", "onedrive-changes":
		return "Examples: /Documents   or   Shared Library/Reports   (leave empty for the whole drive)"
	default:
		return ""
	}
}
