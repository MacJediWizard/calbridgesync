package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/config"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestNewHandlers(t *testing.T) {
	t.Run("creates handlers with all nil dependencies", func(t *testing.T) {
		handlers := NewHandlers(nil, nil, nil, nil, nil, nil, nil, nil, nil)

		if handlers == nil {
			t.Fatal("expected non-nil handlers")
		}
	})

	t.Run("creates handlers with config", func(t *testing.T) {
		cfg := &config.Config{}
		handlers := NewHandlers(cfg, nil, nil, nil, nil, nil, nil, nil, nil)

		if handlers == nil {
			t.Fatal("expected non-nil handlers")
		}
		if handlers.cfg != cfg {
			t.Error("expected cfg to be set")
		}
	})
}

func TestSourceFormDataValidate(t *testing.T) {
	t.Run("returns true for valid data", func(t *testing.T) {
		form := &sourceFormData{
			Name:           "Test Source",
			SourceURL:      "https://caldav.example.com",
			SourceUsername: "user",
			SourcePassword: "pass",
		}

		if !form.validate() {
			t.Error("expected validate() to return true")
		}
	})

	t.Run("returns false when name is empty", func(t *testing.T) {
		form := &sourceFormData{
			Name:           "",
			SourceURL:      "https://caldav.example.com",
			SourceUsername: "user",
			SourcePassword: "pass",
		}

		if form.validate() {
			t.Error("expected validate() to return false for empty name")
		}
	})

	t.Run("returns false when source URL is empty", func(t *testing.T) {
		form := &sourceFormData{
			Name:           "Test",
			SourceURL:      "",
			SourceUsername: "user",
			SourcePassword: "pass",
		}

		if form.validate() {
			t.Error("expected validate() to return false for empty source URL")
		}
	})

	t.Run("returns false when source username is empty", func(t *testing.T) {
		form := &sourceFormData{
			Name:           "Test",
			SourceURL:      "https://caldav.example.com",
			SourceUsername: "",
			SourcePassword: "pass",
		}

		if form.validate() {
			t.Error("expected validate() to return false for empty username")
		}
	})

	t.Run("returns false when source password is empty", func(t *testing.T) {
		form := &sourceFormData{
			Name:           "Test",
			SourceURL:      "https://caldav.example.com",
			SourceUsername: "user",
			SourcePassword: "",
		}

		if form.validate() {
			t.Error("expected validate() to return false for empty password")
		}
	})
}

func TestSourceFormDataHasDestCredentials(t *testing.T) {
	t.Run("returns true when all dest fields are set", func(t *testing.T) {
		form := &sourceFormData{
			DestURL:      "https://dest.example.com",
			DestUsername: "destuser",
			DestPassword: "destpass",
		}

		if !form.hasDestCredentials() {
			t.Error("expected hasDestCredentials() to return true")
		}
	})

	t.Run("returns false when dest URL is empty", func(t *testing.T) {
		form := &sourceFormData{
			DestURL:      "",
			DestUsername: "destuser",
			DestPassword: "destpass",
		}

		if form.hasDestCredentials() {
			t.Error("expected hasDestCredentials() to return false for empty URL")
		}
	})

	t.Run("returns false when dest username is empty", func(t *testing.T) {
		form := &sourceFormData{
			DestURL:      "https://dest.example.com",
			DestUsername: "",
			DestPassword: "destpass",
		}

		if form.hasDestCredentials() {
			t.Error("expected hasDestCredentials() to return false for empty username")
		}
	})

	t.Run("returns false when dest password is empty", func(t *testing.T) {
		form := &sourceFormData{
			DestURL:      "https://dest.example.com",
			DestUsername: "destuser",
			DestPassword: "",
		}

		if form.hasDestCredentials() {
			t.Error("expected hasDestCredentials() to return false for empty password")
		}
	})

	t.Run("returns false when all dest fields are empty", func(t *testing.T) {
		form := &sourceFormData{}

		if form.hasDestCredentials() {
			t.Error("expected hasDestCredentials() to return false for all empty")
		}
	})
}

