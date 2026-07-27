package main

import (
	"slices"

	cloudstic "github.com/cloudstic/cli"
)

// Profile-form domain helpers shared by the dashboard's forms backend
// (cmd_tui_forms_backend.go). They translate between a profile's stored source
// URI and the parts a form edits, and list the auth refs a source can use.

func profileAuthOptions(cfg *cloudstic.ProfilesConfig, provider string) []string {
	options := []string{}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	slices.Sort(options)
	return options
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sourceTypeFromSource(raw string) string {
	parts, err := parseSourceURI(raw)
	if err != nil {
		return ""
	}
	return parts.scheme
}

func sourceValueFromSource(raw string) string {
	parts, err := parseSourceURI(raw)
	if err != nil {
		return ""
	}
	switch parts.scheme {
	case "local":
		return parts.path
	case "sftp":
		target := ""
		if parts.user != "" {
			target += parts.user + "@"
		}
		target += parts.host
		if parts.port != "" {
			target += ":" + parts.port
		}
		target += parts.path
		return target
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		if parts.host != "" {
			if parts.path == "/" {
				return parts.host
			}
			return parts.host + parts.path
		}
		if parts.path == "/" {
			return "/"
		}
		return parts.path
	default:
		return raw
	}
}
