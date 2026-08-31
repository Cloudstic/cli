package store_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/cloudstic/cli/pkg/store"
)

// The property the helper exists for: a length is a number read off a store,
// so it may not be believed to the extent of allocating it. Reserving
// math.MaxInt64 panics in make() before a byte is read, which is how a
// malformed repository turned into a crash rather than an error.
func TestReadExactlyDoesNotAllocateALengthItWasMerelyAsked(t *testing.T) {
	body := strings.Repeat("x", 64)
	_, err := store.ReadExactly(strings.NewReader(body), math.MaxInt64)
	if err == nil {
		t.Fatal("a length no reader could satisfy was accepted")
	}
	if !strings.Contains(err.Error(), "short read") {
		t.Fatalf("err = %v, want a short read", err)
	}
}

func TestReadExactlyRejectsANegativeLength(t *testing.T) {
	if _, err := store.ReadExactly(strings.NewReader("abc"), -1); err == nil {
		t.Fatal("a negative length was accepted")
	}
}

// A short read is an error, not a truncated slice: the caller asked for bytes
// that are not there, and returning what arrived would be a silently wrong
// answer.
func TestReadExactlyTreatsAShortReadAsAnError(t *testing.T) {
	if _, err := store.ReadExactly(strings.NewReader("abc"), 10); err == nil {
		t.Fatal("a short read was accepted")
	}
}

func TestReadExactlyReturnsExactlyWhatWasAsked(t *testing.T) {
	src := bytes.Repeat([]byte("abcd"), 100)
	got, err := store.ReadExactly(bytes.NewReader(src), 42)
	if err != nil {
		t.Fatalf("ReadExactly: %v", err)
	}
	if len(got) != 42 || !bytes.Equal(got, src[:42]) {
		t.Fatalf("got %d bytes %q, want the first 42", len(got), got)
	}
}

func TestReadExactlyZeroLengthIsEmptyNotAnError(t *testing.T) {
	got, err := store.ReadExactly(strings.NewReader("abc"), 0)
	if err != nil {
		t.Fatalf("zero length: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %q, want nothing", got)
	}
}

// A reader that fails partway must surface its own error rather than a short
// read, so a network fault is not reported as a malformed range.
func TestReadExactlySurfacesTheReadersError(t *testing.T) {
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("abc"), errReader{boom})
	if _, err := store.ReadExactly(r, 10); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader's own error", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
