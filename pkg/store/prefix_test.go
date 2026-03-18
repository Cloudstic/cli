package store

import "testing"

func TestNormalizeKeyPrefix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"prod", "prod/"},
		{"prod/", "prod/"},
		{"/prod", "prod/"},
		{"/nested/path/", "nested/path/"},
	}

	for _, tc := range tests {
		if got := normalizeKeyPrefix(tc.in); got != tc.want {
			t.Fatalf("normalizeKeyPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
