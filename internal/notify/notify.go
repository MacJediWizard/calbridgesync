package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	// emailRegex is a simple email validation regex
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
)

// AlertType represents the type of alert.
type AlertType string

const (
	AlertTypeStale    AlertType = "stale"
	AlertTypeRecovery AlertType = "recovery"
	AlertTypeError    AlertType = "error"
)

// Alert represents a notification alert.
type Alert struct {
	Type       AlertType
	SourceID   string
	SourceName string
	UserEmail  string // Email of the user who owns the source
	Message    string
	Details    string
	Timestamp  time.Time
}

// Config holds notification configuration.
type Config struct {
	// Webhook settings
	WebhookEnabled bool
	WebhookURL     string

	// Email settings
	EmailEnabled bool
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTo       []string // Recipients
	SMTPTLS      bool

	// Alert settings
	CooldownPeriod time.Duration // How long to wait before re-alerting for same source
}

// UserPreferences holds per-user alert preferences.
// Nil values mean "use global default".
type UserPreferences struct {
	EmailEnabled    *bool
	WebhookEnabled  *bool
	WebhookURL      string // Empty = no personal webhook
	CooldownMinutes *int
}

// Notifier sends alert notifications.
type Notifier struct {
	cfg        *Config
	httpClient *http.Client

	// Track last SUCCESSFUL alert time per source to implement cooldown.
	// Previously this was set BEFORE the background send fired, so a
	// failed send still consumed the cooldown window. Since Issue #33,
	// the timestamp is recorded only after sendWithPrefs confirms at
	// least one channel delivered.
	mu             sync.RWMutex
	lastAlertTimes map[string]time.Time
	staleState     map[string]bool // Track if source is currently in stale state

	// Failure alert cooldown is tracked separately from stale alerts so a
	// source that is both stale and failing doesn't lose one signal because
	// the other already consumed the cooldown window.
	lastFailureAlertTimes map[string]time.Time

	// inFlightAlerts tracks sends that have been queued but not yet
	// completed, keyed by sourceID. Prevents duplicate alerts when
	// concurrent callers both see "no cooldown set" and both try to
	// fire. The flag is set synchronously in the Send* method and
	// cleared inside the background goroutine after sendWithPrefs
	// returns, regardless of delivery success.
	inFlightAlerts map[string]bool
}

// New creates a new Notifier.
func New(cfg *Config) *Notifier {
	return &Notifier{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		lastAlertTimes:        make(map[string]time.Time),
		staleState:            make(map[string]bool),
		lastFailureAlertTimes: make(map[string]time.Time),
		inFlightAlerts:        make(map[string]bool),
	}
}

// ValidateConfig validates the notification configuration.
// Returns an error if the configuration is invalid.
func ValidateConfig(cfg *Config) error {
	if cfg.WebhookEnabled {
		if cfg.WebhookURL == "" {
			return fmt.Errorf("webhook URL is required when webhook is enabled")
		}
		if err := validateWebhookURL(cfg.WebhookURL); err != nil {
			return fmt.Errorf("invalid webhook URL: %w", err)
		}
	}

	if cfg.EmailEnabled {
		if cfg.SMTPHost == "" {
			return fmt.Errorf("SMTP host is required when email is enabled")
		}
		if cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 {
			return fmt.Errorf("SMTP port must be between 1 and 65535")
		}
		if cfg.SMTPFrom == "" {
			return fmt.Errorf("SMTP from address is required when email is enabled")
		}
		if !isValidEmail(cfg.SMTPFrom) {
			return fmt.Errorf("invalid SMTP from address")
		}
		for _, to := range cfg.SMTPTo {
			if !isValidEmail(to) {
				return fmt.Errorf("invalid SMTP recipient address: %s", to)
			}
		}
	}

	if cfg.CooldownPeriod < time.Minute {
		return fmt.Errorf("cooldown period must be at least 1 minute")
	}

	return nil
}

