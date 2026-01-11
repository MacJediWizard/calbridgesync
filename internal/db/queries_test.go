package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupTestDB creates a temporary test database.
func setupTestDB(t *testing.T) (*DB, func()) {
	t.Helper()

	// Create a temp directory for the test database
	tempDir, err := os.MkdirTemp("", "calbridge-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	db, err := New(dbPath)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create test database: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tempDir)
	}

	return db, cleanup
}

// createTestUser creates a test user and returns the user ID.
func createTestUser(t *testing.T, db *DB, email string) string {
	t.Helper()

	user, err := db.GetOrCreateUser(email, "Test User")
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user.ID
}

// createTestSource creates a test source for a user.
func createTestSource(t *testing.T, db *DB, userID, name string) *Source {
	t.Helper()

	source := &Source{
		UserID:           userID,
		Name:             name,
		SourceType:       SourceTypeCustom,
		SourceURL:        "https://example.com/caldav",
		SourceUsername:   "user",
		SourcePassword:   "encrypted-password",
		DestURL:          "https://dest.com/caldav",
		DestUsername:     "destuser",
		DestPassword:     "encrypted-dest-password",
		SyncInterval:     300,
		SyncDirection:    SyncDirectionOneWay,
		ConflictStrategy: ConflictSourceWins,
		Enabled:          true,
	}

	err := db.CreateSource(source)
	if err != nil {
		t.Fatalf("failed to create test source: %v", err)
	}
	return source
}

func TestDeleteAllMalformedEventsForUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("deletes all malformed events for user", func(t *testing.T) {
		// Create test user and source
		userID := createTestUser(t, db, "test@example.com")
		source := createTestSource(t, db, userID, "Test Source")

		// Create some malformed events
		err := db.SaveMalformedEvent(source.ID, "/calendar/event1.ics", "Error 1")
		if err != nil {
			t.Fatalf("failed to save malformed event: %v", err)
		}
		err = db.SaveMalformedEvent(source.ID, "/calendar/event2.ics", "Error 2")
		if err != nil {
			t.Fatalf("failed to save malformed event: %v", err)
		}
		err = db.SaveMalformedEvent(source.ID, "/calendar/event3.ics", "Error 3")
		if err != nil {
			t.Fatalf("failed to save malformed event: %v", err)
		}

		// Verify events exist
		events, err := db.GetMalformedEvents(userID)
		if err != nil {
			t.Fatalf("failed to get malformed events: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("expected 3 events, got %d", len(events))
		}

		// Delete all malformed events
		deleted, err := db.DeleteAllMalformedEventsForUser(userID)
		if err != nil {
			t.Fatalf("failed to delete all malformed events: %v", err)
		}
		if deleted != 3 {
			t.Fatalf("expected 3 deleted, got %d", deleted)
		}

		// Verify events are gone
		events, err = db.GetMalformedEvents(userID)
		if err != nil {
			t.Fatalf("failed to get malformed events: %v", err)
		}
		if len(events) != 0 {
			t.Fatalf("expected 0 events, got %d", len(events))
		}
	})

	t.Run("does not delete other user's events", func(t *testing.T) {
		// Create two test users
		user1ID := createTestUser(t, db, "user1@example.com")
		user2ID := createTestUser(t, db, "user2@example.com")

		source1 := createTestSource(t, db, user1ID, "User1 Source")
		source2 := createTestSource(t, db, user2ID, "User2 Source")

		// Create events for both users
		db.SaveMalformedEvent(source1.ID, "/user1/event1.ics", "User1 Error")
		db.SaveMalformedEvent(source2.ID, "/user2/event1.ics", "User2 Error")
		db.SaveMalformedEvent(source2.ID, "/user2/event2.ics", "User2 Error 2")

		// Delete user1's events
		deleted, err := db.DeleteAllMalformedEventsForUser(user1ID)
		if err != nil {
			t.Fatalf("failed to delete: %v", err)
		}
		if deleted != 1 {
			t.Fatalf("expected 1 deleted for user1, got %d", deleted)
		}

		// User2's events should still exist
		events, err := db.GetMalformedEvents(user2ID)
		if err != nil {
			t.Fatalf("failed to get user2 events: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events for user2, got %d", len(events))
		}
	})

	t.Run("returns zero when no events exist", func(t *testing.T) {
		userID := createTestUser(t, db, "empty@example.com")

		deleted, err := db.DeleteAllMalformedEventsForUser(userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deleted != 0 {
			t.Fatalf("expected 0 deleted, got %d", deleted)
		}
	})

	t.Run("handles multiple sources for same user", func(t *testing.T) {
		userID := createTestUser(t, db, "multi@example.com")
		source1 := createTestSource(t, db, userID, "Source 1")
		source2 := createTestSource(t, db, userID, "Source 2")

		// Add events to both sources
		db.SaveMalformedEvent(source1.ID, "/source1/event.ics", "Error")
		db.SaveMalformedEvent(source2.ID, "/source2/event.ics", "Error")

		// Delete all
		deleted, err := db.DeleteAllMalformedEventsForUser(userID)
		if err != nil {
			t.Fatalf("failed to delete: %v", err)
		}
		if deleted != 2 {
			t.Fatalf("expected 2 deleted, got %d", deleted)
		}

		// Verify all gone
		events, err := db.GetMalformedEvents(userID)
		if err != nil {
			t.Fatalf("failed to get events: %v", err)
		}
		if len(events) != 0 {
			t.Fatalf("expected 0 events, got %d", len(events))
		}
	})
}

// ============================================================================
// User Tests
// ============================================================================

func TestGetOrCreateUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("creates new user", func(t *testing.T) {
		user, err := db.GetOrCreateUser("new@example.com", "New User")
		if err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		if user.ID == "" {
			t.Error("expected user ID to be set")
		}
		if user.Email != "new@example.com" {
			t.Errorf("expected email 'new@example.com', got %q", user.Email)
		}
		if user.Name != "New User" {
			t.Errorf("expected name 'New User', got %q", user.Name)
		}
	})

	t.Run("returns existing user", func(t *testing.T) {
		// Create user first
		user1, _ := db.GetOrCreateUser("existing@example.com", "First Name")

		// Get same user again
		user2, err := db.GetOrCreateUser("existing@example.com", "Different Name")
		if err != nil {
			t.Fatalf("failed to get user: %v", err)
		}

		if user1.ID != user2.ID {
			t.Error("expected same user ID")
		}
		// Name should be original, not updated
		if user2.Name != "First Name" {
			t.Errorf("expected original name 'First Name', got %q", user2.Name)
		}
	})
}

