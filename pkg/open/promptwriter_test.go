package open

import (
	"bytes"
	"testing"
)

// TestWithPromptWriter checks the option lands where openSource reads it.
//
// This package cannot assert the rest of the path — that the writer reaches
// both cloud sources — without constructing one, which authenticates. The
// per-source tests cover the far end; this covers the near end.
func TestWithPromptWriter(t *testing.T) {
	var buf bytes.Buffer
	o := newOptions([]Option{WithPromptWriter(&buf)})

	if o.promptWriter != &buf {
		t.Error("WithPromptWriter did not set promptWriter")
	}
	if o.logWriter != nil || o.debugWriter != nil {
		t.Error("WithPromptWriter set a diagnostic sink; it is a separate channel")
	}
}
