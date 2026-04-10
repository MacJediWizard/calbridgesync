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
	err := retryTransient(context.Background(), 3, defaultInitialBackoff, func(_ context.Context) error {
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
	err := retryTransient(context.Background(), 5, defaultInitialBackoff, func(_ context.Context) error {
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
	err := retryTransient(context.Background(), 3, defaultInitialBackoff, func(_ context.Context) error {
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
	err := retryTransient(context.Background(), 3, defaultInitialBackoff, func(_ context.Context) error {
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
	err := retryTransient(ctx, 5, defaultInitialBackoff, func(_ context.Context) error {
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

// TestNotifier_MaxSendAttempts_DefaultWhenZero verifies that a
// zero-valued Config.MaxSendAttempts falls back to the package default
// rather than meaning "zero attempts" (which would silence all alerts).
// Issue #64.
func TestNotifier_MaxSendAttempts_DefaultWhenZero(t *testing.T) {
	n := New(&Config{MaxSendAttempts: 0})
	if got := n.maxSendAttempts(); got != defaultMaxSendAttempts {
		t.Errorf("expected default %d, got %d", defaultMaxSendAttempts, got)
	}
}

// TestNotifier_MaxSendAttempts_Configured verifies that a non-zero
// Config.MaxSendAttempts is honored. Operators tuning via
// ALERT_MAX_SEND_ATTEMPTS should see their value take effect.
func TestNotifier_MaxSendAttempts_Configured(t *testing.T) {
	n := New(&Config{MaxSendAttempts: 5})
	if got := n.maxSendAttempts(); got != 5 {
		t.Errorf("expected configured 5, got %d", got)
	}
}

// TestNotifier_InitialBackoff_DefaultWhenZero verifies that a
// zero-valued Config.InitialBackoff falls back to the package default
// rather than meaning "no backoff at all" (which would hammer the
// endpoint with no delay between retries). Issue #64.
func TestNotifier_InitialBackoff_DefaultWhenZero(t *testing.T) {
	n := New(&Config{InitialBackoff: 0})
	if got := n.initialBackoff(); got != defaultInitialBackoff {
		t.Errorf("expected default %v, got %v", defaultInitialBackoff, got)
	}
}

// TestNotifier_InitialBackoff_Configured verifies that a non-zero
// Config.InitialBackoff is honored.
func TestNotifier_InitialBackoff_Configured(t *testing.T) {
	want := 250 * time.Millisecond
	n := New(&Config{InitialBackoff: want})
	if got := n.initialBackoff(); got != want {
		t.Errorf("expected configured %v, got %v", want, got)
	}
}

// TestRetryTransient_HonorsInitialBackoffArgument verifies that the
// initialBackoff parameter actually controls the delay between
// retries. A 10ms backoff should complete 3 attempts very quickly;
// a 200ms backoff should take noticeably longer.
func TestRetryTransient_HonorsInitialBackoffArgument(t *testing.T) {
	var attempts int32
	fast := time.Now()
	err := retryTransient(context.Background(), 3, 10*time.Millisecond, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("transient")
	}, func(error) bool { return true })
	fastElapsed := time.Since(fast)
	if err == nil {
		t.Error("expected error after exhausted retries")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
	// 3 attempts with 10ms initial backoff: delays of 10ms + 20ms = 30ms
	// plus jitter ≤ 7.5ms = 37.5ms worst case. Give 500ms headroom for
	// scheduler variance.
	if fastElapsed > 500*time.Millisecond {
		t.Errorf("fast backoff took too long: %v", fastElapsed)
	}
}

// TestRetryTransient_ZeroInitialBackoffFallsBackToDefault verifies the
// zero-value safety: callers who pass 0 for initialBackoff get the
// package default instead of "no delay at all".
func TestRetryTransient_ZeroInitialBackoffFallsBackToDefault(t *testing.T) {
	var attempts int32
	start := time.Now()
	_ = retryTransient(context.Background(), 2, 0, func(_ context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("transient")
	}, func(error) bool { return true })
	elapsed := time.Since(start)
	// 2 attempts with defaultInitialBackoff (500ms): one delay of 500ms.
	// The zero fallback means we must have slept at least close to
	// defaultInitialBackoff, not 0.
	if elapsed < defaultInitialBackoff/2 {
		t.Errorf("zero initialBackoff appears to have used 0 instead of default; elapsed=%v", elapsed)
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