func TestGetUserByEmail(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("returns user by email", func(t *testing.T) {
		created, _ := db.GetOrCreateUser("byemail@example.com", "Test User")

		found, err := db.GetUserByEmail("byemail@example.com")
		if err != nil {
			t.Fatalf("failed to get user: %v", err)
		}

		if found.ID != created.ID {
			t.Error("expected same user")
		}
	})

	t.Run("returns ErrNotFound for unknown email", func(t *testing.T) {
		_, err := db.GetUserByEmail("unknown@example.com")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestGetUserByID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("returns user by ID", func(t *testing.T) {
		created, _ := db.GetOrCreateUser("byid@example.com", "Test User")

		found, err := db.GetUserByID(created.ID)
		if err != nil {
			t.Fatalf("failed to get user: %v", err)
		}

		if found.Email != created.Email {
			t.Error("expected same user")
		}
	})

	t.Run("returns ErrNotFound for unknown ID", func(t *testing.T) {
		_, err := db.GetUserByID("nonexistent-id")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

// ============================================================================
// Source Tests
// ============================================================================

func TestCreateSource(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "source-test@example.com")

	t.Run("creates source with all fields", func(t *testing.T) {
		source := &Source{
			UserID:            userID,
			Name:              "Test Calendar",
			SourceType:        SourceTypeICloud,
			SourceURL:         "https://caldav.icloud.com/",
			SourceUsername:    "user@icloud.com",
			SourcePassword:    "encrypted-pwd",
			DestURL:           "https://dest.example.com/dav/",
			DestUsername:      "destuser",
			DestPassword:      "encrypted-dest-pwd",
			SyncInterval:      600,
			SyncDirection:     SyncDirectionTwoWay,
			ConflictStrategy:  ConflictLatestWins,
			SelectedCalendars: []string{"/cal1/", "/cal2/"},
			Enabled:           true,
		}

		err := db.CreateSource(source)
		if err != nil {
			t.Fatalf("failed to create source: %v", err)
		}

		if source.ID == "" {
			t.Error("expected ID to be generated")
		}
		if source.LastSyncStatus != SyncStatusPending {
			t.Errorf("expected status pending, got %q", source.LastSyncStatus)
		}
	})

	t.Run("defaults sync direction to one_way", func(t *testing.T) {
		source := &Source{
			UserID:           userID,
			Name:             "Default Direction",
			SourceType:       SourceTypeCustom,
			SourceURL:        "https://example.com/",
			SourceUsername:   "user",
			SourcePassword:   "pwd",
			DestURL:          "https://dest.com/",
			DestUsername:     "dest",
			DestPassword:     "pwd",
			SyncInterval:     300,
			ConflictStrategy: ConflictSourceWins,
			Enabled:          true,
		}

		db.CreateSource(source)

		// Retrieve and check
		retrieved, _ := db.GetSourceByID(source.ID)
		if retrieved.SyncDirection != SyncDirectionOneWay {
			t.Errorf("expected one_way, got %q", retrieved.SyncDirection)
		}
	})
}

func TestGetSourceByID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "getsource@example.com")
	source := createTestSource(t, db, userID, "Test Source")

	t.Run("returns source by ID", func(t *testing.T) {
		found, err := db.GetSourceByID(source.ID)
		if err != nil {
			t.Fatalf("failed to get source: %v", err)
		}

		if found.Name != "Test Source" {
			t.Errorf("expected name 'Test Source', got %q", found.Name)
		}
	})

	t.Run("returns ErrNotFound for unknown ID", func(t *testing.T) {
		_, err := db.GetSourceByID("nonexistent-id")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestGetSourceByIDForUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user1ID := createTestUser(t, db, "user1-source@example.com")
	user2ID := createTestUser(t, db, "user2-source@example.com")
	source := createTestSource(t, db, user1ID, "User1 Source")

	t.Run("returns source for correct user", func(t *testing.T) {
		found, err := db.GetSourceByIDForUser(source.ID, user1ID)
		if err != nil {
			t.Fatalf("failed to get source: %v", err)
		}
		if found.Name != "User1 Source" {
			t.Error("wrong source returned")
		}
	})

	t.Run("returns ErrNotFound for wrong user", func(t *testing.T) {
		_, err := db.GetSourceByIDForUser(source.ID, user2ID)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestGetSourcesByUserID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "multi-source@example.com")
	createTestSource(t, db, userID, "Alpha Source")
	createTestSource(t, db, userID, "Beta Source")
	createTestSource(t, db, userID, "Gamma Source")

	t.Run("returns all sources for user ordered by name", func(t *testing.T) {
		sources, err := db.GetSourcesByUserID(userID)
		if err != nil {
			t.Fatalf("failed to get sources: %v", err)
		}

		if len(sources) != 3 {
			t.Fatalf("expected 3 sources, got %d", len(sources))
		}

		// Should be ordered by name
		if sources[0].Name != "Alpha Source" {
			t.Errorf("expected first source 'Alpha Source', got %q", sources[0].Name)
		}
		if sources[1].Name != "Beta Source" {
			t.Errorf("expected second source 'Beta Source', got %q", sources[1].Name)
		}
	})

	t.Run("returns empty slice for user with no sources", func(t *testing.T) {
		emptyUserID := createTestUser(t, db, "nosources@example.com")
		sources, err := db.GetSourcesByUserID(emptyUserID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sources) != 0 {
			t.Errorf("expected 0 sources, got %d", len(sources))
		}
	})
}

func TestGetEnabledSources(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "enabled@example.com")

	// Create enabled source
	enabledSource := createTestSource(t, db, userID, "Enabled")

	// Create disabled source
	disabledSource := &Source{
		UserID:           userID,
		Name:             "Disabled",
		SourceType:       SourceTypeCustom,
		SourceURL:        "https://example.com/",
		SourceUsername:   "user",
		SourcePassword:   "pwd",
		DestURL:          "https://dest.com/",
		DestUsername:     "dest",
		DestPassword:     "pwd",
		SyncInterval:     300,
		ConflictStrategy: ConflictSourceWins,
		Enabled:          false,
	}
	db.CreateSource(disabledSource)

	t.Run("returns only enabled sources", func(t *testing.T) {
		sources, err := db.GetEnabledSources()
		if err != nil {
			t.Fatalf("failed to get enabled sources: %v", err)
		}

		// Should only include enabled source
		found := false
		for _, s := range sources {
			if s.ID == enabledSource.ID {
				found = true
			}
			if s.ID == disabledSource.ID {
				t.Error("should not include disabled source")
			}
		}
		if !found {
			t.Error("should include enabled source")
		}
	})
}

func TestUpdateSource(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "update@example.com")
	source := createTestSource(t, db, userID, "Original Name")

	t.Run("updates source fields", func(t *testing.T) {
		source.Name = "Updated Name"
		source.SyncDirection = SyncDirectionTwoWay
		source.SyncInterval = 900
		source.SelectedCalendars = []string{"/updated/"}

		err := db.UpdateSource(source)
		if err != nil {
			t.Fatalf("failed to update source: %v", err)
		}

		// Retrieve and verify
		updated, _ := db.GetSourceByID(source.ID)
		if updated.Name != "Updated Name" {
			t.Errorf("expected name 'Updated Name', got %q", updated.Name)
		}
		if updated.SyncDirection != SyncDirectionTwoWay {
			t.Errorf("expected two_way, got %q", updated.SyncDirection)
		}
		if updated.SyncInterval != 900 {
			t.Errorf("expected interval 900, got %d", updated.SyncInterval)
		}
	})

	t.Run("returns ErrNotFound for nonexistent source", func(t *testing.T) {
		nonexistent := &Source{ID: "nonexistent-id"}
		err := db.UpdateSource(nonexistent)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestUpdateSourceSyncStatus(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "syncstatus@example.com")
	source := createTestSource(t, db, userID, "Status Test")

	t.Run("updates sync status", func(t *testing.T) {
		err := db.UpdateSourceSyncStatus(source.ID, SyncStatusSuccess, "Sync completed")
		if err != nil {
			t.Fatalf("failed to update status: %v", err)
		}

		updated, _ := db.GetSourceByID(source.ID)
		if updated.LastSyncStatus != SyncStatusSuccess {
			t.Errorf("expected status 'success', got %q", updated.LastSyncStatus)
		}
		if updated.LastSyncMessage != "Sync completed" {
			t.Errorf("expected message 'Sync completed', got %q", updated.LastSyncMessage)
		}
		if updated.LastSyncAt == nil {
			t.Error("expected LastSyncAt to be set")
		}
	})

	t.Run("returns ErrNotFound for nonexistent source", func(t *testing.T) {
		err := db.UpdateSourceSyncStatus("nonexistent-id", SyncStatusError, "Error")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestDeleteSource(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "delete@example.com")
	source := createTestSource(t, db, userID, "To Delete")

	t.Run("deletes source", func(t *testing.T) {
		err := db.DeleteSource(source.ID)
		if err != nil {
			t.Fatalf("failed to delete source: %v", err)
		}

		_, err = db.GetSourceByID(source.ID)
		if !errors.Is(err, ErrNotFound) {
			t.Error("source should be deleted")
		}
	})

	t.Run("returns ErrNotFound for nonexistent source", func(t *testing.T) {
		err := db.DeleteSource("nonexistent-id")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

// ============================================================================
// SyncState Tests
// ============================================================================

func TestSyncState(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "syncstate@example.com")
	source := createTestSource(t, db, userID, "Sync State Test")

	t.Run("upsert creates new sync state", func(t *testing.T) {
		state := &SyncState{
			SourceID:     source.ID,
			CalendarHref: "/calendar/default/",
			SyncToken:    "sync-token-123",
			CTag:         "ctag-456",
		}

		err := db.UpsertSyncState(state)
		if err != nil {
			t.Fatalf("failed to upsert: %v", err)
		}

		// Retrieve and verify
		retrieved, err := db.GetSyncState(source.ID, "/calendar/default/")
		if err != nil {
			t.Fatalf("failed to get sync state: %v", err)
		}
		if retrieved.SyncToken != "sync-token-123" {
			t.Errorf("expected sync token 'sync-token-123', got %q", retrieved.SyncToken)
		}
	})

	t.Run("upsert updates existing sync state", func(t *testing.T) {
		state := &SyncState{
			SourceID:     source.ID,
			CalendarHref: "/calendar/default/",
			SyncToken:    "updated-token",
			CTag:         "updated-ctag",
		}

		err := db.UpsertSyncState(state)
		if err != nil {
			t.Fatalf("failed to upsert: %v", err)
		}

		retrieved, _ := db.GetSyncState(source.ID, "/calendar/default/")
		if retrieved.SyncToken != "updated-token" {
			t.Errorf("expected 'updated-token', got %q", retrieved.SyncToken)
		}
	})

	t.Run("get returns ErrNotFound for unknown state", func(t *testing.T) {
		_, err := db.GetSyncState(source.ID, "/nonexistent/")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

// ============================================================================
// SyncLog Tests
// ============================================================================

func TestSyncLog(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "synclog@example.com")
	source := createTestSource(t, db, userID, "Sync Log Test")

	t.Run("creates and retrieves sync logs", func(t *testing.T) {
		log := &SyncLog{
			SourceID:        source.ID,
			Status:          SyncStatusSuccess,
			Message:         "Sync completed successfully",
			Details:         "Detailed info",
			Duration:        5 * time.Second,
			EventsCreated:   10,
			EventsUpdated:   5,
			EventsDeleted:   2,
			EventsSkipped:   1,
			CalendarsSynced: 3,
			EventsProcessed: 18,
		}

		err := db.CreateSyncLog(log)
		if err != nil {
			t.Fatalf("failed to create log: %v", err)
		}

		if log.ID == "" {
			t.Error("expected ID to be generated")
		}

		// Retrieve logs
		logs, err := db.GetSyncLogs(source.ID, 10)
		if err != nil {
			t.Fatalf("failed to get logs: %v", err)
		}

		if len(logs) != 1 {
			t.Fatalf("expected 1 log, got %d", len(logs))
		}

		if logs[0].EventsCreated != 10 {
			t.Errorf("expected 10 created, got %d", logs[0].EventsCreated)
		}
		if logs[0].Duration != 5*time.Second {
			t.Errorf("expected 5s duration, got %v", logs[0].Duration)
		}
	})

	t.Run("get logs respects limit", func(t *testing.T) {
		// Create multiple logs
		for i := 0; i < 5; i++ {
			db.CreateSyncLog(&SyncLog{
				SourceID: source.ID,
				Status:   SyncStatusSuccess,
				Message:  "Log entry",
			})
		}

		logs, _ := db.GetSyncLogs(source.ID, 3)
		if len(logs) != 3 {
			t.Errorf("expected 3 logs with limit, got %d", len(logs))
		}
	})

	t.Run("clean old logs", func(t *testing.T) {
		// Logs created above should be recent, so cleaning old ones shouldn't affect them
		deleted, err := db.CleanOldSyncLogs(time.Now().Add(-24 * time.Hour))
		if err != nil {
			t.Fatalf("failed to clean logs: %v", err)
		}
		// Should delete 0 since all logs are recent
		if deleted != 0 {
			t.Logf("deleted %d old logs", deleted)
		}
	})
}

// ============================================================================
// SyncedEvent Tests
// ============================================================================

func TestSyncedEvent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "syncedevent@example.com")
	source := createTestSource(t, db, userID, "Synced Event Test")
	calendarHref := "/calendar/default/"

	t.Run("upsert creates synced event", func(t *testing.T) {
		event := &SyncedEvent{
			SourceID:     source.ID,
			CalendarHref: calendarHref,
			EventUID:     "event-uid-123@example.com",
			SourceETag:   "source-etag",
			DestETag:     "dest-etag",
		}

		err := db.UpsertSyncedEvent(event)
		if err != nil {
			t.Fatalf("failed to upsert: %v", err)
		}

		// Retrieve
		events, err := db.GetSyncedEvents(source.ID, calendarHref)
		if err != nil {
			t.Fatalf("failed to get events: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].EventUID != "event-uid-123@example.com" {
			t.Error("wrong event UID")
		}
	})

	t.Run("upsert updates existing event", func(t *testing.T) {
		event := &SyncedEvent{
			SourceID:     source.ID,
			CalendarHref: calendarHref,
			EventUID:     "event-uid-123@example.com",
			SourceETag:   "updated-source-etag",
			DestETag:     "updated-dest-etag",
		}

		err := db.UpsertSyncedEvent(event)
		if err != nil {
			t.Fatalf("failed to upsert: %v", err)
		}

		events, _ := db.GetSyncedEvents(source.ID, calendarHref)
		if events[0].SourceETag != "updated-source-etag" {
			t.Error("etag not updated")
		}
	})

	t.Run("delete synced event", func(t *testing.T) {
		err := db.DeleteSyncedEvent(source.ID, calendarHref, "event-uid-123@example.com")
		if err != nil {
			t.Fatalf("failed to delete: %v", err)
		}

		events, _ := db.GetSyncedEvents(source.ID, calendarHref)
		if len(events) != 0 {
			t.Error("event should be deleted")
		}
	})

	t.Run("delete all events for calendar", func(t *testing.T) {
		// Create multiple events
		db.UpsertSyncedEvent(&SyncedEvent{SourceID: source.ID, CalendarHref: calendarHref, EventUID: "uid1"})
		db.UpsertSyncedEvent(&SyncedEvent{SourceID: source.ID, CalendarHref: calendarHref, EventUID: "uid2"})

		err := db.DeleteSyncedEventsForCalendar(source.ID, calendarHref)
		if err != nil {
			t.Fatalf("failed to delete: %v", err)
		}

		events, _ := db.GetSyncedEvents(source.ID, calendarHref)
		if len(events) != 0 {
			t.Error("all events should be deleted")
		}
	})
}

// ============================================================================
// MalformedEvent Tests
// ============================================================================

func TestMalformedEvent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	userID := createTestUser(t, db, "malformed@example.com")
	source := createTestSource(t, db, userID, "Malformed Test")

	t.Run("save and get malformed event", func(t *testing.T) {
		err := db.SaveMalformedEvent(source.ID, "/calendar/broken.ics", "Invalid iCal format")
		if err != nil {
			t.Fatalf("failed to save: %v", err)
		}

		events, err := db.GetMalformedEvents(userID)
		if err != nil {
			t.Fatalf("failed to get events: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].EventPath != "/calendar/broken.ics" {
			t.Error("wrong event path")
		}
		if events[0].SourceName != "Malformed Test" {
			t.Errorf("expected source name 'Malformed Test', got %q", events[0].SourceName)
		}
	})

	t.Run("get malformed event by ID for user", func(t *testing.T) {
		events, _ := db.GetMalformedEvents(userID)
		eventID := events[0].ID

		event, err := db.GetMalformedEventByIDForUser(eventID, userID)
		if err != nil {
			t.Fatalf("failed to get event: %v", err)
		}
		if event.EventPath != "/calendar/broken.ics" {
			t.Error("wrong event")
		}
	})

	t.Run("get malformed event by ID for wrong user returns error", func(t *testing.T) {
		events, _ := db.GetMalformedEvents(userID)
		eventID := events[0].ID

		otherUserID := createTestUser(t, db, "other@example.com")
		_, err := db.GetMalformedEventByIDForUser(eventID, otherUserID)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("delete malformed event", func(t *testing.T) {
		events, _ := db.GetMalformedEvents(userID)
		eventID := events[0].ID

		err := db.DeleteMalformedEvent(eventID)
		if err != nil {
			t.Fatalf("failed to delete: %v", err)
		}

		_, err = db.GetMalformedEventByID(eventID)
		if !errors.Is(err, ErrNotFound) {
			t.Error("event should be deleted")
		}
	})

	t.Run("clear malformed events for source", func(t *testing.T) {
		// Create some events
		db.SaveMalformedEvent(source.ID, "/event1.ics", "Error 1")
		db.SaveMalformedEvent(source.ID, "/event2.ics", "Error 2")

		err := db.ClearMalformedEventsForSource(source.ID)
		if err != nil {
			t.Fatalf("failed to clear: %v", err)
		}

		events, _ := db.GetMalformedEvents(userID)
		if len(events) != 0 {
			t.Error("all events should be cleared")
		}
	})
}

// ============================================================================
// Database Connection Tests
// ============================================================================

func TestDatabaseConnection(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("ping succeeds", func(t *testing.T) {
		err := db.Ping()
		if err != nil {
			t.Errorf("ping failed: %v", err)
		}
	})

	t.Run("conn returns connection", func(t *testing.T) {
		conn := db.Conn()
		if conn == nil {
			t.Error("expected non-nil connection")
		}
	})
}
