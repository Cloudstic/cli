package onedrive

import "testing"

func TestStripOneDriveRootPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/drive/root:", ""},
		{"/drive/root:/Documents", "Documents"},
		{"/drive/root:/Documents/Reports", "Documents/Reports"},
		{"/drive/root:/a/b/c", "a/b/c"},
		{"", ""},
	}
	for _, tc := range tests {
		got := stripOneDriveRootPrefix(tc.input)
		if got != tc.want {
			t.Errorf("stripOneDriveRootPrefix(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
