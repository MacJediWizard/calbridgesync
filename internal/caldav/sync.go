package caldav

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/activity"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

// SyncResult represents the result of a sync operation.
type SyncResult struct {
	Success           bool          `json:"success"`
	Message           string        `json:"message"`
	Created           int           `json:"created"`
	Updated           int           `json:"updated"`
	Deleted           int           `json:"deleted"`
	Skipped           int           `json:"skipped"`
	DuplicatesRemoved int           `json:"duplicates_removed"`
	CalendarsSynced   int           `json:"calendars_synced"`
	EventsProcessed   int           `json:"events_processed"`
	Errors            []string      `json:"errors,omitempty"`   // Critical errors that prevent sync
	Warnings          []string      `json:"warnings,omitempty"` // Non-critical issues (individual event failures)
	Duration          time.Duration `json:"duration"`
}

// sanitizeLogDetails removes potentially sensitive information from sync log details.
// This prevents leaking server internal paths, stack traces, or network info.
func sanitizeLogDetails(details string) string {
	if details == "" {
		return ""
	}

	// Remove potential IP addresses
	// Remove potential file paths that might reveal server structure
	// Keep the message useful but remove internal details

	// Truncate very long details (could contain memory dumps or stack traces)
	const maxLength = 2000
	if len(details) > maxLength {
		details = details[:maxLength] + "... (truncated)"
	}

	return details
}

// retryDBOperation retries a database operation with exponential backoff.
// This helps handle SQLite "database is locked" errors during concurrent operations.
func retryDBOperation(operation func() error, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := operation(); err != nil {
			lastErr = err
			// Check if it's a busy/locked error worth retrying
			if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
				backoff := time.Duration(100*(1<<i)) * time.Millisecond // 100ms, 200ms, 400ms, ...
				if backoff > 5*time.Second {
					backoff = 5 * time.Second
				}
				time.Sleep(backoff)
				continue
			}
			return err // Non-retryable error
		}
		return nil // Success
	}
	return lastErr
}

// SyncEngine orchestrates calendar synchronization.
type SyncEngine struct {
	db        *db.DB
	encryptor *crypto.Encryptor
	tracker   *activity.Tracker
}

// NewSyncEngine creates a new sync engine.
func NewSyncEngine(database *db.DB, encryptor *crypto.Encryptor) *SyncEngine {
	return &SyncEngine{
		db:        database,
		encryptor: encryptor,
		tracker:   activity.NewTracker(),
	}
}

// GetActivityTracker returns the activity tracker for external use.
func (se *SyncEngine) GetActivityTracker() *activity.Tracker {
	return se.tracker
}

