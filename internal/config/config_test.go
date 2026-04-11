package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestEnvironmentMethods(t *testing.T) {
	t.Run("IsDevelopment returns true for development", func(t *testing.T) {
		cfg := &Config{
			Server: ServerConfig{
				Environment: EnvDevelopment,
			},
		}
		if !cfg.IsDevelopment() {
			t.Error("expected IsDevelopment() to be true")
		}
		if cfg.IsProduction() {
			t.Error("expected IsProduction() to be false")
		}
	})

	t.Run("IsProduction returns true for production", func(t *testing.T) {
		cfg := &Config{
			Server: ServerConfig{
				Environment: EnvProduction,
			},
		}
		if !cfg.IsProduction() {
			t.Error("expected IsProduction() to be true")
		}
		if cfg.IsDevelopment() {
			t.Error("expected IsDevelopment() to be false")
		}
	})
}

func TestGetMissingRequired(t *testing.T) {
	t.Run("returns empty list when all required fields set", func(t *testing.T) {
		cfg := &Config{
			Server: ServerConfig{
				BaseURL: "https://example.com",
			},
			OIDC: OIDCConfig{
				Issuer:       "https://auth.example.com",
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				RedirectURL:  "https://example.com/callback",
			},
			Security: SecurityConfig{
				EncryptionKey: make([]byte, 32),
				SessionSecret: "session-secret",
			},
			CalDAV: CalDAVConfig{
				DefaultDestURL: "https://caldav.example.com",
			},
		}

		missing := cfg.getMissingRequired()
		if len(missing) != 0 {
			t.Errorf("expected no missing fields, got %v", missing)
		}
	})

	t.Run("returns all missing fields", func(t *testing.T) {
		cfg := &Config{}

		missing := cfg.getMissingRequired()

		expectedMissing := []string{
			"BASE_URL",
			"OIDC_ISSUER",
			"OIDC_CLIENT_ID",
			"OIDC_CLIENT_SECRET",
			"OIDC_REDIRECT_URL",
			"ENCRYPTION_KEY",
			"SESSION_SECRET",
			"DEFAULT_DEST_URL",
		}

		if len(missing) != len(expectedMissing) {
			t.Errorf("expected %d missing fields, got %d: %v", len(expectedMissing), len(missing), missing)
		}

		for _, expected := range expectedMissing {
			found := false
			for _, m := range missing {
				if m == expected {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %q to be in missing list", expected)
			}
		}
	})

	t.Run("detects partial missing fields", func(t *testing.T) {
		cfg := &Config{
			Server: ServerConfig{
				BaseURL: "https://example.com",
			},
			OIDC: OIDCConfig{
				Issuer: "https://auth.example.com",
				// Missing ClientID, ClientSecret, RedirectURL
			},
			Security: SecurityConfig{
				EncryptionKey: make([]byte, 32),
				// Missing SessionSecret
			},
			// Missing CalDAV.DefaultDestURL
		}

		missing := cfg.getMissingRequired()

		// Missing: OIDC_CLIENT_ID, OIDC_CLIENT_SECRET, OIDC_REDIRECT_URL, SESSION_SECRET, DEFAULT_DEST_URL
		if len(missing) != 5 {
			t.Errorf("expected 5 missing fields, got %d: %v", len(missing), missing)
		}
	})
}

func TestGetEnvFunctions(t *testing.T) {
	// Save and restore environment
	cleanup := func(keys []string) func() {
		saved := make(map[string]string)
		for _, key := range keys {
			saved[key] = os.Getenv(key)
		}
		return func() {
			for key, val := range saved {
				if val == "" {
					os.Unsetenv(key)
				} else {
					os.Setenv(key, val)
				}
			}
		}
	}

	t.Run("getEnv returns default when not set", func(t *testing.T) {
		restore := cleanup([]string{"TEST_VAR"})
		defer restore()

		os.Unsetenv("TEST_VAR")
		result := getEnv("TEST_VAR", "default-value")
		if result != "default-value" {
			t.Errorf("expected 'default-value', got %q", result)
		}
	})

	t.Run("getEnv returns value when set", func(t *testing.T) {
		restore := cleanup([]string{"TEST_VAR"})
		defer restore()

		os.Setenv("TEST_VAR", "actual-value")
		result := getEnv("TEST_VAR", "default-value")
		if result != "actual-value" {
			t.Errorf("expected 'actual-value', got %q", result)
		}
	})

	t.Run("getEnvInt returns default when not set", func(t *testing.T) {
		restore := cleanup([]string{"TEST_INT"})
		defer restore()

		os.Unsetenv("TEST_INT")
		result, err := getEnvInt("TEST_INT", 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != 42 {
			t.Errorf("expected 42, got %d", result)
		}
	})

	t.Run("getEnvInt parses integer value", func(t *testing.T) {
		restore := cleanup([]string{"TEST_INT"})
		defer restore()

		os.Setenv("TEST_INT", "123")
		result, err := getEnvInt("TEST_INT", 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != 123 {
			t.Errorf("expected 123, got %d", result)
		}
	})

	t.Run("getEnvInt returns error for invalid integer", func(t *testing.T) {
		restore := cleanup([]string{"TEST_INT"})
		defer restore()

		os.Setenv("TEST_INT", "not-a-number")
		_, err := getEnvInt("TEST_INT", 42)
		if err == nil {
			t.Error("expected error for invalid integer")
		}
	})

	t.Run("getEnvFloat returns default when not set", func(t *testing.T) {
		restore := cleanup([]string{"TEST_FLOAT"})
		defer restore()

		os.Unsetenv("TEST_FLOAT")
		result, err := getEnvFloat("TEST_FLOAT", 3.14)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != 3.14 {
			t.Errorf("expected 3.14, got %f", result)
		}
	})

	t.Run("getEnvFloat parses float value", func(t *testing.T) {
		restore := cleanup([]string{"TEST_FLOAT"})
		defer restore()

		os.Setenv("TEST_FLOAT", "2.718")
		result, err := getEnvFloat("TEST_FLOAT", 3.14)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != 2.718 {
			t.Errorf("expected 2.718, got %f", result)
		}
	})

	t.Run("getEnvFloat returns error for invalid float", func(t *testing.T) {
		restore := cleanup([]string{"TEST_FLOAT"})
		defer restore()

		os.Setenv("TEST_FLOAT", "not-a-float")
		_, err := getEnvFloat("TEST_FLOAT", 3.14)
		if err == nil {
			t.Error("expected error for invalid float")
		}
	})
}

func TestEnvironmentConstants(t *testing.T) {
	t.Run("environment constants are correct", func(t *testing.T) {
		if EnvDevelopment != "development" {
			t.Errorf("expected EnvDevelopment to be 'development', got %q", EnvDevelopment)
		}
		if EnvProduction != "production" {
			t.Errorf("expected EnvProduction to be 'production', got %q", EnvProduction)
		}
	})
}

func TestErrorConstants(t *testing.T) {
	t.Run("error constants are not nil", func(t *testing.T) {
		errors := []error{
			ErrMissingConfig,
			ErrInvalidConfig,
			ErrEncryptionKeySize,
			ErrSessionSecretSize,
			ErrValidationFailed,
		}

		for _, err := range errors {
			if err == nil {
				t.Error("expected error constant to be non-nil")
			}
		}
	})
}

func TestGetEnvRequired(t *testing.T) {
	cleanup := func(keys []string) func() {
		saved := make(map[string]string)
		for _, key := range keys {
			saved[key] = os.Getenv(key)
		}
		return func() {
			for key, val := range saved {
				if val == "" {
					os.Unsetenv(key)
				} else {
					os.Setenv(key, val)
				}
			}
		}
	}

	t.Run("returns empty string when not set", func(t *testing.T) {
		restore := cleanup([]string{"TEST_REQUIRED"})
		defer restore()

		os.Unsetenv("TEST_REQUIRED")
		result := getEnvRequired("TEST_REQUIRED")
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("returns value when set", func(t *testing.T) {
		restore := cleanup([]string{"TEST_REQUIRED"})
		defer restore()

		os.Setenv("TEST_REQUIRED", "some-value")
		result := getEnvRequired("TEST_REQUIRED")
		if result != "some-value" {
			t.Errorf("expected 'some-value', got %q", result)
		}
	})
}

func TestLoad(t *testing.T) {
	// Helper to save and restore all config-related env vars
	configEnvVars := []string{
		"PORT", "BASE_URL", "ENVIRONMENT",
		"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URL",
		"ENCRYPTION_KEY", "SESSION_SECRET", "SESSION_MAX_AGE_SECS", "OAUTH_STATE_MAX_AGE_SECS",
		"DATABASE_PATH",
		"DEFAULT_DEST_URL",
		"RATE_LIMIT_RPS", "RATE_LIMIT_BURST",
		"MIN_SYNC_INTERVAL", "MAX_SYNC_INTERVAL",
	}

	cleanup := func() func() {
		saved := make(map[string]string)
		for _, key := range configEnvVars {
			saved[key] = os.Getenv(key)
		}
		return func() {
			for key, val := range saved {
				if val == "" {
					os.Unsetenv(key)
				} else {
					os.Setenv(key, val)
				}
			}
		}
	}

	setRequiredEnvVars := func() {
		os.Setenv("BASE_URL", "https://example.com")
		os.Setenv("OIDC_ISSUER", "https://auth.example.com")
		os.Setenv("OIDC_CLIENT_ID", "client-id")
		os.Setenv("OIDC_CLIENT_SECRET", "client-secret")
		os.Setenv("OIDC_REDIRECT_URL", "https://example.com/callback")
		// Valid 32-byte hex key (64 hex characters)
		os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		os.Setenv("SESSION_SECRET", "this-is-a-session-secret-that-is-at-least-32-chars")
		os.Setenv("DEFAULT_DEST_URL", "https://caldav.example.com")
	}

	clearAllEnvVars := func() {
		for _, key := range configEnvVars {
			os.Unsetenv(key)
		}
	}

	t.Run("loads config with all required env vars", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.Server.BaseURL != "https://example.com" {
			t.Errorf("expected BaseURL 'https://example.com', got %q", cfg.Server.BaseURL)
		}
		if cfg.Server.Port != 8080 {
			t.Errorf("expected default Port 8080, got %d", cfg.Server.Port)
		}
		if cfg.Server.Environment != EnvProduction {
			t.Errorf("expected default Environment 'production', got %q", cfg.Server.Environment)
		}
		if cfg.OIDC.Issuer != "https://auth.example.com" {
			t.Errorf("expected OIDC Issuer, got %q", cfg.OIDC.Issuer)
		}
		if len(cfg.Security.EncryptionKey) != 32 {
			t.Errorf("expected 32-byte encryption key, got %d bytes", len(cfg.Security.EncryptionKey))
		}
		if cfg.Database.Path != "./data/calbridgesync.db" {
			t.Errorf("expected default database path, got %q", cfg.Database.Path)
		}
		if cfg.RateLimiting.RPS != 10.0 {
			t.Errorf("expected default RPS 10.0, got %f", cfg.RateLimiting.RPS)
		}
		if cfg.RateLimiting.Burst != 20 {
			t.Errorf("expected default Burst 20, got %d", cfg.RateLimiting.Burst)
		}
		if cfg.Sync.MinInterval != 30 {
			t.Errorf("expected default MinInterval 30, got %d", cfg.Sync.MinInterval)
		}
		if cfg.Sync.MaxInterval != 3600 {
			t.Errorf("expected default MaxInterval 3600, got %d", cfg.Sync.MaxInterval)
		}
		if cfg.Security.SessionMaxAgeSecs != 86400 {
			t.Errorf("expected default SessionMaxAgeSecs 86400, got %d", cfg.Security.SessionMaxAgeSecs)
		}
		if cfg.Security.OAuthStateMaxAgeSecs != 300 {
			t.Errorf("expected default OAuthStateMaxAgeSecs 300, got %d", cfg.Security.OAuthStateMaxAgeSecs)
		}
	})

	t.Run("loads config with custom values", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()

		os.Setenv("PORT", "9000")
		os.Setenv("ENVIRONMENT", "development")
		os.Setenv("DATABASE_PATH", "/custom/path.db")
		os.Setenv("RATE_LIMIT_RPS", "5.5")
		os.Setenv("RATE_LIMIT_BURST", "10")
		os.Setenv("MIN_SYNC_INTERVAL", "60")
		os.Setenv("MAX_SYNC_INTERVAL", "7200")
		os.Setenv("SESSION_MAX_AGE_SECS", "3600")
		os.Setenv("OAUTH_STATE_MAX_AGE_SECS", "600")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.Server.Port != 9000 {
			t.Errorf("expected Port 9000, got %d", cfg.Server.Port)
		}
		if cfg.Server.Environment != EnvDevelopment {
			t.Errorf("expected Environment 'development', got %q", cfg.Server.Environment)
		}
		if cfg.Database.Path != "/custom/path.db" {
			t.Errorf("expected custom database path, got %q", cfg.Database.Path)
		}
		if cfg.RateLimiting.RPS != 5.5 {
			t.Errorf("expected RPS 5.5, got %f", cfg.RateLimiting.RPS)
		}
		if cfg.RateLimiting.Burst != 10 {
			t.Errorf("expected Burst 10, got %d", cfg.RateLimiting.Burst)
		}
		if cfg.Sync.MinInterval != 60 {
			t.Errorf("expected MinInterval 60, got %d", cfg.Sync.MinInterval)
		}
		if cfg.Sync.MaxInterval != 7200 {
			t.Errorf("expected MaxInterval 7200, got %d", cfg.Sync.MaxInterval)
		}
		if cfg.Security.SessionMaxAgeSecs != 3600 {
			t.Errorf("expected SessionMaxAgeSecs 3600, got %d", cfg.Security.SessionMaxAgeSecs)
		}
		if cfg.Security.OAuthStateMaxAgeSecs != 600 {
			t.Errorf("expected OAuthStateMaxAgeSecs 600, got %d", cfg.Security.OAuthStateMaxAgeSecs)
		}
	})

	t.Run("returns error for missing required fields", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for missing required fields")
		}
		if !errors.Is(err, ErrMissingConfig) {
			t.Errorf("expected ErrMissingConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid PORT", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("PORT", "not-a-number")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid PORT")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid ENCRYPTION_KEY hex", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("ENCRYPTION_KEY", "not-valid-hex!")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid ENCRYPTION_KEY hex")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for wrong ENCRYPTION_KEY size", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		// Only 16 bytes (32 hex chars) instead of 32 bytes
		os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for wrong ENCRYPTION_KEY size")
		}
		if !errors.Is(err, ErrEncryptionKeySize) {
			t.Errorf("expected ErrEncryptionKeySize, got %v", err)
		}
	})

	t.Run("returns error for short SESSION_SECRET", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("SESSION_SECRET", "too-short")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for short SESSION_SECRET")
		}
		if !errors.Is(err, ErrSessionSecretSize) {
			t.Errorf("expected ErrSessionSecretSize, got %v", err)
		}
	})

	t.Run("returns error for invalid RATE_LIMIT_RPS", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("RATE_LIMIT_RPS", "not-a-float")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid RATE_LIMIT_RPS")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid RATE_LIMIT_BURST", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("RATE_LIMIT_BURST", "not-an-int")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid RATE_LIMIT_BURST")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid MIN_SYNC_INTERVAL", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("MIN_SYNC_INTERVAL", "invalid")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid MIN_SYNC_INTERVAL")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid MAX_SYNC_INTERVAL", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("MAX_SYNC_INTERVAL", "invalid")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid MAX_SYNC_INTERVAL")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid SESSION_MAX_AGE_SECS", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("SESSION_MAX_AGE_SECS", "invalid")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid SESSION_MAX_AGE_SECS")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("returns error for invalid OAUTH_STATE_MAX_AGE_SECS", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("OAUTH_STATE_MAX_AGE_SECS", "invalid")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid OAUTH_STATE_MAX_AGE_SECS")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("environment is case-insensitive", func(t *testing.T) {
		restore := cleanup()
		defer restore()
		clearAllEnvVars()
		setRequiredEnvVars()
		os.Setenv("ENVIRONMENT", "DEVELOPMENT")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.Server.Environment != EnvDevelopment {
			t.Errorf("expected Environment 'development', got %q", cfg.Server.Environment)
		}
	})
}

