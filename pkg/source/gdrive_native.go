package source

import "strings"

const googleAppsPrefix = "application/vnd.google-apps."

// nativeExportMap maps Google native MIME types to their preferred export
// format (MIME type and file extension). Types not in the map fall back to PDF.
var nativeExportMap = map[string]struct {
	exportMIME string
	ext        string
}{
	"application/vnd.google-apps.document":     {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"application/vnd.google-apps.spreadsheet":  {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"application/vnd.google-apps.presentation": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
	"application/vnd.google-apps.drawing":      {"image/svg+xml", ".svg"},
	"application/vnd.google-apps.script":       {"application/vnd.google-apps.script+json", ".json"},
	"application/vnd.google-apps.form":         {"application/pdf", ".pdf"},
	"application/vnd.google-apps.site":         {"text/plain", ".txt"},
	"application/vnd.google-apps.jam":          {"application/pdf", ".pdf"},
	"application/vnd.google-apps.map":          {"application/pdf", ".pdf"},
}

// isGoogleNativeMimeType returns true if the MIME type is a Google-native
// format (Docs, Sheets, Slides, etc.) that cannot be downloaded directly and
// must be exported instead. Folders are excluded.
func isGoogleNativeMimeType(mimeType string) bool {
	return strings.HasPrefix(mimeType, googleAppsPrefix) &&
		mimeType != "application/vnd.google-apps.folder"
}

// nativeExportMimeType returns the MIME type to use when exporting a Google
// native file. Falls back to PDF for unknown native types.
func nativeExportMimeType(mimeType string) string {
	if e, ok := nativeExportMap[mimeType]; ok {
		return e.exportMIME
	}
	return "application/pdf"
}

// nativeExportExtension returns the file extension (including the leading dot)
// to append to native file names after export. Falls back to ".pdf".
func nativeExportExtension(mimeType string) string {
	if e, ok := nativeExportMap[mimeType]; ok {
		return e.ext
	}
	return ".pdf"
}
