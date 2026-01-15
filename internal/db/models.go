package db

import (
	"time"
)

// SyncStatus represents the status of a sync operation.
type SyncStatus string

const (
	SyncStatusPending SyncStatus = "pending"
	SyncStatusRunning SyncStatus = "running"
	SyncStatusSuccess SyncStatus = "success"
	SyncStatusPartial SyncStatus = "partial" // Sync completed with some non-critical warnings
	SyncStatusError   SyncStatus = "error"   // Sync failed due to critical error
)

// ConflictStrategy represents how to handle sync conflicts.
type ConflictStrategy string

const (
	ConflictSourceWins ConflictStrategy = "source_wins"
	ConflictDestWins   ConflictStrategy = "dest_wins"
	ConflictLatestWins ConflictStrategy = "latest_wins"
)

// SyncDirection represents the direction of synchronization.
type SyncDirection string

const (
	SyncDirectionOneWay SyncDirection = "one_way" // Source -> Destination only
	SyncDirectionTwoWay SyncDirection = "two_way" // Bidirectional sync
)

// SourceType represents the type of calendar source.
type SourceType string

const (
	SourceTypeICloud    SourceType = "icloud"
	SourceTypeGoogle    SourceType = "google"
	SourceTypeFastmail  SourceType = "fastmail"
	SourceTypeNextcloud SourceType = "nextcloud"
	SourceTypeCustom    SourceType = "custom"
	SourceTypeCalDAV    SourceType = "caldav"
	SourceTypeOutlook   SourceType = "outlook"
)

// ValidSourceTypes contains all valid source type values.
var ValidSourceTypes = map[SourceType]bool{
	SourceTypeICloud:    true,
	SourceTypeGoogle:    true,
	SourceTypeFastmail:  true,
	SourceTypeNextcloud: true,
	SourceTypeCustom:    true,
	SourceTypeCalDAV:    true,
	SourceTypeOutlook:   true,
}

// IsValid returns true if the source type is a known valid value.
func (st SourceType) IsValid() bool {
	return ValidSourceTypes[st]
}

// ValidConflictStrategies contains all valid conflict strategy values.
var ValidConflictStrategies = map[ConflictStrategy]bool{
	ConflictSourceWins: true,
	ConflictDestWins:   true,
	ConflictLatestWins: true,
}

// IsValid returns true if the conflict strategy is a known valid value.
func (cs ConflictStrategy) IsValid() bool {
	return ValidConflictStrategies[cs]
}

// ValidSyncDirections contains all valid sync direction values.
var ValidSyncDirections = map[SyncDirection]bool{
	SyncDirectionOneWay: true,
	SyncDirectionTwoWay: true,
}

// IsValid returns true if the sync direction is a known valid value.
func (sd SyncDirection) IsValid() bool {
	return ValidSyncDirections[sd]
}

// SourcePreset contains preset configuration for known calendar providers.
type SourcePreset struct {
	Name        string
	Type        SourceType
	BaseURL     string
	Description string
}

// SourcePresets maps source types to their preset configurations.
var SourcePresets = map[SourceType]SourcePreset{
	SourceTypeICloud: {
		Name:        "iCloud",
		Type:        SourceTypeICloud,
		BaseURL:     "https://caldav.icloud.com/",
		Description: "Apple iCloud Calendar",
	},
	SourceTypeGoogle: {
		Name:        "Google Calendar",
		Type:        SourceTypeGoogle,
		BaseURL:     "https://apidata.googleusercontent.com/caldav/v2/",
		Description: "Google Calendar (requires OAuth)",
	},
	SourceTypeFastmail: {
		Name:        "Fastmail",
		Type:        SourceTypeFastmail,
		BaseURL:     "https://caldav.fastmail.com/dav/",
		Description: "Fastmail Calendar",
	},
	SourceTypeNextcloud: {
		Name:        "Nextcloud",
		Type:        SourceTypeNextcloud,
		BaseURL:     "",
		Description: "Nextcloud Calendar (self-hosted)",
	},
	SourceTypeCustom: {
		Name:        "Custom CalDAV",
		Type:        SourceTypeCustom,
		BaseURL:     "",
		Description: "Custom CalDAV server",
	},
	SourceTypeCalDAV: {
		Name:        "CalDAV",
		Type:        SourceTypeCalDAV,
		BaseURL:     "",
		Description: "Generic CalDAV server",
	},
	SourceTypeOutlook: {
		Name:        "Outlook",
		Type:        SourceTypeOutlook,
		BaseURL:     "https://outlook.office365.com/caldav/",
		Description: "Microsoft Outlook Calendar",
	},
}

