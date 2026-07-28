package main

import (
	"fmt"
	"github.com/cloudstic/cli/pkg/config"
	"io"
	"sort"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"

	"github.com/jedib0t/go-pretty/v6/table"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
)

func newConfigTableWriter(out io.Writer) table.Writer {
	t := table.NewWriter()
	t.SetOutputMirror(out)
	t.SetStyle(table.StyleRounded)
	return t
}

func renderSectionHeading(out io.Writer, title string, count int) {
	tw := ui.NewTermWriter(out)
	if count >= 0 {
		tw.HeadingSub(title, fmt.Sprintf("%d", count))
		return
	}
	tw.Heading(title)
}

func renderKVTable(out io.Writer, rows []table.Row) {
	t := newConfigTableWriter(out)
	t.AppendHeader(table.Row{"Field", "Value"})
	for _, row := range rows {
		t.AppendRow(row)
	}
	t.Render()
}

func renderMessageRow(out io.Writer, msg string) {
	_, _ = fmt.Fprintf(out, "%s%s%s\n", ui.Dim, msg, ui.Reset)
}

func statusLabel(kind string) string {
	switch kind {
	case "ready", "ok":
		return logger.ColorGreen + "OK" + logger.ColorReset
	case "warning", "disabled":
		return logger.ColorYellow + strings.ToUpper(kind) + logger.ColorReset
	default:
		return logger.ColorRed + strings.ToUpper(kind) + logger.ColorReset
	}
}

func sourceScheme(raw string) string {
	uri, err := config.ParseSourceURI(raw)
	if err != nil {
		return "unknown"
	}
	return uri.Scheme
}

func storeScheme(raw string) string {
	uri, err := config.ParseStoreURI(raw)
	if err != nil {
		return "unknown"
	}
	return uri.Scheme
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func shortList(values []string, limit int) string {
	if len(values) == 0 {
		return "-"
	}
	if len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:limit], ", ") + fmt.Sprintf(" +%d", len(values)-limit)
}

func boolLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func profileHealth(cfg *profile.Config, p profile.Profile) (status string, details []string) {
	status = "ready"
	provider := profileProviderFromSource(p.Source)
	if !p.IsEnabled() {
		status = "disabled"
	}
	if p.Store == "" {
		return "error", []string{"no store ref"}
	}
	if _, ok := cfg.Stores[p.Store]; !ok {
		return "error", []string{"missing store"}
	}
	if p.AuthRef != "" {
		auth, ok := cfg.Auth[p.AuthRef]
		if !ok {
			return "error", []string{"missing auth ref"}
		}
		if provider != "" && auth.Provider != "" && auth.Provider != provider {
			return "error", []string{"provider mismatch"}
		}
	}
	if provider != "" {
		if p.AuthRef == "" {
			return "error", []string{"missing auth"}
		}
	}
	return status, details
}

func authHealth(auth profile.Auth) (string, []string) {
	switch auth.Provider {
	case "google":
		if auth.GoogleTokenFile == "" && auth.GoogleTokenRef == "" {
			return "warning", []string{"missing token storage"}
		}
		return "ready", nil
	case "onedrive":
		if auth.OneDriveTokenFile == "" && auth.OneDriveTokenRef == "" {
			return "warning", []string{"missing token storage"}
		}
		return "ready", nil
	default:
		return "error", []string{"unknown provider"}
	}
}

func storeHealth(s profile.Store) (string, []string) {
	if s.URI == "" {
		return "error", []string{"missing uri"}
	}
	if _, err := config.ParseStoreURI(s.URI); err != nil {
		return "error", []string{"invalid uri"}
	}
	return "ready", nil
}

func profilesUsingStore(cfg *profile.Config, storeName string) []string {
	var refs []string
	for pName, p := range cfg.Profiles {
		if p.Store == storeName {
			refs = append(refs, pName)
		}
	}
	sort.Strings(refs)
	return refs
}

func profilesUsingAuth(cfg *profile.Config, authName string) []string {
	var refs []string
	for pName, p := range cfg.Profiles {
		if p.AuthRef == authName {
			refs = append(refs, pName)
		}
	}
	sort.Strings(refs)
	return refs
}

