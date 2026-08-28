package main

import (
	"github.com/cloudstic/cli/pkg/config"
	"slices"

	"github.com/cloudstic/cli/pkg/profile"
)

// Profile-form domain helpers shared by the dashboard's forms backend
// (cmd_tui_forms_backend.go). They translate between a profile's stored source
// URI and the parts a form edits, and list the auth refs a source can use.

func profileAuthOptions(cfg *profile.Config, provider string) []string {
	options := []string{}
	for name, auth := range cfg.Auth {
		if auth.Provider == provider {
			options = append(options, name)
		}
	}
	slices.Sort(options)
	return options
}

func sourceTypeFromSource(raw string) string {
	parts, err := config.ParseSourceURI(raw)
	if err != nil {
		return ""
	}
	return parts.Scheme
}

func sourceValueFromSource(raw string) string {
	parts, err := config.ParseSourceURI(raw)
	if err != nil {
		return ""
	}
	switch parts.Scheme {
	case "local":
		return parts.Path
	case "sftp":
		target := ""
		if parts.User != "" {
			target += parts.User + "@"
		}
		target += parts.Host
		if parts.Port != "" {
			target += ":" + parts.Port
		}
		target += parts.Path
		return target
	case "gdrive", "gdrive-changes", "onedrive", "onedrive-changes":
		if parts.Host != "" {
			if parts.Path == "/" {
				return parts.Host
			}
			return parts.Host + parts.Path
		}
		if parts.Path == "/" {
			return "/"
		}
		return parts.Path
	default:
		return raw
	}
}
