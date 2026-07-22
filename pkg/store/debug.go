package store

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/cloudstic/cli/internal/logger"
)

// DebugStore wraps an ObjectStore and logs every operation with timing
// information. Output goes to the provided writer, which should be a
// ui.SafeLogWriter so lines coexist with progress bars.
type DebugStore struct {
	inner ObjectStore
	w     io.Writer
	calls atomic.Int64
}

func (s *DebugStore) Unwrap() ObjectStore { return s.inner }

func NewDebugStore(inner ObjectStore, w io.Writer) *DebugStore {
	return &DebugStore{inner: inner, w: w}
}

func (s *DebugStore) Put(ctx context.Context, key string, data []byte) error {
	start := time.Now()
	err := s.inner.Put(ctx, key, data)
	s.log("PUT", key, len(data), 0, time.Since(start), err)
	return err
}

func (s *DebugStore) Get(ctx context.Context, key string) ([]byte, error) {
	start := time.Now()
	data, err := s.inner.Get(ctx, key)
	s.log("GET", key, len(data), 0, time.Since(start), err)
	return data, err
}

// GetRange implements RangeGetter. DebugStore only logs, so it is safe to pass
// a ranged read straight through — and it has to, or wrapping a backend in
// --debug would silently turn every footer read into a full object transfer.
//
// Declaring this method makes DebugStore satisfy RangeGetter unconditionally,
// including over an inner store that cannot range, so the fallback is explicit
// rather than inherited.
func (s *DebugStore) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	start := time.Now()

	ranger, ok := s.inner.(RangeGetter)
	if !ok {
		data, err := s.inner.Get(ctx, key)
		if err != nil {
			s.log("GETRANGE", key, 0, 0, time.Since(start), err)
			return nil, err
		}
		if offset < 0 || length < 0 || offset+length > int64(len(data)) {
			err := fmt.Errorf("range %d+%d is outside %s (%d bytes)", offset, length, key, len(data))
			s.log("GETRANGE", key, 0, 0, time.Since(start), err)
			return nil, err
		}
		out := make([]byte, length)
		copy(out, data[offset:offset+length])
		s.log("GETRANGE", key, len(out), 0, time.Since(start), nil)
		return out, nil
	}

	data, err := ranger.GetRange(ctx, key, offset, length)
	s.log("GETRANGE", key, len(data), 0, time.Since(start), err)
	return data, err
}

func (s *DebugStore) Exists(ctx context.Context, key string) (bool, error) {
	start := time.Now()
	ok, err := s.inner.Exists(ctx, key)
	s.log("EXISTS", key, 0, 0, time.Since(start), err)
	return ok, err
}

func (s *DebugStore) Delete(ctx context.Context, key string) error {
	start := time.Now()
	err := s.inner.Delete(ctx, key)
	s.log("DELETE", key, 0, 0, time.Since(start), err)
	return err
}

func (s *DebugStore) List(ctx context.Context, prefix string) ([]string, error) {
	start := time.Now()
	keys, err := s.inner.List(ctx, prefix)
	s.log("LIST", prefix, 0, len(keys), time.Since(start), err)
	return keys, err
}

func (s *DebugStore) Size(ctx context.Context, key string) (int64, error) {
	start := time.Now()
	size, err := s.inner.Size(ctx, key)
	s.log("SIZE", key, int(size), 0, time.Since(start), err)
	return size, err
}

func (s *DebugStore) TotalSize(ctx context.Context) (int64, error) {
	start := time.Now()
	size, err := s.inner.TotalSize(ctx)
	s.log("TOTAL_SIZE", "", int(size), 0, time.Since(start), err)
	return size, err
}

func (s *DebugStore) Flush(ctx context.Context) error {
	start := time.Now()
	var err error
	if f, ok := s.inner.(interface{ Flush(context.Context) error }); ok {
		err = f.Flush(ctx)
	}
	s.log("FLUSH", "", 0, 0, time.Since(start), err)
	return err
}

var opColor = map[string]string{
	"GET":        logger.ColorGreen,
	"PUT":        logger.ColorYellow,
	"LIST":       logger.ColorCyan,
	"DELETE":     logger.ColorRed,
	"EXISTS":     logger.ColorDim,
	"SIZE":       logger.ColorDim,
	"TOTAL_SIZE": logger.ColorDim,
}

var debugLog = logger.New("store", logger.ColorYellow)

func debugf(format string, args ...any) {
	debugLog.Debugf(format, args...)
}

func (s *DebugStore) log(op, key string, bytes, count int, d time.Duration, err error) {
	n := s.calls.Add(1)
	ms := float64(d.Microseconds()) / 1000.0

	oc := opColor[op]

	detail := ""
	switch {
	case err != nil:
		detail = fmt.Sprintf(" %serr=%s%s", logger.ColorRed, err, logger.ColorReset)
	case count > 0:
		detail = fmt.Sprintf(" %s%d keys%s", logger.ColorDim, count, logger.ColorReset)
	case bytes > 0:
		detail = fmt.Sprintf(" %s%s%s", logger.ColorDim, fmtBytes(bytes), logger.ColorReset)
	}

	_, _ = fmt.Fprintf(s.w, "%s[store #%d]%s %s%-6s%s %-50s %s%7.1fms%s%s\n",
		logger.ColorDim, n, logger.ColorReset,
		oc, op, logger.ColorReset,
		key,
		logger.ColorDim, ms, logger.ColorReset,
		detail)
}

func fmtBytes(b int) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