// validateWebhookURL validates that the webhook URL is safe to use.
func validateWebhookURL(webhookURL string) error {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow HTTPS for webhooks (security requirement)
	if parsed.Scheme != "https" {
		return fmt.Errorf("webhook URL must use HTTPS")
	}

	// Block localhost and private IP ranges to prevent SSRF
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return fmt.Errorf("webhook URL cannot point to localhost")
	}

	// Block common internal hostnames
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("webhook URL cannot point to internal hosts")
	}

	// Block private IP ranges (10.x.x.x, 172.16-31.x.x, 192.168.x.x)
	if strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "172.16.") ||
		strings.HasPrefix(host, "172.17.") ||
		strings.HasPrefix(host, "172.18.") ||
		strings.HasPrefix(host, "172.19.") ||
		strings.HasPrefix(host, "172.20.") ||
		strings.HasPrefix(host, "172.21.") ||
		strings.HasPrefix(host, "172.22.") ||
		strings.HasPrefix(host, "172.23.") ||
		strings.HasPrefix(host, "172.24.") ||
		strings.HasPrefix(host, "172.25.") ||
		strings.HasPrefix(host, "172.26.") ||
		strings.HasPrefix(host, "172.27.") ||
		strings.HasPrefix(host, "172.28.") ||
		strings.HasPrefix(host, "172.29.") ||
		strings.HasPrefix(host, "172.30.") ||
		strings.HasPrefix(host, "172.31.") {
		return fmt.Errorf("webhook URL cannot point to private IP addresses")
	}

	return nil
}

// isValidEmail validates an email address format.
func isValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

// sanitizeForEmail removes characters that could be used for email header injection.
func sanitizeForEmail(s string) string {
	// Remove CR and LF characters that could inject headers
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	// Limit length to prevent abuse
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// IsEnabled returns true if any notification method is enabled.
func (n *Notifier) IsEnabled() bool {
	return n.cfg.WebhookEnabled || n.cfg.EmailEnabled
}

// SendStaleAlert sends an alert for a stale source.
// userEmail is the email of the user who owns the source (for per-user notifications).
// Returns true if alert was sent, false if still in cooldown.
func (n *Notifier) SendStaleAlert(ctx context.Context, sourceID, sourceName, userEmail string, timeSinceSync, threshold time.Duration) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if already in stale state and in cooldown
	if n.staleState[sourceID] {
		lastAlert, exists := n.lastAlertTimes[sourceID]
		if exists && time.Since(lastAlert) < n.cfg.CooldownPeriod {
			return false // Still in cooldown
		}
	}

	// Mark as stale and update alert time
	n.staleState[sourceID] = true
	n.lastAlertTimes[sourceID] = time.Now()

	alert := Alert{
		Type:       AlertTypeStale,
		SourceID:   sourceID,
		SourceName: sourceName,
		UserEmail:  userEmail,
		Message:    fmt.Sprintf("Source '%s' is stale", sourceName),
		Details:    fmt.Sprintf("Last sync was %v ago (threshold: %v)", timeSinceSync.Round(time.Minute), threshold),
		Timestamp:  time.Now(),
	}

	// Send in background to not block
	go n.send(ctx, alert)
	return true
}

// SendRecoveryAlert sends an alert when a source recovers from stale state.
// userEmail is the email of the user who owns the source (for per-user notifications).
func (n *Notifier) SendRecoveryAlert(ctx context.Context, sourceID, sourceName, userEmail string) bool {
	n.mu.Lock()
	wasStale := n.staleState[sourceID]
	if wasStale {
		delete(n.staleState, sourceID)
		delete(n.lastAlertTimes, sourceID)
	}
	n.mu.Unlock()

	if !wasStale {
		return false // Wasn't stale, no need to send recovery
	}

	alert := Alert{
		Type:       AlertTypeRecovery,
		SourceID:   sourceID,
		SourceName: sourceName,
		UserEmail:  userEmail,
		Message:    fmt.Sprintf("Source '%s' has recovered", sourceName),
		Details:    "Source is now syncing normally",
		Timestamp:  time.Now(),
	}

	go n.send(ctx, alert)
	return true
}

