package store

import "testing"

// fmtBytes is unexported, so its unit test stays in the internal test
// package; the rest of the DebugStore tests are external (package
// store_test) so they can construct a real backend without an import cycle.
func TestFmtBytes(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{500, "500B"},
		{1024, "1.0KB"},
		{2048, "2.0KB"},
		{1 << 20, "1.0MB"},
		{2 << 20, "2.0MB"},
	}
	for _, tc := range tests {
		got := fmtBytes(tc.input)
		if got != tc.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
