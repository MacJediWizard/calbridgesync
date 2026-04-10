package notify

import (
	"context"
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
// Backoff sequence with defaultInitialBackoff=500ms:
//
//	attempt 1: no delay
//	attempt 2: 500ms + jitter (retry after first failure)
//	attempt 3: 1s + jitter (retry after second failure)
//
// Jitter is uniform random in [0, backoff/4) to avoid thundering-herd
// retries from multiple concurrent failed sends synchronizing.
func retryTransient(ctx context.Context, maxAttempts int, fn func(ctx context.Context) error, isTransient func(error) bool) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter.
			backoff := defaultInitialBackoff * time.Duration(1<<(attempt-1))
			jitterMax := int64(backoff / 4)
			var jitter time.Duration
			if jitterMax > 0 {
				jitter = time.Duration(rand.Int63n(jitterMax))
			}
			timer := time.NewTimer(backoff + jitter)
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
//
// Permanent:
//   - 4xx client errors: "webhook returned status 4xx"
//   - URL validation failures: "invalid webhook URL"
//   - Request construction: "create request"
//   - Payload marshaling: "marshal payload"
//
// The classification is string-based rather than type-based because the
// error paths in sendWebhook / sendWebhookToURL wrap the low-level
// errors into formatted messages, losing the original type. Since all
// those messages are under our control, string matching is stable.
func isTransientHTTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()

	// Permanent: URL or payload setup failures — retry would give the
	// same result.
	if strings.Contains(msg, "invalid webhook URL") ||
		strings.Contains(msg, "marshal payload") ||
		strings.Contains(msg, "create request") {
		return false
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
