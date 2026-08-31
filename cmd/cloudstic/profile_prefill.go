package main

import "github.com/cloudstic/cli/pkg/profile"

func prefillProfileArgs(a *profileNewArgs, p profile.Profile) {
	if !a.flagProvided("source") && p.Source != "" {
		a.source = p.Source
	}
	if !a.flagProvided("store-ref") && p.Store != "" {
		a.storeRef = p.Store
	}
	if !a.flagProvided("auth-ref") && p.AuthRef != "" {
		a.authRef = p.AuthRef
	}
	if !a.flagProvided("exclude-file") && p.ExcludeFile != "" {
		a.excludeFile = p.ExcludeFile
	}
	if !a.flagProvided("skip-native-files") && p.SkipNativeFiles {
		a.skipNativeFiles = true
	}
	if !a.flagProvided("volume-uuid") && p.VolumeUUID != "" {
		a.volumeUUID = p.VolumeUUID
	}
	if !a.flagProvided("google-credentials") && p.GoogleCreds != "" {
		a.googleCreds = p.GoogleCreds
	}
	if !a.flagProvided("google-credentials-ref") && p.GoogleCredsRef != "" {
		a.googleCredsRef = p.GoogleCredsRef
	}
	if !a.flagProvided("google-credentials-json") && p.GoogleCredsJSON != "" {
		a.googleCredsJSON = p.GoogleCredsJSON
	}
	if !a.flagProvided("google-token-file") && p.GoogleTokenFile != "" {
		a.googleTokenFile = p.GoogleTokenFile
	}
	if !a.flagProvided("google-token-ref") && p.GoogleTokenRef != "" {
		a.googleTokenRef = p.GoogleTokenRef
	}
	if !a.flagProvided("onedrive-client-id") && p.OneDriveClientID != "" {
		a.onedriveClientID = p.OneDriveClientID
	}
	if !a.flagProvided("onedrive-token-file") && p.OneDriveTokenFile != "" {
		a.onedriveTokenFile = p.OneDriveTokenFile
	}
	if !a.flagProvided("onedrive-token-ref") && p.OneDriveTokenRef != "" {
		a.onedriveTokenRef = p.OneDriveTokenRef
	}
	if len(a.tags) == 0 && len(p.Tags) > 0 {
		a.tags = append(stringArrayFlags{}, p.Tags...)
	}
	if len(a.excludes) == 0 && len(p.Excludes) > 0 {
		a.excludes = append(stringArrayFlags{}, p.Excludes...)
	}
}
