package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// drainTimeout is the max time we wait for in-flight alert sends to
// complete in tests. Must comfortably exceed the full retry window
// (defaultMaxSendAttempts attempts with exponential backoff + jitter,
// ~1.5s total for 3 attempts at 500ms base) for tests that exercise
// failure paths. Issue #39 introduced the retry behavior that pushed
// failed-send durations into the low-seconds range.
const drainTimeout = 5 * time.Second

// waitForDrain blocks until all in-flight alert sends have completed,
// or until drainTimeout expires. Test helper for sequencing multi-alert
// scenarios — since Issue #33, the cooldown timestamp is recorded
// inside the background goroutine only after sendWithPrefs returns,
// so tests that rely on cooldown-after-first-send must wait for
// that goroutine to finish before making assertions about state.
func waitForDrain(t *testing.T, n *Notifier) {
	t.Helper()
	deadline := time.Now().Add(drainTimeout)
	for time.Now().Before(deadline) {
		n.mu.RLock()
		empty := len(n.inFlightAlerts) == 0
		n.mu.RUnlock()
		if empty {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("in-flight alerts did not drain within %v", drainTimeout)
}

// waitForKeyDrain blocks until a specific in-flight key has cleared.
// Used by tests that synthetically populate some keys and want to wait
// for only the real goroutine(s) to finish, not the synthetic ones.
func waitForKeyDrain(t *testing.T, n *Notifier, key string) {
	t.Helper()
	deadline := time.Now().Add(drainTimeout)
	for time.Now().Before(deadline) {
		n.mu.RLock()
		gone := !n.inFlightAlerts[key]
		n.mu.RUnlock()
		if gone {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("in-flight key %q did not clear within %v", key, drainTimeout)
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid webhook config",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "https://hooks.slack.com/services/xxx",
				CooldownPeriod: time.Hour,
			},
			wantErr: false,
		},
		{
			name: "webhook with HTTP (not HTTPS) fails",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "http://hooks.slack.com/services/xxx",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "HTTPS",
		},
		{
			name: "webhook pointing to localhost fails",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "https://localhost:8080/webhook",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "localhost",
		},
		{
			name: "webhook pointing to private IP fails",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "https://192.168.1.1/webhook",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "private IP",
		},
		{
			name: "webhook pointing to 10.x.x.x fails",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "https://10.0.0.1/webhook",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "private IP",
		},
		{
			name: "webhook with empty URL fails",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "required",
		},
		{
			name: "valid email config",
			cfg: &Config{
				EmailEnabled:   true,
				SMTPHost:       "smtp.gmail.com",
				SMTPPort:       587,
				SMTPFrom:       "alerts@example.com",
				SMTPTo:         []string{"admin@example.com"},
				CooldownPeriod: time.Hour,
			},
			wantErr: false,
		},
		{
			name: "email with invalid port fails",
			cfg: &Config{
				EmailEnabled:   true,
				SMTPHost:       "smtp.gmail.com",
				SMTPPort:       0,
				SMTPFrom:       "alerts@example.com",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "port",
		},
		{
			name: "email with missing host fails",
			cfg: &Config{
				EmailEnabled:   true,
				SMTPPort:       587,
				SMTPFrom:       "alerts@example.com",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "host",
		},
		{
			name: "email with invalid from address fails",
			cfg: &Config{
				EmailEnabled:   true,
				SMTPHost:       "smtp.gmail.com",
				SMTPPort:       587,
				SMTPFrom:       "not-an-email",
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "from address",
		},
		{
			name: "email with invalid recipient fails",
			cfg: &Config{
				EmailEnabled:   true,
				SMTPHost:       "smtp.gmail.com",
				SMTPPort:       587,
				SMTPFrom:       "alerts@example.com",
				SMTPTo:         []string{"invalid-email"},
				CooldownPeriod: time.Hour,
			},
			wantErr: true,
			errMsg:  "recipient",
		},
		{
			name: "cooldown too short fails",
			cfg: &Config{
				WebhookEnabled: true,
				WebhookURL:     "https://hooks.slack.com/services/xxx",
				CooldownPeriod: 30 * time.Second,
			},
			wantErr: true,
			errMsg:  "minute",
		},
		{
			name: "disabled notifications need no validation",
			cfg: &Config{
				WebhookEnabled: false,
				EmailEnabled:   false,
				CooldownPeriod: time.Hour,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateConfig() error = %v, expected to contain %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestIsValidEmail(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"user@example.com", true},
		{"user.name@example.com", true},
		{"user+tag@example.com", true},
		{"user@sub.example.com", true},
		{"not-an-email", false},
		{"@example.com", false},
		{"user@", false},
		{"user@.com", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			if got := isValidEmail(tt.email); got != tt.valid {
				t.Errorf("isValidEmail(%q) = %v, want %v", tt.email, got, tt.valid)
			}
		})
	}
}

func TestSanitizeForEmail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "normal text unchanged",
			input: "Hello World",
			want:  "Hello World",
		},
		{
			name:  "CR removed",
			input: "Line1\rLine2",
			want:  "Line1Line2",
		},
		{
			name:  "LF replaced with space",
			input: "Line1\nLine2",
			want:  "Line1 Line2",
		},
		{
			name:  "CRLF header injection attempt blocked",
			input: "Subject\r\nBcc: attacker@evil.com",
			want:  "Subject Bcc: attacker@evil.com",
		},
		{
			name:  "long string truncated",
			input: string(make([]byte, 300)),
			want:  string(make([]byte, 200)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeForEmail(tt.input); got != tt.want {
				t.Errorf("sanitizeForEmail() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateWebhookURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		// Happy path
		{"valid HTTPS URL", "https://hooks.slack.com/services/xxx", false},
		{"valid HTTPS URL with path", "https://hooks.example.com/endpoint/12345", false},
		{"valid HTTPS URL with public IPv4", "https://8.8.8.8/webhook", false},

		// Scheme
		{"HTTP not allowed", "http://example.com/webhook", true},
		{"ftp scheme not allowed", "ftp://example.com/webhook", true},

		// Hostname-based blocks (existing cases, still must pass)
		{"localhost not allowed", "https://localhost/webhook", true},
		{"localhost uppercase not allowed", "https://LOCALHOST/webhook", true},
		{".local domain not allowed", "https://server.local/webhook", true},
		{".internal domain not allowed", "https://api.internal/webhook", true},

		// Loopback — regression for #115: the old check only blocked
		// the exact literal "127.0.0.1", not the rest of 127.0.0.0/8.
		{"127.0.0.1 not allowed", "https://127.0.0.1/webhook", true},
		{"127.0.0.2 not allowed (regression)", "https://127.0.0.2/webhook", true},
		{"127.1.2.3 not allowed (regression)", "https://127.1.2.3/webhook", true},
		{"IPv6 loopback ::1 not allowed", "https://[::1]/webhook", true},

		// RFC 1918 private ranges — existing coverage
		{"192.168.x.x not allowed", "https://192.168.0.1/webhook", true},
		{"10.x.x.x not allowed", "https://10.0.0.1/webhook", true},
		{"172.16.x.x not allowed", "https://172.16.0.1/webhook", true},
		{"172.31.x.x not allowed", "https://172.31.255.254/webhook", true},
		// 172.32.x.x is NOT RFC 1918; should be allowed (old prefix-
		// based check was correct to exclude it, new check uses
		// net.IP.IsPrivate which has the RFC 1918 range exactly).
		{"172.32.x.x is NOT private (public)", "https://172.32.0.1/webhook", false},

		// Link-local / cloud metadata — NEW in #115. The 169.254.x.x
		// block includes AWS/GCP/Azure IMDS endpoints at
		// 169.254.169.254 which were not blocked by the old prefix
		// check at all.
		{"AWS IMDS 169.254.169.254 not allowed (regression)", "https://169.254.169.254/latest/meta-data/", true},
		{"link-local 169.254.x.x not allowed", "https://169.254.1.2/webhook", true},

		// Unspecified address — NEW in #115. On many systems
		// 0.0.0.0 routes to loopback.
		{"0.0.0.0 not allowed (regression)", "https://0.0.0.0/webhook", true},

		// Carrier-grade NAT (100.64.0.0/10) — Tailscale and some
		// ISPs. Not caught by net.IP.IsPrivate(), explicit check.
		{"100.64.x.x carrier NAT not allowed", "https://100.64.0.1/webhook", true},
		{"100.127.255.254 carrier NAT not allowed", "https://100.127.255.254/webhook", true},

		// IPv6 private / link-local
		{"IPv6 unique-local fc00:: not allowed", "https://[fc00::1]/webhook", true},
		{"IPv6 link-local fe80:: not allowed", "https://[fe80::1]/webhook", true},

		// Malformed URLs
		{"invalid URL", "not-a-url", true},
		{"URL with no host", "https:///path", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebhookURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateWebhookURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestNotifierCooldown(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// First alert should succeed
	sent := n.SendStaleAlertWithPrefs(context.Background(), "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour, nil)
	if !sent {
		t.Error("First stale alert should be sent")
	}
	waitForDrain(t, n)

	// Second alert within cooldown should be blocked
	sent = n.SendStaleAlertWithPrefs(context.Background(), "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour, nil)
	if sent {
		t.Error("Second stale alert within cooldown should be blocked")
	}

	// Different source should still work
	sent = n.SendStaleAlertWithPrefs(context.Background(), "source2", "Test Source 2", "user@example.com", 2*time.Hour, time.Hour, nil)
	if !sent {
		t.Error("Alert for different source should be sent")
	}
	waitForDrain(t, n)
}

func TestNotifierRecovery(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)
	ctx := context.Background()

	// Recovery without prior stale should return false
	recovered := n.SendRecoveryAlertWithPrefs(ctx, "source1", "Test Source", "user@example.com", nil)
	if recovered {
		t.Error("Recovery without prior stale should return false")
	}

	// Send stale alert first
	n.SendStaleAlertWithPrefs(ctx, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour, nil)
	waitForDrain(t, n)

	// Now recovery should work
	recovered = n.SendRecoveryAlertWithPrefs(ctx, "source1", "Test Source", "user@example.com", nil)
	if !recovered {
		t.Error("Recovery after stale should return true")
	}

	// Second recovery should fail (already recovered)
	recovered = n.SendRecoveryAlertWithPrefs(ctx, "source1", "Test Source", "user@example.com", nil)
	if recovered {
		t.Error("Second recovery should return false")
	}
}

func TestClearStaleState(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)
	ctx := context.Background()

	// Send stale alert
	n.SendStaleAlertWithPrefs(ctx, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour, nil)
	waitForDrain(t, n)

	// Clear stale state
	n.ClearStaleState("source1")

	// Should be able to send stale alert again (not in cooldown)
	sent := n.SendStaleAlertWithPrefs(ctx, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour, nil)
	if !sent {
		t.Error("Should be able to send alert after clearing stale state")
	}
	waitForDrain(t, n)
}

// TestSendSyncFailureAlertCooldown verifies that a second failure alert for
// the same source inside the cooldown window is rejected, preventing alert
// storms on a persistently broken source.
func TestSendSyncFailureAlertCooldown(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	sent := n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "connection refused", nil,
	)
	if !sent {
		t.Error("first failure alert should be sent")
	}

	// Second alert within cooldown should be suppressed.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "connection refused", nil,
	)
	if sent {
		t.Error("second failure alert within cooldown should be suppressed")
	}

	// Different source should still work.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source2", "Test Source 2", "user@example.com",
		"Sync failed", "auth expired", nil,
	)
	if !sent {
		t.Error("failure alert for a different source should be sent")
	}
}

// TestSendSyncFailureAlertIndependentFromStale verifies that the failure
// cooldown and the stale cooldown are tracked separately. A source that is
// both stale AND failing should be able to fire BOTH alert types without
// one consuming the other's cooldown.
func TestSendSyncFailureAlertIndependentFromStale(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Fire a stale alert first.
	sent := n.SendStaleAlertWithPrefs(context.Background(), "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour, nil)
	if !sent {
		t.Fatal("stale alert should fire")
	}

	// A failure alert for the same source should NOT be blocked by the
	// stale cooldown — they use independent maps.
	sent = n.SendSyncFailureAlertWithPrefs(
		context.Background(), "source1", "Test Source", "user@example.com",
		"Sync failed", "some error", nil,
	)
	if !sent {
		t.Error("failure alert should not be blocked by stale cooldown")
	}
	waitForDrain(t, n)
}

// TestClearFailureAlertState verifies that clearing the failure-alert state
// lets the next failure fire immediately instead of waiting for cooldown.
func TestClearFailureAlertState(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	sent := n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "connection refused", nil,
	)
	if !sent {
		t.Fatal("first failure alert should fire")
	}

	// Before clear, second alert is blocked.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "connection refused", nil,
	)
	if sent {
		t.Fatal("second failure alert should be blocked before clear")
	}

	n.ClearFailureAlertState("source1")

	// After clear, next alert fires.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "connection refused", nil,
	)
	if !sent {
		t.Error("failure alert should fire after ClearFailureAlertState")
	}

	// ClearFailureAlertState must be safe for unknown source IDs.
	n.ClearFailureAlertState("never-alerted-source")
}

// TestSendSyncFailureAlertUserCooldownOverride verifies that user preferences
// can shorten or lengthen the failure cooldown independently of the global
// cooldown setting.
func TestSendSyncFailureAlertUserCooldownOverride(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Zero-minute cooldown via user pref — second alert should fire immediately.
	zero := 0
	prefs := &UserPreferences{CooldownMinutes: &zero}

	sent := n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "first", prefs,
	)
	if !sent {
		t.Fatal("first failure alert should fire")
	}

	// Wait for the first send's background goroutine to finish and
	// clear its in-flight guard before attempting the second call.
	// The in-flight dedup is Issue #33's fix for concurrent duplicate
	// sends; without this wait the second call would be legitimately
	// suppressed by the in-flight guard even though the cooldown
	// itself is zero.
	waitForDrain(t, n)

	// With zero cooldown the second alert fires immediately.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "second", prefs,
	)
	if !sent {
		t.Error("second alert should fire with zero-minute user cooldown")
	}
}

// TestCooldownNotConsumedByFailedSend verifies Issue #33's core fix:
// when a webhook send fails, the cooldown timestamp must NOT be recorded,
// so the next alert attempt can fire immediately rather than being
// silenced for the full cooldown window.
//
// Setup: point the webhook at an httptest server that always returns
// HTTP 500, so the send reaches the server but sendWebhook reports a
// delivery failure.
func TestCooldownNotConsumedByFailedSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &Config{
		WebhookEnabled: true,
		WebhookURL:     server.URL,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)
	ctx := context.Background()

	// First attempt. Returns true (queued) but the background send will
	// fail because the webhook returns 500.
	sent := n.SendSyncFailureAlertWithPrefs(
		ctx, "source1", "Test Source", "user@example.com",
		"Sync failed", "first attempt", nil,
	)
	if !sent {
		t.Fatal("first failure alert should be queued")
	}

	// Wait for the background send to finish failing.
	waitForDrain(t, n)

	// Cooldown should NOT be set because the delivery failed.
	n.mu.RLock()
	_, cooldownSet := n.lastFailureAlertTimes["source1"]
	n.mu.RUnlock()
	if cooldownSet {
		t.Error("cooldown should NOT be set after a failed send; failed deliveries must not consume the cooldown window")
	}

	// Second attempt must succeed because the cooldown was not consumed.
	sent = n.SendSyncFailureAlertWithPrefs(
		ctx, "source1", "Test Source", "user@example.com",
		"Sync failed", "second attempt", nil,
	)
	if !sent {
		t.Error("second alert should fire immediately because the first delivery failed (no cooldown consumed)")
	}
	waitForDrain(t, n)
}

// TestCooldownRecordedAfterSuccessfulSend verifies that when a delivery
// DOES succeed, the cooldown is recorded correctly so repeat alerts
// are suppressed. This is the happy path for Issue #33.
//
// Setup: webhook points at an httptest server that returns 200, so
// sendWebhook reports a successful delivery.
func TestCooldownRecordedAfterSuccessfulSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &Config{
		WebhookEnabled: true,
		WebhookURL:     server.URL,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)
	ctx := context.Background()

	sent := n.SendSyncFailureAlertWithPrefs(
		ctx, "source1", "Test Source", "user@example.com",
		"Sync failed", "first", nil,
	)
	if !sent {
		t.Fatal("first alert should fire")
	}

	// Wait for the background send to complete successfully.
	waitForDrain(t, n)

	// Cooldown should be set now.
	n.mu.RLock()
	_, cooldownSet := n.lastFailureAlertTimes["source1"]
	n.mu.RUnlock()
	if !cooldownSet {
		t.Fatal("cooldown should be set after a successful webhook 200 response")
	}

	// Second attempt must be blocked by the cooldown.
	sent = n.SendSyncFailureAlertWithPrefs(
		ctx, "source1", "Test Source", "user@example.com",
		"Sync failed", "second", nil,
	)
	if sent {
		t.Error("second alert should be blocked by the cooldown set by the first successful send")
	}
}