func TestSourceFormDataStruct(t *testing.T) {
	t.Run("struct has all expected fields", func(t *testing.T) {
		form := &sourceFormData{
			Name:             "Test Source",
			SourceType:       db.SourceTypeICloud,
			SourceURL:        "https://caldav.icloud.com",
			SourceUsername:   "user@icloud.com",
			SourcePassword:   "password123",
			DestURL:          "https://dest.example.com",
			DestUsername:     "destuser",
			DestPassword:     "destpass",
			ConflictStrategy: db.ConflictSourceWins,
			SyncInterval:     300,
		}

		if form.Name != "Test Source" {
			t.Errorf("expected Name 'Test Source', got %q", form.Name)
		}
		if form.SourceType != db.SourceTypeICloud {
			t.Errorf("expected SourceType 'icloud', got %q", form.SourceType)
		}
		if form.SyncInterval != 300 {
			t.Errorf("expected SyncInterval 300, got %d", form.SyncInterval)
		}
	})
}

func TestRespondError(t *testing.T) {
	handlers := &Handlers{}

	t.Run("responds with JSON for non-HTMX request", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		handlers.respondError(c, http.StatusBadRequest, "Test error")

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}

		body := w.Body.String()
		if body != `{"error":"Test error"}` {
			t.Errorf("expected JSON error response, got %q", body)
		}
	})

	t.Run("detects HTMX request", func(t *testing.T) {
		// Test that isHTMX returns true for HTMX requests
		// Full HTML render testing requires the full gin engine setup
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("HX-Request", "true")

		if !isHTMX(c) {
			t.Error("expected isHTMX to return true")
		}
	})
}

func TestIsHTMXFunction(t *testing.T) {
	t.Run("returns true for HTMX request", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("HX-Request", "true")

		if !isHTMX(c) {
			t.Error("expected isHTMX() to return true")
		}
	})

	t.Run("returns false for non-HTMX request", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		if isHTMX(c) {
			t.Error("expected isHTMX() to return false")
		}
	})

	t.Run("returns false for HX-Request with value other than true", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("HX-Request", "false")

		if isHTMX(c) {
			t.Error("expected isHTMX() to return false for 'false' value")
		}
	})
}

func TestHandlersStruct(t *testing.T) {
	t.Run("Handlers struct has expected fields", func(t *testing.T) {
		// Verify struct can be created with all fields
		h := &Handlers{
			cfg:        &config.Config{},
			db:         nil, // Would be *db.DB in real use
			oidc:       nil,
			session:    nil,
			encryptor:  nil,
			syncEngine: nil,
			scheduler:  nil,
			health:     nil,
			notifier:   nil,
		}

		if h.cfg == nil {
			t.Error("expected cfg to be set")
		}
	})
}

