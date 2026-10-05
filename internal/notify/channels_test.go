package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

// TestHasChannelsFor verifies that per-user channels count as enabled
// on their own, independent of the global alert flags, and that the
// global flags only decide the global channels. (#222)
func TestHasChannelsFor(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		prefs *UserPreferences
		want  bool
	}{
		{"all off, no prefs", Config{}, nil, false},
		{"global webhook on, no prefs", Config{WebhookEnabled: true, WebhookURL: "https://hooks.example.com/x"}, nil, true},
		{"global email on, no prefs", Config{EmailEnabled: true, SMTPHost: "smtp.example.com"}, nil, true},
		{"global off, user webhook URL set", Config{}, &UserPreferences{WebhookURL: "https://hooks.example.com/u"}, true},
		{"global off, user webhook URL set but disabled", Config{}, &UserPreferences{WebhookURL: "https://hooks.example.com/u", WebhookEnabled: boolPtr(false)}, false},
		{"global off, user webhook enabled without URL", Config{WebhookURL: "https://hooks.example.com/global"}, &UserPreferences{WebhookEnabled: boolPtr(true)}, false},
		{"global off, user email on with SMTP configured", Config{SMTPHost: "smtp.example.com"}, &UserPreferences{EmailEnabled: boolPtr(true)}, true},
		{"global off, user email on without SMTP host", Config{}, &UserPreferences{EmailEnabled: boolPtr(true)}, false},
		{"global on, user opted out of everything", Config{WebhookEnabled: true, WebhookURL: "https://hooks.example.com/x", EmailEnabled: true, SMTPHost: "smtp.example.com"}, &UserPreferences{WebhookEnabled: boolPtr(false), EmailEnabled: boolPtr(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if got := New(&cfg).HasChannelsFor(tt.prefs); got != tt.want {
				t.Errorf("HasChannelsFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSendWithPrefs_UserPrefDoesNotEnableDisabledGlobalWebhook verifies
// that a user turning on "Webhook Alerts" does not route their alerts to
// the operator's global webhook when the operator disabled it. Global
// flags gate the global channels. (#222)
func TestSendWithPrefs_UserPrefDoesNotEnableDisabledGlobalWebhook(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	alert := Alert{Type: AlertTypeError, SourceID: "s", SourceName: "S", Message: "m", Timestamp: time.Now()}

	disabled := New(&Config{WebhookEnabled: false, WebhookURL: server.URL, MaxSendAttempts: 1})
	disabled.sendWithPrefs(context.Background(), alert, &UserPreferences{WebhookEnabled: boolPtr(true)})
	if got := hits.Load(); got != 0 {
		t.Fatalf("disabled global webhook received %d requests, want 0", got)
	}

	// Regression: an enabled global webhook still fires for a user with
	// no preferences.
	enabled := New(&Config{WebhookEnabled: true, WebhookURL: server.URL, MaxSendAttempts: 1})
	if !enabled.sendWithPrefs(context.Background(), alert, nil) {
		t.Fatal("expected delivery to the enabled global webhook")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("enabled global webhook received %d requests, want 1", got)
	}
}
