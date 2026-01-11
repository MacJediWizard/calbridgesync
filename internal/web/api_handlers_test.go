package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/db"
	"github.com/macjediwizard/calbridgesync/internal/scheduler"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// testHandlers holds test dependencies.
type testHandlers struct {
	db       *db.DB
	handlers *Handlers
	cleanup  func()
}

// setupTestHandlers creates handlers with a test database.
func setupTestHandlers(t *testing.T) *testHandlers {
	t.Helper()

	// Create a temp directory for the test database
	tempDir, err := os.MkdirTemp("", "calbridge-api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.New(dbPath)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create test database: %v", err)
	}

	// Create a scheduler with nil dependencies (safe for testing)
	sched := scheduler.New(nil, nil)

	handlers := &Handlers{
		db:        database,
		scheduler: sched,
	}

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}

	return &testHandlers{
		db:       database,
		handlers: handlers,
		cleanup:  cleanup,
	}
}

// setAuthContext sets the authenticated user context for testing.
func setAuthContext(c *gin.Context, userID, email string) {
	session := &auth.SessionData{
		UserID:    userID,
		Email:     email,
		Name:      "Test User",
		CSRFToken: "test-csrf-token",
	}
	c.Set(auth.ContextKeySession, session)
}

// createTestUserAndSource creates a user and source for testing.
func createTestUserAndSource(t *testing.T, database *db.DB, email, sourceName string) (string, *db.Source) {
	t.Helper()

	user, err := database.GetOrCreateUser(email, "Test User")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	source := &db.Source{
		UserID:           user.ID,
		Name:             sourceName,
		SourceType:       db.SourceTypeCustom,
		SourceURL:        "https://example.com/caldav",
		SourceUsername:   "user",
		SourcePassword:   "encrypted-password",
		DestURL:          "https://dest.com/caldav",
		DestUsername:     "destuser",
		DestPassword:     "encrypted-dest-password",
		SyncInterval:     300,
		SyncDirection:    db.SyncDirectionOneWay,
		ConflictStrategy: db.ConflictSourceWins,
		Enabled:          true,
	}

	err = database.CreateSource(source)
	if err != nil {
		t.Fatalf("failed to create source: %v", err)
	}

	return user.ID, source
}