func TestConfigStructs(t *testing.T) {
	t.Run("ServerConfig has expected fields", func(t *testing.T) {
		sc := ServerConfig{
			Port:        8080,
			BaseURL:     "https://example.com",
			Environment: EnvProduction,
		}
		if sc.Port != 8080 {
			t.Error("Port field not working")
		}
		if sc.BaseURL != "https://example.com" {
			t.Error("BaseURL field not working")
		}
		if sc.Environment != EnvProduction {
			t.Error("Environment field not working")
		}
	})

	t.Run("OIDCConfig has expected fields", func(t *testing.T) {
		oc := OIDCConfig{
			Issuer:       "https://auth.example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RedirectURL:  "https://example.com/callback",
		}
		if oc.Issuer != "https://auth.example.com" {
			t.Error("Issuer field not working")
		}
		if oc.ClientID != "client-id" {
			t.Error("ClientID field not working")
		}
		if oc.ClientSecret != "client-secret" {
			t.Error("ClientSecret field not working")
		}
		if oc.RedirectURL != "https://example.com/callback" {
			t.Error("RedirectURL field not working")
		}
	})

	t.Run("SecurityConfig has expected fields", func(t *testing.T) {
		sc := SecurityConfig{
			EncryptionKey:        make([]byte, 32),
			SessionSecret:        "secret",
			SessionMaxAgeSecs:    86400,
			OAuthStateMaxAgeSecs: 300,
		}
		if len(sc.EncryptionKey) != 32 {
			t.Error("EncryptionKey field not working")
		}
		if sc.SessionSecret != "secret" {
			t.Error("SessionSecret field not working")
		}
		if sc.SessionMaxAgeSecs != 86400 {
			t.Error("SessionMaxAgeSecs field not working")
		}
		if sc.OAuthStateMaxAgeSecs != 300 {
			t.Error("OAuthStateMaxAgeSecs field not working")
		}
	})

	t.Run("DatabaseConfig has expected fields", func(t *testing.T) {
		dc := DatabaseConfig{
			Path: "/path/to/db",
		}
		if dc.Path != "/path/to/db" {
			t.Error("Path field not working")
		}
	})

	t.Run("CalDAVConfig has expected fields", func(t *testing.T) {
		cc := CalDAVConfig{
			DefaultDestURL: "https://caldav.example.com",
		}
		if cc.DefaultDestURL != "https://caldav.example.com" {
			t.Error("DefaultDestURL field not working")
		}
	})

	t.Run("RateLimitConfig has expected fields", func(t *testing.T) {
		rlc := RateLimitConfig{
			RPS:   10.0,
			Burst: 20,
		}
		if rlc.RPS != 10.0 {
			t.Error("RPS field not working")
		}
		if rlc.Burst != 20 {
			t.Error("Burst field not working")
		}
	})

	t.Run("SyncConfig has expected fields", func(t *testing.T) {
		sc := SyncConfig{
			MinInterval: 30,
			MaxInterval: 3600,
		}
		if sc.MinInterval != 30 {
			t.Error("MinInterval field not working")
		}
		if sc.MaxInterval != 3600 {
			t.Error("MaxInterval field not working")
		}
	})

	t.Run("Config has all sub-configs", func(t *testing.T) {
		cfg := Config{
			Server:       ServerConfig{Port: 8080},
			OIDC:         OIDCConfig{Issuer: "https://auth.example.com"},
			Security:     SecurityConfig{SessionSecret: "secret"},
			Database:     DatabaseConfig{Path: "/path"},
			CalDAV:       CalDAVConfig{DefaultDestURL: "https://caldav.example.com"},
			RateLimiting: RateLimitConfig{RPS: 10.0},
			Sync:         SyncConfig{MinInterval: 30},
		}
		if cfg.Server.Port != 8080 {
			t.Error("Server sub-config not working")
		}
		if cfg.OIDC.Issuer != "https://auth.example.com" {
			t.Error("OIDC sub-config not working")
		}
		if cfg.Security.SessionSecret != "secret" {
			t.Error("Security sub-config not working")
		}
		if cfg.Database.Path != "/path" {
			t.Error("Database sub-config not working")
		}
		if cfg.CalDAV.DefaultDestURL != "https://caldav.example.com" {
			t.Error("CalDAV sub-config not working")
		}
		if cfg.RateLimiting.RPS != 10.0 {
			t.Error("RateLimiting sub-config not working")
		}
		if cfg.Sync.MinInterval != 30 {
			t.Error("Sync sub-config not working")
		}
	})
}