// send sends the alert via all configured channels.
func (n *Notifier) send(ctx context.Context, alert Alert) {
	if n.cfg.WebhookEnabled && n.cfg.WebhookURL != "" {
		if err := n.sendWebhook(ctx, alert); err != nil {
			log.Printf("[Notify] Webhook error: %v", err)
		}
	}

	if n.cfg.EmailEnabled {
		// Build recipient list: user email + admin emails (deduplicated)
		recipientSet := make(map[string]struct{})

		// Add user email if provided and valid
		if alert.UserEmail != "" && isValidEmail(alert.UserEmail) {
			recipientSet[strings.ToLower(alert.UserEmail)] = struct{}{}
		}

		// Add admin emails
		for _, email := range n.cfg.SMTPTo {
			recipientSet[strings.ToLower(email)] = struct{}{}
		}

		// Convert to slice
		recipients := make([]string, 0, len(recipientSet))
		for email := range recipientSet {
			recipients = append(recipients, email)
		}

		if len(recipients) > 0 {
			if err := n.sendEmail(ctx, alert, recipients); err != nil {
				log.Printf("[Notify] Email error: %v", err)
			}
		}
	}
}

// WebhookPayload is the JSON payload sent to webhooks.
type WebhookPayload struct {
	AlertType  string `json:"alert_type"`
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	Message    string `json:"message"`
	Details    string `json:"details"`
	Timestamp  string `json:"timestamp"`
	// Slack-compatible fields
	Text string `json:"text,omitempty"`
}

func (n *Notifier) sendWebhook(ctx context.Context, alert Alert) error {
	// Build Slack-compatible message.
	// Idempotent setup lives outside the retry: re-marshaling the same
	// payload every attempt is wasted work.
	emoji := ""
	switch alert.Type {
	case AlertTypeStale:
		emoji = ":warning:"
	case AlertTypeRecovery:
		emoji = ":white_check_mark:"
	case AlertTypeError:
		emoji = ":x:"
	}

	payload := WebhookPayload{
		AlertType:  string(alert.Type),
		SourceID:   alert.SourceID,
		SourceName: alert.SourceName,
		Message:    alert.Message,
		Details:    alert.Details,
		Timestamp:  alert.Timestamp.Format(time.RFC3339),
		Text:       fmt.Sprintf("%s *%s*\n%s", emoji, alert.Message, alert.Details),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	// Retry the HTTP call itself on transient failures (DNS, TCP, TLS,
	// 5xx). Permanent errors (4xx, request construction) fall through
	// immediately — see isTransientHTTPError for the full taxonomy.
	return retryTransient(ctx, defaultMaxSendAttempts, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, "POST", n.cfg.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := n.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}

		log.Printf("[Notify] Webhook sent: %s", alert.Message)
		return nil
	}, isTransientHTTPError)
}