func TestAPIDeleteAllMalformedEvents(t *testing.T) {
	t.Run("deletes all malformed events and returns count", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		// Create test data
		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		// Add malformed events
		th.db.SaveMalformedEvent(source.ID, "/event1.ics", "Error 1")
		th.db.SaveMalformedEvent(source.ID, "/event2.ics", "Error 2")
		th.db.SaveMalformedEvent(source.ID, "/event3.ics", "Error 3")

		// Create request
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/malformed-events", nil)

		// Set auth context
		setAuthContext(c, userID, "test@example.com")

		// Call handler
		th.handlers.APIDeleteAllMalformedEvents(c)

		// Check response
		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if response["message"] != "All malformed events deleted" {
			t.Errorf("unexpected message: %v", response["message"])
		}

		deleted, ok := response["deleted"].(float64)
		if !ok {
			t.Fatalf("deleted field not found or wrong type")
		}
		if int(deleted) != 3 {
			t.Errorf("expected 3 deleted, got %v", deleted)
		}

		// Verify events are actually gone
		events, _ := th.db.GetMalformedEvents(userID)
		if len(events) != 0 {
			t.Errorf("expected 0 events remaining, got %d", len(events))
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/malformed-events", nil)

		// Don't set auth context

		th.handlers.APIDeleteAllMalformedEvents(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})

	t.Run("returns zero when no events exist", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		// Create user without any malformed events
		user, _ := th.db.GetOrCreateUser("empty@example.com", "Empty User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/malformed-events", nil)

		setAuthContext(c, user.ID, "empty@example.com")

		th.handlers.APIDeleteAllMalformedEvents(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		deleted := response["deleted"].(float64)
		if int(deleted) != 0 {
			t.Errorf("expected 0 deleted, got %v", deleted)
		}
	})

	t.Run("does not delete other user's events", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		// Create two users with malformed events
		user1ID, source1 := createTestUserAndSource(t, th.db, "user1@example.com", "Source 1")
		user2ID, source2 := createTestUserAndSource(t, th.db, "user2@example.com", "Source 2")

		th.db.SaveMalformedEvent(source1.ID, "/user1/event.ics", "User1 Error")
		th.db.SaveMalformedEvent(source2.ID, "/user2/event1.ics", "User2 Error 1")
		th.db.SaveMalformedEvent(source2.ID, "/user2/event2.ics", "User2 Error 2")

		// Delete user1's events
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/malformed-events", nil)
		setAuthContext(c, user1ID, "user1@example.com")

		th.handlers.APIDeleteAllMalformedEvents(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		// User1's events should be gone
		events1, _ := th.db.GetMalformedEvents(user1ID)
		if len(events1) != 0 {
			t.Errorf("expected 0 events for user1, got %d", len(events1))
		}

		// User2's events should still exist
		events2, _ := th.db.GetMalformedEvents(user2ID)
		if len(events2) != 2 {
			t.Errorf("expected 2 events for user2, got %d", len(events2))
		}
	})
}

func TestSanitizeError(t *testing.T) {
	t.Run("returns user message and logs error", func(t *testing.T) {
		err := errors.New("internal database error with sensitive info")
		result := sanitizeError(err, "Failed to process request")

		if result != "Failed to process request" {
			t.Errorf("expected 'Failed to process request', got %q", result)
		}
	})

	t.Run("handles nil error", func(t *testing.T) {
		result := sanitizeError(nil, "Something went wrong")

		if result != "Something went wrong" {
			t.Errorf("expected 'Something went wrong', got %q", result)
		}
	})
}

func TestCategorizeConnectionError(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		contains string
	}{
		{"nil error", nil, "Connection failed"},
		{"no such host", errors.New("dial tcp: lookup unknown.host: no such host"), "Server not found"},
		{"connection refused", errors.New("connection refused"), "Connection refused"},
		{"timeout", errors.New("context deadline exceeded"), "Connection timed out"},
		{"unauthorized", errors.New("HTTP 401 Unauthorized"), "Authentication failed"},
		{"forbidden", errors.New("HTTP 403 Forbidden"), "Access denied"},
		{"not found", errors.New("HTTP 404 Not Found"), "Calendar not found"},
		{"certificate error", errors.New("x509 certificate signed by unknown authority"), "SSL/TLS error"},
		{"generic error", errors.New("something unexpected"), "Connection failed"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := categorizeConnectionError(tc.err)
			if !strings.Contains(result, tc.contains) {
				t.Errorf("expected message to contain %q, got %q", tc.contains, result)
			}
		})
	}
}

func TestValidateSourceInput(t *testing.T) {
	t.Run("returns empty string for valid input", func(t *testing.T) {
		result := validateSourceInput(
			"My Source",
			string(db.SourceTypeCustom),
			string(db.SyncDirectionOneWay),
			string(db.ConflictSourceWins),
			"https://caldav.example.com",
			"https://dest.example.com",
			"user",
			"destuser",
		)

		if result != "" {
			t.Errorf("expected empty string for valid input, got %q", result)
		}
	})

	t.Run("rejects name too long", func(t *testing.T) {
		longName := strings.Repeat("a", 101)
		result := validateSourceInput(longName, "", "", "", "", "", "", "")

		if result == "" || !strings.Contains(result, "Name") {
			t.Error("expected error about name length")
		}
	})

	t.Run("rejects source URL too long", func(t *testing.T) {
		longURL := "https://" + strings.Repeat("a", 500)
		result := validateSourceInput("Name", "", "", "", longURL, "", "", "")

		if result == "" || !strings.Contains(result, "Source URL") {
			t.Error("expected error about source URL length")
		}
	})

	t.Run("rejects dest URL too long", func(t *testing.T) {
		longURL := "https://" + strings.Repeat("a", 500)
		result := validateSourceInput("Name", "", "", "", "", longURL, "", "")

		if result == "" || !strings.Contains(result, "Destination URL") {
			t.Error("expected error about destination URL length")
		}
	})

	t.Run("rejects invalid source type", func(t *testing.T) {
		result := validateSourceInput("Name", "invalid_type", "", "", "", "", "", "")

		if result == "" || !strings.Contains(result, "source type") {
			t.Error("expected error about invalid source type")
		}
	})

	t.Run("rejects invalid sync direction", func(t *testing.T) {
		result := validateSourceInput("Name", "", "invalid_direction", "", "", "", "", "")

		if result == "" || !strings.Contains(result, "sync direction") {
			t.Error("expected error about invalid sync direction")
		}
	})

	t.Run("rejects invalid conflict strategy", func(t *testing.T) {
		result := validateSourceInput("Name", "", "", "invalid_strategy", "", "", "", "")

		if result == "" || !strings.Contains(result, "conflict strategy") {
			t.Error("expected error about invalid conflict strategy")
		}
	})

	t.Run("allows empty enum values", func(t *testing.T) {
		result := validateSourceInput("Name", "", "", "", "", "", "", "")

		if result != "" {
			t.Errorf("expected empty string for empty enum values, got %q", result)
		}
	})
}