// TestGoogleOAuthConfig_Enabled verifies the feature-gate helper. (#70)
// Enabled() reports whether the Google OAuth feature is usable on
// this instance. As of #79 the per-source client_id / client_secret
// live in the database (not in env vars), so the only instance-level
// requirement is the redirect URL. Per-source credential validation
// happens in the web handlers when each source is created.
func TestGoogleOAuthConfig_Enabled(t *testing.T) {
	tests := []struct {
		name   string
		cfg    GoogleOAuthConfig
		wantOk bool
	}{
		{
			name:   "empty config is disabled",
			cfg:    GoogleOAuthConfig{},
			wantOk: false,
		},
		{
			name:   "redirect url set is enabled",
			cfg:    GoogleOAuthConfig{RedirectURL: "https://example.com/auth/oauth/google/callback"},
			wantOk: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Enabled(); got != tt.wantOk {
				t.Errorf("Enabled() = %v, want %v", got, tt.wantOk)
			}
		})
	}
}

// TestValidateAllowedOriginsForProd covers the production-mode
// hard-fail from #101: if ENVIRONMENT=production and ALLOWED_ORIGINS
// is unset, Config.Validate must return an error. Development mode
// and explicitly-set production values must pass cleanly.
func TestValidateAllowedOriginsForProd(t *testing.T) {
	cases := []struct {
		name        string
		isProd      bool
		envVar      string
		wantErr     bool
		wantErrType error
	}{
		{
			name:    "dev mode + empty is OK (localhost defaults)",
			isProd:  false,
			envVar:  "",
			wantErr: false,
		},
		{
			name:    "dev mode + explicit is OK",
			isProd:  false,
			envVar:  "http://localhost:3000",
			wantErr: false,
		},
		{
			name:        "prod mode + empty is a hard fail",
			isProd:      true,
			envVar:      "",
			wantErr:     true,
			wantErrType: ErrValidationFailed,
		},
		{
			name:    "prod mode + single origin is OK",
			isProd:  true,
			envVar:  "https://calbridgesync.example.com",
			wantErr: false,
		},
		{
			name:    "prod mode + multiple origins is OK",
			isProd:  true,
			envVar:  "https://calbridgesync.example.com,https://admin.example.com",
			wantErr: false,
		},
		{
			// The validator only checks "non-empty." Format
			// validation (isValidOrigin) happens later in
			// middleware.go's getAllowedOrigins. A whitespace-only
			// value makes it past this check but then gets logged
			// as invalid by middleware and falls back to empty,
			// which in turn blocks all non-empty origins. That's
			// a deployment bug but not our job to catch here —
			// the goal of this check is the far-more-common
			// "operator forgot to set it" case.
			name:    "prod mode + whitespace-only env passes (not our layer to format-check)",
			isProd:  true,
			envVar:  "   ",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAllowedOriginsForProd(tc.isProd, tc.envVar)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if tc.wantErrType != nil && !errors.Is(err, tc.wantErrType) {
					t.Errorf("want error type %v, got %v", tc.wantErrType, err)
				}
				// The error message should mention ALLOWED_ORIGINS so
				// operators can grep the startup logs and find it.
				if !strings.Contains(err.Error(), "ALLOWED_ORIGINS") {
					t.Errorf("error message must mention ALLOWED_ORIGINS for operator grep-ability, got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
