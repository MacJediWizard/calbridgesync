package caldav

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/macjediwizard/calbridge/internal/crypto"
	"github.com/macjediwizard/calbridge/internal/db"
)

// SyncResult represents the result of a sync operation.
type SyncResult struct {
	Success         bool          `json:"success"`
	Message         string        `json:"message"`
	Created         int           `json:"created"`
	Updated         int           `json:"updated"`
	Deleted         int           `json:"deleted"`
	Skipped         int           `json:"skipped"`
	CalendarsSynced int           `json:"calendars_synced"`
	EventsProcessed int           `json:"events_processed"`
	Errors          []string      `json:"errors,omitempty"`
	Duration        time.Duration `json:"duration"`
}

// SyncEngine orchestrates calendar synchronization.
type SyncEngine struct {
	db        *db.DB
	encryptor *crypto.Encryptor
}

// NewSyncEngine creates a new sync engine.
func NewSyncEngine(database *db.DB, encryptor *crypto.Encryptor) *SyncEngine {
	return &SyncEngine{
		db:        database,
		encryptor: encryptor,
	}
}

// SyncSource performs synchronization for a single source.
func (se *SyncEngine) SyncSource(ctx context.Context, source *db.Source) *SyncResult {
	start := time.Now()
	result := &SyncResult{
		Errors: make([]string, 0),
	}

	// Update status to running
	if err := se.db.UpdateSourceSyncStatus(source.ID, db.SyncStatusRunning, "Sync in progress"); err != nil {
		log.Printf("Failed to update sync status: %v", err)
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

	// Sync each calendar
	for _, cal := range sourceCalendars {
		calResult := se.syncCalendar(ctx, source, sourceClient, destClient, cal)
		result.Created += calResult.Created
		result.Updated += calResult.Updated
		result.Deleted += calResult.Deleted
		result.Skipped += calResult.Skipped
		result.EventsProcessed += calResult.EventsProcessed
		result.Errors = append(result.Errors, calResult.Errors...)
	}

	result.CalendarsSynced = len(sourceCalendars)
	result.Success = len(result.Errors) == 0
	if result.Success {
		result.Message = fmt.Sprintf("Synced %d calendar(s): %d created, %d updated, %d deleted, %d skipped",
			len(sourceCalendars), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else {
		result.Message = fmt.Sprintf("Sync completed with %d errors", len(result.Errors))
	}

	result.Duration = time.Since(start)
	se.finishSync(source.ID, result)

	return result
}

func (se *SyncEngine) syncCalendar(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar) *SyncResult {
	result := &SyncResult{
		Errors: make([]string, 0),
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
						result.Errors = append(result.Errors, fmt.Sprintf("Failed to sync event: %v", err))
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
		Errors: make([]string, 0),
	}

	// Get all events from source
	sourceEvents, err := sourceClient.GetEvents(ctx, calendar.Path)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get source events: %v", err))
		return result
	}

	// Get the destination calendar path from the destination client's base URL
	destCalendarPath := destClient.GetCalendarPath()
	log.Printf("Using destination calendar path: %s", destCalendarPath)

	// Get all events from destination
	destEvents, err := destClient.GetEvents(ctx, destCalendarPath)
	if err != nil {
		log.Printf("Failed to get destination events: %v", err)
		destEvents = []Event{}
	}

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
	if source.SyncDirection == db.SyncDirectionTwoWay {
		for uid, syncedEvent := range previouslySyncedMap {
			_, existsOnSource := sourceEventMap[uid]
			destEvent, existsOnDest := destEventMap[uid]

			if !existsOnSource && existsOnDest {
				// Event was deleted from source - delete from destination too
				log.Printf("Event %s deleted from source, deleting from destination", uid)
				if err := destClient.DeleteEvent(ctx, destEvent.Path); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to delete event from dest: %v", err))
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
				// Event was deleted from destination - delete from source too
				log.Printf("Event %s deleted from destination, deleting from source", uid)
				if err := sourceClient.DeleteEvent(ctx, sourceEvent.Path); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to delete event from source: %v", err))
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
				result.Errors = append(result.Errors, fmt.Sprintf("Failed to create event on dest: %v", err))
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
				result.Errors = append(result.Errors, fmt.Sprintf("Failed to update event on dest: %v", err))
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
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to create event on source: %v", err))
				} else {
					result.Created++
					currentUIDs[destEvent.UID] = true
				}
			} else if destEvent.ETag != sourceEvent.ETag {
				if source.ConflictStrategy == db.ConflictDestWins {
					destEvent.Path = sourceEvent.Path
					if err := sourceClient.PutEvent(ctx, calendar.Path, &destEvent); err != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("Failed to update event on source: %v", err))
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
				result.Errors = append(result.Errors, fmt.Sprintf("Failed to delete orphan event: %v", err))
			} else {
				result.Deleted++
			}
		}
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

func (se *SyncEngine) finishSync(sourceID string, result *SyncResult) {
	status := db.SyncStatusSuccess
	if !result.Success {
		status = db.SyncStatusError
	}

	if err := se.db.UpdateSourceSyncStatus(sourceID, status, result.Message); err != nil {
		log.Printf("Failed to update sync status: %v", err)
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
	if len(result.Errors) > 0 {
		syncLog.Details = fmt.Sprintf("Errors: %v", result.Errors)
	}

	if err := se.db.CreateSyncLog(syncLog); err != nil {
		log.Printf("Failed to create sync log: %v", err)
	}
}

// TestConnection tests connection to a CalDAV endpoint.
func (se *SyncEngine) TestConnection(ctx context.Context, url, username, password string) error {
	client, err := NewClient(url, username, password)
	if err != nil {
		return err
	}
	return client.TestConnection(ctx)
}