func (n *Notifier) sendEmail(ctx context.Context, alert Alert, recipients []string) error {
	// Sanitize user-controlled inputs to prevent email header injection.
	// This is idempotent setup and stays outside the retry.
	sanitizedSourceName := sanitizeForEmail(alert.SourceName)
	sanitizedMessage := sanitizeForEmail(alert.Message)
	sanitizedDetails := sanitizeForEmail(alert.Details)

	subject := fmt.Sprintf("[CalBridgeSync] %s", sanitizedMessage)

	// Build email body
	var body strings.Builder
	body.WriteString(fmt.Sprintf("Alert Type: %s\n", alert.Type))
	body.WriteString(fmt.Sprintf("Source: %s\n", sanitizedSourceName))
	body.WriteString(fmt.Sprintf("Source ID: %s\n", alert.SourceID))
	body.WriteString(fmt.Sprintf("Time: %s\n\n", alert.Timestamp.Format(time.RFC1123)))
	body.WriteString(fmt.Sprintf("Message: %s\n", sanitizedMessage))
	body.WriteString(fmt.Sprintf("Details: %s\n", sanitizedDetails))

	// Build email message with proper MIME headers
	to := strings.Join(recipients, ", ")
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		n.cfg.SMTPFrom, to, subject, body.String())

	addr := fmt.Sprintf("%s:%d", n.cfg.SMTPHost, n.cfg.SMTPPort)

	var auth smtp.Auth
	if n.cfg.SMTPUsername != "" {
		auth = smtp.PlainAuth("", n.cfg.SMTPUsername, n.cfg.SMTPPassword, n.cfg.SMTPHost)
	}

	// Retry transient SMTP failures. SMTP errors are mostly transient
	// (connection drops, TLS hiccups, temporary server rejection) and
	// the isTransientSMTPError classifier is intentionally permissive —
	// the outer cooldown loop (PR #34) will eventually give up on
	// persistently broken destinations by not retrying for another
	// full cooldown window.
	//
	// Context is honored during backoff sleeps via retryTransient.
	// Note: the stdlib smtp.SendMail itself does not take a context,
	// so a mid-attempt cancellation only affects the sleep between
	// attempts, not the send in progress.
	return retryTransient(ctx, defaultMaxSendAttempts, func(ctx context.Context) error {
		var err error
		if n.cfg.SMTPTLS {
			err = n.sendEmailTLS(addr, auth, n.cfg.SMTPFrom, recipients, []byte(msg))
		} else {
			err = smtp.SendMail(addr, auth, n.cfg.SMTPFrom, recipients, []byte(msg))
		}
		if err != nil {
			return fmt.Errorf("send email: %w", err)
		}
		log.Printf("[Notify] Email sent to %d recipients: %s", len(recipients), sanitizedMessage)
		return nil
	}, isTransientSMTPError)
}

// sendEmailTLS sends email over TLS (for port 465).
func (n *Notifier) sendEmailTLS(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	tlsConfig := &tls.Config{
		ServerName: n.cfg.SMTPHost,
		MinVersion: tls.VersionTLS12, // Require TLS 1.2 or higher for security
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("dial TLS: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, n.cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}

	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("rcpt to %s: %w", recipient, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}

	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return client.Quit()
}

// ClearStaleState clears the stale state for a source (used on source deletion).
// Also clears any in-flight stale alert guard so a new source reusing the
// same ID starts with a clean slate.
func (n *Notifier) ClearStaleState(sourceID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.staleState, sourceID)
	delete(n.lastAlertTimes, sourceID)
	delete(n.inFlightAlerts, "stale:"+sourceID)
}

// GetStaleSourceIDs returns a list of currently stale source IDs.
func (n *Notifier) GetStaleSourceIDs() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	ids := make([]string, 0, len(n.staleState))
	for id, isStale := range n.staleState {
		if isStale {
			ids = append(ids, id)
		}
	}
	return ids
}

