package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestParseRetryAfter_DeltaSeconds verifies parsing of the numeric
// delta-seconds format per RFC 7231.
func TestParseRetryAfter_DeltaSeconds(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"1", 1 * time.Second},
		{"30", 30 * time.Second},
		{"60", 60 * time.Second},
		{"  45  ", 45 * time.Second}, // trimmed
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			got := parseRetryAfter(tt.header)
			if got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

// TestParseRetryAfter_CapsExcessiveDelays verifies that a server
// asking for an unreasonably long Retry-After is capped at
// maxRetryAfterDelay, preventing a misbehaving endpoint from pinning
// the scheduler indefinitely.
func TestParseRetryAfter_CapsExcessiveDelays(t *testing.T) {
	got := parseRetryAfter("86400") // 1 day
	if got != maxRetryAfterDelay {
		t.Errorf("expected excessive delay to be capped at %v, got %v", maxRetryAfterDelay, got)
	}

	// Exactly at the cap: allowed
	got = parseRetryAfter(strconv.Itoa(int(maxRetryAfterDelay / time.Second)))
	if got != maxRetryAfterDelay {
		t.Errorf("expected exact-cap value to pass through, got %v", got)
	}
}

// TestParseRetryAfter_HTTPDate verifies parsing of the HTTP-date
// format per RFC 7231 §7.1.3. The date format is RFC1123 (Go's
// time.RFC1123).
func TestParseRetryAfter_HTTPDate(t *testing.T) {
	// A date 10 seconds in the future
	future := time.Now().Add(10 * time.Second).UTC().Format(time.RFC1123)

	got := parseRetryAfter(future)
	// Allow some slack for test-execution time; the parsed delta
	// should be in [5s, 15s].
	if got < 5*time.Second || got > 15*time.Second {
		t.Errorf("expected ~10s from future HTTP-date, got %v", got)
	}
}

// TestParseRetryAfter_PastDate verifies that a Retry-After pointing
// to the past returns zero (not a negative duration). Clients should
// retry immediately in that case, which retryTransient handles via
// the fallback exponential backoff.
func TestParseRetryAfter_PastDate(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC1123)
	got := parseRetryAfter(past)
	if got != 0 {
		t.Errorf("expected 0 for past date, got %v", got)
	}
}

// TestParseRetryAfter_Empty verifies the empty string and
// unrecognized format cases return zero.
func TestParseRetryAfter_Empty(t *testing.T) {
	tests := []string{
		"",
		"   ",
		"not a number or date",
		"abc123",
	}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if got := parseRetryAfter(tt); got != 0 {
				t.Errorf("parseRetryAfter(%q) = %v, want 0", tt, got)
			}
		})
	}
}

// TestRetryAfterError_ErrorMessage verifies the wrapper's Error()
// method formats a useful message including the underlying error
// and the retry delay.
func TestRetryAfterError_ErrorMessage(t *testing.T) {
	inner := errors.New("webhook returned status 429")
	wrapped := &RetryAfterError{
		Underlying: inner,
		RetryAfter: 30 * time.Second,
	}
	msg := wrapped.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	// Must mention both the underlying cause and the delay
	if !contains(msg, "429") {
		t.Errorf("expected message to mention 429, got %q", msg)
	}
	if !contains(msg, "30s") {
		t.Errorf("expected message to mention 30s, got %q", msg)
	}
}

// TestRetryAfterError_Unwrap verifies that errors.Is / errors.As
// continue to work through the wrapper, so callers that don't care
// about Retry-After semantics see the underlying error unchanged.
func TestRetryAfterError_Unwrap(t *testing.T) {
	inner := errors.New("webhook returned status 429")
	wrapped := &RetryAfterError{Underlying: inner, RetryAfter: 5 * time.Second}

	if !errors.Is(wrapped, inner) {
		t.Error("errors.Is must see through the RetryAfterError wrapper")
	}

	var got *RetryAfterError
	if !errors.As(wrapped, &got) {
		t.Error("errors.As must recognize RetryAfterError")
	}
	if got.RetryAfter != 5*time.Second {
		t.Errorf("expected RetryAfter=5s after errors.As, got %v", got.RetryAfter)
	}
}

// TestIsTransientHTTPError_429IsTransient verifies the critical
// classification change from Issue #66: 429 must now be treated as
// transient so retryTransient actually retries it instead of bailing
// as if it were a permanent 4xx.
func TestIsTransientHTTPError_429IsTransient(t *testing.T) {
	err := errors.New("webhook returned status 429")
	if !isTransientHTTPError(err) {
		t.Error("429 must be classified as transient")
	}
}

