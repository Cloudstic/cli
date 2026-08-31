package engine

import (
	"context"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// A budget set from the environment must actually change how blobs seal, or
// the sweep it exists for measures nothing.
func TestBlobBudgetOverrideChangesSealing(t *testing.T) {
	count := func(budget string) int {
		t.Setenv(envBlobBudget, budget)
		ctx := context.Background()
		dest := NewMockStore()
		w := newBlobWriter(dest, nil)
		for i := range 64 {
			body := append([]byte("body"), byte(i>>8), byte(i))
			body = append(body, make([]byte, 32*1024)...)
			body[len(body)-1] = byte(i)
			if _, err := w.Add(ctx, core.ComputeHash(body), body); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		return dest.CountPrefix("blob/")
	}

	small, large := count("262144"), count("8388608")
	if small <= large {
		t.Errorf("a 256 KB budget produced %d blobs and an 8 MB budget %d; "+
			"the smaller budget must seal more often", small, large)
	}
}
