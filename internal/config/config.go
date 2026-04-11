package config

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/macjediwizard/calbridgesync/internal/validator"
)

var (
	ErrMissingConfig     = errors.New("missing required configuration")
	ErrInvalidConfig     = errors.New("invalid configuration value")
	ErrEncryptionKeySize = errors.New("encryption key must be exactly 32 bytes (64 hex characters)")
	ErrSessionSecretSize = errors.New("session secret must be at least 32 characters")
	ErrValidationFailed  = errors.New("configuration validation failed")
)

// Environment represents the deployment environment.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// Config holds all application configuration.
type Config struct {
	Server       ServerConfig
	OIDC         OIDCConfig
	Security     SecurityConfig
	Database     DatabaseConfig
	CalDAV       CalDAVConfig
	RateLimiting RateLimitConfig
	Sync         SyncConfig
	Alerts       AlertConfig
	GoogleOAuth  GoogleOAuthConfig
}

// GoogleOAuthConfig holds the OAuth2 credentials for Google Calendar
// source_type. These are optional: if ClientID or ClientSecret is unset,
// the feature is disabled and the web UI surfaces a clear error instead
// of letting the user start a flow that would 401 at the end. (#70)
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Enabled returns true if both client ID and secret are configured.
// Call this before routing users into the Google OAuth flow.
func (g GoogleOAuthConfig) Enabled() bool {
	return g.ClientID != "" && g.ClientSecret != ""
}

// AlertConfig holds alerting configuration.
type AlertConfig struct {
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
	SMTPTo       []string
	SMTPTLS      bool

	// Cooldown period in minutes (default: 60)
	CooldownMinutes int

	// Retry configuration (Issue #64). Wired from ALERT_MAX_SEND_ATTEMPTS
	// and ALERT_INITIAL_BACKOFF_MS env vars. Zero values fall back to the
	// defaults in the notify package (3 attempts, 500ms).
	MaxSendAttempts  int
	InitialBackoffMS int
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Port        int
	BaseURL     string
	Environment Environment
}

// OIDCConfig holds OIDC authentication configuration.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// SecurityConfig holds security-related configuration.
type SecurityConfig struct {
	EncryptionKey        []byte
	SessionSecret        string
	SessionMaxAgeSecs    int // Session timeout in seconds (default: 86400 = 24 hours)
	OAuthStateMaxAgeSecs int // OAuth state timeout in seconds (default: 300 = 5 minutes)
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	Path string
}

// CalDAVConfig holds CalDAV-related configuration.
type CalDAVConfig struct {
	DefaultDestURL     string
	RequestTimeoutSecs int // HTTP request timeout in seconds (default: 300 = 5 minutes)
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	RPS   float64
	Burst int
}

// SyncConfig holds sync interval configuration.
type SyncConfig struct {
	MinInterval int
	MaxInterval int
}

