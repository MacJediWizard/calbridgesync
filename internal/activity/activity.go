package activity

import (
	"sync"
	"time"
)

// SyncActivity represents the current state of a sync operation.
type SyncActivity struct {
	SourceID        string    `json:"source_id"`
	SourceName      string    `json:"source_name"`
	Status          string    `json:"status"` // "running", "completed", "error"
	CurrentCalendar string    `json:"current_calendar,omitempty"`
	TotalCalendars  int       `json:"total_calendars"`
	Calendarssynced int       `json:"calendars_synced"`
	EventsProcessed int       `json:"events_processed"`
	EventsCreated   int       `json:"events_created"`
	EventsUpdated   int       `json:"events_updated"`
	EventsDeleted   int       `json:"events_deleted"`
	EventsSkipped   int       `json:"events_skipped"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Duration        string    `json:"duration,omitempty"`
	Message         string    `json:"message,omitempty"`
	Errors          []string  `json:"errors,omitempty"`
}

// Tracker tracks sync activity across all sources.
type Tracker struct {
	mu              sync.RWMutex
	active          map[string]*SyncActivity // sourceID -> activity
	recent          []*SyncActivity          // Recently completed syncs
	maxRecentSyncs  int
}

// NewTracker creates a new activity tracker.
func NewTracker() *Tracker {
	return &Tracker{
		active:         make(map[string]*SyncActivity),
		recent:         make([]*SyncActivity, 0),
		maxRecentSyncs: 20, // Keep last 20 completed syncs
	}
}

// StartSync begins tracking a new sync operation.
func (t *Tracker) StartSync(sourceID, sourceName string, totalCalendars int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.active[sourceID] = &SyncActivity{
		SourceID:       sourceID,
		SourceName:     sourceName,
		Status:         "running",
		TotalCalendars: totalCalendars,
		StartedAt:      time.Now(),
	}
}

// UpdateCalendar updates the current calendar being synced.
func (t *Tracker) UpdateCalendar(sourceID, calendarName string, calendarIndex int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if activity, exists := t.active[sourceID]; exists {
		activity.CurrentCalendar = calendarName
		activity.Calendarssynced = calendarIndex
	}
}

// UpdateProgress updates sync progress counters.
func (t *Tracker) UpdateProgress(sourceID string, created, updated, deleted, skipped, processed int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if activity, exists := t.active[sourceID]; exists {
		activity.EventsCreated = created
		activity.EventsUpdated = updated
		activity.EventsDeleted = deleted
		activity.EventsSkipped = skipped
		activity.EventsProcessed = processed
	}
}

// IncrementProgress increments progress counters by the given amounts.
func (t *Tracker) IncrementProgress(sourceID string, created, updated, deleted, skipped, processed int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if activity, exists := t.active[sourceID]; exists {
		activity.EventsCreated += created
		activity.EventsUpdated += updated
		activity.EventsDeleted += deleted
		activity.EventsSkipped += skipped
		activity.EventsProcessed += processed
	}
}

// FinishSync marks a sync as completed and moves it to recent.
func (t *Tracker) FinishSync(sourceID string, success bool, message string, errors []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	activity, exists := t.active[sourceID]
	if !exists {
		return
	}

	now := time.Now()
	activity.CompletedAt = &now
	activity.Duration = now.Sub(activity.StartedAt).Round(time.Millisecond).String()
	activity.Message = message
	activity.Errors = errors
	activity.CurrentCalendar = ""

	if success {
		if len(errors) > 0 {
			activity.Status = "partial"
		} else {
			activity.Status = "completed"
		}
	} else {
		activity.Status = "error"
	}

	// Move to recent list
	t.recent = append([]*SyncActivity{activity}, t.recent...)
	if len(t.recent) > t.maxRecentSyncs {
		t.recent = t.recent[:t.maxRecentSyncs]
	}

	// Remove from active
	delete(t.active, sourceID)
}

// GetActive returns all currently active syncs.
func (t *Tracker) GetActive() []*SyncActivity {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]*SyncActivity, 0, len(t.active))
	for _, activity := range t.active {
		// Create a copy to avoid race conditions
		copy := *activity
		copy.Duration = time.Since(activity.StartedAt).Round(time.Millisecond).String()
		result = append(result, &copy)
	}
	return result
}

// GetRecent returns recently completed syncs.
func (t *Tracker) GetRecent() []*SyncActivity {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]*SyncActivity, len(t.recent))
	for i, activity := range t.recent {
		copy := *activity
		result[i] = &copy
	}
	return result
}

// GetAll returns both active and recent syncs.
func (t *Tracker) GetAll() map[string]interface{} {
	return map[string]interface{}{
		"active": t.GetActive(),
		"recent": t.GetRecent(),
	}
}

// IsSourceSyncing returns true if the given source is currently syncing.
func (t *Tracker) IsSourceSyncing(sourceID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, exists := t.active[sourceID]
	return exists
}
