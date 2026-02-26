package retry

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Default parameters for the retry loop.
const (
	DefaultMaxAttempts = 5
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultMaxDelay    = 30 * time.Second
)

// RetryableError wraps an error that should be retried and optionally carries
// a server-suggested wait duration (e.g. from a Retry-After header).
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Policy controls retry behaviour.
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultPolicy returns a sensible default retry policy.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts: DefaultMaxAttempts,
		BaseDelay:   DefaultBaseDelay,
		MaxDelay:    DefaultMaxDelay,
	}
}

// Do executes fn, retrying on RetryableError up to p.MaxAttempts times with
// exponential backoff and full jitter. If fn returns a non-retryable error or
// the context is cancelled, it returns immediately.
func Do(ctx context.Context, p Policy, fn func() error) error {
	var lastErr error
	for attempt := range p.MaxAttempts {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		var re *RetryableError
		if !errors.As(lastErr, &re) {
			return lastErr
		}

		if attempt == p.MaxAttempts-1 {
			return re.Err
		}

		delay := backoffDelay(attempt, p.BaseDelay, p.MaxDelay)
		if re.RetryAfter > 0 && re.RetryAfter > delay {
			delay = re.RetryAfter
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

// backoffDelay computes exponential backoff with full jitter:
// delay = random(0, min(maxDelay, baseDelay * 2^attempt))
func backoffDelay(attempt int, base, max time.Duration) time.Duration {
	exp := math.Pow(2, float64(attempt))
	calculated := time.Duration(float64(base) * exp)
	if calculated > max {
		calculated = max
	}
	if calculated <= 0 {
		return base
	}
	return time.Duration(rand.Int64N(int64(calculated)))
}

// ClassifyHTTPResponse returns a RetryableError for transient HTTP failures
// (429, 5xx) and a plain error for permanent ones. Returns nil on success (2xx).
// The retryAfter duration is parsed from the Retry-After header when present.
func ClassifyHTTPResponse(resp *http.Response, bodyForError []byte) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	err := &httpError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       string(bodyForError),
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return &RetryableError{Err: err, RetryAfter: ra}
	}

	return err
}

type httpError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *httpError) Error() string {
	if e.Body != "" {
		return e.Status + " " + e.Body
	}
	return e.Status
}

// parseRetryAfter parses the Retry-After header value as either seconds (integer)
// or an HTTP-date, returning the duration to wait.
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// IsRetryable returns true if the error is a RetryableError.
func IsRetryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}