// Load loads configuration from environment variables.
// It attempts to load from .env file first, but continues if not found.
func Load() (*Config, error) {
	// Attempt to load .env file (ignore error if not found)
	_ = godotenv.Load() //nolint:errcheck // Intentionally ignore - .env file is optional

	cfg := &Config{}

	// Server configuration
	port, err := getEnvInt("PORT", 8080)
	if err != nil {
		return nil, fmt.Errorf("%w: PORT: %w", ErrInvalidConfig, err)
	}
	cfg.Server.Port = port
	cfg.Server.BaseURL = getEnvRequired("BASE_URL")
	cfg.Server.Environment = Environment(strings.ToLower(getEnv("ENVIRONMENT", "production")))

	// OIDC configuration
	cfg.OIDC.Issuer = getEnvRequired("OIDC_ISSUER")
	cfg.OIDC.ClientID = getEnvRequired("OIDC_CLIENT_ID")
	cfg.OIDC.ClientSecret = getEnvRequired("OIDC_CLIENT_SECRET")
	cfg.OIDC.RedirectURL = getEnvRequired("OIDC_REDIRECT_URL")

	// Security configuration
	encKeyHex := getEnvRequired("ENCRYPTION_KEY")
	if encKeyHex != "" {
		encKey, err := hex.DecodeString(encKeyHex)
		if err != nil {
			return nil, fmt.Errorf("%w: ENCRYPTION_KEY: invalid hex: %w", ErrInvalidConfig, err)
		}
		if len(encKey) != 32 {
			return nil, ErrEncryptionKeySize
		}
		cfg.Security.EncryptionKey = encKey
	}

	cfg.Security.SessionSecret = getEnvRequired("SESSION_SECRET")
	if cfg.Security.SessionSecret != "" && len(cfg.Security.SessionSecret) < 32 {
		return nil, ErrSessionSecretSize
	}

	// Session timeout - default 24 hours (reduced from 7 days for security)
	sessionMaxAge, err := getEnvInt("SESSION_MAX_AGE_SECS", 86400)
	if err != nil {
		return nil, fmt.Errorf("%w: SESSION_MAX_AGE_SECS: %w", ErrInvalidConfig, err)
	}
	cfg.Security.SessionMaxAgeSecs = sessionMaxAge

	// OAuth state timeout - default 5 minutes (OWASP recommended)
	oauthStateMaxAge, err := getEnvInt("OAUTH_STATE_MAX_AGE_SECS", 300)
	if err != nil {
		return nil, fmt.Errorf("%w: OAUTH_STATE_MAX_AGE_SECS: %w", ErrInvalidConfig, err)
	}
	cfg.Security.OAuthStateMaxAgeSecs = oauthStateMaxAge

	// Database configuration
	cfg.Database.Path = getEnv("DATABASE_PATH", "./data/calbridgesync.db")

	// CalDAV configuration
	cfg.CalDAV.DefaultDestURL = getEnvRequired("DEFAULT_DEST_URL")
	caldavTimeout, err := getEnvInt("CALDAV_REQUEST_TIMEOUT", 300)
	if err != nil {
		return nil, fmt.Errorf("%w: CALDAV_REQUEST_TIMEOUT: %w", ErrInvalidConfig, err)
	}
	cfg.CalDAV.RequestTimeoutSecs = caldavTimeout

	// Rate limiting configuration
	rps, err := getEnvFloat("RATE_LIMIT_RPS", 10.0)
	if err != nil {
		return nil, fmt.Errorf("%w: RATE_LIMIT_RPS: %w", ErrInvalidConfig, err)
	}
	cfg.RateLimiting.RPS = rps

	burst, err := getEnvInt("RATE_LIMIT_BURST", 20)
	if err != nil {
		return nil, fmt.Errorf("%w: RATE_LIMIT_BURST: %w", ErrInvalidConfig, err)
	}
	cfg.RateLimiting.Burst = burst

	// Sync configuration
	minInterval, err := getEnvInt("MIN_SYNC_INTERVAL", 30)
	if err != nil {
		return nil, fmt.Errorf("%w: MIN_SYNC_INTERVAL: %w", ErrInvalidConfig, err)
	}
	cfg.Sync.MinInterval = minInterval

	maxInterval, err := getEnvInt("MAX_SYNC_INTERVAL", 3600)
	if err != nil {
		return nil, fmt.Errorf("%w: MAX_SYNC_INTERVAL: %w", ErrInvalidConfig, err)
	}
	cfg.Sync.MaxInterval = maxInterval

	// Alert configuration (all optional)
	cfg.Alerts.WebhookEnabled = getEnv("ALERT_WEBHOOK_ENABLED", "") == "true"
	cfg.Alerts.WebhookURL = getEnv("ALERT_WEBHOOK_URL", "")

	cfg.Alerts.EmailEnabled = getEnv("ALERT_EMAIL_ENABLED", "") == "true"
	cfg.Alerts.SMTPHost = getEnv("ALERT_SMTP_HOST", "")
	smtpPort, err := getEnvInt("ALERT_SMTP_PORT", 587)
	if err != nil {
		return nil, fmt.Errorf("%w: ALERT_SMTP_PORT: %w", ErrInvalidConfig, err)
	}
	cfg.Alerts.SMTPPort = smtpPort
	cfg.Alerts.SMTPUsername = getEnv("ALERT_SMTP_USERNAME", "")
	cfg.Alerts.SMTPPassword = getEnv("ALERT_SMTP_PASSWORD", "")
	cfg.Alerts.SMTPFrom = getEnv("ALERT_SMTP_FROM", "")
	smtpTo := getEnv("ALERT_SMTP_TO", "")
	if smtpTo != "" {
		cfg.Alerts.SMTPTo = strings.Split(smtpTo, ",")
		for i, addr := range cfg.Alerts.SMTPTo {
			cfg.Alerts.SMTPTo[i] = strings.TrimSpace(addr)
		}
	}
	cfg.Alerts.SMTPTLS = getEnv("ALERT_SMTP_TLS", "") == "true"

	cooldownMinutes, err := getEnvInt("ALERT_COOLDOWN_MINUTES", 60)
	if err != nil {
		return nil, fmt.Errorf("%w: ALERT_COOLDOWN_MINUTES: %w", ErrInvalidConfig, err)
	}
	cfg.Alerts.CooldownMinutes = cooldownMinutes

	// Retry tuning (Issue #64). Optional — unset means "use notify
	// package defaults" (3 attempts, 500ms initial backoff). Bounded
	// to prevent pathological values: zero or negative attempts would
	// silence all alerts; excessive backoff would delay them.
	maxAttempts, err := getEnvInt("ALERT_MAX_SEND_ATTEMPTS", 0)
	if err != nil {
		return nil, fmt.Errorf("%w: ALERT_MAX_SEND_ATTEMPTS: %w", ErrInvalidConfig, err)
	}
	if maxAttempts < 0 || maxAttempts > 10 {
		return nil, fmt.Errorf("%w: ALERT_MAX_SEND_ATTEMPTS must be between 0 and 10, got %d",
			ErrInvalidConfig, maxAttempts)
	}
	cfg.Alerts.MaxSendAttempts = maxAttempts

	initialBackoffMS, err := getEnvInt("ALERT_INITIAL_BACKOFF_MS", 0)
	if err != nil {
		return nil, fmt.Errorf("%w: ALERT_INITIAL_BACKOFF_MS: %w", ErrInvalidConfig, err)
	}
	if initialBackoffMS < 0 || initialBackoffMS > 10000 {
		return nil, fmt.Errorf("%w: ALERT_INITIAL_BACKOFF_MS must be between 0 and 10000, got %d",
			ErrInvalidConfig, initialBackoffMS)
	}
	cfg.Alerts.InitialBackoffMS = initialBackoffMS

	// Google OAuth2 configuration (optional; feature is disabled when
	// ClientID/ClientSecret are unset). (#70)
	cfg.GoogleOAuth.ClientID = getEnv("GOOGLE_OAUTH_CLIENT_ID", "")
	cfg.GoogleOAuth.ClientSecret = getEnv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	cfg.GoogleOAuth.RedirectURL = getEnv("GOOGLE_OAUTH_REDIRECT_URL", "")
	// If redirect URL is not explicitly set but base URL is, default to
	// <BASE_URL>/auth/oauth/google/callback. This matches the route
	// registered in internal/web/routes.go and means operators usually
	// only need to set the client id and secret.
	if cfg.GoogleOAuth.RedirectURL == "" && cfg.Server.BaseURL != "" && cfg.GoogleOAuth.ClientID != "" {
		cfg.GoogleOAuth.RedirectURL = strings.TrimRight(cfg.Server.BaseURL, "/") + "/auth/oauth/google/callback"
	}

	// Check for missing required configuration
	missing := cfg.getMissingRequired()
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrMissingConfig, strings.Join(missing, ", "))
	}

	return cfg, nil
}

