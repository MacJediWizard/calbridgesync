package notify

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// defaultMaxSendAttempts is the maximum total attempts (initial + retries)
// for a single webhook or email send. 3 attempts with the default backoff
// sequence of 500ms and 1s yields ~1.5s worst-case for a fully failed send
// before the outer loop (PR #34 cooldown-not-consumed-on-failure)
// schedules another attempt on the next tick.
const defaultMaxSendAttempts = 3

// defaultInitialBackoff is the base delay before the first retry. Retry
// N uses defaultInitialBackoff * 2^(N-1) plus up to 25% jitter.
const defaultInitialBackoff = 500 * time.Millisecond

// maxRetryAfterDelay caps how long we honor a server-supplied
// Retry-After header. A misbehaving or malicious endpoint that asks
// for "Retry-After: 86400" (1 day) would otherwise pin the scheduler
// indefinitely. The cap is 60 seconds — long enough to handle real
// rate-limit situations (most services ask for a few seconds to a
// minute) but short enough to keep alerts flowing during an incident.
// Issue #66.
const maxRetryAfterDelay = 60 * time.Second

// RetryAfterError wraps an underlying HTTP error (e.g., a 429 response)
// with a server-suggested retry delay. retryTransient treats a
// RetryAfterError as transient and uses RetryAfter as the sleep
// duration before the next attempt, bypassing the exponential
// backoff sequence. This lets rate-limited endpoints tell the client
// exactly when to try again, which is both more respectful and more
// efficient than guessing.
//
// Issue #66 introduces this type. Used by sendWebhook and
// sendWebhookToURL when parsing an HTTP 429 response's Retry-After
// header. The underlying error is preserved via Unwrap so
// errors.Is / errors.As continue to work across the wrapper.
type RetryAfterError struct {
	// Underlying is the original error (e.g., "webhook returned status 429").
	Underlying error
	// RetryAfter is the delay the server asked us to wait, already
	// capped at maxRetryAfterDelay. Zero means "no Retry-After
	// header was present; fall back to the normal backoff".
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *RetryAfterError) Error() string {
	if e == nil || e.Underlying == nil {
		return "retry-after error"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%v (retry after %v)", e.Underlying, e.RetryAfter)
	}
	return e.Underlying.Error()
}

// Unwrap returns the underlying error so errors.Is and errors.As
// continue to work. Callers that do not care about Retry-After
// behave as if they got the plain underlying error.
func (e *RetryAfterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Underlying
}

// parseRetryAfter converts a Retry-After HTTP header value into a
// Duration. Supports both delta-seconds format ("120") and HTTP-date
// format ("Wed, 21 Oct 2015 07:28:00 GMT") per RFC 7231 §7.1.3.
// Returns 0 if the header is empty, malformed, or in the past.
// The returned duration is capped at maxRetryAfterDelay — a
// misbehaving server cannot pin the scheduler indefinitely.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}

	// Delta-seconds format: "120"
	if secs, err := time.ParseDuration(header + "s"); err == nil && secs > 0 {
		if secs > maxRetryAfterDelay {
			return maxRetryAfterDelay
		}
		return secs
	}

	// HTTP-date format: "Wed, 21 Oct 2015 07:28:00 GMT"
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		delta := time.Until(t)
		if delta <= 0 {
			return 0
		}
		if delta > maxRetryAfterDelay {
			return maxRetryAfterDelay
		}
		return delta
	}

	// Unrecognized format
	return 0
}

