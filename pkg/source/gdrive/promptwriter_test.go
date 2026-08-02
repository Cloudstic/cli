package gdrive

import (
	"bytes"
	"testing"
)

// TestWithPromptWriter checks the option reaches the field the OAuth flow reads.
//
// The value of asserting the field rather than just calling the constructor is
// that promptWriter sits next to logWriter, and wiring it to that one instead
// would compile, pass a "does it set something" test, and silently hide the
// browser banner behind -debug.
func TestWithPromptWriter(t *testing.T) {
	var buf bytes.Buffer
	var o gDriveOptions

	WithPromptWriter(&buf)(&o)

	if o.promptWriter != &buf {
		t.Error("WithPromptWriter did not set promptWriter")
	}
	if o.logWriter != nil {
		t.Error("WithPromptWriter set logWriter; the browser banner is not diagnostics " +
			"and must not be hidden behind a debug flag")
	}
}