// SendStaleAlertWithPrefs sends an alert for a stale source using per-user preferences.
// userPrefs can be nil to use global defaults only.
//
// Since Issue #33, the cooldown timestamp is recorded only AFTER the
// background send reports successful delivery. Previously it was set
// synchronously before the goroutine fired, which meant a failed send
// consumed the cooldown and the user got zero alerts for the entire
// cooldown period on a broken alert endpoint.
//
// To prevent concurrent duplicate sends (two callers both seeing no
// cooldown and both firing), an inFlightAlerts guard suppresses overlap
// while a previous send is still in-flight.
func (n *Notifier) SendStaleAlertWithPrefs(ctx context.Context, sourceID, sourceName, userEmail string, timeSinceSync, threshold time.Duration, userPrefs *UserPreferences) bool {
	cooldown := n.getCooldownPeriod(userPrefs)

	// inFlightAlerts is keyed by "{type}:{sourceID}" so stale and failure
	// alerts have independent in-flight slots and can fire concurrently
	// for the same source.
	inFlightKey := "stale:" + sourceID

	n.mu.Lock()

	// Check if already in stale state and in cooldown. lastAlertTimes
	// now reflects the last SUCCESSFUL delivery, so a failed prior send
	// won't block this one.
	if n.staleState[sourceID] {
		if lastAlert, exists := n.lastAlertTimes[sourceID]; exists && time.Since(lastAlert) < cooldown {
			n.mu.Unlock()
			return false // Still in cooldown
		}
	}

	// Deduplication: if a stale alert for this source is already in
	// flight, suppress this one to avoid two concurrent sends.
	if n.inFlightAlerts[inFlightKey] {
		n.mu.Unlock()
		return false
	}
	n.inFlightAlerts[inFlightKey] = true
	n.staleState[sourceID] = true

	n.mu.Unlock()

	alert := Alert{
		Type:       AlertTypeStale,
		SourceID:   sourceID,
		SourceName: sourceName,
		UserEmail:  userEmail,
		Message:    fmt.Sprintf("Source '%s' is stale", sourceName),
		Details:    fmt.Sprintf("Last sync was %v ago (threshold: %v)", timeSinceSync.Round(time.Minute), threshold),
		Timestamp:  time.Now(),
	}

	// Send in background so the scheduler tick isn't blocked by SMTP
	// or webhook latency. The cooldown is recorded inside the goroutine
	// only if sendWithPrefs reports that at least one channel delivered.
	go func() {
		delivered := n.sendWithPrefs(ctx, alert, userPrefs)
		n.mu.Lock()
		delete(n.inFlightAlerts, inFlightKey)
		if delivered {
			n.lastAlertTimes[sourceID] = time.Now()
		}
		n.mu.Unlock()
	}()
	return true
}

// SendRecoveryAlertWithPrefs sends an alert when a source recovers, using per-user preferences.
// userPrefs can be nil to use global defaults only.
func (n *Notifier) SendRecoveryAlertWithPrefs(ctx context.Context, sourceID, sourceName, userEmail string, userPrefs *UserPreferences) bool {
	n.mu.Lock()
	wasStale := n.staleState[sourceID]
	if wasStale {
		delete(n.staleState, sourceID)
		delete(n.lastAlertTimes, sourceID)
	}
	n.mu.Unlock()

	if !wasStale {
		return false // Wasn't stale, no need to send recovery
	}

	alert := Alert{
		Type:       AlertTypeRecovery,
		SourceID:   sourceID,
		SourceName: sourceName,
		UserEmail:  userEmail,
		Message:    fmt.Sprintf("Source '%s' has recovered", sourceName),
		Details:    "Source is now syncing normally",
		Timestamp:  time.Now(),
	}

	go n.sendWithPrefs(ctx, alert, userPrefs)
	return true
}

// getCooldownPeriod returns the effective cooldown period, considering user preferences.
func (n *Notifier) getCooldownPeriod(userPrefs *UserPreferences) time.Duration {
	if userPrefs != nil && userPrefs.CooldownMinutes != nil {
		return time.Duration(*userPrefs.CooldownMinutes) * time.Minute
	}
	return n.cfg.CooldownPeriod
}