// TestInFlightGuardSuppressesSameSourceAndType verifies that a concurrent
// second caller for the same source AND same alert type is suppressed
// while a prior send is still in flight. Uses a manually-set in-flight
// flag to avoid racing real goroutines.
func TestInFlightGuardSuppressesSameSourceAndType(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Manually set in-flight to simulate a send in progress. We do this
	// synthetically rather than kicking off a real send so the test
	// doesn't race against goroutine scheduling.
	n.mu.Lock()
	n.inFlightAlerts["failure:source1"] = true
	n.mu.Unlock()

	// Concurrent attempt for the same source+type must be suppressed.
	sent := n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "concurrent attempt", nil,
	)
	if sent {
		t.Error("alert must be suppressed while an in-flight send exists for the same source+type")
	}

	// Clean up so the test leaves no stale state.
	n.mu.Lock()
	delete(n.inFlightAlerts, "failure:source1")
	n.mu.Unlock()
}

// TestInFlightGuardIsScopedByAlertType verifies that a stale alert in
// flight does NOT block a failure alert for the same source. The two
// alert types use different key prefixes in inFlightAlerts so they can
// fire concurrently for the same source.
func TestInFlightGuardIsScopedByAlertType(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Synthetically mark a stale-alert in flight for source1. This
	// simulates a stale-alert goroutine that hasn't finished yet.
	n.mu.Lock()
	n.inFlightAlerts["stale:source1"] = true
	n.mu.Unlock()

	// A failure alert for the SAME source should fire — different key.
	sent := n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "different type", nil,
	)
	if !sent {
		t.Error("failure alert must not be blocked by a stale alert in flight (different key prefix)")
	}

	// Wait only for the failure key to drain — the synthetic stale key
	// will never clear because there's no goroutine behind it.
	waitForKeyDrain(t, n, "failure:source1")

	// Clean up the synthetic stale entry.
	n.mu.Lock()
	delete(n.inFlightAlerts, "stale:source1")
	n.mu.Unlock()
}

