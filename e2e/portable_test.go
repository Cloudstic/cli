package e2e

import (
	"runtime"
	"testing"
)

// portableDriveSources returns factory functions for portable drive test
// sources. On macOS and Linux, this creates a real GPT-formatted disk
// (RAM disk or loopback device). On other platforms, it returns nil.
func portableDriveSources() []func(t *testing.T) TestSource {
	switch runtime.GOOS {
	case "darwin", "linux":
		return []func(t *testing.T) TestSource{
			func(t *testing.T) TestSource { return newPortableDriveSource(t) },
		}
	default:
		return nil
	}
}
