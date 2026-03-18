package store

import "testing"

func TestWithPrefix_NormalizesPrefix(t *testing.T) {
	var opts b2Options
	WithPrefix("nested/prefix")(&opts)
	if opts.prefix != "nested/prefix/" {
		t.Fatalf("prefix = %q", opts.prefix)
	}
}
