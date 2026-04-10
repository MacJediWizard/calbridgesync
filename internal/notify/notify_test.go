package notify

import (
	"testing"
	"time"
)

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
		{"valid HTTPS URL", "https://hooks.slack.com/services/xxx", false},
		{"HTTP not allowed", "http://example.com/webhook", true},
		{"localhost not allowed", "https://localhost/webhook", true},
		{"127.0.0.1 not allowed", "https://127.0.0.1/webhook", true},
		{"192.168.x.x not allowed", "https://192.168.0.1/webhook", true},
		{"10.x.x.x not allowed", "https://10.0.0.1/webhook", true},
		{"172.16.x.x not allowed", "https://172.16.0.1/webhook", true},
		{".local domain not allowed", "https://server.local/webhook", true},
		{".internal domain not allowed", "https://api.internal/webhook", true},
		{"invalid URL", "not-a-url", true},
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
	sent := n.SendStaleAlert(nil, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour)
	if !sent {
		t.Error("First stale alert should be sent")
	}

	// Second alert within cooldown should be blocked
	sent = n.SendStaleAlert(nil, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour)
	if sent {
		t.Error("Second stale alert within cooldown should be blocked")
	}

	// Different source should still work
	sent = n.SendStaleAlert(nil, "source2", "Test Source 2", "user@example.com", 2*time.Hour, time.Hour)
	if !sent {
		t.Error("Alert for different source should be sent")
	}
}

func TestNotifierRecovery(t *testing.T) {
	cfg := &Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	}
	n := New(cfg)

	// Recovery without prior stale should return false
	recovered := n.SendRecoveryAlert(nil, "source1", "Test Source", "user@example.com")
	if recovered {
		t.Error("Recovery without prior stale should return false")
	}

	// Send stale alert first
	n.SendStaleAlert(nil, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour)

	// Now recovery should work
	recovered = n.SendRecoveryAlert(nil, "source1", "Test Source", "user@example.com")
	if !recovered {
		t.Error("Recovery after stale should return true")
	}

	// Second recovery should fail (already recovered)
	recovered = n.SendRecoveryAlert(nil, "source1", "Test Source", "user@example.com")
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

	// Send stale alert
	n.SendStaleAlert(nil, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour)

	// Clear stale state
	n.ClearStaleState("source1")

	// Should be able to send stale alert again (not in cooldown)
	sent := n.SendStaleAlert(nil, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour)
	if !sent {
		t.Error("Should be able to send alert after clearing stale state")
	}
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
	sent := n.SendStaleAlert(nil, "source1", "Test Source", "user@example.com", 2*time.Hour, time.Hour)
	if !sent {
		t.Fatal("stale alert should fire")
	}

	// A failure alert for the same source should NOT be blocked by the
	// stale cooldown — they use independent maps.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "some error", nil,
	)
	if !sent {
		t.Error("failure alert should not be blocked by stale cooldown")
	}
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

	// With zero cooldown the second alert fires immediately.
	sent = n.SendSyncFailureAlertWithPrefs(
		nil, "source1", "Test Source", "user@example.com",
		"Sync failed", "second", prefs,
	)
	if !sent {
		t.Error("second alert should fire with zero-minute user cooldown")
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