func appendWarningRow(rows []table.Row, warnings []string) []table.Row {
	if len(warnings) == 0 {
		return rows
	}
	return append(rows, table.Row{"Warnings", strings.Join(warnings, ", ")})
}

func renderStoreList(out io.Writer, cfg *profile.Config) {
	names := sortedKeys(cfg.Stores)
	renderSectionHeading(out, "Stores", len(names))
	if len(names) == 0 {
		renderMessageRow(out, "No stores configured.")
		return
	}
	t := newConfigTableWriter(out)
	t.AppendHeader(table.Row{"Name", "Type", "Target", "Auth", "Used By", "Status"})
	for _, name := range names {
		s := cfg.Stores[name]
		status, warnings := storeHealth(s)
		t.AppendRow(table.Row{
			name,
			storeScheme(s.URI),
			s.URI,
			profileStoreAuthMode(s),
			len(profilesUsingStore(cfg, name)),
			statusLabel(status) + warningSuffix(warnings),
		})
	}
	t.Render()
}

func renderAuthList(out io.Writer, cfg *profile.Config) {
	names := sortedKeys(cfg.Auth)
	renderSectionHeading(out, "Auth", len(names))
	if len(names) == 0 {
		renderMessageRow(out, "No auth entries configured.")
		return
	}
	t := newConfigTableWriter(out)
	t.AppendHeader(table.Row{"Name", "Provider", "Token", "Used By", "Status"})
	for _, name := range names {
		auth := cfg.Auth[name]
		status, warnings := authHealth(auth)
		t.AppendRow(table.Row{
			name,
			auth.Provider,
			authTokenPath(auth),
			len(profilesUsingAuth(cfg, name)),
			statusLabel(status) + warningSuffix(warnings),
		})
	}
	t.Render()
}

func renderProfileList(out io.Writer, cfg *profile.Config) {
	names := sortedKeys(cfg.Profiles)
	renderSectionHeading(out, "Profiles", len(names))
	if len(names) == 0 {
		renderMessageRow(out, "No profiles configured.")
		return
	}
	t := newConfigTableWriter(out)
	t.AppendHeader(table.Row{"Name", "Source", "Store", "Auth", "Tags", "Status"})
	for _, name := range names {
		profile := cfg.Profiles[name]
		status, warnings := profileHealth(cfg, profile)
		t.AppendRow(table.Row{
			name,
			profile.Source,
			dashIfEmpty(profile.Store),
			dashIfEmpty(profile.AuthRef),
			shortList(profile.Tags, 2),
			statusLabel(status) + warningSuffix(warnings),
		})
	}
	t.Render()
}

func warningSuffix(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return " " + logger.ColorYellow + "(" + strings.Join(warnings, ", ") + ")" + logger.ColorReset
}

func dashIfEmpty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func authTokenPath(auth profile.Auth) string {
	if auth.GoogleTokenRef != "" {
		return auth.GoogleTokenRef
	}
	if auth.GoogleTokenFile != "" {
		return auth.GoogleTokenFile
	}
	if auth.OneDriveTokenRef != "" {
		return auth.OneDriveTokenRef
	}
	if auth.OneDriveTokenFile != "" {
		return auth.OneDriveTokenFile
	}
	return "-"
}

func renderStoreShow(out io.Writer, cfg *profile.Config, name string, s profile.Store) {
	status, warnings := storeHealth(s)
	renderSectionHeading(out, fmt.Sprintf("Store %s", name), -1)
	renderKVTable(out, appendWarningRow([]table.Row{
		{"URI", s.URI},
		{"Type", storeScheme(s.URI)},
		{"Auth Mode", profileStoreAuthMode(s)},
		{"Status", statusLabel(status)},
	}, warnings))

	connection := []table.Row{}
	if s.S3Region != "" {
		connection = append(connection, table.Row{"S3 Region", s.S3Region})
	}
	if s.S3Profile != "" {
		connection = append(connection, table.Row{"S3 Profile", s.S3Profile})
	}
	if s.S3Endpoint != "" {
		connection = append(connection, table.Row{"S3 Endpoint", s.S3Endpoint})
	}
	if s.KMSKeyARN != "" {
		connection = append(connection, table.Row{"KMS Key ARN", s.KMSKeyARN})
	}
	if s.KMSRegion != "" {
		connection = append(connection, table.Row{"KMS Region", s.KMSRegion})
	}
	if s.KMSEndpoint != "" {
		connection = append(connection, table.Row{"KMS Endpoint", s.KMSEndpoint})
	}
	if len(connection) > 0 {
		renderSectionHeading(out, "Connection", -1)
		renderKVTable(out, connection)
	}

	credentials := secretDisplayRows(s)
	if len(credentials) > 0 {
		renderSectionHeading(out, "Credential References", -1)
		renderKVTable(out, credentials)
	}

	usedBy := profilesUsingStore(cfg, name)
	renderSectionHeading(out, "Used By", len(usedBy))
	if len(usedBy) == 0 {
		renderMessageRow(out, "No profiles reference this store.")
		return
	}
	t := newConfigTableWriter(out)
	t.AppendHeader(table.Row{"Profile"})
	for _, ref := range usedBy {
		t.AppendRow(table.Row{ref})
	}
	t.Render()
}