// SendSyncFailureAlertWithPrefs sends an alert when a sync fails or when a
// data-loss protection guard is triggered, using per-user preferences.
// userPrefs can be nil to use global defaults only.
//
// This method has its own cooldown window independent from stale alerts:
// a source that is both stale AND failing will not lose one signal because
// the other already consumed the cooldown.
//
// Since Issue #33, the cooldown timestamp is recorded only AFTER the
// background send reports successful delivery. Previously it was set
// synchronously before the goroutine fired, which meant a failed send
// consumed the cooldown for the full window.
//
// errorMessage is a short human-readable summary shown in the alert subject.
// details is the full error/warning text shown in the alert body.
//
// Returns true if the alert was queued for send, false if still in cooldown
// or a prior send is still in flight.
func (n *Notifier) SendSyncFailureAlertWithPrefs(
	ctx context.Context,
	sourceID, sourceName, userEmail, errorMessage, details string,
	userPrefs *UserPreferences,
) bool {
	cooldown := n.getCooldownPeriod(userPrefs)
	inFlightKey := "failure:" + sourceID

	n.mu.Lock()

	// Cooldown check — failure alerts use their own map, independent of
	// the stale-alert cooldown. lastFailureAlertTimes now reflects the
	// last SUCCESSFUL delivery.
	if lastAlert, exists := n.lastFailureAlertTimes[sourceID]; exists && time.Since(lastAlert) < cooldown {
		n.mu.Unlock()
		return false
	}

	// Deduplication: if a failure alert for this source is already in
	// flight, suppress this one.
	if n.inFlightAlerts[inFlightKey] {
		n.mu.Unlock()
		return false
	}
	n.inFlightAlerts[inFlightKey] = true

	n.mu.Unlock()

	alert := Alert{
		Type:       AlertTypeError,
		SourceID:   sourceID,
		SourceName: sourceName,
		UserEmail:  userEmail,
		Message:    errorMessage,
		Details:    details,
		Timestamp:  time.Now(),
	}

	// Send in background. Cooldown is recorded inside the goroutine only
	// if at least one channel delivered.
	go func() {
		delivered := n.sendWithPrefs(ctx, alert, userPrefs)
		n.mu.Lock()
		delete(n.inFlightAlerts, inFlightKey)
		if delivered {
			n.lastFailureAlertTimes[sourceID] = time.Now()
		}
		n.mu.Unlock()
	}()
	return true
}

// ClearFailureAlertState clears the failure-alert cooldown for a source
// (used on source deletion). Safe to call for sources that have never
// triggered a failure alert. Also clears any in-flight failure alert
// guard so a new source reusing the same ID starts with a clean slate.
func (n *Notifier) ClearFailureAlertState(sourceID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.lastFailureAlertTimes, sourceID)
	delete(n.inFlightAlerts, "failure:"+sourceID)
}

// IsDangerousWarning returns true if a sync warning indicates a data-loss
// protection guard was triggered and the user should be alerted, even if
// the overall sync result reports success.
//
// These patterns are produced by planOrphanDeletion in internal/caldav/sync.go
// (added in PR #22, issue #21). They represent cases where the sync engine
// refused to delete destination events because the inputs looked unsafe
// (source returned zero events, or the planned deletion ratio exceeded the
// configured safety threshold).
//
// Harmless warnings (individual event put/delete failures, 403/412 skip
// counts, etc.) do NOT match and do not trigger alerts.
func IsDangerousWarning(w string) bool {
	if w == "" {
		return false
	}
	return strings.Contains(w, "previously-synced records exist") ||
		strings.Contains(w, "exceeds safety threshold")
}

