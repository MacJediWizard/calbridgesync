package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDetectWebhookPlatform(t *testing.T) {
	cases := []struct {
		url  string
		want webhookPlatform
	}{
		{"https://hooks.slack.com/services/T00/B00/xxx", platformSlack},
		{"https://hooks.slack.com/workflows/xxx", platformSlack},
		{"https://HOOKS.SLACK.COM/services/xxx", platformSlack},
		{"https://discord.com/api/webhooks/123/abc", platformDiscord},
		{"https://discordapp.com/api/webhooks/123/abc", platformDiscord},
		{"https://DISCORD.COM/api/webhooks/123/abc", platformDiscord},
		{"https://example.com/webhook", platformGeneric},
		{"https://my-slack-clone.com/hooks", platformGeneric},
		{"", platformGeneric},
	}

	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got := detectWebhookPlatform(tc.url)
			if got != tc.want {
				t.Errorf("detectWebhookPlatform(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestFormatSlackPayload(t *testing.T) {
	alert := Alert{
		Type:       AlertTypeError,
		SourceID:   "src-123",
		SourceName: "My Calendar",
		Message:    "Sync failed",
		Details:    "Connection timeout",
		Timestamp:  time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC),
	}

	body, err := formatSlackPayload(alert)
	if err != nil {
		t.Fatalf("formatSlackPayload error: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Has text field (Slack fallback)
	text, ok := parsed["text"].(string)
	if !ok || text == "" {
		t.Error("missing or empty text field")
	}
	if !strings.Contains(text, "Sync failed") {
		t.Errorf("text should contain message, got: %s", text)
	}

	// Has attachments with color
	attachments, ok := parsed["attachments"].([]interface{})
	if !ok || len(attachments) == 0 {
		t.Fatal("missing attachments")
	}
	att := attachments[0].(map[string]interface{})
	color, ok := att["color"].(string)
	if !ok || color == "" {
		t.Error("missing color in attachment")
	}
}

func TestFormatDiscordPayload(t *testing.T) {
	alert := Alert{
		Type:       AlertTypeRecovery,
		SourceID:   "src-456",
		SourceName: "iCloud Caldav",
		Message:    "Source recovered",
		Details:    "Back online",
		Timestamp:  time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC),
	}

	body, err := formatDiscordPayload(alert)
	if err != nil {
		t.Fatalf("formatDiscordPayload error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Has content (Discord fallback text)
	content, ok := parsed["content"].(string)
	if !ok || content == "" {
		t.Error("missing content")
	}

	// Has embeds with color + fields
	embeds, ok := parsed["embeds"].([]interface{})
	if !ok || len(embeds) == 0 {
		t.Fatal("missing embeds")
	}
	embed := embeds[0].(map[string]interface{})
	if embed["title"] != "CalBridgeSync Alert" {
		t.Errorf("unexpected title: %v", embed["title"])
	}
	if embed["color"] == nil {
		t.Error("missing color in embed")
	}
	fields, ok := embed["fields"].([]interface{})
	if !ok || len(fields) < 2 {
		t.Error("expected at least 2 fields in embed")
	}
}

func TestBuildWebhookPayload_RoutesToCorrectFormatter(t *testing.T) {
	alert := Alert{
		Type:       AlertTypeStale,
		SourceID:   "src-789",
		SourceName: "Test Source",
		Message:    "Source stale",
		Details:    "No sync in 2 hours",
		Timestamp:  time.Now(),
	}

	t.Run("slack URL gets attachments", func(t *testing.T) {
		body, err := buildWebhookPayload(alert, "https://hooks.slack.com/services/T00/B00/xxx")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "attachments") {
			t.Error("Slack payload should contain 'attachments'")
		}
	})

	t.Run("discord URL gets embeds", func(t *testing.T) {
		body, err := buildWebhookPayload(alert, "https://discord.com/api/webhooks/123/abc")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "embeds") {
			t.Error("Discord payload should contain 'embeds'")
		}
	})

	t.Run("generic URL gets WebhookPayload", func(t *testing.T) {
		body, err := buildWebhookPayload(alert, "https://example.com/webhook")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "alert_type") {
			t.Error("Generic payload should contain 'alert_type'")
		}
	})
}