// SyncSource performs synchronization for a single source.
func (se *SyncEngine) SyncSource(ctx context.Context, source *db.Source) *SyncResult {
	start := time.Now()
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Update status to running (with retry for concurrent access)
	if err := retryDBOperation(func() error {
		return se.db.UpdateSourceSyncStatus(source.ID, db.SyncStatusRunning, "Sync in progress")
	}, 5); err != nil {
		log.Printf("Failed to update sync status after retries: %v", err)
	}

	// Decrypt credentials - NEVER log these
	sourcePassword, err := se.encryptor.Decrypt(source.SourcePassword)
	if err != nil {
		result.Message = "Failed to decrypt source credentials"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	destPassword, err := se.encryptor.Decrypt(source.DestPassword)
	if err != nil {
		result.Message = "Failed to decrypt destination credentials"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create source client
	sourceClient, err := NewClient(source.SourceURL, source.SourceUsername, sourcePassword)
	if err != nil {
		result.Message = "Failed to connect to source"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create destination client
	destClient, err := NewClient(source.DestURL, source.DestUsername, destPassword)
	if err != nil {
		result.Message = "Failed to connect to destination"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Test connections
	if err := sourceClient.TestConnection(ctx); err != nil {
		result.Message = "Source connection test failed"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	if err := destClient.TestConnection(ctx); err != nil {
		result.Message = "Destination connection test failed"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Find calendars on source
	sourceCalendars, err := sourceClient.FindCalendars(ctx)
	if err != nil {
		result.Message = "Failed to find source calendars"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Log discovered calendars
	log.Printf("Found %d calendars on source:", len(sourceCalendars))
	for i, cal := range sourceCalendars {
		log.Printf("  [%d] Name: %q, Path: %s", i+1, cal.Name, cal.Path)
	}

	// Filter calendars based on selected_calendars setting
	if len(source.SelectedCalendars) > 0 {
		selectedSet := make(map[string]bool)
		for _, path := range source.SelectedCalendars {
			selectedSet[path] = true
		}

		var filteredCalendars []Calendar
		for _, cal := range sourceCalendars {
			if selectedSet[cal.Path] {
				filteredCalendars = append(filteredCalendars, cal)
			}
		}

		log.Printf("Filtered to %d selected calendars (from %d discovered)", len(filteredCalendars), len(sourceCalendars))
		sourceCalendars = filteredCalendars
	}

	// Start activity tracking
	se.tracker.StartSync(source.ID, source.Name, len(sourceCalendars))

	// Sync each calendar
	for i, cal := range sourceCalendars {
		// Update activity tracker with current calendar
		se.tracker.UpdateCalendar(source.ID, cal.Name, i+1)

		calResult := se.syncCalendar(ctx, source, sourceClient, destClient, cal)
		result.Created += calResult.Created
		result.Updated += calResult.Updated
		result.Deleted += calResult.Deleted
		result.Skipped += calResult.Skipped
		result.EventsProcessed += calResult.EventsProcessed
		result.Errors = append(result.Errors, calResult.Errors...)
		result.Warnings = append(result.Warnings, calResult.Warnings...)

		// Update progress in activity tracker
		se.tracker.UpdateProgress(source.ID, result.Created, result.Updated, result.Deleted, result.Skipped, result.EventsProcessed)
	}

	result.CalendarsSynced = len(sourceCalendars)
	// Success if no critical errors (warnings are OK)
	result.Success = len(result.Errors) == 0
	if result.Success && len(result.Warnings) == 0 {
		result.Message = fmt.Sprintf("Synced %d calendar(s): %d created, %d updated, %d deleted, %d skipped",
			len(sourceCalendars), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else if result.Success && len(result.Warnings) > 0 {
		result.Message = fmt.Sprintf("Synced %d calendar(s) with %d warnings: %d created, %d updated, %d deleted, %d skipped",
			len(sourceCalendars), len(result.Warnings), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else {
		result.Message = fmt.Sprintf("Sync failed with %d errors", len(result.Errors))
	}

	result.Duration = time.Since(start)
	se.finishSync(source.ID, result)

	return result
}

func (se *SyncEngine) syncCalendar(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Check for existing sync state
	syncState, err := se.db.GetSyncState(source.ID, calendar.Path)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get sync state: %v", err))
		return result
	}

	var syncToken string
	if syncState != nil {
		syncToken = syncState.SyncToken
	}

	// Get the destination calendar path from the destination client's base URL
	destCalendarPath := destClient.GetCalendarPath()

	// Try WebDAV-Sync if supported
	if sourceClient.SupportsWebDAVSync(ctx, calendar.Path) {
		syncResult, err := sourceClient.SyncCollection(ctx, calendar.Path, syncToken)
		if err == nil {
			// Process changes
			for _, item := range syncResult.Changed {
				if item.Data != "" {
					event := &Event{
						Path: item.Path,
						ETag: item.ETag,
						Data: item.Data,
					}
					if err := destClient.PutEvent(ctx, destCalendarPath, event); err != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to sync event: %v", err))
					} else {
						result.Updated++
					}
				}
			}

			for _, path := range syncResult.Deleted {
				if err := destClient.DeleteEvent(ctx, path); err != nil {
					// Don't count as error if event doesn't exist on destination
					log.Printf("Failed to delete event %s: %v", path, err)
				} else {
					result.Deleted++
				}
			}

			// Update sync state
			newState := &db.SyncState{
				SourceID:     source.ID,
				CalendarHref: calendar.Path,
				SyncToken:    syncResult.SyncToken,
			}
			if err := se.db.UpsertSyncState(newState); err != nil {
				log.Printf("Failed to update sync state: %v", err)
			}

			return result
		}
		// Fall through to full sync if WebDAV-Sync fails
		log.Printf("WebDAV-Sync failed, falling back to full sync: %v", err)
	}

	// Full sync fallback
	return se.fullSync(ctx, source, sourceClient, destClient, calendar)
}

func (se *SyncEngine) fullSync(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Create collector for malformed events from source
	malformedCollector := NewMalformedEventCollector()

	// Clear old malformed events for this source before sync
	if err := se.db.ClearMalformedEventsForSource(source.ID); err != nil {
		log.Printf("Failed to clear old malformed events: %v", err)
	}

	// Get all events from source
	sourceEvents, err := sourceClient.GetEvents(ctx, calendar.Path, malformedCollector)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get source events: %v", err))
		return result
	}

	// Store any malformed events found
	for _, mf := range malformedCollector.GetEvents() {
		if err := se.db.SaveMalformedEvent(source.ID, mf.Path, mf.ErrorMessage); err != nil {
			log.Printf("Failed to save malformed event record: %v", err)
		}
	}

	// Discover destination calendar path - try calendar discovery first, then fall back to URL path
	destCalendarPath := ""
	destCalendars, err := destClient.FindCalendars(ctx)
	if err != nil {
		log.Printf("Failed to discover destination calendars, falling back to URL path: %v", err)
		destCalendarPath = destClient.GetCalendarPath()
	} else if len(destCalendars) == 0 {
		log.Printf("No calendars found on destination, using URL path as fallback")
		destCalendarPath = destClient.GetCalendarPath()
	} else {
		log.Printf("Found %d calendar(s) on destination:", len(destCalendars))
		for i, cal := range destCalendars {
			log.Printf("  [%d] Name: %q, Path: %s", i+1, cal.Name, cal.Path)
		}
		// Use the first calendar found (most destinations have a single calendar for syncing)
		// Note: Future enhancement could match calendars by name for multi-calendar destinations.
		// Current behavior: Uses first available calendar, which works for typical single-calendar setups.
		destCalendarPath = destCalendars[0].Path
		if len(destCalendars) > 1 {
			log.Printf("WARNING: Multiple destination calendars found, using first one: %s", destCalendarPath)
		}
	}
	log.Printf("Using destination calendar path: %s", destCalendarPath)

	// Get all events from destination (no collector needed - we only track source issues)
	destEvents, err := destClient.GetEvents(ctx, destCalendarPath, nil)
	if err != nil {
		log.Printf("Failed to get destination events (path: %s): %v", destCalendarPath, err)
		destEvents = []Event{}
	}
	log.Printf("Fetched %d events from destination calendar", len(destEvents))

	// Get previously synced events for deletion detection
	previouslySynced, err := se.db.GetSyncedEvents(source.ID, calendar.Path)
	if err != nil {
		log.Printf("Failed to get synced events: %v", err)
		previouslySynced = []*db.SyncedEvent{}
	}

	// Build map of previously synced UIDs
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	for _, se := range previouslySynced {
		previouslySyncedMap[se.EventUID] = se
	}

	// Create maps for comparison by UID
	sourceEventMap := make(map[string]Event)
	for _, e := range sourceEvents {
		if e.UID != "" {
			sourceEventMap[e.UID] = e
		}
	}

	destEventMap := make(map[string]Event)
	for _, e := range destEvents {
		if e.UID != "" {
			destEventMap[e.UID] = e
		}
	}

	// Create deduplication map using summary + start time
	destDedupeMap := make(map[string]bool)
	for _, e := range destEvents {
		key := e.DedupeKey()
		if key != "|" {
			destDedupeMap[key] = true
			log.Printf("Dest dedupe key: %q (UID: %s)", key, e.UID)
		}
	}

	skippedDupes := 0

	// Track UIDs that exist in current sync (for updating synced_events table)
	currentUIDs := make(map[string]bool)

	// Handle deletions first (for two-way sync)
	// SAFETY: Only delete from source if the event was synced at least one sync cycle ago
	// This prevents deleting events that failed to sync to destination
	syncSafetyThreshold := time.Now().Add(-time.Duration(source.SyncInterval) * time.Second)

	// SAFETY: Skip two-way deletion if destination query returned empty but we have synced events
	// This prevents mass deletion from source when destination query fails
	skipTwoWayDeletion := false
	if source.SyncDirection == db.SyncDirectionTwoWay && len(destEventMap) == 0 && len(previouslySyncedMap) > 0 {
		log.Printf("WARNING: Destination returned 0 events but we have %d previously synced events - skipping two-way deletions for safety", len(previouslySyncedMap))
		skipTwoWayDeletion = true
	}

	if source.SyncDirection == db.SyncDirectionTwoWay && !skipTwoWayDeletion {
		for uid, syncedEvent := range previouslySyncedMap {
			_, existsOnSource := sourceEventMap[uid]
			destEvent, existsOnDest := destEventMap[uid]

			if !existsOnSource && existsOnDest {
				// Event was deleted from source - delete from destination too
				log.Printf("Event %s deleted from source, deleting from destination", uid)
				if err := destClient.DeleteEvent(ctx, destEvent.Path); err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete event from dest: %v", err))
				} else {
					result.Deleted++
				}
				// Remove from synced_events
				if err := se.db.DeleteSyncedEvent(source.ID, calendar.Path, uid); err != nil {
					log.Printf("Failed to delete synced event record: %v", err)
				}
				delete(destEventMap, uid)
				continue
			}

			sourceEvent, existsOnSource := sourceEventMap[uid]
			if existsOnSource && !existsOnDest {
				// SAFETY CHECK: Only delete from source if the event was synced before the safety threshold
				// This prevents deleting events that never synced successfully to destination
				if syncedEvent.UpdatedAt.After(syncSafetyThreshold) {
					log.Printf("Event %s not on destination but synced recently - skipping deletion from source (safety)", uid)
					continue
				}

				// Event was deleted from destination - delete from source too
				log.Printf("Event %s deleted from destination, deleting from source", uid)
				if err := sourceClient.DeleteEvent(ctx, sourceEvent.Path); err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete event from source: %v", err))
				} else {
					result.Deleted++
				}
				// Remove from synced_events
				if err := se.db.DeleteSyncedEvent(source.ID, calendar.Path, uid); err != nil {
					log.Printf("Failed to delete synced event record: %v", err)
				}
				delete(sourceEventMap, uid)
				continue
			}

			if !existsOnSource && !existsOnDest {
				// Event deleted from both - just clean up the record
				if err := se.db.DeleteSyncedEvent(source.ID, calendar.Path, syncedEvent.EventUID); err != nil {
					log.Printf("Failed to delete synced event record: %v", err)
				}
			}
		}
	}

	// Sync source events to destination
	for _, sourceEvent := range sourceEvents {
		if sourceEvent.UID == "" {
			continue
		}

		destEvent, existsByUID := destEventMap[sourceEvent.UID]

		if !existsByUID {
			// Check for duplicate by content
			dedupeKey := sourceEvent.DedupeKey()
			log.Printf("Source dedupe key: %q (UID: %s)", dedupeKey, sourceEvent.UID)
			if dedupeKey != "|" && destDedupeMap[dedupeKey] {
				skippedDupes++
				log.Printf("Skipping duplicate event: %s at %s (dedupe key match)", sourceEvent.Summary, sourceEvent.StartTime)
				continue
			}

			// Create new event on destination
			if err := destClient.PutEvent(ctx, destCalendarPath, &sourceEvent); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to create event on dest: %v", err))
			} else {
				result.Created++
				if dedupeKey != "|" {
					destDedupeMap[dedupeKey] = true
				}
				currentUIDs[sourceEvent.UID] = true
			}
		} else if sourceEvent.ETag != destEvent.ETag {
			// Update existing event
			sourceEvent.Path = destEvent.Path
			if err := destClient.PutEvent(ctx, destCalendarPath, &sourceEvent); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to update event on dest: %v", err))
			} else {
				result.Updated++
				currentUIDs[sourceEvent.UID] = true
			}
		} else {
			// Event unchanged, still track it
			currentUIDs[sourceEvent.UID] = true
		}
		delete(destEventMap, sourceEvent.UID)
	}

	result.Skipped = skippedDupes
	result.EventsProcessed = len(sourceEvents)

	if skippedDupes > 0 {
		log.Printf("Skipped %d duplicate events", skippedDupes)
	}

	// Two-way sync: sync destination events back to source
	if source.SyncDirection == db.SyncDirectionTwoWay {
		log.Printf("Two-way sync enabled, syncing destination events to source")
		for _, destEvent := range destEvents {
			if destEvent.UID == "" {
				continue
			}

			sourceEvent, exists := sourceEventMap[destEvent.UID]

			if !exists {
				// Check if this was previously synced (meaning it was deleted from source)
				if _, wasSynced := previouslySyncedMap[destEvent.UID]; wasSynced {
					// Already handled in deletion phase above
					continue
				}

				// New event on destination - create on source
				if err := sourceClient.PutEvent(ctx, calendar.Path, &destEvent); err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to create event on source: %v", err))
				} else {
					result.Created++
					currentUIDs[destEvent.UID] = true
				}
			} else if destEvent.ETag != sourceEvent.ETag {
				if source.ConflictStrategy == db.ConflictDestWins {
					destEvent.Path = sourceEvent.Path
					if err := sourceClient.PutEvent(ctx, calendar.Path, &destEvent); err != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to update event on source: %v", err))
					} else {
						result.Updated++
					}
				}
				currentUIDs[destEvent.UID] = true
			} else {
				currentUIDs[destEvent.UID] = true
			}
		}
	}

	// One-way sync: delete orphan events on destination
	if source.SyncDirection == db.SyncDirectionOneWay && source.ConflictStrategy == db.ConflictSourceWins {
		for _, event := range destEventMap {
			if err := destClient.DeleteEvent(ctx, event.Path); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete orphan event: %v", err))
			} else {
				result.Deleted++
			}
		}
	}

	// Clean up duplicate events on destination
	duplicatesRemoved := se.cleanupDuplicates(ctx, destClient, destCalendarPath, sourceEventMap)
	result.DuplicatesRemoved = duplicatesRemoved
	if duplicatesRemoved > 0 {
		log.Printf("Removed %d duplicate events from destination", duplicatesRemoved)
	}

	// Update synced_events table with current state
	for uid := range currentUIDs {
		syncedEvent := &db.SyncedEvent{
			SourceID:     source.ID,
			CalendarHref: calendar.Path,
			EventUID:     uid,
		}
		if err := se.db.UpsertSyncedEvent(syncedEvent); err != nil {
			log.Printf("Failed to upsert synced event: %v", err)
		}
	}

	return result
}

// cleanupDuplicates removes duplicate events from destination calendar.
// It groups events by Summary+StartTime and keeps the one matching a source UID,
// or the first one if no match. Returns the number of duplicates removed.
func (se *SyncEngine) cleanupDuplicates(ctx context.Context, destClient *Client, destCalendarPath string, sourceEventMap map[string]Event) int {
	log.Printf("Starting duplicate cleanup for destination: %s", destCalendarPath)

	// Re-fetch destination events to get current state
	destEvents, err := destClient.GetEvents(ctx, destCalendarPath, nil)
	if err != nil {
		log.Printf("Failed to get destination events for duplicate cleanup: %v", err)
		return 0
	}
	log.Printf("Fetched %d destination events for duplicate check", len(destEvents))

	// Group events by dedupe key (Summary + StartTime)
	type eventGroup struct {
		events []Event
	}
	groups := make(map[string]*eventGroup)

	for _, event := range destEvents {
		key := event.DedupeKey()
		if key == "|" { // Empty summary and start time
			continue
		}
		if groups[key] == nil {
			groups[key] = &eventGroup{events: make([]Event, 0)}
		}
		groups[key].events = append(groups[key].events, event)
	}

	// Find and delete duplicates
	duplicatesRemoved := 0
	duplicateGroups := 0
	for key, group := range groups {
		if len(group.events) <= 1 {
			continue // No duplicates
		}
		duplicateGroups++
		log.Printf("Found %d duplicates for: %s", len(group.events), key)

		// Determine which event to keep:
		// 1. Prefer event with UID matching a source event
		// 2. Otherwise keep the first one (arbitrary but consistent)
		keepIndex := 0
		for i, event := range group.events {
			if _, existsInSource := sourceEventMap[event.UID]; existsInSource {
				keepIndex = i
				break
			}
		}

		// Delete all except the one we're keeping
		for i, event := range group.events {
			if i == keepIndex {
				log.Printf("Keeping event: %s (UID: %s)", event.Path, event.UID)
				continue
			}

			log.Printf("Deleting duplicate event: %s (UID: %s)", event.Path, event.UID)
			if err := destClient.DeleteEvent(ctx, event.Path); err != nil {
				log.Printf("Failed to delete duplicate event %s: %v", event.Path, err)
			} else {
				duplicatesRemoved++
			}
		}
	}

	log.Printf("Duplicate cleanup complete: found %d duplicate groups, removed %d events", duplicateGroups, duplicatesRemoved)
	return duplicatesRemoved
}

func (se *SyncEngine) finishSync(sourceID string, result *SyncResult) {
	// Determine status: error > partial > success
	var status db.SyncStatus
	if !result.Success {
		status = db.SyncStatusError
	} else if len(result.Warnings) > 0 {
		status = db.SyncStatusPartial
	} else {
		status = db.SyncStatusSuccess
	}

	// Update status with retry for concurrent access
	if err := retryDBOperation(func() error {
		return se.db.UpdateSourceSyncStatus(sourceID, status, result.Message)
	}, 5); err != nil {
		log.Printf("Failed to update sync status after retries: %v", err)
	}

	// Create sync log with detailed stats
	syncLog := &db.SyncLog{
		SourceID:        sourceID,
		Status:          status,
		Message:         result.Message,
		Duration:        result.Duration,
		EventsCreated:   result.Created,
		EventsUpdated:   result.Updated,
		EventsDeleted:   result.Deleted,
		EventsSkipped:   result.Skipped,
		CalendarsSynced: result.CalendarsSynced,
		EventsProcessed: result.EventsProcessed,
	}

	// Include both errors and warnings in details (sanitized to remove sensitive info)
	var details []string
	if len(result.Errors) > 0 {
		details = append(details, fmt.Sprintf("Errors: %v", result.Errors))
	}
	if len(result.Warnings) > 0 {
		details = append(details, fmt.Sprintf("Warnings: %v", result.Warnings))
	}
	if len(details) > 0 {
		syncLog.Details = sanitizeLogDetails(strings.Join(details, "\n"))
	}

	// Create sync log with retry for concurrent access
	if err := retryDBOperation(func() error {
		return se.db.CreateSyncLog(syncLog)
	}, 5); err != nil {
		log.Printf("Failed to create sync log after retries: %v", err)
	}

	// Finish activity tracking
	se.tracker.FinishSync(sourceID, result.Success, result.Message, result.Errors)
}

// TestConnection tests connection to a CalDAV endpoint.
func (se *SyncEngine) TestConnection(ctx context.Context, url, username, password string) error {
	client, err := NewClient(url, username, password)
	if err != nil {
		return err
	}
	return client.TestConnection(ctx)
}
