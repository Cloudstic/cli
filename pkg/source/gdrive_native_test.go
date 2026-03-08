package source

import "testing"

func TestIsGoogleNativeMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		want     bool
	}{
		{"application/vnd.google-apps.document", true},
		{"application/vnd.google-apps.spreadsheet", true},
		{"application/vnd.google-apps.presentation", true},
		{"application/vnd.google-apps.drawing", true},
		{"application/vnd.google-apps.form", true},
		{"application/vnd.google-apps.script", true},
		{"application/vnd.google-apps.site", true},
		{"application/vnd.google-apps.jam", true},
		{"application/vnd.google-apps.map", true},
		{"application/vnd.google-apps.folder", false},
		{"application/pdf", false},
		{"image/png", false},
		{"application/vnd.google-apps.unknown_future_type", true},
		{"", false},
	}
	for _, tt := range tests {
		if got := isGoogleNativeMimeType(tt.mimeType); got != tt.want {
			t.Errorf("isGoogleNativeMimeType(%q) = %v, want %v", tt.mimeType, got, tt.want)
		}
	}
}

func TestNativeExportMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		want     string
	}{
		{"application/vnd.google-apps.document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"application/vnd.google-apps.spreadsheet", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"application/vnd.google-apps.presentation", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"application/vnd.google-apps.drawing", "image/svg+xml"},
		{"application/vnd.google-apps.script", "application/vnd.google-apps.script+json"},
		{"application/vnd.google-apps.form", "application/pdf"},
		{"application/vnd.google-apps.site", "text/plain"},
		{"application/vnd.google-apps.jam", "application/pdf"},
		{"application/vnd.google-apps.map", "application/pdf"},
		{"application/vnd.google-apps.unknown_future_type", "application/pdf"}, // fallback
	}
	for _, tt := range tests {
		if got := nativeExportMimeType(tt.mimeType); got != tt.want {
			t.Errorf("nativeExportMimeType(%q) = %q, want %q", tt.mimeType, got, tt.want)
		}
	}
}

func TestNativeExportExtension(t *testing.T) {
	tests := []struct {
		mimeType string
		want     string
	}{
		{"application/vnd.google-apps.document", ".docx"},
		{"application/vnd.google-apps.spreadsheet", ".xlsx"},
		{"application/vnd.google-apps.presentation", ".pptx"},
		{"application/vnd.google-apps.drawing", ".svg"},
		{"application/vnd.google-apps.script", ".json"},
		{"application/vnd.google-apps.form", ".pdf"},
		{"application/vnd.google-apps.site", ".txt"},
		{"application/vnd.google-apps.jam", ".pdf"},
		{"application/vnd.google-apps.map", ".pdf"},
		{"application/vnd.google-apps.unknown_future_type", ".pdf"}, // fallback
	}
	for _, tt := range tests {
		if got := nativeExportExtension(tt.mimeType); got != tt.want {
			t.Errorf("nativeExportExtension(%q) = %q, want %q", tt.mimeType, got, tt.want)
		}
	}
}