// sendWithPrefs sends the alert via configured channels, respecting per-user
// preferences. Returns true if at least one configured channel delivered
// successfully — meaning the user actually received a notification.
// Returns false if all configured channels failed, in which case the caller
// must NOT record the cooldown timestamp (so the next retry can fire
// immediately instead of being silenced for the full cooldown window).
//
// Since Issue #33, this return value is what distinguishes
// "alert delivered, start the cooldown" from "alert attempted but bounced,
// please retry on the next tick".
func (n *Notifier) sendWithPrefs(ctx context.Context, alert Alert, userPrefs *UserPreferences) bool {
	anyAttempted := false
	anyDelivered := false

	// Determine if webhook is enabled (user pref overrides global)
	webhookEnabled := n.cfg.WebhookEnabled
	if userPrefs != nil && userPrefs.WebhookEnabled != nil {
		webhookEnabled = *userPrefs.WebhookEnabled
	}

	// Send to global webhook if enabled
	if webhookEnabled && n.cfg.WebhookURL != "" {
		anyAttempted = true
		if err := n.sendWebhook(ctx, alert); err != nil {
			log.Printf("[Notify] Webhook error: %v", err)
		} else {
			anyDelivered = true
		}
	}

	// Send to user's personal webhook if configured and enabled
	if userPrefs != nil && userPrefs.WebhookURL != "" {
		userWebhookEnabled := true // Default to enabled if URL is set
		if userPrefs.WebhookEnabled != nil {
			userWebhookEnabled = *userPrefs.WebhookEnabled
		}
		if userWebhookEnabled {
			anyAttempted = true
			if err := n.sendWebhookToURL(ctx, alert, userPrefs.WebhookURL); err != nil {
				log.Printf("[Notify] User webhook error: %v", err)
			} else {
				anyDelivered = true
			}
		}
	}

	// Determine if email is enabled (user pref overrides global)
	emailEnabled := n.cfg.EmailEnabled
	if userPrefs != nil && userPrefs.EmailEnabled != nil {
		emailEnabled = *userPrefs.EmailEnabled
	}

	if emailEnabled {
		// Build recipient list: user email + admin emails (deduplicated)
		recipientSet := make(map[string]struct{})

		// Add user email if provided and valid
		if alert.UserEmail != "" && isValidEmail(alert.UserEmail) {
			recipientSet[strings.ToLower(alert.UserEmail)] = struct{}{}
		}

		// Add admin emails
		for _, email := range n.cfg.SMTPTo {
			recipientSet[strings.ToLower(email)] = struct{}{}
		}

		// Convert to slice
		recipients := make([]string, 0, len(recipientSet))
		for email := range recipientSet {
			recipients = append(recipients, email)
		}

		if len(recipients) > 0 {
			anyAttempted = true
			if err := n.sendEmail(ctx, alert, recipients); err != nil {
				log.Printf("[Notify] Email error: %v", err)
			} else {
				anyDelivered = true
			}
		}
	}

	// If no channel was even configured/attempted, treat as "delivered" so
	// the cooldown still applies — otherwise a notifier with no channels
	// would busy-loop the scheduler. This matches the prior fire-and-forget
	// behavior for the no-channel case.
	if !anyAttempted {
		return true
	}
	return anyDelivered
}

// sendWebhookToURL sends a webhook to a specific URL (for user webhooks).
func (n *Notifier) sendWebhookToURL(ctx context.Context, alert Alert, webhookURL string) error {
	// Validate URL before sending (security check). Validation failures
	// are permanent — no point retrying a URL that will never pass.
	if err := validateWebhookURL(webhookURL); err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}

	// Build Slack-compatible message. Idempotent setup stays outside
	// the retry.
	emoji := ""
	switch alert.Type {
	case AlertTypeStale:
		emoji = ":warning:"
	case AlertTypeRecovery:
		emoji = ":white_check_mark:"
	case AlertTypeError:
		emoji = ":x:"
	}

	payload := WebhookPayload{
		AlertType:  string(alert.Type),
		SourceID:   alert.SourceID,
		SourceName: alert.SourceName,
		Message:    alert.Message,
		Details:    alert.Details,
		Timestamp:  alert.Timestamp.Format(time.RFC3339),
		Text:       fmt.Sprintf("%s *%s*\n%s", emoji, alert.Message, alert.Details),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	return retryTransient(ctx, defaultMaxSendAttempts, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := n.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}

		log.Printf("[Notify] User webhook sent: %s", alert.Message)
		return nil
	}, isTransientHTTPError)
}

// SendTestWebhook sends a test message to a webhook URL.
// Returns an error if the webhook fails or URL is invalid.
func (n *Notifier) SendTestWebhook(ctx context.Context, webhookURL string) error {
	// Validate URL before sending (security check)
	if err := validateWebhookURL(webhookURL); err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}

	payload := WebhookPayload{
		AlertType:  "test",
		SourceID:   "test",
		SourceName: "Test",
		Message:    "Test webhook from CalBridgeSync",
		Details:    "This is a test message to verify your webhook configuration",
		Timestamp:  time.Now().Format(time.RFC3339),
		Text:       ":rocket: *Test webhook from CalBridgeSync*\nThis is a test message to verify your webhook configuration",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// ValidateWebhookURL validates that a webhook URL is safe to use (exported for API use).
func ValidateWebhookURL(webhookURL string) error {
	return validateWebhookURL(webhookURL)
}
