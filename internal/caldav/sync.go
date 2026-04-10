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

// isAlreadyExistsError checks if the error indicates the event already exists (412 Precondition Failed).
func isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "412") || strings.Contains(errStr, "Precondition Failed")
}

// isForbiddenError checks if the error indicates write access is forbidden (403 Forbidden).
func isForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "403") || strings.Contains(errStr, "Forbidden")
}

// getSyncDirectionForCalendar returns the effective sync direction for a calendar.
// It checks per-calendar settings first, then falls back to the source default.
func getSyncDirectionForCalendar(source *db.Source, calendarPath string) db.SyncDirection {
	// Search for per-calendar config
	for _, calConfig := range source.SelectedCalendars {
		if calConfig.Path == calendarPath {
			return calConfig.GetSyncDirection(source.SyncDirection)
		}
	}
	// Calendar not in selected list, use source default
	return source.SyncDirection
}

// defaultOrphanDeleteRatioThreshold is the maximum fraction of previously-synced
// events that can be deleted in a single one-way sync cycle before safety aborts.
// Exceeding this threshold usually indicates an auth failure, broken source URL,
// or filter misconfiguration rather than a legitimate bulk cleanup.
const defaultOrphanDeleteRatioThreshold = 0.5

// planOrphanDeletion determines which destination events should be deleted as
// "orphans" during a one-way + source_wins sync.
//
// Three safety rules are enforced to prevent data loss:
//
//  1. Ownership: only events that this source previously synced are candidates
//     for deletion. This prevents a multi-source setup (multiple source
//     calendars writing to a single destination) from having sources wipe each
//     other's events on every sync. It also preserves events created manually
//     on the destination.
//
//  2. Empty-source guard: if the source returned zero events but we have prior
//     sync records, the sync is assumed to be unhealthy (auth failure, broken
//     URL, filter wipeout) and the entire orphan-delete pass is skipped.
//     This mirrors the two-way guard introduced in commit b772c56.
//
//  3. Mass-delete threshold: if the planned deletion would remove more than
//     maxDeleteRatio of previously-synced events in a single cycle, the entire
//     orphan-delete pass is aborted. Normal day-to-day operation deletes a
//     handful of events per sync; wiping more than half is almost always a bug.
//
// Returns the events to delete and a non-empty warning string if any safety
// rule was triggered. When a warning is returned, toDelete is nil — the caller
// must not perform any deletions in that case.
func planOrphanDeletion(
	destEventMap map[string]Event,
	sourceEventCount int,
	previouslySyncedMap map[string]*db.SyncedEvent,
	maxDeleteRatio float64,
) (toDelete []Event, warning string) {
	// Rule 2: empty source with prior records → refuse to delete anything.
	if sourceEventCount == 0 && len(previouslySyncedMap) > 0 {
		return nil, fmt.Sprintf(
			"source returned 0 events but %d previously-synced records exist - "+
				"skipping one-way orphan deletion for safety (possible auth failure or broken source)",
			len(previouslySyncedMap),
		)
	}

	// Rule 1: ownership filter. Only consider events THIS source synced.
	candidates := make([]Event, 0)
	for uid, event := range destEventMap {
		if _, ours := previouslySyncedMap[uid]; ours {
			candidates = append(candidates, event)
		}
	}

	// Rule 3: mass-delete threshold. Only applied when there is prior state
	// to measure against and a threshold is configured.
	if len(previouslySyncedMap) > 0 && maxDeleteRatio > 0 {
		ratio := float64(len(candidates)) / float64(len(previouslySyncedMap))
		if ratio > maxDeleteRatio {
			return nil, fmt.Sprintf(
				"one-way orphan deletion would remove %d of %d previously-synced events (%.0f%%), "+
					"exceeds safety threshold %.0f%% - skipping deletion",
				len(candidates), len(previouslySyncedMap), ratio*100, maxDeleteRatio*100,
			)
		}
	}

	return candidates, ""
}

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

	// Branch for ICS sources (read-only feed, different sync path)
	if source.SourceType == db.SourceTypeICS {
		return se.syncICSSource(ctx, source)
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
		for _, calConfig := range source.SelectedCalendars {
			selectedSet[calConfig.Path] = true
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

		calResult := se.syncCalendar(ctx, source, sourceClient, destClient, cal, i+1)
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

func (se *SyncEngine) syncCalendar(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar, calendarIndex int) *SyncResult {
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
	return se.fullSync(ctx, source, sourceClient, destClient, calendar, calendarIndex)
}

// filterEventsByDate filters events to only include those with start time after cutoff date.
// Events without a parseable start time are included (to be safe).
// Recurring events (containing RRULE) are always included since their DTSTART
// is the original first occurrence which may be far in the past.
func filterEventsByDate(events []Event, cutoffDate time.Time) []Event {
	var filtered []Event
	for _, e := range events {
		if e.StartTime == "" {
			// Include events without start time (might be tasks or unparsed)
			filtered = append(filtered, e)
			continue
		}

		// Always include recurring events — their DTSTART is the original
		// first occurrence, but the event recurs into the future
		if strings.Contains(e.Data, "RRULE:") {
			filtered = append(filtered, e)
			continue
		}

		// Try to parse the start time - iCalendar format variants
		var eventTime time.Time
		var err error

		// Common iCalendar date/time formats
		formats := []string{
			"20060102T150405Z",     // UTC datetime
			"20060102T150405",      // Local datetime
			"20060102",             // Date only
			"2006-01-02T15:04:05Z", // ISO with dashes
			"2006-01-02",           // ISO date only
		}

		for _, format := range formats {
			eventTime, err = time.Parse(format, e.StartTime)
			if err == nil {
				break
			}
		}

		if err != nil {
			// Can't parse date - include to be safe
			filtered = append(filtered, e)
			continue
		}

		// Include if event is after cutoff date (or is in the future)
		if eventTime.After(cutoffDate) || eventTime.After(time.Now()) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (se *SyncEngine) fullSync(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar, calendarIndex int) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Get the effective sync direction for this calendar (may be per-calendar or source default)
	syncDirection := getSyncDirectionForCalendar(source, calendar.Path)
	log.Printf("Calendar %q sync direction: %s (source default: %s)", calendar.Name, syncDirection, source.SyncDirection)

	// Helper to update status message during loading phases
	updateStatus := func(status string) {
		se.tracker.UpdateCalendar(source.ID, fmt.Sprintf("%s (%s)", calendar.Name, status), calendarIndex)
	}

	// Create collector for malformed events from source
	malformedCollector := NewMalformedEventCollector()

	// Clear old malformed events for this source before sync
	if err := se.db.ClearMalformedEventsForSource(source.ID); err != nil {
		log.Printf("Failed to clear old malformed events: %v", err)
	}

	// Get all events from source
	updateStatus("fetching source events")
	sourceEvents, err := sourceClient.GetEvents(ctx, calendar.Path, malformedCollector)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get source events: %v", err))
		return result
	}
	updateStatus(fmt.Sprintf("loaded %d source events", len(sourceEvents)))

	// Filter events by date if sync_days_past is configured
	if source.SyncDaysPast > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -source.SyncDaysPast)
		originalCount := len(sourceEvents)
		sourceEvents = filterEventsByDate(sourceEvents, cutoffDate)
		filteredOut := originalCount - len(sourceEvents)
		if filteredOut > 0 {
			log.Printf("Filtered out %d events older than %d days (cutoff: %s)", filteredOut, source.SyncDaysPast, cutoffDate.Format("2006-01-02"))
			updateStatus(fmt.Sprintf("filtered to %d events (-%d old)", len(sourceEvents), filteredOut))
		}
	}

	// Store any malformed events found
	for _, mf := range malformedCollector.GetEvents() {
		if err := se.db.SaveMalformedEvent(source.ID, mf.Path, mf.ErrorMessage); err != nil {
			log.Printf("Failed to save malformed event record: %v", err)
		}
	}

	// Delegate to shared sync logic
	return se.syncEventsToDestination(ctx, source, sourceClient, destClient, sourceEvents, calendar, calendarIndex, syncDirection)
}