// User represents a user in the system.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserAlertPreferences stores per-user alert notification preferences.
// Nil values mean "use global default" from environment configuration.
type UserAlertPreferences struct {
	ID              string    `json:"id"`
	UserID          string    `json:"user_id"`
	EmailEnabled    *bool     `json:"email_enabled"`    // nil = use global default
	WebhookEnabled  *bool     `json:"webhook_enabled"`  // nil = use global default
	WebhookURL      string    `json:"webhook_url"`      // empty = no personal webhook
	CooldownMinutes *int      `json:"cooldown_minutes"` // nil = use global default
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Source represents a calendar source configuration.
type Source struct {
	ID                string           `json:"id"`
	UserID            string           `json:"user_id"`
	Name              string           `json:"name"`
	SourceType        SourceType       `json:"source_type"`
	SourceURL         string           `json:"source_url"`
	SourceUsername    string           `json:"source_username"`
	SourcePassword    string           `json:"-"` // Never include in JSON
	DestURL           string           `json:"dest_url"`
	DestUsername      string           `json:"dest_username"`
	DestPassword      string           `json:"-"` // Never include in JSON
	SyncInterval      int              `json:"sync_interval"`
	SyncDirection     SyncDirection    `json:"sync_direction"`
	ConflictStrategy  ConflictStrategy `json:"conflict_strategy"`
	SelectedCalendars []string         `json:"selected_calendars"` // Calendar paths to sync (empty = all)
	Enabled           bool             `json:"enabled"`
	LastSyncAt        *time.Time       `json:"last_sync_at"`
	LastSyncStatus    SyncStatus       `json:"last_sync_status"`
	LastSyncMessage   string           `json:"last_sync_message"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// SyncState represents the synchronization state for a calendar.
type SyncState struct {
	ID           string    `json:"id"`
	SourceID     string    `json:"source_id"`
	CalendarHref string    `json:"calendar_href"`
	SyncToken    string    `json:"sync_token"`
	CTag         string    `json:"ctag"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SyncLog represents a log entry for a sync operation.
type SyncLog struct {
	ID              string        `json:"id"`
	SourceID        string        `json:"source_id"`
	Status          SyncStatus    `json:"status"`
	Message         string        `json:"message"`
	Details         string        `json:"details"`
	EventsCreated   int           `json:"events_created"`
	EventsUpdated   int           `json:"events_updated"`
	EventsDeleted   int           `json:"events_deleted"`
	EventsSkipped   int           `json:"events_skipped"`
	CalendarsSynced int           `json:"calendars_synced"`
	EventsProcessed int           `json:"events_processed"`
	Duration        time.Duration `json:"duration"`
	CreatedAt       time.Time     `json:"created_at"`
}

// SyncedEvent tracks known event UIDs for deletion detection in two-way sync.
type SyncedEvent struct {
	ID           string    `json:"id"`
	SourceID     string    `json:"source_id"`
	CalendarHref string    `json:"calendar_href"`
	EventUID     string    `json:"event_uid"`
	SourceETag   string    `json:"source_etag"` // ETag on source calendar
	DestETag     string    `json:"dest_etag"`   // ETag on destination calendar
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// MalformedEvent tracks corrupted calendar events that cannot be synced.
type MalformedEvent struct {
	ID           string    `json:"id"`
	SourceID     string    `json:"source_id"`
	SourceName   string    `json:"source_name"` // Populated via join
	EventPath    string    `json:"event_path"`
	ErrorMessage string    `json:"error_message"`
	DiscoveredAt time.Time `json:"discovered_at"`
}