func TestSourceToAPI(t *testing.T) {
	t.Run("converts source to API format", func(t *testing.T) {
		now := time.Now()
		lastSync := now.Add(-1 * time.Hour)
		source := &db.Source{
			ID:                "source-123",
			UserID:            "user-456",
			Name:              "Test Source",
			SourceType:        db.SourceTypeICloud,
			SourceURL:         "https://caldav.icloud.com",
			SourceUsername:    "user@icloud.com",
			SourcePassword:    "encrypted",
			DestURL:           "https://dest.com",
			DestUsername:      "destuser",
			DestPassword:      "destencrypted",
			SyncInterval:      300,
			SyncDirection:     db.SyncDirectionTwoWay,
			ConflictStrategy:  db.ConflictDestWins,
			SelectedCalendars: []string{"cal1", "cal2"},
			Enabled:           true,
			LastSyncStatus:    db.SyncStatusSuccess,
			LastSyncAt:        &lastSync,
			CreatedAt:         now,
			UpdatedAt:         now,
		}

		api := sourceToAPI(source)

		if api.ID != "source-123" {
			t.Errorf("expected ID 'source-123', got %q", api.ID)
		}
		if api.Name != "Test Source" {
			t.Errorf("expected Name 'Test Source', got %q", api.Name)
		}
		if api.SourceType != "icloud" {
			t.Errorf("expected SourceType 'icloud', got %q", api.SourceType)
		}
		if api.SyncDirection != "two_way" {
			t.Errorf("expected SyncDirection 'two_way', got %q", api.SyncDirection)
		}
		if api.ConflictStrategy != "dest_wins" {
			t.Errorf("expected ConflictStrategy 'dest_wins', got %q", api.ConflictStrategy)
		}
		if len(api.SelectedCalendars) != 2 {
			t.Errorf("expected 2 selected calendars, got %d", len(api.SelectedCalendars))
		}
		if !api.Enabled {
			t.Error("expected Enabled to be true")
		}
		if api.SyncStatus != "success" {
			t.Errorf("expected SyncStatus 'success', got %q", api.SyncStatus)
		}
		if api.LastSyncAt == nil {
			t.Error("expected LastSyncAt to be set")
		}
	})

	t.Run("handles nil last sync time", func(t *testing.T) {
		source := &db.Source{
			ID:         "source-123",
			Name:       "Test",
			LastSyncAt: nil,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}

		api := sourceToAPI(source)

		if api.LastSyncAt != nil {
			t.Error("expected LastSyncAt to be nil")
		}
	})

	t.Run("handles nil selected calendars", func(t *testing.T) {
		source := &db.Source{
			ID:                "source-123",
			Name:              "Test",
			SelectedCalendars: nil,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}

		api := sourceToAPI(source)

		if api.SelectedCalendars == nil {
			t.Error("expected SelectedCalendars to be empty slice, not nil")
		}
		if len(api.SelectedCalendars) != 0 {
			t.Errorf("expected 0 selected calendars, got %d", len(api.SelectedCalendars))
		}
	})
}

