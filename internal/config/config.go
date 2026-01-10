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
	"github.com/macjediwizard/calbridge/internal/validator"
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
	EncryptionKey []byte
	SessionSecret string
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	Path string
}

// CalDAVConfig holds CalDAV-related configuration.
type CalDAVConfig struct {
	DefaultDestURL string
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

	// Database configuration
	cfg.Database.Path = getEnv("DATABASE_PATH", "./data/calbridge.db")

	// CalDAV configuration
	cfg.CalDAV.DefaultDestURL = getEnvRequired("DEFAULT_DEST_URL")

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
