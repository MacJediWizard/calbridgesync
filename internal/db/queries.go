package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// GetOrCreateUser returns an existing user by email or creates a new one.
func (db *DB) GetOrCreateUser(email, name string) (*User, error) {
	user, err := db.GetUserByEmail(email)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Create new user
	user = &User{
		ID:        uuid.New().String(),
		Email:     email,
		Name:      name,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	query := `INSERT INTO users (id, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`
	_, err = db.conn.Exec(query, user.ID, user.Email, user.Name, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

// GetUserByEmail returns a user by their email address.
func (db *DB) GetUserByEmail(email string) (*User, error) {
	query := `SELECT id, email, name, created_at, updated_at FROM users WHERE email = ?`
	row := db.conn.QueryRow(query, email)

	user := &User{}
	err := row.Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	return user, nil
}

// GetUserByID returns a user by their ID.
func (db *DB) GetUserByID(id string) (*User, error) {
	query := `SELECT id, email, name, created_at, updated_at FROM users WHERE id = ?`
	row := db.conn.QueryRow(query, id)

	user := &User{}
	err := row.Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by ID: %w", err)
	}

	return user, nil
}

// CreateSource creates a new source.
func (db *DB) CreateSource(source *Source) error {
	if source.ID == "" {
		source.ID = uuid.New().String()
	}
	source.CreatedAt = time.Now().UTC()
	source.UpdatedAt = time.Now().UTC()
	source.LastSyncStatus = SyncStatusPending

	// Default sync direction if not set
	if source.SyncDirection == "" {
		source.SyncDirection = SyncDirectionOneWay
	}

	// Encode selected_calendars as JSON
	var selectedCalendarsJSON *string
	if len(source.SelectedCalendars) > 0 {
		data, err := json.Marshal(source.SelectedCalendars)
		if err != nil {
			return fmt.Errorf("failed to encode selected calendars: %w", err)
		}
		s := string(data)
		selectedCalendarsJSON = &s
	}

	// OAuth refresh token is stored in its own column; callers populate
	// it directly on the Source struct before calling CreateSource
	// (encrypted upstream by the API handler, same as passwords).
	var oauthRefreshToken *string
	if source.OAuthRefreshToken != "" {
		t := source.OAuthRefreshToken
		oauthRefreshToken = &t
	}

	// Per-source Google OAuth credentials (#79). Stored as nullable
	// text columns; non-Google sources leave both empty. ClientID is
	// plain text (a public identifier), ClientSecret is encrypted
	// upstream by the API handler.
	var googleClientID *string
	if source.GoogleClientID != "" {
		t := source.GoogleClientID
		googleClientID = &t
	}
	var googleClientSecret *string
	if source.GoogleClientSecret != "" {
		t := source.GoogleClientSecret
		googleClientSecret = &t
	}

	query := `INSERT INTO sources (
		id, user_id, name, source_type, source_url, source_username, source_password,
		dest_url, dest_username, dest_password, sync_interval, sync_days_past, sync_direction, conflict_strategy,
		selected_calendars, enabled, last_sync_status, oauth_refresh_token,
		google_client_id, google_client_secret, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := db.conn.Exec(query,
		source.ID, source.UserID, source.Name, source.SourceType,
		source.SourceURL, source.SourceUsername, source.SourcePassword,
		source.DestURL, source.DestUsername, source.DestPassword,
		source.SyncInterval, source.SyncDaysPast, source.SyncDirection, source.ConflictStrategy,
		selectedCalendarsJSON, source.Enabled,
		source.LastSyncStatus, oauthRefreshToken,
		googleClientID, googleClientSecret,
		source.CreatedAt, source.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create source: %w", err)
	}

	return nil
}

// sourceSelectColumns is the canonical SELECT column list for sources,
// kept in one place so every query + scan function stays in lockstep.
// (#70) added oauth_refresh_token at the end. (#79) appended
// google_client_id and google_client_secret so per-source Google
// credentials follow the same scan-positional contract.
const sourceSelectColumns = `id, user_id, name, source_type, source_url, source_username, source_password,
	dest_url, dest_username, dest_password, sync_interval, sync_days_past, sync_direction, conflict_strategy,
	selected_calendars, enabled, last_sync_at, last_sync_status, last_sync_message, created_at, updated_at,
	oauth_refresh_token, google_client_id, google_client_secret`

// GetSourceByID returns a source by its ID.
func (db *DB) GetSourceByID(id string) (*Source, error) {
	query := `SELECT ` + sourceSelectColumns + ` FROM sources WHERE id = ?`

	row := db.conn.QueryRow(query, id)
	return scanSource(row)
}

// GetSourceByIDForUser returns a source by its ID only if it belongs to the user.
// This prevents timing attacks by combining auth check with the query.
func (db *DB) GetSourceByIDForUser(id, userID string) (*Source, error) {
	query := `SELECT ` + sourceSelectColumns + ` FROM sources WHERE id = ? AND user_id = ?`

	row := db.conn.QueryRow(query, id, userID)
	return scanSource(row)
}

// GetSourcesByUserID returns all sources for a user.
func (db *DB) GetSourcesByUserID(userID string) ([]*Source, error) {
	query := `SELECT ` + sourceSelectColumns + ` FROM sources WHERE user_id = ? ORDER BY name`

	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query sources: %w", err)
	}
	defer rows.Close()

	var sources []*Source
	for rows.Next() {
		source, err := scanSourceFromRows(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sources: %w", err)
	}

	return sources, nil
}

// GetEnabledSources returns all enabled sources.
func (db *DB) GetEnabledSources() ([]*Source, error) {
	query := `SELECT ` + sourceSelectColumns + ` FROM sources WHERE enabled = 1`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query enabled sources: %w", err)
	}
	defer rows.Close()

	var sources []*Source
	for rows.Next() {
		source, err := scanSourceFromRows(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sources: %w", err)
	}

	return sources, nil
}

// UpdateSource updates an existing source.
func (db *DB) UpdateSource(source *Source) error {
	source.UpdatedAt = time.Now().UTC()

	// Default sync direction if not set
	if source.SyncDirection == "" {
		source.SyncDirection = SyncDirectionOneWay
	}

	// Encode selected_calendars as JSON
	var selectedCalendarsJSON *string
	if len(source.SelectedCalendars) > 0 {
		data, err := json.Marshal(source.SelectedCalendars)
		if err != nil {
			return fmt.Errorf("failed to encode selected calendars: %w", err)
		}
		s := string(data)
		selectedCalendarsJSON = &s
	}

	// Only write oauth_refresh_token if the caller populated it.
	// An empty string on UpdateSource must NOT clobber an existing
	// refresh token — that would silently break a working Google
	// source. Use UpdateSourceOAuthRefreshToken to change it explicitly.
	var oauthRefreshToken *string
	if source.OAuthRefreshToken != "" {
		t := source.OAuthRefreshToken
		oauthRefreshToken = &t
	}

	// Same COALESCE-on-empty rule for the per-source Google OAuth
	// credentials (#79). A normal source-edit form does not re-collect
	// the client secret from the user, so an empty value here means
	// "no change" and must preserve the existing stored value.
	var googleClientID *string
	if source.GoogleClientID != "" {
		t := source.GoogleClientID
		googleClientID = &t
	}
	var googleClientSecret *string
	if source.GoogleClientSecret != "" {
		t := source.GoogleClientSecret
		googleClientSecret = &t
	}

	query := `UPDATE sources SET
		name = ?, source_type = ?, source_url = ?, source_username = ?, source_password = ?,
		dest_url = ?, dest_username = ?, dest_password = ?, sync_interval = ?, sync_days_past = ?,
		sync_direction = ?, conflict_strategy = ?, selected_calendars = ?, enabled = ?,
		oauth_refresh_token = COALESCE(?, oauth_refresh_token),
		google_client_id = COALESCE(?, google_client_id),
		google_client_secret = COALESCE(?, google_client_secret),
		updated_at = ?
		WHERE id = ?`

	result, err := db.conn.Exec(query,
		source.Name, source.SourceType, source.SourceURL, source.SourceUsername, source.SourcePassword,
		source.DestURL, source.DestUsername, source.DestPassword, source.SyncInterval, source.SyncDaysPast,
		source.SyncDirection, source.ConflictStrategy, selectedCalendarsJSON, source.Enabled,
		oauthRefreshToken, googleClientID, googleClientSecret,
		source.UpdatedAt, source.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update source: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	return nil
}

// UpdateSourceSyncStatus updates the sync status of a source.
func (db *DB) UpdateSourceSyncStatus(id string, status SyncStatus, message string) error {
	now := time.Now().UTC()
	query := `UPDATE sources SET last_sync_at = ?, last_sync_status = ?, last_sync_message = ?, updated_at = ? WHERE id = ?`

	result, err := db.conn.Exec(query, now, status, message, now, id)
	if err != nil {
		return fmt.Errorf("failed to update source sync status: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	return nil
}

// ResetRunningSyncStatuses resets any sources with "running" status to "pending".
// This should be called on startup to clean up statuses from interrupted syncs.
func (db *DB) ResetRunningSyncStatuses() (int64, error) {
	now := time.Now().UTC()
	query := `UPDATE sources SET last_sync_status = ?, last_sync_message = ?, updated_at = ? WHERE last_sync_status = ?`

	result, err := db.conn.Exec(query, SyncStatusPending, "Reset on startup", now, SyncStatusRunning)
	if err != nil {
		return 0, fmt.Errorf("failed to reset running sync statuses: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return affected, nil
}

// DeleteSource deletes a source by its ID.
func (db *DB) DeleteSource(id string) error {
	query := `DELETE FROM sources WHERE id = ?`

	result, err := db.conn.Exec(query, id)
	if err != nil {
		return fmt.Errorf("failed to delete source: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	return nil
}

// GetSyncState returns the sync state for a source and calendar.
func (db *DB) GetSyncState(sourceID, calendarHref string) (*SyncState, error) {
	query := `SELECT id, source_id, calendar_href, sync_token, ctag, updated_at
		FROM sync_states WHERE source_id = ? AND calendar_href = ?`

	row := db.conn.QueryRow(query, sourceID, calendarHref)

	state := &SyncState{}
	var syncToken, ctag sql.NullString
	err := row.Scan(&state.ID, &state.SourceID, &state.CalendarHref, &syncToken, &ctag, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get sync state: %w", err)
	}

	state.SyncToken = syncToken.String
	state.CTag = ctag.String

	return state, nil
}

// UpsertSyncState creates or updates a sync state.
func (db *DB) UpsertSyncState(state *SyncState) error {
	now := time.Now().UTC()

	// Try to update first
	query := `UPDATE sync_states SET sync_token = ?, ctag = ?, updated_at = ?
		WHERE source_id = ? AND calendar_href = ?`

	result, err := db.conn.Exec(query, state.SyncToken, state.CTag, now, state.SourceID, state.CalendarHref)
	if err != nil {
		return fmt.Errorf("failed to update sync state: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if affected == 0 {
		// Insert new record
		if state.ID == "" {
			state.ID = uuid.New().String()
		}
		state.UpdatedAt = now

		insertQuery := `INSERT INTO sync_states (id, source_id, calendar_href, sync_token, ctag, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`

		_, err = db.conn.Exec(insertQuery, state.ID, state.SourceID, state.CalendarHref, state.SyncToken, state.CTag, state.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to insert sync state: %w", err)
		}
	}

	return nil
}

// CreateSyncLog creates a new sync log entry.
func (db *DB) CreateSyncLog(log *SyncLog) error {
	if log.ID == "" {
		log.ID = uuid.New().String()
	}
	log.CreatedAt = time.Now().UTC()

	query := `INSERT INTO sync_logs (id, source_id, status, message, details, duration_ms,
		events_created, events_updated, events_deleted, events_skipped, calendars_synced, events_processed, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := db.conn.Exec(query, log.ID, log.SourceID, log.Status, log.Message, log.Details, log.Duration.Milliseconds(),
		log.EventsCreated, log.EventsUpdated, log.EventsDeleted, log.EventsSkipped, log.CalendarsSynced, log.EventsProcessed, log.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to create sync log: %w", err)
	}

	return nil
}

// GetSyncLogs returns sync logs for a source.
func (db *DB) GetSyncLogs(sourceID string, limit int) ([]*SyncLog, error) {
	query := `SELECT id, source_id, status, message, details, duration_ms,
		events_created, events_updated, events_deleted, events_skipped, calendars_synced, events_processed, created_at
		FROM sync_logs WHERE source_id = ? ORDER BY created_at DESC LIMIT ?`

	rows, err := db.conn.Query(query, sourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query sync logs: %w", err)
	}
	defer rows.Close()

	var logs []*SyncLog
	for rows.Next() {
		log := &SyncLog{}
		var durationMs int64
		err := rows.Scan(&log.ID, &log.SourceID, &log.Status, &log.Message, &log.Details, &durationMs,
			&log.EventsCreated, &log.EventsUpdated, &log.EventsDeleted, &log.EventsSkipped, &log.CalendarsSynced, &log.EventsProcessed, &log.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan sync log: %w", err)
		}
		log.Duration = time.Duration(durationMs) * time.Millisecond
		logs = append(logs, log)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sync logs: %w", err)
	}

	return logs, nil
}

// CleanOldSyncLogs deletes sync logs older than the given time.
func (db *DB) CleanOldSyncLogs(olderThan time.Time) (int64, error) {
	query := `DELETE FROM sync_logs WHERE created_at < ?`

	result, err := db.conn.Exec(query, olderThan)
	if err != nil {
		return 0, fmt.Errorf("failed to clean old sync logs: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return affected, nil
}

// GetSourceStats returns per-source statistics including event count,
// malformed count, recent sync history, success rate, and health
// score for the dashboard. (#136)
func (db *DB) GetSourceStats(sourceID string) (*SourceStats, error) {
	stats := &SourceStats{}

	// Synced event count
	row := db.conn.QueryRow(`SELECT COUNT(*) FROM synced_events WHERE source_id = ?`, sourceID)
	if err := row.Scan(&stats.SyncedEventCount); err != nil {
		return nil, fmt.Errorf("failed to count synced events: %w", err)
	}

	// Malformed event count
	row = db.conn.QueryRow(`SELECT COUNT(*) FROM malformed_events WHERE source_id = ?`, sourceID)
	if err := row.Scan(&stats.MalformedCount); err != nil {
		return nil, fmt.Errorf("failed to count malformed events: %w", err)
	}

	// Last 20 sync logs for sparkline + success rate
	rows, err := db.conn.Query(
		`SELECT status, duration_ms, created_at FROM sync_logs WHERE source_id = ? ORDER BY created_at DESC LIMIT 20`,
		sourceID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent sync logs: %w", err)
	}
	defer rows.Close()

	var successCount int
	for rows.Next() {
		var mini MiniSyncLog
		var durationMs int
		if err := rows.Scan(&mini.Status, &durationMs, &mini.CreatedAt); err != nil {
			continue
		}
		mini.DurationMs = durationMs
		stats.RecentSyncs = append(stats.RecentSyncs, mini)
		if mini.Status == SyncStatusSuccess {
			successCount++
		}
	}

	total := len(stats.RecentSyncs)
	if total > 0 {
		stats.SuccessRate = float64(successCount) / float64(total) * 100
	}

	// Health score: weighted
	// Success rate (0.5) + malformed (0.2) + warning frequency (0.3)
	successScore := stats.SuccessRate / 100.0
	malformedScore := 1.0
	if stats.MalformedCount > 5 {
		malformedScore = 0.3
	} else if stats.MalformedCount > 0 {
		malformedScore = 0.7
	}
	// Warning frequency: count partial/error in recent syncs
	warningCount := 0
	for _, s := range stats.RecentSyncs {
		if s.Status == SyncStatusPartial || s.Status == SyncStatusError {
			warningCount++
		}
	}
	warningScore := 1.0
	if total > 0 {
		warningScore = 1.0 - float64(warningCount)/float64(total)
	}

	stats.HealthScore = successScore*0.5 + malformedScore*0.2 + warningScore*0.3
	if stats.HealthScore >= 0.8 {
		stats.HealthLabel = "healthy"
	} else if stats.HealthScore >= 0.5 {
		stats.HealthLabel = "degraded"
	} else {
		stats.HealthLabel = "unhealthy"
	}

	return stats, nil
}

// UpdateSourceAdaptiveState updates the ICS content hash and adaptive
// interval for a source. Used by the scheduler after each ICS fetch
// to track whether the feed content changed. (#146)
func (db *DB) UpdateSourceAdaptiveState(sourceID, contentHash string, adaptiveInterval int) error {
	query := `UPDATE sources SET last_content_hash = ?, adaptive_interval = ?, updated_at = ? WHERE id = ?`
	_, err := db.conn.Exec(query, contentHash, adaptiveInterval, time.Now().UTC(), sourceID)
	if err != nil {
		return fmt.Errorf("failed to update source adaptive state: %w", err)
	}
	return nil
}

// GetSyncLogStats returns aggregate stats about the sync_logs table:
// total count and oldest log timestamp. Used by the Settings page to
// show log retention status. (#136)
func (db *DB) GetSyncLogStats() (count int64, oldest time.Time, err error) {
	row := db.conn.QueryRow(`SELECT COUNT(*), COALESCE(MIN(created_at), CURRENT_TIMESTAMP) FROM sync_logs`)
	err = row.Scan(&count, &oldest)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to get sync log stats: %w", err)
	}
	return count, oldest, nil
}

// parseSelectedCalendars parses selected_calendars JSON with backward compatibility.
// Old format: ["path1", "path2"] (array of strings)
// New format: [{"path": "path1", "sync_direction": "one_way"}] (array of CalendarConfig)
func parseSelectedCalendars(jsonStr string) []CalendarConfig {
	if jsonStr == "" {
		return nil
	}

	// First try parsing as new format (array of CalendarConfig)
	var configs []CalendarConfig
	if err := json.Unmarshal([]byte(jsonStr), &configs); err == nil {
		// Check if it's actually the new format (has Path field)
		if len(configs) > 0 && configs[0].Path != "" {
			return configs
		}
	}

	// Try parsing as old format (array of strings)
	var paths []string
	if err := json.Unmarshal([]byte(jsonStr), &paths); err == nil {
		// Convert to new format with empty sync_direction (use source default)
		configs = make([]CalendarConfig, len(paths))
		for i, path := range paths {
			configs[i] = CalendarConfig{Path: path}
		}
		return configs
	}

	// If both fail, return nil
	return nil
}

// scanSource scans a single row into a Source struct.
func scanSource(row *sql.Row) (*Source, error) {
	source := &Source{}
	var lastSyncAt sql.NullTime
	var lastSyncMessage sql.NullString
	var syncDirection sql.NullString
	var selectedCalendarsJSON sql.NullString
	var oauthRefreshToken sql.NullString
	var googleClientID sql.NullString
	var googleClientSecret sql.NullString

	err := row.Scan(
		&source.ID, &source.UserID, &source.Name, &source.SourceType,
		&source.SourceURL, &source.SourceUsername, &source.SourcePassword,
		&source.DestURL, &source.DestUsername, &source.DestPassword,
		&source.SyncInterval, &source.SyncDaysPast, &syncDirection, &source.ConflictStrategy,
		&selectedCalendarsJSON, &source.Enabled,
		&lastSyncAt, &source.LastSyncStatus, &lastSyncMessage,
		&source.CreatedAt, &source.UpdatedAt,
		&oauthRefreshToken, &googleClientID, &googleClientSecret,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan source: %w", err)
	}

	if lastSyncAt.Valid {
		source.LastSyncAt = &lastSyncAt.Time
	}
	source.LastSyncMessage = lastSyncMessage.String
	source.SyncDirection = SyncDirection(syncDirection.String)
	if source.SyncDirection == "" {
		source.SyncDirection = SyncDirectionOneWay
	}
	if oauthRefreshToken.Valid {
		source.OAuthRefreshToken = oauthRefreshToken.String
	}
	if googleClientID.Valid {
		source.GoogleClientID = googleClientID.String
	}
	if googleClientSecret.Valid {
		source.GoogleClientSecret = googleClientSecret.String
	}

	// Decode selected_calendars from JSON (backward compatible)
	if selectedCalendarsJSON.Valid {
		source.SelectedCalendars = parseSelectedCalendars(selectedCalendarsJSON.String)
	}

	return source, nil
}

// scanSourceFromRows scans a row from sql.Rows into a Source struct.
func scanSourceFromRows(rows *sql.Rows) (*Source, error) {
	source := &Source{}
	var lastSyncAt sql.NullTime
	var lastSyncMessage sql.NullString
	var syncDirection sql.NullString
	var selectedCalendarsJSON sql.NullString
	var oauthRefreshToken sql.NullString
	var googleClientID sql.NullString
	var googleClientSecret sql.NullString

	err := rows.Scan(
		&source.ID, &source.UserID, &source.Name, &source.SourceType,
		&source.SourceURL, &source.SourceUsername, &source.SourcePassword,
		&source.DestURL, &source.DestUsername, &source.DestPassword,
		&source.SyncInterval, &source.SyncDaysPast, &syncDirection, &source.ConflictStrategy,
		&selectedCalendarsJSON, &source.Enabled,
		&lastSyncAt, &source.LastSyncStatus, &lastSyncMessage,
		&source.CreatedAt, &source.UpdatedAt,
		&oauthRefreshToken, &googleClientID, &googleClientSecret,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan source: %w", err)
	}

	if lastSyncAt.Valid {
		source.LastSyncAt = &lastSyncAt.Time
	}
	source.LastSyncMessage = lastSyncMessage.String
	source.SyncDirection = SyncDirection(syncDirection.String)
	if source.SyncDirection == "" {
		source.SyncDirection = SyncDirectionOneWay
	}
	if oauthRefreshToken.Valid {
		source.OAuthRefreshToken = oauthRefreshToken.String
	}
	if googleClientID.Valid {
		source.GoogleClientID = googleClientID.String
	}
	if googleClientSecret.Valid {
		source.GoogleClientSecret = googleClientSecret.String
	}

	// Decode selected_calendars from JSON (backward compatible)
	if selectedCalendarsJSON.Valid {
		source.SelectedCalendars = parseSelectedCalendars(selectedCalendarsJSON.String)
	}

	return source, nil
}

// GetSyncedEvents returns all synced event UIDs for a source and calendar.
func (db *DB) GetSyncedEvents(sourceID, calendarHref string) ([]*SyncedEvent, error) {
	query := `SELECT id, source_id, calendar_href, event_uid, source_etag, dest_etag, created_at, updated_at
		FROM synced_events WHERE source_id = ? AND calendar_href = ?`

	rows, err := db.conn.Query(query, sourceID, calendarHref)
	if err != nil {
		return nil, fmt.Errorf("failed to query synced events: %w", err)
	}
	defer rows.Close()

	var events []*SyncedEvent
	for rows.Next() {
		event := &SyncedEvent{}
		var sourceETag, destETag sql.NullString
		err := rows.Scan(&event.ID, &event.SourceID, &event.CalendarHref, &event.EventUID,
			&sourceETag, &destETag, &event.CreatedAt, &event.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan synced event: %w", err)
		}
		event.SourceETag = sourceETag.String
		event.DestETag = destETag.String
		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating synced events: %w", err)
	}

	return events, nil
}

// UpsertSyncedEvent creates or updates a synced event record.
func (db *DB) UpsertSyncedEvent(event *SyncedEvent) error {
	now := time.Now().UTC()

	// Try to update first
	query := `UPDATE synced_events SET source_etag = ?, dest_etag = ?, updated_at = ?
		WHERE source_id = ? AND calendar_href = ? AND event_uid = ?`

	result, err := db.conn.Exec(query, event.SourceETag, event.DestETag, now,
		event.SourceID, event.CalendarHref, event.EventUID)
	if err != nil {
		return fmt.Errorf("failed to update synced event: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if affected == 0 {
		// Insert new record
		if event.ID == "" {
			event.ID = uuid.New().String()
		}
		event.CreatedAt = now
		event.UpdatedAt = now

		insertQuery := `INSERT INTO synced_events (id, source_id, calendar_href, event_uid, source_etag, dest_etag, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

		_, err = db.conn.Exec(insertQuery, event.ID, event.SourceID, event.CalendarHref,
			event.EventUID, event.SourceETag, event.DestETag, event.CreatedAt, event.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to insert synced event: %w", err)
		}
	}

	return nil
}

// DeleteSyncedEvent removes a synced event record.
func (db *DB) DeleteSyncedEvent(sourceID, calendarHref, eventUID string) error {
	query := `DELETE FROM synced_events WHERE source_id = ? AND calendar_href = ? AND event_uid = ?`

	_, err := db.conn.Exec(query, sourceID, calendarHref, eventUID)
	if err != nil {
		return fmt.Errorf("failed to delete synced event: %w", err)
	}

	return nil
}

// DeleteSyncedEventsForCalendar removes all synced event records for a calendar.
func (db *DB) DeleteSyncedEventsForCalendar(sourceID, calendarHref string) error {
	query := `DELETE FROM synced_events WHERE source_id = ? AND calendar_href = ?`

	_, err := db.conn.Exec(query, sourceID, calendarHref)
	if err != nil {
		return fmt.Errorf("failed to delete synced events for calendar: %w", err)
	}

	return nil
}

// SaveMalformedEvent saves or updates a malformed event record.
func (db *DB) SaveMalformedEvent(sourceID, eventPath, errorMessage string) error {
	// Use INSERT OR REPLACE to handle the unique constraint
	query := `INSERT OR REPLACE INTO malformed_events (id, source_id, event_path, error_message, discovered_at)
		VALUES (?, ?, ?, ?, ?)`

	id := uuid.New().String()
	now := time.Now().UTC()

	_, err := db.conn.Exec(query, id, sourceID, eventPath, errorMessage, now)
	if err != nil {
		return fmt.Errorf("failed to save malformed event: %w", err)
	}

	return nil
}

// GetMalformedEvents returns all malformed events for a user (via their sources).
func (db *DB) GetMalformedEvents(userID string) ([]*MalformedEvent, error) {
	query := `SELECT m.id, m.source_id, s.name, m.event_path, m.error_message, m.discovered_at
		FROM malformed_events m
		JOIN sources s ON m.source_id = s.id
		WHERE s.user_id = ?
		ORDER BY m.discovered_at DESC`

	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query malformed events: %w", err)
	}
	defer rows.Close()

	var events []*MalformedEvent
	for rows.Next() {
		event := &MalformedEvent{}
		err := rows.Scan(&event.ID, &event.SourceID, &event.SourceName,
			&event.EventPath, &event.ErrorMessage, &event.DiscoveredAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan malformed event: %w", err)
		}
		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating malformed events: %w", err)
	}

	return events, nil
}

// GetMalformedEventByID returns a single malformed event by ID.
func (db *DB) GetMalformedEventByID(id string) (*MalformedEvent, error) {
	query := `SELECT m.id, m.source_id, s.name, m.event_path, m.error_message, m.discovered_at
		FROM malformed_events m
		JOIN sources s ON m.source_id = s.id
		WHERE m.id = ?`

	event := &MalformedEvent{}
	err := db.conn.QueryRow(query, id).Scan(&event.ID, &event.SourceID, &event.SourceName,
		&event.EventPath, &event.ErrorMessage, &event.DiscoveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get malformed event: %w", err)
	}

	return event, nil
}

// GetMalformedEventByIDForUser returns a malformed event by ID only if it belongs to the user.
// This prevents timing attacks by combining auth check with the query.
func (db *DB) GetMalformedEventByIDForUser(id, userID string) (*MalformedEvent, error) {
	query := `SELECT m.id, m.source_id, s.name, m.event_path, m.error_message, m.discovered_at
		FROM malformed_events m
		JOIN sources s ON m.source_id = s.id
		WHERE m.id = ? AND s.user_id = ?`

	event := &MalformedEvent{}
	err := db.conn.QueryRow(query, id, userID).Scan(&event.ID, &event.SourceID, &event.SourceName,
		&event.EventPath, &event.ErrorMessage, &event.DiscoveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get malformed event: %w", err)
	}

	return event, nil
}

// DeleteMalformedEvent removes a malformed event record.
func (db *DB) DeleteMalformedEvent(id string) error {
	query := `DELETE FROM malformed_events WHERE id = ?`

	result, err := db.conn.Exec(query, id)
	if err != nil {
		return fmt.Errorf("failed to delete malformed event: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if affected == 0 {
		return ErrNotFound
	}

	return nil
}

// ClearMalformedEventsForSource removes all malformed events for a source.
func (db *DB) ClearMalformedEventsForSource(sourceID string) error {
	query := `DELETE FROM malformed_events WHERE source_id = ?`

	_, err := db.conn.Exec(query, sourceID)
	if err != nil {
		return fmt.Errorf("failed to clear malformed events: %w", err)
	}

	return nil
}

// DeleteAllMalformedEventsForUser removes all malformed events for a user's sources.
// Returns the number of events deleted.
func (db *DB) DeleteAllMalformedEventsForUser(userID string) (int64, error) {
	query := `DELETE FROM malformed_events
		WHERE source_id IN (SELECT id FROM sources WHERE user_id = ?)`

	result, err := db.conn.Exec(query, userID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete all malformed events: %w", err)
	}

	// Previously this call discarded the RowsAffected error with
	// `deleted, _ :=`. Every other RowsAffected call in this file
	// checks the error; this one did not, which meant a driver-side
	// RowsAffected failure would silently return deleted=0 and make
	// the UI show "0 events deleted" even if the DELETE itself
	// succeeded. Matches the pattern used at line ~254/275/297/314/
	// 361/446/619/768/889. (#91)
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected for delete all malformed events: %w", err)
	}
	return deleted, nil
}

// GetUserAlertPreferences returns alert preferences for a user.
// Returns nil (not ErrNotFound) if preferences haven't been set yet.
func (db *DB) GetUserAlertPreferences(userID string) (*UserAlertPreferences, error) {
	query := `SELECT id, user_id, email_enabled, webhook_enabled, webhook_url, cooldown_minutes, created_at, updated_at
		FROM user_alert_preferences WHERE user_id = ?`

	row := db.conn.QueryRow(query, userID)

	prefs := &UserAlertPreferences{}
	var emailEnabled, webhookEnabled, cooldownMinutes sql.NullInt64
	var webhookURL sql.NullString

	err := row.Scan(&prefs.ID, &prefs.UserID, &emailEnabled, &webhookEnabled, &webhookURL, &cooldownMinutes, &prefs.CreatedAt, &prefs.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // Return nil, nil to indicate no preferences set (use defaults)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user alert preferences: %w", err)
	}

	// Convert nullable fields to pointers
	if emailEnabled.Valid {
		val := emailEnabled.Int64 != 0
		prefs.EmailEnabled = &val
	}
	if webhookEnabled.Valid {
		val := webhookEnabled.Int64 != 0
		prefs.WebhookEnabled = &val
	}
	if webhookURL.Valid {
		prefs.WebhookURL = webhookURL.String
	}
	if cooldownMinutes.Valid {
		val := int(cooldownMinutes.Int64)
		prefs.CooldownMinutes = &val
	}

	return prefs, nil
}

// UpsertUserAlertPreferences creates or updates alert preferences for a user.
func (db *DB) UpsertUserAlertPreferences(prefs *UserAlertPreferences) error {
	now := time.Now().UTC()

	// Convert pointer bools to nullable integers for SQLite
	var emailEnabled, webhookEnabled, cooldownMinutes sql.NullInt64
	if prefs.EmailEnabled != nil {
		emailEnabled.Valid = true
		if *prefs.EmailEnabled {
			emailEnabled.Int64 = 1
		} else {
			emailEnabled.Int64 = 0
		}
	}
	if prefs.WebhookEnabled != nil {
		webhookEnabled.Valid = true
		if *prefs.WebhookEnabled {
			webhookEnabled.Int64 = 1
		} else {
			webhookEnabled.Int64 = 0
		}
	}
	if prefs.CooldownMinutes != nil {
		cooldownMinutes.Valid = true
		cooldownMinutes.Int64 = int64(*prefs.CooldownMinutes)
	}

	var webhookURL sql.NullString
	if prefs.WebhookURL != "" {
		webhookURL.Valid = true
		webhookURL.String = prefs.WebhookURL
	}

	// Try to update first
	query := `UPDATE user_alert_preferences SET email_enabled = ?, webhook_enabled = ?, webhook_url = ?, cooldown_minutes = ?, updated_at = ?
		WHERE user_id = ?`

	result, err := db.conn.Exec(query, emailEnabled, webhookEnabled, webhookURL, cooldownMinutes, now, prefs.UserID)
	if err != nil {
		return fmt.Errorf("failed to update user alert preferences: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if affected == 0 {
		// Insert new record
		if prefs.ID == "" {
			prefs.ID = uuid.New().String()
		}
		prefs.CreatedAt = now
		prefs.UpdatedAt = now

		insertQuery := `INSERT INTO user_alert_preferences (id, user_id, email_enabled, webhook_enabled, webhook_url, cooldown_minutes, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

		_, err = db.conn.Exec(insertQuery, prefs.ID, prefs.UserID, emailEnabled, webhookEnabled, webhookURL, cooldownMinutes, prefs.CreatedAt, prefs.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to insert user alert preferences: %w", err)
		}
	}

	return nil
}