func TestSyncLogToAPI(t *testing.T) {
	t.Run("converts sync log to API format", func(t *testing.T) {
		log := &db.SyncLog{
			ID:              "log-123",
			SourceID:        "source-456",
			Status:          db.SyncStatusSuccess,
			Message:         "Sync completed",
			Details:         "Synced 10 events",
			EventsCreated:   5,
			EventsUpdated:   3,
			EventsDeleted:   2,
			EventsSkipped:   0,
			CalendarsSynced: 2,
			EventsProcessed: 10,
			Duration:        5 * time.Second,
			CreatedAt:       time.Now(),
		}

		api := syncLogToAPI(log)

		if api.ID != "log-123" {
			t.Errorf("expected ID 'log-123', got %q", api.ID)
		}
		if api.Status != "success" {
			t.Errorf("expected Status 'success', got %q", api.Status)
		}
		if api.EventsCreated != 5 {
			t.Errorf("expected EventsCreated 5, got %d", api.EventsCreated)
		}
		if api.Details == nil || *api.Details != "Synced 10 events" {
			t.Error("expected Details to be set")
		}
		if api.Duration == nil || *api.Duration != 5.0 {
			t.Error("expected Duration to be 5.0 seconds")
		}
	})

	t.Run("handles empty details", func(t *testing.T) {
		log := &db.SyncLog{
			ID:        "log-123",
			Details:   "",
			CreatedAt: time.Now(),
		}

		api := syncLogToAPI(log)

		if api.Details != nil {
			t.Error("expected Details to be nil for empty string")
		}
	})

	t.Run("handles zero duration", func(t *testing.T) {
		log := &db.SyncLog{
			ID:        "log-123",
			Duration:  0,
			CreatedAt: time.Now(),
		}

		api := syncLogToAPI(log)

		if api.Duration != nil {
			t.Error("expected Duration to be nil for zero duration")
		}
	})
}

func TestMalformedEventToAPI(t *testing.T) {
	t.Run("converts malformed event to API format", func(t *testing.T) {
		now := time.Now()
		event := &db.MalformedEvent{
			ID:           "event-123",
			SourceID:     "source-456",
			SourceName:   "My Calendar",
			EventPath:    "/calendars/default/broken.ics",
			ErrorMessage: "Invalid VCALENDAR format",
			DiscoveredAt: now,
		}

		api := malformedEventToAPI(event)

		if api.ID != "event-123" {
			t.Errorf("expected ID 'event-123', got %q", api.ID)
		}
		if api.SourceID != "source-456" {
			t.Errorf("expected SourceID 'source-456', got %q", api.SourceID)
		}
		if api.SourceName != "My Calendar" {
			t.Errorf("expected SourceName 'My Calendar', got %q", api.SourceName)
		}
		if api.EventPath != "/calendars/default/broken.ics" {
			t.Errorf("expected EventPath, got %q", api.EventPath)
		}
		if api.ErrorMessage != "Invalid VCALENDAR format" {
			t.Errorf("expected ErrorMessage, got %q", api.ErrorMessage)
		}
		if api.DiscoveredAt == "" {
			t.Error("expected DiscoveredAt to be set")
		}
	})
}

func TestAPIAuthStatus(t *testing.T) {
	t.Run("returns unauthenticated when no session", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)

		th.handlers.APIAuthStatus(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var response APIAuthStatus
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if response.Authenticated {
			t.Error("expected Authenticated to be false")
		}
		if response.User != nil {
			t.Error("expected User to be nil")
		}
	})

	t.Run("returns authenticated with user info", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
		setAuthContext(c, "user-123", "test@example.com")

		th.handlers.APIAuthStatus(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var response APIAuthStatus
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if !response.Authenticated {
			t.Error("expected Authenticated to be true")
		}
		if response.User == nil {
			t.Fatal("expected User to be set")
		}
		if response.User.ID != "user-123" {
			t.Errorf("expected UserID 'user-123', got %q", response.User.ID)
		}
		if response.User.Email != "test@example.com" {
			t.Errorf("expected Email 'test@example.com', got %q", response.User.Email)
		}
	})
}

