package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/jedib0t/go-pretty/v6/table"
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
	uri, err := parseSourceURI(raw)
	if err != nil {
		return "unknown"
	}
	return uri.scheme
}

func storeScheme(raw string) string {
	uri, err := parseStoreURI(raw)
	if err != nil {
		return "unknown"
	}
	return uri.scheme
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

func profileHealth(cfg *cloudstic.ProfilesConfig, p cloudstic.BackupProfile) (status string, details []string) {
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

func authHealth(auth cloudstic.ProfileAuth) (string, []string) {
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

func storeHealth(s cloudstic.ProfileStore) (string, []string) {
	if s.URI == "" {
		return "error", []string{"missing uri"}
	}
	if _, err := parseStoreURI(s.URI); err != nil {
		return "error", []string{"invalid uri"}
	}
	return "ready", nil
}

func profilesUsingStore(cfg *cloudstic.ProfilesConfig, storeName string) []string {
	var refs []string
	for pName, p := range cfg.Profiles {
		if p.Store == storeName {
			refs = append(refs, pName)
		}
	}
	sort.Strings(refs)
	return refs
}

func profilesUsingAuth(cfg *cloudstic.ProfilesConfig, authName string) []string {
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

func (r *runner) renderStoreList(cfg *cloudstic.ProfilesConfig) {
	names := sortedKeys(cfg.Stores)
	renderSectionHeading(r.out, "Stores", len(names))
	if len(names) == 0 {
		renderMessageRow(r.out, "No stores configured.")
		return
	}
	t := newConfigTableWriter(r.out)
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

func (r *runner) renderAuthList(cfg *cloudstic.ProfilesConfig) {
	names := sortedKeys(cfg.Auth)
	renderSectionHeading(r.out, "Auth", len(names))
	if len(names) == 0 {
		renderMessageRow(r.out, "No auth entries configured.")
		return
	}
	t := newConfigTableWriter(r.out)
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

func (r *runner) renderProfileList(cfg *cloudstic.ProfilesConfig) {
	names := sortedKeys(cfg.Profiles)
	renderSectionHeading(r.out, "Profiles", len(names))
	if len(names) == 0 {
		renderMessageRow(r.out, "No profiles configured.")
		return
	}
	t := newConfigTableWriter(r.out)
	t.AppendHeader(table.Row{"Name", "Source", "Store", "Auth", "Tags", "Status"})
	for _, name := range names {
		p := cfg.Profiles[name]
		status, warnings := profileHealth(cfg, p)
		t.AppendRow(table.Row{
			name,
			p.Source,
			dashIfEmpty(p.Store),
			dashIfEmpty(p.AuthRef),
			shortList(p.Tags, 2),
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

func authTokenPath(auth cloudstic.ProfileAuth) string {
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

func (r *runner) renderStoreShow(cfg *cloudstic.ProfilesConfig, name string, s cloudstic.ProfileStore) {
	status, warnings := storeHealth(s)
	renderSectionHeading(r.out, fmt.Sprintf("Store %s", name), -1)
	renderKVTable(r.out, appendWarningRow([]table.Row{
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
		renderSectionHeading(r.out, "Connection", -1)
		renderKVTable(r.out, connection)
	}

	credentials := secretDisplayRows(s)
	if len(credentials) > 0 {
		renderSectionHeading(r.out, "Credential References", -1)
		renderKVTable(r.out, credentials)
	}

	usedBy := profilesUsingStore(cfg, name)
	renderSectionHeading(r.out, "Used By", len(usedBy))
	if len(usedBy) == 0 {
		renderMessageRow(r.out, "No profiles reference this store.")
		return
	}
	t := newConfigTableWriter(r.out)
	t.AppendHeader(table.Row{"Profile"})
	for _, ref := range usedBy {
		t.AppendRow(table.Row{ref})
	}
	t.Render()
}

func secretDisplayRows(s cloudstic.ProfileStore) []table.Row {
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
	appendRow("S3 Access Key Env", s.S3AccessKeyEnv, true)
	appendRow("S3 Secret Key Secret", s.S3SecretKeySecret, false)
	appendRow("S3 Secret Key Env", s.S3SecretKeyEnv, true)
	appendRow("S3 Profile Env", s.S3ProfileEnv, false)
	appendRow("SFTP Password Secret", s.StoreSFTPPasswordSecret, false)
	appendRow("SFTP Password Env", s.StoreSFTPPasswordEnv, true)
	appendRow("SFTP Key Secret", s.StoreSFTPKeySecret, false)
	appendRow("SFTP Key Env", s.StoreSFTPKeyEnv, true)
	appendRow("Password Secret", s.PasswordSecret, false)
	appendRow("Password Env", s.PasswordEnv, true)
	appendRow("Encryption Key Secret", s.EncryptionKeySecret, false)
	appendRow("Encryption Key Env", s.EncryptionKeyEnv, true)
	appendRow("Recovery Key Secret", s.RecoveryKeySecret, false)
	appendRow("Recovery Key Env", s.RecoveryKeyEnv, true)
	return rows
}

func (r *runner) renderAuthShow(cfg *cloudstic.ProfilesConfig, name string, auth cloudstic.ProfileAuth) {
	status, warnings := authHealth(auth)
	renderSectionHeading(r.out, fmt.Sprintf("Auth %s", name), -1)
	renderKVTable(r.out, appendWarningRow([]table.Row{
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
		renderSectionHeading(r.out, "Provider Details", -1)
		renderKVTable(r.out, providerRows)
	}

	usedBy := profilesUsingAuth(cfg, name)
	renderSectionHeading(r.out, "Used By", len(usedBy))
	if len(usedBy) == 0 {
		renderMessageRow(r.out, "No profiles reference this auth entry.")
		return
	}
	t := newConfigTableWriter(r.out)
	t.AppendHeader(table.Row{"Profile"})
	for _, ref := range usedBy {
		t.AppendRow(table.Row{ref})
	}
	t.Render()
}

func (r *runner) renderProfileShow(cfg *cloudstic.ProfilesConfig, name string, p cloudstic.BackupProfile) {
	status, warnings := profileHealth(cfg, p)
	renderSectionHeading(r.out, fmt.Sprintf("Profile %s", name), -1)
	renderKVTable(r.out, appendWarningRow([]table.Row{
		{"Source", p.Source},
		{"Source Type", sourceScheme(p.Source)},
		{"Provider", dashIfEmpty(profileProviderFromSource(p.Source))},
		{"Enabled", boolLabel(p.IsEnabled())},
		{"Status", statusLabel(status)},
	}, warnings))

	storeValue := "<missing>"
	storeAuthMode := "-"
	storeExtraRows := []table.Row{}
	if p.Store == "" {
		storeValue = "-"
	} else if s, ok := cfg.Stores[p.Store]; ok {
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
	if p.AuthRef != "" {
		if auth, ok := cfg.Auth[p.AuthRef]; ok {
			authProvider = auth.Provider
			authToken = authTokenPath(auth)
		} else {
			authProvider = "<missing>"
		}
	}
	renderSectionHeading(r.out, "Resolved References", -1)
	resolvedRows := []table.Row{
		{"Store Ref", dashIfEmpty(p.Store)},
		{"Store URI", storeValue},
		{"Store Auth Mode", storeAuthMode},
		{"Auth Ref", dashIfEmpty(p.AuthRef)},
		{"Auth Provider", authProvider},
		{"Auth Token", authToken},
	}
	resolvedRows = append(resolvedRows, storeExtraRows...)
	renderKVTable(r.out, resolvedRows)

	optionRows := []table.Row{
		{"Tags", joinOrDash(p.Tags)},
		{"Excludes", fmt.Sprintf("%d pattern(s)", len(p.Excludes))},
		{"Exclude File", dashIfEmpty(p.ExcludeFile)},
		{"Skip Native Files", boolLabel(p.SkipNativeFiles)},
	}
	if p.VolumeUUID != "" {
		optionRows = append(optionRows, table.Row{"Volume UUID", p.VolumeUUID})
	}
	if p.GoogleCreds != "" {
		optionRows = append(optionRows, table.Row{"Google Credentials File", p.GoogleCreds})
	}
	if p.GoogleCredsRef != "" {
		optionRows = append(optionRows, table.Row{"Google Credentials Ref", p.GoogleCredsRef})
	}
	if p.GoogleTokenFile != "" {
		optionRows = append(optionRows, table.Row{"Google Token File", p.GoogleTokenFile})
	}
	if p.GoogleTokenRef != "" {
		optionRows = append(optionRows, table.Row{"Google Token Ref", p.GoogleTokenRef})
	}
	if p.OneDriveClientID != "" {
		optionRows = append(optionRows, table.Row{"OneDrive Client ID", p.OneDriveClientID})
	}
	if p.OneDriveTokenFile != "" {
		optionRows = append(optionRows, table.Row{"OneDrive Token File", p.OneDriveTokenFile})
	}
	if p.OneDriveTokenRef != "" {
		optionRows = append(optionRows, table.Row{"OneDrive Token Ref", p.OneDriveTokenRef})
	}
	renderSectionHeading(r.out, "Options", -1)
	renderKVTable(r.out, optionRows)

	if len(p.Excludes) > 0 {
		renderSectionHeading(r.out, "Exclude Patterns", len(p.Excludes))
		t := newConfigTableWriter(r.out)
		t.AppendHeader(table.Row{"Pattern"})
		for _, pattern := range p.Excludes {
			t.AppendRow(table.Row{pattern})
		}
		t.Render()
	}
}
