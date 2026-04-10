package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestRetryTransient_ReturnsNilOnFirstSuccess verifies the happy path:
// a function that succeeds on its first attempt returns nil without
// the retry loop ever sleeping or running a second attempt.
func TestRetryTransient_ReturnsNilOnFirstSuccess(t *testing.T) {
	var attempts int32
	err := retryTransient(context.Background(), 3, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return nil
	}, func(error) bool { return true })
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected 1 attempt, got %d", got)
	}
}

// TestRetryTransient_StopsImmediatelyOnPermanentError verifies that a
// permanent error (as classified by the isTransient callback) does not
// trigger any retries. This prevents wasted work on deterministic
// failures like invalid URLs or 4xx responses.
func TestRetryTransient_StopsImmediatelyOnPermanentError(t *testing.T) {
	var attempts int32
	permanentErr := errors.New("permanent failure")
	err := retryTransient(context.Background(), 5, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return permanentErr
	}, func(error) bool { return false })
	if !errors.Is(err, permanentErr) {
		t.Errorf("expected permanentErr, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("expected 1 attempt (no retries on permanent error), got %d", got)
	}
}

// TestRetryTransient_RetriesUpToMaxOnTransientError verifies that a
// transient error is retried up to maxAttempts times, and the last
// error is returned if all attempts fail.
func TestRetryTransient_RetriesUpToMaxOnTransientError(t *testing.T) {
	var attempts int32
	transientErr := errors.New("transient failure")
	err := retryTransient(context.Background(), 3, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return transientErr
	}, func(error) bool { return true })
	if !errors.Is(err, transientErr) {
		t.Errorf("expected transientErr, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

// TestRetryTransient_SuccessAfterTransientFailures verifies the most
// valuable case: a function that fails transiently on the first N-1
// attempts and succeeds on the last one returns nil and reports the
// correct attempt count.
func TestRetryTransient_SuccessAfterTransientFailures(t *testing.T) {
	var attempts int32
	err := retryTransient(context.Background(), 3, func(_ context.Context) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errors.New("still flaky")
		}
		return nil
	}, func(error) bool { return true })
	if err != nil {
		t.Errorf("expected nil on eventual success, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", got)
	}
}

// TestRetryTransient_RespectsContextCancellation verifies that context
// cancellation during the backoff sleep returns the last error
// immediately rather than waiting out the full backoff. This is
// critical for tests and for graceful shutdown: a broken endpoint
// shouldn't keep the daemon alive during teardown.
func TestRetryTransient_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var attempts int32

	// Cancel the context after the first attempt starts.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := retryTransient(ctx, 5, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("always fails")
	}, func(error) bool { return true })
	elapsed := time.Since(start)

	// The first attempt runs immediately, then we sleep for
	// defaultInitialBackoff (500ms). Cancellation after 50ms should
	// cut the sleep short, so the total should be well under the
	// combined backoff time of ~1.5s for 3 attempts.
	if elapsed > time.Second {
		t.Errorf("expected early exit on cancel, took %v (>1s)", elapsed)
	}
	// We should have made at most 2 attempts (the first one + one
	// cancelled during backoff).
	if got := atomic.LoadInt32(&attempts); got > 2 {
		t.Errorf("expected at most 2 attempts before cancel, got %d", got)
	}
	if err == nil {
		t.Error("expected non-nil error from cancelled retry")
	}
}

// TestIsTransientHTTPError verifies the classifier used by sendWebhook
// and sendWebhookToURL. The classification contract is fragile
// (string-based on internally-formatted messages) so these tests lock
// in the expected behavior.
func TestIsTransientHTTPError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error is not transient",
			err:  nil,
			want: false,
		},
		{
			name: "invalid webhook URL is permanent",
			err:  fmt.Errorf("invalid webhook URL: localhost not allowed"),
			want: false,
		},
		{
			name: "marshal payload is permanent",
			err:  fmt.Errorf("marshal payload: unsupported type"),
			want: false,
		},
		{
			name: "create request is permanent",
			err:  fmt.Errorf("create request: invalid URL"),
			want: false,
		},
		{
			name: "404 response is permanent",
			err:  fmt.Errorf("webhook returned status 404"),
			want: false,
		},
		{
			name: "403 response is permanent",
			err:  fmt.Errorf("webhook returned status 403"),
			want: false,
		},
		{
			name: "500 response is transient",
			err:  fmt.Errorf("webhook returned status 500"),
			want: true,
		},
		{
			name: "503 response is transient",
			err:  fmt.Errorf("webhook returned status 503"),
			want: true,
		},
		{
			name: "network error is transient",
			err:  fmt.Errorf("send request: dial tcp: i/o timeout"),
			want: true,
		},
		{
			name: "DNS error is transient",
			err:  fmt.Errorf("send request: no such host"),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientHTTPError(tt.err)
			if got != tt.want {
				t.Errorf("isTransientHTTPError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestSendWebhook_RetriesOn5xxAndEventuallySucceeds verifies the full
// integration from sendWebhook through retryTransient against a real
// httptest server that fails the first two attempts with 500 and
// succeeds on the third. If retry is wired correctly, the third
// attempt's 200 turns the overall send into a success.
func TestSendWebhook_RetriesOn5xxAndEventuallySucceeds(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&requestCount, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := New(&Config{
		WebhookEnabled: true,
		WebhookURL:     server.URL,
		CooldownPeriod: time.Hour,
	})

	alert := Alert{
		Type:       AlertTypeError,
		SourceID:   "src1",
		SourceName: "Test Source",
		Message:    "test message",
		Details:    "test details",
		Timestamp:  time.Now(),
	}

	err := n.sendWebhook(context.Background(), alert)
	if err != nil {
		t.Errorf("expected successful send after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&requestCount); got != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), server saw %d", got)
	}
}

// TestSendWebhook_StopsImmediatelyOn4xx verifies that a 404 response
// does NOT trigger any retries — it's a permanent failure from the
// classifier's point of view.
func TestSendWebhook_StopsImmediatelyOn4xx(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	n := New(&Config{
		WebhookEnabled: true,
		WebhookURL:     server.URL,
		CooldownPeriod: time.Hour,
	})

	alert := Alert{
		Type:       AlertTypeError,
		SourceID:   "src1",
		SourceName: "Test Source",
		Message:    "test message",
		Timestamp:  time.Now(),
	}

	err := n.sendWebhook(context.Background(), alert)
	if err == nil {
		t.Error("expected error on 404, got nil")
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Errorf("expected exactly 1 attempt on permanent 4xx, server saw %d", got)
	}
}

// TestSendWebhook_RetriesAllAttemptsOn5xx verifies the worst case: all
// attempts fail with 500, the last error is returned, and the server
// saw exactly defaultMaxSendAttempts requests.
func TestSendWebhook_RetriesAllAttemptsOn5xx(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	n := New(&Config{
		WebhookEnabled: true,
		WebhookURL:     server.URL,
		CooldownPeriod: time.Hour,
	})

	alert := Alert{
		Type:       AlertTypeError,
		SourceID:   "src1",
		SourceName: "Test Source",
		Message:    "test message",
		Timestamp:  time.Now(),
	}

	err := n.sendWebhook(context.Background(), alert)
	if err == nil {
		t.Error("expected error after all retries exhausted, got nil")
	}
	if got := atomic.LoadInt32(&requestCount); got != int32(defaultMaxSendAttempts) {
		t.Errorf("expected %d attempts, server saw %d", defaultMaxSendAttempts, got)
	}
}
