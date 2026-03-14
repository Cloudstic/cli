//go:build darwin

package source

import "testing"

func TestExtractPlistValue(t *testing.T) {
	plistXML := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
<key>DeviceIdentifier</key>
<string>disk2s1</string>
<key>DiskUUID</key>
<string>a1b2c3d4-e5f6-7890-abcd-ef0123456789</string>
<key>VolumeName</key>
<string>MyDrive</string>
<key>Ejectable</key>
<true/>
</dict>
</plist>`

	tests := []struct {
		key  string
		want string
	}{
		{"DiskUUID", "A1B2C3D4-E5F6-7890-ABCD-EF0123456789"},
		{"VolumeName", "MYDRIVE"},
		{"DeviceIdentifier", "DISK2S1"},
		{"NonExistentKey", ""},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got := extractPlistValue([]byte(plistXML), tc.key)
			if got != tc.want {
				t.Errorf("extractPlistValue(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestExtractPlistValue_EmptyInput(t *testing.T) {
	if got := extractPlistValue([]byte(""), "DiskUUID"); got != "" {
		t.Errorf("expected empty for empty input, got %q", got)
	}
}

func TestExtractPlistValue_MalformedXML(t *testing.T) {
	if got := extractPlistValue([]byte("<broken"), "DiskUUID"); got != "" {
		t.Errorf("expected empty for malformed XML, got %q", got)
	}
}

func TestCStringToGo(t *testing.T) {
	tests := []struct {
		name string
		in   []int8
		want string
	}{
		{"normal", []int8{'/', 'd', 'e', 'v', '/', 'd', 'i', 's', 'k', '0', 0, 0, 0}, "/dev/disk0"},
		{"empty", []int8{0, 0, 0}, ""},
		{"no null", []int8{'a', 'b', 'c'}, "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cStringToGo(tc.in); got != tc.want {
				t.Errorf("cStringToGo = %q, want %q", got, tc.want)
			}
		})
	}
}
