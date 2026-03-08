package retry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDo_Success(t *testing.T) {
	p := Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestDo_NonRetryableError(t *testing.T) {
	p := Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	sentinel := errors.New("permanent")
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", calls)
	}
}

func TestDo_RetryableErrorExhausted(t *testing.T) {
	p := Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	inner := errors.New("transient")
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		return &RetryableError{Err: inner}
	})
	if !errors.Is(err, inner) {
		t.Fatalf("expected inner error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_RetryableErrorThenSuccess(t *testing.T) {
	p := Policy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		if calls < 3 {
			return &RetryableError{Err: errors.New("transient")}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ContextCancelled(t *testing.T) {
	p := Policy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Do(ctx, p, func() error {
		calls++
		cancel()
		return &RetryableError{Err: errors.New("transient")}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDo_RetryAfter(t *testing.T) {
	p := Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	calls := 0
	err := Do(context.Background(), p, func() error {
		calls++
		if calls < 2 {
			return &RetryableError{Err: errors.New("transient"), RetryAfter: time.Millisecond}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("MaxAttempts: got %d, want %d", p.MaxAttempts, DefaultMaxAttempts)
	}
	if p.BaseDelay != DefaultBaseDelay {
		t.Errorf("BaseDelay: got %v, want %v", p.BaseDelay, DefaultBaseDelay)
	}
	if p.MaxDelay != DefaultMaxDelay {
		t.Errorf("MaxDelay: got %v, want %v", p.MaxDelay, DefaultMaxDelay)
	}
}

func TestRetryableError(t *testing.T) {
	inner := errors.New("inner")
	re := &RetryableError{Err: inner}
	if re.Error() != inner.Error() {
		t.Errorf("Error() mismatch")
	}
	if !errors.Is(re, inner) {
		t.Errorf("errors.Is should unwrap to inner")
	}
}

func TestIsRetryable(t *testing.T) {
	inner := errors.New("x")
	if IsRetryable(inner) {
		t.Error("plain error should not be retryable")
	}
	if !IsRetryable(&RetryableError{Err: inner}) {
		t.Error("RetryableError should be retryable")
	}
}

func TestClassifyHTTPResponse_2xx(t *testing.T) {
	resp := &http.Response{StatusCode: 200, Status: "200 OK"}
	if err := ClassifyHTTPResponse(resp, nil); err != nil {
		t.Errorf("expected nil for 2xx, got %v", err)
	}
}

func TestClassifyHTTPResponse_429(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": []string{"1"}},
	}
	err := ClassifyHTTPResponse(resp, []byte("slow down"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsRetryable(err) {
		t.Error("429 should be retryable")
	}
}

func TestClassifyHTTPResponse_5xx(t *testing.T) {
	resp := &http.Response{
		StatusCode: 503,
		Status:     "503 Service Unavailable",
		Header:     http.Header{},
	}
	err := ClassifyHTTPResponse(resp, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsRetryable(err) {
		t.Error("5xx should be retryable")
	}
}

func TestClassifyHTTPResponse_4xx(t *testing.T) {
	resp := &http.Response{
		StatusCode: 404,
		Status:     "404 Not Found",
		Header:     http.Header{},
	}
	err := ClassifyHTTPResponse(resp, []byte("not found"))
	if err == nil {
		t.Fatal("expected error")
	}
	if IsRetryable(err) {
		t.Error("4xx should not be retryable")
	}
}

func TestClassifyHTTPResponse_WithBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 404,
		Status:     "404 Not Found",
		Header:     http.Header{},
	}
	err := ClassifyHTTPResponse(resp, []byte("file not found"))
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "404 Not Found file not found" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestClassifyHTTPResponse_NoBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 400,
		Status:     "400 Bad Request",
		Header:     http.Header{},
	}
	err := ClassifyHTTPResponse(resp, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "400 Bad Request" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestParseRetryAfter_Empty(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("expected 0, got %v", d)
	}
}

func TestParseRetryAfter_Seconds(t *testing.T) {
	d := parseRetryAfter("5")
	if d != 5*time.Second {
		t.Errorf("expected 5s, got %v", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	if d <= 0 {
		t.Errorf("expected positive duration for future date, got %v", d)
	}
}

func TestParseRetryAfter_PastDate(t *testing.T) {
	past := time.Now().Add(-10 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(past)
	if d != 0 {
		t.Errorf("expected 0 for past date, got %v", d)
	}
}

func TestParseRetryAfter_Invalid(t *testing.T) {
	d := parseRetryAfter("not-a-date")
	if d != 0 {
		t.Errorf("expected 0 for invalid value, got %v", d)
	}
}

func TestBackoffDelay_Bounds(t *testing.T) {
	base := 100 * time.Millisecond
	max := 1 * time.Second
	for attempt := range 10 {
		d := backoffDelay(attempt, base, max)
		if d < 0 {
			t.Errorf("attempt %d: negative delay %v", attempt, d)
		}
		if d > max {
			t.Errorf("attempt %d: delay %v exceeds max %v", attempt, d, max)
		}
	}
}

// Ensure ClassifyHTTPResponse works end-to-end with an httptest server.
func TestClassifyHTTPResponse_Integration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if err := ClassifyHTTPResponse(resp, nil); err != nil {
		t.Errorf("expected nil for 200, got %v", err)
	}
}