func TestParseSourceForm(t *testing.T) {
	t.Run("parses form data correctly", func(t *testing.T) {
		cfg := &config.Config{
			Sync: config.SyncConfig{
				MinInterval: 60,
				MaxInterval: 3600,
			},
		}
		handlers := &Handlers{cfg: cfg}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.PostForm = make(map[string][]string)
		c.Request.PostForm.Set("name", "Test Source")
		c.Request.PostForm.Set("source_type", "custom")
		c.Request.PostForm.Set("source_url", "https://caldav.example.com")
		c.Request.PostForm.Set("source_username", "user")
		c.Request.PostForm.Set("source_password", "pass")
		c.Request.PostForm.Set("dest_url", "https://dest.example.com")
		c.Request.PostForm.Set("dest_username", "destuser")
		c.Request.PostForm.Set("dest_password", "destpass")
		c.Request.PostForm.Set("conflict_strategy", "source_wins")
		c.Request.PostForm.Set("sync_interval", "300")

		form := handlers.parseSourceForm(c)

		if form.Name != "Test Source" {
			t.Errorf("expected Name 'Test Source', got %q", form.Name)
		}
		if form.SourceType != db.SourceTypeCustom {
			t.Errorf("expected SourceType 'custom', got %q", form.SourceType)
		}
		if form.SourceURL != "https://caldav.example.com" {
			t.Errorf("expected SourceURL, got %q", form.SourceURL)
		}
		if form.SyncInterval != 300 {
			t.Errorf("expected SyncInterval 300, got %d", form.SyncInterval)
		}
		if form.ConflictStrategy != db.ConflictSourceWins {
			t.Errorf("expected ConflictStrategy 'source_wins', got %q", form.ConflictStrategy)
		}
	})

	t.Run("uses min interval for invalid sync_interval", func(t *testing.T) {
		cfg := &config.Config{
			Sync: config.SyncConfig{
				MinInterval: 60,
				MaxInterval: 3600,
			},
		}
		handlers := &Handlers{cfg: cfg}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.PostForm = make(map[string][]string)
		c.Request.PostForm.Set("sync_interval", "invalid")

		form := handlers.parseSourceForm(c)

		if form.SyncInterval != 60 {
			t.Errorf("expected SyncInterval to default to 60, got %d", form.SyncInterval)
		}
	})

	t.Run("uses min interval when below min", func(t *testing.T) {
		cfg := &config.Config{
			Sync: config.SyncConfig{
				MinInterval: 60,
				MaxInterval: 3600,
			},
		}
		handlers := &Handlers{cfg: cfg}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.PostForm = make(map[string][]string)
		c.Request.PostForm.Set("sync_interval", "10") // Below minimum of 60

		form := handlers.parseSourceForm(c)

		if form.SyncInterval != 60 {
			t.Errorf("expected SyncInterval to be clamped to 60, got %d", form.SyncInterval)
		}
	})

	t.Run("uses min interval when above max", func(t *testing.T) {
		cfg := &config.Config{
			Sync: config.SyncConfig{
				MinInterval: 60,
				MaxInterval: 3600,
			},
		}
		handlers := &Handlers{cfg: cfg}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.PostForm = make(map[string][]string)
		c.Request.PostForm.Set("sync_interval", "10000") // Above maximum of 3600

		form := handlers.parseSourceForm(c)

		if form.SyncInterval != 60 {
			t.Errorf("expected SyncInterval to be clamped to 60, got %d", form.SyncInterval)
		}
	})
}

func TestSourceFormDataAllFields(t *testing.T) {
	t.Run("validate checks all required fields", func(t *testing.T) {
		testCases := []struct {
			name     string
			form     sourceFormData
			expected bool
		}{
			{
				name: "all fields valid",
				form: sourceFormData{
					Name:           "Test",
					SourceURL:      "https://example.com",
					SourceUsername: "user",
					SourcePassword: "pass",
				},
				expected: true,
			},
			{
				name: "missing name",
				form: sourceFormData{
					SourceURL:      "https://example.com",
					SourceUsername: "user",
					SourcePassword: "pass",
				},
				expected: false,
			},
			{
				name: "missing URL",
				form: sourceFormData{
					Name:           "Test",
					SourceUsername: "user",
					SourcePassword: "pass",
				},
				expected: false,
			},
			{
				name: "missing username",
				form: sourceFormData{
					Name:           "Test",
					SourceURL:      "https://example.com",
					SourcePassword: "pass",
				},
				expected: false,
			},
			{
				name: "missing password",
				form: sourceFormData{
					Name:           "Test",
					SourceURL:      "https://example.com",
					SourceUsername: "user",
				},
				expected: false,
			},
			{
				name:     "all fields empty",
				form:     sourceFormData{},
				expected: false,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				result := tc.form.validate()
				if result != tc.expected {
					t.Errorf("expected validate() = %v, got %v", tc.expected, result)
				}
			})
		}
	})
}

func TestSourceFormDataDefaultValues(t *testing.T) {
	t.Run("struct has zero values by default", func(t *testing.T) {
		form := &sourceFormData{}

		if form.Name != "" {
			t.Error("expected empty Name by default")
		}
		if form.SourceType != "" {
			t.Error("expected empty SourceType by default")
		}
		if form.SyncInterval != 0 {
			t.Error("expected 0 SyncInterval by default")
		}
		if form.ConflictStrategy != "" {
			t.Error("expected empty ConflictStrategy by default")
		}
	})
}

func TestDurationConversion(t *testing.T) {
	t.Run("sync interval converts to duration correctly", func(t *testing.T) {
		form := &sourceFormData{
			SyncInterval: 300,
		}

		duration := time.Duration(form.SyncInterval) * time.Second
		if duration != 5*time.Minute {
			t.Errorf("expected 5 minutes, got %v", duration)
		}
	})
}