// TestIsTransientHTTPError_RetryAfterErrorIsTransient verifies that
// a RetryAfterError wrapper is always classified as transient, even
// if the underlying error or its message would be ambiguous.
func TestIsTransientHTTPError_RetryAfterErrorIsTransient(t *testing.T) {
	err := &RetryAfterError{
		Underlying: errors.New("some underlying error"),
		RetryAfter: 5 * time.Second,
	}
	if !isTransientHTTPError(err) {
		t.Error("RetryAfterError wrapper must be classified as transient")
	}
}

// TestIsTransientHTTPError_OtherFourxxStillPermanent verifies we
// didn't accidentally flip all 4xx to transient — 404 and 403 must
// still be permanent.
func TestIsTransientHTTPError_OtherFourxxStillPermanent(t *testing.T) {
	tests := []string{
		"webhook returned status 404",
		"webhook returned status 403",
		"webhook returned status 401",
		"webhook returned status 400",
	}
	for _, msg := range tests {
		t.Run(msg, func(t *testing.T) {
			if isTransientHTTPError(errors.New(msg)) {
				t.Errorf("%q must stay permanent, not be classified as transient", msg)
			}
		})
	}
}

// TestRetryTransient_HonorsRetryAfterDelay verifies that when fn
// returns a RetryAfterError with a RetryAfter field, the retry loop
// uses that delay instead of the default exponential backoff.
func TestRetryTransient_HonorsRetryAfterDelay(t *testing.T) {
	var attempts int32
	serverDelay := 100 * time.Millisecond

	// Use a HUGE default backoff (10s) so we can tell whether the
	// server-specified Retry-After delay was used. If the test
	// completes quickly, Retry-After won.
	start := time.Now()
	err := retryTransient(context.Background(), 2, 10*time.Second, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return &RetryAfterError{
			Underlying: errors.New("webhook returned status 429"),
			RetryAfter: serverDelay,
		}
	}, isTransientHTTPError)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected non-nil error after retries exhausted")
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
	}
	// Must have waited roughly the server-specified delay (100ms)
	// and definitely NOT the default 10s backoff.
	if elapsed >= 5*time.Second {
		t.Errorf("retry used default backoff instead of Retry-After; elapsed=%v", elapsed)
	}
	if elapsed < serverDelay/2 {
		t.Errorf("retry completed too quickly, Retry-After may not have been honored; elapsed=%v", elapsed)
	}
}

// TestRetryTransient_FallbackBackoffWhenRetryAfterZero verifies that
// a RetryAfterError with RetryAfter=0 (e.g., the response had no
// Retry-After header) still uses the default exponential backoff
// instead of retrying immediately.
func TestRetryTransient_FallbackBackoffWhenRetryAfterZero(t *testing.T) {
	var attempts int32
	start := time.Now()

	_ = retryTransient(context.Background(), 2, 50*time.Millisecond, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return &RetryAfterError{
			Underlying: errors.New("webhook returned status 429"),
			RetryAfter: 0, // no Retry-After header present
		}
	}, isTransientHTTPError)
	elapsed := time.Since(start)

	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
	}
	// Must have slept at least close to the initialBackoff value,
	// not retried instantly.
	if elapsed < 30*time.Millisecond {
		t.Errorf("zero Retry-After should fall back to initialBackoff; elapsed=%v", elapsed)
	}
}

// TestSendWebhook_Returns429WithRetryAfter is the end-to-end
// integration test. A real httptest server returns 429 with a
// Retry-After header for the first attempt and 200 for the second.
// The webhook send must succeed overall, and the elapsed time must
// reflect the server-supplied delay.
func TestSendWebhook_Returns429WithRetryAfter(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// HUGE initial backoff so any non-zero elapsed time must be due
	// to the server's Retry-After header, not the default backoff.
	cfg := &Config{
		WebhookEnabled: true,
		WebhookURL:     server.URL,
		CooldownPeriod: time.Hour,
		InitialBackoff: 30 * time.Second, // default would be used otherwise
	}
	n := New(cfg)

	alert := Alert{
		Type: AlertTypeError, SourceID: "src1", SourceName: "Test",
		Message: "test", Timestamp: time.Now(),
	}

	start := time.Now()
	err := n.sendWebhook(context.Background(), alert)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("expected eventual success after 429 + retry, got %v", err)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
	}
	// Elapsed should be roughly 1s (Retry-After), definitely not 30s.
	if elapsed > 10*time.Second {
		t.Errorf("retry used 30s initialBackoff instead of 1s Retry-After; elapsed=%v", elapsed)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("retry completed too quickly; elapsed=%v", elapsed)
	}
}