// getMissingRequired returns a list of missing required configuration values.
func (c *Config) getMissingRequired() []string {
	var missing []string

	if c.Server.BaseURL == "" {
		missing = append(missing, "BASE_URL")
	}
	if c.OIDC.Issuer == "" {
		missing = append(missing, "OIDC_ISSUER")
	}
	if c.OIDC.ClientID == "" {
		missing = append(missing, "OIDC_CLIENT_ID")
	}
	if c.OIDC.ClientSecret == "" {
		missing = append(missing, "OIDC_CLIENT_SECRET")
	}
	if c.OIDC.RedirectURL == "" {
		missing = append(missing, "OIDC_REDIRECT_URL")
	}
	if len(c.Security.EncryptionKey) == 0 {
		missing = append(missing, "ENCRYPTION_KEY")
	}
	if c.Security.SessionSecret == "" {
		missing = append(missing, "SESSION_SECRET")
	}
	if c.CalDAV.DefaultDestURL == "" {
		missing = append(missing, "DEFAULT_DEST_URL")
	}

	return missing
}

// Validate validates all URLs are reachable.
func (c *Config) Validate(ctx context.Context) error {
	v := validator.New()

	// Validate base URL format
	if err := v.ValidateURL(c.Server.BaseURL, c.IsProduction()); err != nil {
		return fmt.Errorf("%w: BASE_URL: %w", ErrValidationFailed, err)
	}

	// Validate OIDC issuer is reachable
	if err := v.ValidateOIDCIssuer(ctx, c.OIDC.Issuer); err != nil {
		return fmt.Errorf("%w: OIDC_ISSUER: %w", ErrValidationFailed, err)
	}

	// Validate OIDC redirect URL format
	if err := v.ValidateURL(c.OIDC.RedirectURL, c.IsProduction()); err != nil {
		return fmt.Errorf("%w: OIDC_REDIRECT_URL: %w", ErrValidationFailed, err)
	}

	// Validate CalDAV default destination URL format
	if err := v.ValidateURL(c.CalDAV.DefaultDestURL, c.IsProduction()); err != nil {
		return fmt.Errorf("%w: DEFAULT_DEST_URL: %w", ErrValidationFailed, err)
	}

	// ALLOWED_ORIGINS is required in production mode. See
	// validateAllowedOriginsForProd for the full rationale.
	if err := validateAllowedOriginsForProd(c.IsProduction(), os.Getenv("ALLOWED_ORIGINS")); err != nil {
		return err
	}

	return nil
}