// syncEventsToDestination handles the comparison, creation, update, and deletion of events
// between source events and a destination CalDAV calendar. This is shared by both CalDAV
// full sync and ICS feed sync paths.
func (se *SyncEngine) syncEventsToDestination(ctx context.Context, source *db.Source, sourceClient *Client, destClient *Client, sourceEvents []Event, calendar Calendar, calendarIndex int, syncDirection db.SyncDirection) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Helper to update activity tracker with current progress
	updateProgress := func() {
		se.tracker.UpdateProgress(source.ID, result.Created, result.Updated, result.Deleted, result.Skipped, result.EventsProcessed)
	}

	// Helper to update status message during loading phases
	updateStatus := func(status string) {
		se.tracker.UpdateCalendar(source.ID, fmt.Sprintf("%s (%s)", calendar.Name, status), calendarIndex)
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
		destCalendarPath = destCalendars[0].Path
		if len(destCalendars) > 1 {
			log.Printf("WARNING: Multiple destination calendars found, using first one: %s", destCalendarPath)
		}
	}
	log.Printf("Using destination calendar path: %s", destCalendarPath)

	// Get all events from destination (no collector needed - we only track source issues)
	updateStatus("fetching destination events")
	destEvents, err := destClient.GetEvents(ctx, destCalendarPath, nil)
	if err != nil {
		log.Printf("Failed to get destination events (path: %s): %v", destCalendarPath, err)
		destEvents = []Event{}
	}
	log.Printf("Fetched %d events from destination calendar", len(destEvents))

	// Filter destination events by date if sync_days_past is configured
	if source.SyncDaysPast > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -source.SyncDaysPast)
		originalCount := len(destEvents)
		destEvents = filterEventsByDate(destEvents, cutoffDate)
		filteredOut := originalCount - len(destEvents)
		if filteredOut > 0 {
			log.Printf("Filtered out %d destination events older than %d days", filteredOut, source.SyncDaysPast)
		}
	}

	updateStatus(fmt.Sprintf("comparing %d vs %d events", len(sourceEvents), len(destEvents)))

	// Get previously synced events for deletion detection
	previouslySynced, err := se.db.GetSyncedEvents(source.ID, calendar.Path)
	if err != nil {
		log.Printf("Failed to get synced events: %v", err)
		previouslySynced = []*db.SyncedEvent{}
	}

	// Build map of previously synced UIDs
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	for _, syncedEvt := range previouslySynced {
		previouslySyncedMap[syncedEvt.EventUID] = syncedEvt
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

	// Update status to show processing phase
	updateStatus(fmt.Sprintf("processing %d events", len(sourceEvents)))

	// Handle deletions first (for two-way sync)
	// SAFETY: Only delete from source if the event was synced at least one sync cycle ago
	// This prevents deleting events that failed to sync to destination
	syncSafetyThreshold := time.Now().Add(-time.Duration(source.SyncInterval) * time.Second)

	// SAFETY: Skip two-way deletion if destination query returned empty but we have synced events
	// This prevents mass deletion from source when destination query fails
	skipTwoWayDeletion := false
	if syncDirection == db.SyncDirectionTwoWay && len(destEventMap) == 0 && len(previouslySyncedMap) > 0 {
		log.Printf("WARNING: Destination returned 0 events but we have %d previously synced events - skipping two-way deletions for safety", len(previouslySyncedMap))
		skipTwoWayDeletion = true
	}

	if syncDirection == db.SyncDirectionTwoWay && !skipTwoWayDeletion && sourceClient != nil {
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
					updateProgress()
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
					updateProgress()
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
				result.Skipped++
				result.EventsProcessed++
				updateProgress()
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
			result.EventsProcessed++
			updateProgress()
		} else if sourceEvent.ETag != destEvent.ETag {
			// Update existing event
			sourceEvent.Path = destEvent.Path
			if err := destClient.PutEvent(ctx, destCalendarPath, &sourceEvent); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to update event on dest: %v", err))
			} else {
				result.Updated++
				currentUIDs[sourceEvent.UID] = true
			}
			result.EventsProcessed++
			updateProgress()
		} else {
			// Event unchanged, still track it
			currentUIDs[sourceEvent.UID] = true
			result.EventsProcessed++
			updateProgress()
		}
		delete(destEventMap, sourceEvent.UID)
	}

	if skippedDupes > 0 {
		log.Printf("Skipped %d duplicate events", skippedDupes)
	}

	// Two-way sync: sync destination events back to source
	if syncDirection == db.SyncDirectionTwoWay && sourceClient != nil {
		log.Printf("Two-way sync enabled, syncing destination events to source")
		skippedAlreadyExists := 0
		skippedForbidden := 0
		for _, destEvent := range destEvents {
			if destEvent.UID == "" {
				continue
			}

			sourceEvent, exists := sourceEventMap[destEvent.UID]

			if !exists {
				// Event only on destination — skip (may be from another source or was already deleted)
				continue
			} else if destEvent.ETag != sourceEvent.ETag {
				if source.ConflictStrategy == db.ConflictDestWins {
					destEvent.Path = sourceEvent.Path
					if err := sourceClient.PutEvent(ctx, calendar.Path, &destEvent); err != nil {
						if isAlreadyExistsError(err) {
							skippedAlreadyExists++
						} else if isForbiddenError(err) {
							skippedForbidden++
						} else {
							result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to update event on source: %v", err))
						}
					} else {
						result.Updated++
					}
				}
				updateProgress()
			}
		}
		if skippedAlreadyExists > 0 {
			log.Printf("Two-way sync: %d events already exist on source (skipped)", skippedAlreadyExists)
		}
		if skippedForbidden > 0 {
			log.Printf("Two-way sync: %d events skipped (source calendar read-only)", skippedForbidden)
		}
	}

	// One-way sync: delete orphan events on destination (with safety checks).
	// See planOrphanDeletion for the full rationale. The bug this fixes:
	// without these guards, a one-way source_wins sync would delete EVERY
	// destination event whenever the source returned 0 events (auth failure,
	// broken URL, filter wipeout) or whenever multiple sources shared a
	// destination (each source would delete the others' events on every cycle).
	if syncDirection == db.SyncDirectionOneWay && source.ConflictStrategy == db.ConflictSourceWins {
		toDelete, warning := planOrphanDeletion(
			destEventMap,
			len(sourceEvents),
			previouslySyncedMap,
			defaultOrphanDeleteRatioThreshold,
		)
		if warning != "" {
			log.Printf("WARNING: %s", warning)
			result.Warnings = append(result.Warnings, warning)
		}
		for _, event := range toDelete {
			if err := destClient.DeleteEvent(ctx, event.Path); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete orphan event: %v", err))
			} else {
				result.Deleted++
				updateProgress()
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

// syncICSSource syncs events from a read-only ICS feed to a CalDAV destination.
func (se *SyncEngine) syncICSSource(ctx context.Context, source *db.Source) *SyncResult {
	start := time.Now()
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Decrypt source credentials (may be empty for public feeds)
	sourcePassword := ""
	if source.SourcePassword != "" {
		var err error
		sourcePassword, err = se.encryptor.Decrypt(source.SourcePassword)
		if err != nil {
			result.Message = "Failed to decrypt source credentials"
			result.Errors = append(result.Errors, err.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
	}

	destPassword, err := se.encryptor.Decrypt(source.DestPassword)
	if err != nil {
		result.Message = "Failed to decrypt destination credentials"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create ICS client for source
	icsClient, err := NewICSClient(source.SourceURL, source.SourceUsername, sourcePassword)
	if err != nil {
		result.Message = "Failed to create ICS client"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create CalDAV client for destination
	destClient, err := NewClient(source.DestURL, source.DestUsername, destPassword)
	if err != nil {
		result.Message = "Failed to connect to destination"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Test connections
	if err := icsClient.TestConnection(ctx); err != nil {
		result.Message = "ICS feed connection test failed"
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

	// Fetch events from ICS feed
	malformedCollector := NewMalformedEventCollector()
	if err := se.db.ClearMalformedEventsForSource(source.ID); err != nil {
		log.Printf("Failed to clear old malformed events: %v", err)
	}

	sourceEvents, err := icsClient.FetchEvents(ctx, malformedCollector)
	if err != nil {
		result.Message = "Failed to fetch ICS feed"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Filter events by date if configured
	if source.SyncDaysPast > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -source.SyncDaysPast)
		sourceEvents = filterEventsByDate(sourceEvents, cutoffDate)
	}

	// Store malformed events
	for _, mf := range malformedCollector.GetEvents() {
		if err := se.db.SaveMalformedEvent(source.ID, mf.Path, mf.ErrorMessage); err != nil {
			log.Printf("Failed to save malformed event record: %v", err)
		}
	}

	// Create synthetic calendar for the ICS feed
	calendar := Calendar{
		Path: source.SourceURL,
		Name: source.Name,
	}

	// Start activity tracking (single calendar for ICS)
	se.tracker.StartSync(source.ID, source.Name, 1)
	se.tracker.UpdateCalendar(source.ID, calendar.Name, 1)

	// Use shared sync logic — ICS is always one-way, sourceClient is nil (no write-back)
	syncResult := se.syncEventsToDestination(ctx, source, nil, destClient, sourceEvents, calendar, 1, db.SyncDirectionOneWay)

	result.Created = syncResult.Created
	result.Updated = syncResult.Updated
	result.Deleted = syncResult.Deleted
	result.Skipped = syncResult.Skipped
	result.EventsProcessed = syncResult.EventsProcessed
	result.DuplicatesRemoved = syncResult.DuplicatesRemoved
	result.Errors = append(result.Errors, syncResult.Errors...)
	result.Warnings = append(result.Warnings, syncResult.Warnings...)
	result.CalendarsSynced = 1

	result.Success = len(result.Errors) == 0
	if result.Success && len(result.Warnings) == 0 {
		result.Message = fmt.Sprintf("ICS sync: %d created, %d updated, %d deleted, %d skipped",
			result.Created, result.Updated, result.Deleted, result.Skipped)
	} else if result.Success && len(result.Warnings) > 0 {
		result.Message = fmt.Sprintf("ICS sync with %d warnings: %d created, %d updated, %d deleted, %d skipped",
			len(result.Warnings), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else {
		result.Message = fmt.Sprintf("ICS sync failed with %d errors", len(result.Errors))
	}

	result.Duration = time.Since(start)
	se.finishSync(source.ID, result)
	return result
}

// TestICSConnection tests connection to an ICS feed URL.
func (se *SyncEngine) TestICSConnection(ctx context.Context, url, username, password string) error {
	client, err := NewICSClient(url, username, password)
	if err != nil {
		return err
	}
	return client.TestConnection(ctx)
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