// TestInFlightGuardIsScopedBySourceID verifies that an in-flight alert
// for one source does not block alerts for a different source.
func TestInFlightGuardIsScopedBySourceID(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Synthetically mark a failure alert in flight for source1.
	n.mu.Lock()
	n.inFlightAlerts["failure:source1"] = true
	n.mu.Unlock()

	// A failure alert for source2 must not be blocked.
	sent := n.SendSyncFailureAlertWithPrefs(
		nil, "source2", "Other Source", "user@example.com",
		"Sync failed", "other source", nil,
	)
	if !sent {
		t.Error("source2 must not be blocked by source1's in-flight guard")
	}

	// Wait only for the source2 key to drain; source1's synthetic entry
	// will never clear because no goroutine is running it.
	waitForKeyDrain(t, n, "failure:source2")

	// Clean up the synthetic source1 entry.
	n.mu.Lock()
	delete(n.inFlightAlerts, "failure:source1")
	n.mu.Unlock()
}

// TestClearFailureAlertStateClearsInFlight verifies that
// ClearFailureAlertState cleans up in-flight state too, so a newly
// re-created source (reusing the same ID) starts with a clean slate.
func TestClearFailureAlertStateClearsInFlight(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Manually set in-flight
	n.mu.Lock()
	n.inFlightAlerts["failure:source1"] = true
	n.mu.Unlock()

	n.ClearFailureAlertState("source1")

	n.mu.RLock()
	stillInFlight := n.inFlightAlerts["failure:source1"]
	n.mu.RUnlock()
	if stillInFlight {
		t.Error("ClearFailureAlertState must also clear in-flight guard")
	}
}