// retryTransient calls fn up to maxAttempts times with exponential backoff
// and jitter between attempts. If fn returns nil on any attempt, the
// function returns nil. If fn returns an error and isTransient(err) is
// false, the function returns the error immediately without retrying.
// If fn returns a transient error on every attempt, the function returns
// the last error.
//
// Context cancellation is honored during the backoff sleep: if ctx is
// cancelled while waiting to retry, the function returns the last error
// immediately rather than waiting out the full backoff.
//
// Backoff sequence with initialBackoff=500ms:
//
//	attempt 1: no delay
//	attempt 2: 500ms + jitter (retry after first failure)
//	attempt 3: 1s + jitter (retry after second failure)
//
// Jitter is uniform random in [0, backoff/4) to avoid thundering-herd
// retries from multiple concurrent failed sends synchronizing.
//
// initialBackoff controls the base delay before the first retry; it
// was introduced in Issue #64 to allow per-notifier tuning via env
// vars. A zero or negative value falls back to defaultInitialBackoff.
func retryTransient(ctx context.Context, maxAttempts int, initialBackoff time.Duration, fn func(ctx context.Context) error, isTransient func(error) bool) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if initialBackoff <= 0 {
		initialBackoff = defaultInitialBackoff
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Default delay: exponential backoff with jitter.
			delay := initialBackoff * time.Duration(1<<(attempt-1))
			jitterMax := int64(delay / 4)
			var jitter time.Duration
			if jitterMax > 0 {
				jitter = time.Duration(rand.Int63n(jitterMax))
			}
			delay += jitter

			// If the last error carried a server-supplied Retry-After
			// (via RetryAfterError), use that value instead. This
			// respects rate-limited endpoints more accurately than
			// the default exponential backoff. Issue #66.
			var retryAfterErr *RetryAfterError
			if errors.As(lastErr, &retryAfterErr) && retryAfterErr.RetryAfter > 0 {
				delay = retryAfterErr.RetryAfter
			}

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return lastErr
			case <-timer.C:
			}
		}

		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransient(err) {
			return err
		}
	}
	return lastErr
}

// isTransientHTTPError classifies an HTTP send error as either transient
// (worth retrying) or permanent (retry would give the same result).
//
// Transient:
//   - Network errors (DNS, TCP, TLS, read timeouts): the error message
//     doesn't contain "status" since the request never got an HTTP
//     response back
//   - 5xx server errors: "webhook returned status 5xx"
//   - 429 Too Many Requests: rate-limit signal, retry after the
//     Retry-After delay (Issue #66). Also recognized via
//     RetryAfterError wrapper type.
//
// Permanent:
//   - Other 4xx client errors: "webhook returned status 4xx"
//   - URL validation failures: "invalid webhook URL"
//   - Request construction: "create request"
//   - Payload marshaling: "marshal payload"
//
// The classification is string-based rather than type-based because the
// error paths in sendWebhook / sendWebhookToURL wrap the low-level
// errors into formatted messages, losing the original type. Since all
// those messages are under our control, string matching is stable.
// RetryAfterError is detected via errors.As to handle the wrapper.
func isTransientHTTPError(err error) bool {
	if err == nil {
		return false
	}

	// A RetryAfterError is always transient by construction — it's
	// the signal for "retry, here's when". Check the wrapper type
	// first so callers wrapping their 429 errors work correctly.
	var retryAfterErr *RetryAfterError
	if errors.As(err, &retryAfterErr) {
		return true
	}

	msg := err.Error()

	// Permanent: URL or payload setup failures — retry would give the
	// same result.
	if strings.Contains(msg, "invalid webhook URL") ||
		strings.Contains(msg, "marshal payload") ||
		strings.Contains(msg, "create request") {
		return false
	}

	// Transient: 429 Too Many Requests. Even though it starts with 4,
	// it's a "try again later" signal, not "stop trying". Must be
	// checked BEFORE the general 4xx permanent check below.
	if strings.Contains(msg, "returned status 429") {
		return true
	}

	// Permanent: 4xx responses. Note that "status 5xx" is transient so
	// we must check for the explicit 4xx pattern — a naive "status 4"
	// substring would also match "status 500"'s wraps if we happened
	// to format them in an odd way, but the current format is
	// "webhook returned status N" where N is a decimal code.
	if strings.Contains(msg, "returned status 4") {
		return false
	}

	// Everything else — network errors, 5xx, read timeouts — is
	// transient and worth retrying.
	return true
}

// isTransientSMTPError classifies an SMTP send error. SMTP error taxonomy
// is much less crisp than HTTP, so the default is to retry everything
// except for the very clearly permanent cases. The cooldown-not-consumed
// outer loop (PR #34) ensures that even permanent errors are eventually
// given up on rather than looped forever.
func isTransientSMTPError(err error) bool {
	if err == nil {
		return false
	}
	// Very few SMTP errors are deterministically permanent at the
	// Go SDK level. "530 authentication required" is, but the error
	// message varies by server. For now, retry everything and let
	// the outer cooldown loop handle persistent failures.
	return true
}