func secretDisplayRows(s profile.Store) []table.Row {
	var rows []table.Row
	appendRow := func(label, value string, deprecated bool) {
		if value == "" {
			return
		}
		if deprecated {
			label += " (deprecated)"
		}
		rows = append(rows, table.Row{label, value})
	}
	appendRow("S3 Access Key Secret", s.S3AccessKeySecret, false)
	appendRow("S3 Secret Key Secret", s.S3SecretKeySecret, false)
	appendRow("B2 Key ID Secret", s.B2KeyIDSecret, false)
	appendRow("B2 App Key Secret", s.B2AppKeySecret, false)
	appendRow("SFTP Password Secret", s.StoreSFTPPasswordSecret, false)
	appendRow("SFTP Key Secret", s.StoreSFTPKeySecret, false)
	appendRow("Password Secret", s.PasswordSecret, false)
	appendRow("Encryption Key Secret", s.EncryptionKeySecret, false)
	appendRow("Recovery Key Secret", s.RecoveryKeySecret, false)
	return rows
}

func renderAuthShow(out io.Writer, cfg *profile.Config, name string, auth profile.Auth) {
	status, warnings := authHealth(auth)
	renderSectionHeading(out, fmt.Sprintf("Auth %s", name), -1)
	renderKVTable(out, appendWarningRow([]table.Row{
		{"Provider", auth.Provider},
		{"Token Storage", authTokenPath(auth)},
		{"Status", statusLabel(status)},
	}, warnings))

	providerRows := []table.Row{}
	if auth.GoogleCreds != "" {
		providerRows = append(providerRows, table.Row{"Google Credentials File", auth.GoogleCreds})
	}
	if auth.GoogleCredsRef != "" {
		providerRows = append(providerRows, table.Row{"Google Credentials Ref", auth.GoogleCredsRef})
	}
	if auth.GoogleTokenFile != "" {
		providerRows = append(providerRows, table.Row{"Google Token File", auth.GoogleTokenFile})
	}
	if auth.GoogleTokenRef != "" {
		providerRows = append(providerRows, table.Row{"Google Token Ref", auth.GoogleTokenRef})
	}
	if auth.OneDriveClientID != "" {
		providerRows = append(providerRows, table.Row{"OneDrive Client ID", auth.OneDriveClientID})
	}
	if auth.OneDriveTokenFile != "" {
		providerRows = append(providerRows, table.Row{"OneDrive Token File", auth.OneDriveTokenFile})
	}
	if auth.OneDriveTokenRef != "" {
		providerRows = append(providerRows, table.Row{"OneDrive Token Ref", auth.OneDriveTokenRef})
	}
	if len(providerRows) > 0 {
		renderSectionHeading(out, "Provider Details", -1)
		renderKVTable(out, providerRows)
	}

	usedBy := profilesUsingAuth(cfg, name)
	renderSectionHeading(out, "Used By", len(usedBy))
	if len(usedBy) == 0 {
		renderMessageRow(out, "No profiles reference this auth entry.")
		return
	}
	t := newConfigTableWriter(out)
	t.AppendHeader(table.Row{"Profile"})
	for _, ref := range usedBy {
		t.AppendRow(table.Row{ref})
	}
	t.Render()
}