// TestClearStaleStateClearsInFlight — same contract for stale path.
func TestClearStaleStateClearsInFlight(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	n.mu.Lock()
	n.inFlightAlerts["stale:source1"] = true
	n.mu.Unlock()

	n.ClearStaleState("source1")

	n.mu.RLock()
	stillInFlight := n.inFlightAlerts["stale:source1"]
	n.mu.RUnlock()
	if stillInFlight {
		t.Error("ClearStaleState must also clear in-flight guard")
	}
}

// TestIsDangerousWarning verifies the pattern match for data-loss protection
// warnings. The scheduler uses this to decide whether a "successful" sync
// with warnings should still fire an alert.
func TestIsDangerousWarning(t *testing.T) {
	tests := []struct {
		name    string
		warning string
		want    bool
	}{
		{
			name:    "empty-source guard warning from planOrphanDeletion",
			warning: "source returned 0 events but 42 previously-synced records exist - skipping one-way orphan deletion for safety (possible auth failure or broken source)",
			want:    true,
		},
		{
			name:    "mass-delete threshold warning from planOrphanDeletion",
			warning: "one-way orphan deletion would remove 80 of 100 previously-synced events (80%), exceeds safety threshold 50% - skipping deletion",
			want:    true,
		},
		{
			name:    "harmless individual delete failure",
			warning: "Failed to delete orphan event: 404 not found",
			want:    false,
		},
		{
			name:    "harmless 403 skip",
			warning: "Two-way sync: 3 events skipped (source calendar read-only)",
			want:    false,
		},
		{
			name:    "empty string",
			warning: "",
			want:    false,
		},
		{
			name:    "unrelated warning containing none of the patterns",
			warning: "Failed to update sync log",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDangerousWarning(tt.warning)
			if got != tt.want {
				t.Errorf("IsDangerousWarning(%q) = %v, want %v", tt.warning, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