func TestAPIListSources(t *testing.T) {
	t.Run("returns empty list when no sources", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources", nil)
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIListSources(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var sources []*APISource
		if err := json.Unmarshal(w.Body.Bytes(), &sources); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if len(sources) != 0 {
			t.Errorf("expected 0 sources, got %d", len(sources))
		}
	})

	t.Run("returns sources for user", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, _ := createTestUserAndSource(t, th.db, "test@example.com", "Source 1")
		// Create second source
		source2 := &db.Source{
			UserID:         userID,
			Name:           "Source 2",
			SourceType:     db.SourceTypeCustom,
			SourceURL:      "https://example2.com",
			SourceUsername: "user2",
			SourcePassword: "pass2",
			SyncInterval:   300,
			Enabled:        true,
		}
		th.db.CreateSource(source2)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources", nil)
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIListSources(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var sources []*APISource
		if err := json.Unmarshal(w.Body.Bytes(), &sources); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if len(sources) != 2 {
			t.Errorf("expected 2 sources, got %d", len(sources))
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources", nil)

		th.handlers.APIListSources(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIGetSource(t *testing.T) {
	t.Run("returns source for valid ID", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/"+source.ID, nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIGetSource(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var apiSource APISource
		if err := json.Unmarshal(w.Body.Bytes(), &apiSource); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if apiSource.ID != source.ID {
			t.Errorf("expected ID %q, got %q", source.ID, apiSource.ID)
		}
		if apiSource.Name != "Test Source" {
			t.Errorf("expected Name 'Test Source', got %q", apiSource.Name)
		}
	})

	t.Run("returns 404 for nonexistent source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/nonexistent", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIGetSource(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns 404 for other user's source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		// Create source for user1
		_, source := createTestUserAndSource(t, th.db, "user1@example.com", "User1 Source")

		// Try to access as user2
		user2, _ := th.db.GetOrCreateUser("user2@example.com", "User 2")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/"+source.ID, nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, user2.ID, "user2@example.com")

		th.handlers.APIGetSource(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})
}

func TestAPIGetMalformedEvents(t *testing.T) {
	t.Run("returns malformed events for user", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		th.db.SaveMalformedEvent(source.ID, "/event1.ics", "Error 1")
		th.db.SaveMalformedEvent(source.ID, "/event2.ics", "Error 2")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/malformed-events", nil)
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIGetMalformedEvents(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var events []*APIMalformedEvent
		if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if len(events) != 2 {
			t.Errorf("expected 2 events, got %d", len(events))
		}
	})

	t.Run("returns empty list when no malformed events", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/malformed-events", nil)
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIGetMalformedEvents(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var events []*APIMalformedEvent
		if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if len(events) != 0 {
			t.Errorf("expected 0 events, got %d", len(events))
		}
	})
}

func TestAPIDashboardStats(t *testing.T) {
	t.Run("returns stats for user with sources", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, _ := createTestUserAndSource(t, th.db, "test@example.com", "Source 1")

		// Create second disabled source
		source2 := &db.Source{
			UserID:         userID,
			Name:           "Source 2",
			SourceType:     db.SourceTypeCustom,
			SourceURL:      "https://example2.com",
			SourceUsername: "user2",
			SourcePassword: "pass2",
			SyncInterval:   300,
			Enabled:        false,
		}
		th.db.CreateSource(source2)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/stats", nil)
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIDashboardStats(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var stats APIDashboardStats
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if stats.TotalSources != 2 {
			t.Errorf("expected TotalSources 2, got %d", stats.TotalSources)
		}
		if stats.ActiveSources != 1 {
			t.Errorf("expected ActiveSources 1, got %d", stats.ActiveSources)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/stats", nil)

		th.handlers.APIDashboardStats(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIDeleteSource(t *testing.T) {
	t.Run("deletes source for valid owner", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/sources/"+source.ID, nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIDeleteSource(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		// Verify source is deleted
		sources, _ := th.db.GetSourcesByUserID(userID)
		if len(sources) != 0 {
			t.Errorf("expected 0 sources after deletion, got %d", len(sources))
		}
	})

	t.Run("returns 404 for nonexistent source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/sources/nonexistent", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIDeleteSource(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns 404 for other user's source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		_, source := createTestUserAndSource(t, th.db, "user1@example.com", "User1 Source")
		user2, _ := th.db.GetOrCreateUser("user2@example.com", "User 2")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/sources/"+source.ID, nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, user2.ID, "user2@example.com")

		th.handlers.APIDeleteSource(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/sources/some-id", nil)
		c.Params = gin.Params{{Key: "id", Value: "some-id"}}

		th.handlers.APIDeleteSource(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIToggleSource(t *testing.T) {
	t.Run("toggles source from enabled to disabled", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/toggle", nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIToggleSource(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var apiSource APISource
		if err := json.Unmarshal(w.Body.Bytes(), &apiSource); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if apiSource.Enabled {
			t.Error("expected Enabled to be false after toggle")
		}
	})

	t.Run("returns 404 for nonexistent source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources/nonexistent/toggle", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIToggleSource(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources/some-id/toggle", nil)
		c.Params = gin.Params{{Key: "id", Value: "some-id"}}

		th.handlers.APIToggleSource(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPITriggerSync(t *testing.T) {
	t.Run("returns 404 for nonexistent source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources/nonexistent/sync", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APITriggerSync(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources/some-id/sync", nil)
		c.Params = gin.Params{{Key: "id", Value: "some-id"}}

		th.handlers.APITriggerSync(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIGetSourceLogs(t *testing.T) {
	t.Run("returns logs for valid source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		// Create some logs
		th.db.CreateSyncLog(&db.SyncLog{
			SourceID:      source.ID,
			Status:        db.SyncStatusSuccess,
			Message:       "Sync completed",
			EventsCreated: 5,
			Duration:      time.Second * 2,
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/"+source.ID+"/logs", nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIGetSourceLogs(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		logs := response["logs"].([]interface{})
		if len(logs) != 1 {
			t.Errorf("expected 1 log, got %d", len(logs))
		}
	})

	t.Run("returns paginated logs", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/"+source.ID+"/logs?page=2", nil)
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIGetSourceLogs(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		page := int(response["page"].(float64))
		if page != 2 {
			t.Errorf("expected page 2, got %d", page)
		}
	})

	t.Run("returns 404 for nonexistent source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/nonexistent/logs", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIGetSourceLogs(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sources/some-id/logs", nil)
		c.Params = gin.Params{{Key: "id", Value: "some-id"}}

		th.handlers.APIGetSourceLogs(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPISyncHistory(t *testing.T) {
	t.Run("returns sync history with default 7 days", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		// Create some logs
		th.db.CreateSyncLog(&db.SyncLog{
			SourceID:      source.ID,
			Status:        db.SyncStatusSuccess,
			Message:       "Sync completed",
			EventsCreated: 5,
			Duration:      time.Second * 2,
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/sync-history", nil)
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APISyncHistory(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response APISyncHistory
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if len(response.History) != 7 {
			t.Errorf("expected 7 history points, got %d", len(response.History))
		}
	})

	t.Run("accepts custom days parameter", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/sync-history?days=14", nil)
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APISyncHistory(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var response APISyncHistory
		json.Unmarshal(w.Body.Bytes(), &response)

		if len(response.History) != 14 {
			t.Errorf("expected 14 history points, got %d", len(response.History))
		}
	})

	t.Run("clamps days parameter to valid range", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/sync-history?days=100", nil)
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APISyncHistory(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var response APISyncHistory
		json.Unmarshal(w.Body.Bytes(), &response)

		// Should default to 7 since 100 > 30
		if len(response.History) != 7 {
			t.Errorf("expected 7 history points for invalid days, got %d", len(response.History))
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/sync-history", nil)

		th.handlers.APISyncHistory(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIDeleteMalformedEvent(t *testing.T) {
	t.Run("returns 404 for nonexistent event", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/malformed-events/nonexistent", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIDeleteMalformedEvent(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/malformed-events/some-id", nil)
		c.Params = gin.Params{{Key: "id", Value: "some-id"}}

		th.handlers.APIDeleteMalformedEvent(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIUpdateSource(t *testing.T) {
	t.Run("returns 404 for nonexistent source", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		body := `{"name": "Updated"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/sources/nonexistent", strings.NewReader(body))
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIUpdateSource(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns bad request for invalid JSON", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/sources/"+source.ID, strings.NewReader("invalid json"))
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIUpdateSource(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns bad request for invalid input", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		userID, source := createTestUserAndSource(t, th.db, "test@example.com", "Test Source")

		body := `{"name": "` + strings.Repeat("a", 200) + `"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/sources/"+source.ID, strings.NewReader(body))
		c.Params = gin.Params{{Key: "id", Value: source.ID}}
		setAuthContext(c, userID, "test@example.com")

		th.handlers.APIUpdateSource(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/sources/some-id", nil)
		c.Params = gin.Params{{Key: "id", Value: "some-id"}}

		th.handlers.APIUpdateSource(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPICreateSource(t *testing.T) {
	t.Run("returns bad request for invalid JSON", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader("invalid json"))
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APICreateSource(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns bad request for missing required fields", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		body := `{"name": "Test"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(body))
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APICreateSource(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns bad request for name too long", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		body := `{"name": "` + strings.Repeat("a", 200) + `", "source_url": "https://caldav.example.com", "source_username": "user", "source_password": "pass"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(body))
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APICreateSource(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/sources", nil)

		th.handlers.APICreateSource(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

func TestAPIDiscoverCalendars(t *testing.T) {
	t.Run("returns bad request for invalid JSON", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/calendars/discover", strings.NewReader("invalid"))
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIDiscoverCalendars(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns bad request for missing fields", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		user, _ := th.db.GetOrCreateUser("test@example.com", "Test User")

		body := `{"url": "https://example.com"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/calendars/discover", strings.NewReader(body))
		setAuthContext(c, user.ID, "test@example.com")

		th.handlers.APIDiscoverCalendars(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("returns unauthorized when not authenticated", func(t *testing.T) {
		th := setupTestHandlers(t)
		defer th.cleanup()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/calendars/discover", nil)

		th.handlers.APIDiscoverCalendars(c)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}

// Note: APILogout requires a session manager to be present.
// Full testing would require mocking the session manager.
// The handler is tested indirectly through integration tests.

func TestValidationInputConstants(t *testing.T) {
	t.Run("validation constants have expected values", func(t *testing.T) {
		if maxNameLength != 100 {
			t.Errorf("expected maxNameLength 100, got %d", maxNameLength)
		}
		if maxURLLength != 500 {
			t.Errorf("expected maxURLLength 500, got %d", maxURLLength)
		}
		if maxUsernameLength != 100 {
			t.Errorf("expected maxUsernameLength 100, got %d", maxUsernameLength)
		}
		if maxPasswordLength != 500 {
			t.Errorf("expected maxPasswordLength 500, got %d", maxPasswordLength)
		}
	})
}

func TestValidateSourceInputUsernameLength(t *testing.T) {
	t.Run("rejects source username too long", func(t *testing.T) {
		longUsername := strings.Repeat("a", 150)
		result := validateSourceInput("Name", "", "", "", "", "", longUsername, "")

		if result == "" || !strings.Contains(result, "Source username") {
			t.Error("expected error about source username length")
		}
	})

	t.Run("rejects dest username too long", func(t *testing.T) {
		longUsername := strings.Repeat("a", 150)
		result := validateSourceInput("Name", "", "", "", "", "", "", longUsername)

		if result == "" || !strings.Contains(result, "Destination username") {
			t.Error("expected error about destination username length")
		}
	})
}