func renderProfileShow(out io.Writer, cfg *profile.Config, name string, profile profile.Profile) {
	status, warnings := profileHealth(cfg, profile)
	renderSectionHeading(out, fmt.Sprintf("Profile %s", name), -1)
	renderKVTable(out, appendWarningRow([]table.Row{
		{"Source", profile.Source},
		{"Source Type", sourceScheme(profile.Source)},
		{"Provider", dashIfEmpty(profileProviderFromSource(profile.Source))},
		{"Enabled", boolLabel(profile.IsEnabled())},
		{"Status", statusLabel(status)},
	}, warnings))

	storeValue := "<missing>"
	storeAuthMode := "-"
	storeExtraRows := []table.Row{}
	if profile.Store == "" {
		storeValue = "-"
	} else if s, ok := cfg.Stores[profile.Store]; ok {
		storeValue = s.URI
		storeAuthMode = profileStoreAuthMode(s)
		if s.S3Region != "" {
			storeExtraRows = append(storeExtraRows, table.Row{"Store S3 Region", s.S3Region})
		}
		if s.S3Profile != "" {
			storeExtraRows = append(storeExtraRows, table.Row{"Store S3 Profile", s.S3Profile})
		}
		if s.S3Endpoint != "" {
			storeExtraRows = append(storeExtraRows, table.Row{"Store S3 Endpoint", s.S3Endpoint})
		}
	}
	authProvider := "-"
	authToken := "-"
	if profile.AuthRef != "" {
		if auth, ok := cfg.Auth[profile.AuthRef]; ok {
			authProvider = auth.Provider
			authToken = authTokenPath(auth)
		} else {
			authProvider = "<missing>"
		}
	}
	renderSectionHeading(out, "Resolved References", -1)
	resolvedRows := []table.Row{
		{"Store Ref", dashIfEmpty(profile.Store)},
		{"Store URI", storeValue},
		{"Store Auth Mode", storeAuthMode},
		{"Auth Ref", dashIfEmpty(profile.AuthRef)},
		{"Auth Provider", authProvider},
		{"Auth Token", authToken},
	}
	resolvedRows = append(resolvedRows, storeExtraRows...)
	renderKVTable(out, resolvedRows)

	optionRows := []table.Row{
		{"Tags", joinOrDash(profile.Tags)},
		{"Excludes", fmt.Sprintf("%d pattern(s)", len(profile.Excludes))},
		{"Exclude File", dashIfEmpty(profile.ExcludeFile)},
		{"Ignore Empty Snapshot", boolLabel(profile.IgnoreEmpty)},
		{"Skip Native Files", boolLabel(profile.SkipNativeFiles)},
	}
	if profile.VolumeUUID != "" {
		optionRows = append(optionRows, table.Row{"Volume UUID", profile.VolumeUUID})
	}
	if profile.GoogleCreds != "" {
		optionRows = append(optionRows, table.Row{"Google Credentials File", profile.GoogleCreds})
	}
	if profile.GoogleCredsRef != "" {
		optionRows = append(optionRows, table.Row{"Google Credentials Ref", profile.GoogleCredsRef})
	}
	if profile.GoogleTokenFile != "" {
		optionRows = append(optionRows, table.Row{"Google Token File", profile.GoogleTokenFile})
	}
	if profile.GoogleTokenRef != "" {
		optionRows = append(optionRows, table.Row{"Google Token Ref", profile.GoogleTokenRef})
	}
	if profile.OneDriveClientID != "" {
		optionRows = append(optionRows, table.Row{"OneDrive Client ID", profile.OneDriveClientID})
	}
	if profile.OneDriveTokenFile != "" {
		optionRows = append(optionRows, table.Row{"OneDrive Token File", profile.OneDriveTokenFile})
	}
	if profile.OneDriveTokenRef != "" {
		optionRows = append(optionRows, table.Row{"OneDrive Token Ref", profile.OneDriveTokenRef})
	}
	renderSectionHeading(out, "Options", -1)
	renderKVTable(out, optionRows)

	if len(profile.Excludes) > 0 {
		renderSectionHeading(out, "Exclude Patterns", len(profile.Excludes))
		t := newConfigTableWriter(out)
		t.AppendHeader(table.Row{"Pattern"})
		for _, pattern := range profile.Excludes {
			t.AppendRow(table.Row{pattern})
		}
		t.Render()
	}
}