// validateAllowedOriginsForProd enforces that ALLOWED_ORIGINS is
// set when running in production mode. Returns nil in development
// mode or when the env var is non-empty. Returns a wrapped
// ErrValidationFailed otherwise.
//
// Without this check, internal/web/middleware.go falls back to
// localhost-only defaults — correct for dev, but silently blocks
// every legitimate non-localhost origin in prod. The bug path the
// audit flagged (#101) was: operator deploys to a public domain,
// forgets ALLOWED_ORIGINS, CORS rejects every request, user sees
// a blank dashboard, operator blames the app. Fail-fast at
// startup instead.
//
// Extracted as a package-private helper so the rule can be
// unit-tested without spinning up a full Config.Validate() call
// chain (which also hits ValidateOIDCIssuer, a network call).
// The caller in Validate() passes the pre-computed inputs.
func validateAllowedOriginsForProd(isProd bool, allowedOriginsEnv string) error {
	if !isProd {
		return nil
	}
	if allowedOriginsEnv == "" {
		return fmt.Errorf("%w: ALLOWED_ORIGINS must be set in production mode (comma-separated list of allowed origins, e.g. https://calbridgesync.example.com) - the localhost-only defaults silently block non-localhost CORS requests", ErrValidationFailed)
	}
	return nil
}

// IsDevelopment returns true if running in development mode.
func (c *Config) IsDevelopment() bool {
	return c.Server.Environment == EnvDevelopment
}

// IsProduction returns true if running in production mode.
func (c *Config) IsProduction() bool {
	return c.Server.Environment == EnvProduction
}

// getEnv returns the value of an environment variable or a default value.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvRequired returns the value of an environment variable.
// Returns empty string if not set (caller should check for required values).
func getEnvRequired(key string) string {
	return os.Getenv(key)
}

// getEnvInt returns the integer value of an environment variable or a default.
func getEnvInt(key string, defaultValue int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid integer: %w", err)
	}
	return parsed, nil
}

// getEnvFloat returns the float value of an environment variable or a default.
func getEnvFloat(key string, defaultValue float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float: %w", err)
	}
	return parsed, nil
}
