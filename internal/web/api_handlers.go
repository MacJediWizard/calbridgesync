package web

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

// APISource represents a source in JSON format for the API.
type APISource struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SourceType       string `json:"source_type"`
	SourceURL        string `json:"source_url"`
	SourceUsername   string `json:"source_username"`
	DestURL          string `json:"dest_url"`
	DestUsername     string `json:"dest_username"`
	SyncInterval     int    `json:"sync_interval"`
	SyncDirection    string `json:"sync_direction"`
	ConflictStrategy string `json:"conflict_strategy"`
	Enabled          bool   `json:"enabled"`
	SyncStatus       string `json:"sync_status"`
	LastSyncAt       *string `json:"last_sync_at"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// APISyncLog represents a sync log in JSON format for the API.
type APISyncLog struct {
	ID              string   `json:"id"`
	SourceID        string   `json:"source_id"`
	Status          string   `json:"status"`
	Message         string   `json:"message"`
	Details         *string  `json:"details"`
	EventsCreated   int      `json:"events_created"`
	EventsUpdated   int      `json:"events_updated"`
	EventsDeleted   int      `json:"events_deleted"`
	EventsSkipped   int      `json:"events_skipped"`
	CalendarsSynced int      `json:"calendars_synced"`
	EventsProcessed int      `json:"events_processed"`
	Duration        *float64 `json:"duration"`
	CreatedAt       string   `json:"created_at"`
}

// APIDashboardStats represents dashboard statistics.
type APIDashboardStats struct {
	TotalSources  int `json:"total_sources"`
	ActiveSources int `json:"active_sources"`
	SyncsToday    int `json:"syncs_today"`
	FailedSyncs   int `json:"failed_syncs"`
}

// APISyncHistoryPoint represents a single data point in sync history.
type APISyncHistoryPoint struct {
	Date          string `json:"date"`
	Success       int    `json:"success"`
	Partial       int    `json:"partial"`
	Error         int    `json:"error"`
	EventsCreated int    `json:"events_created"`
	EventsUpdated int    `json:"events_updated"`
	EventsDeleted int    `json:"events_deleted"`
}

// APISyncHistory represents sync history data for charts.
type APISyncHistory struct {
	History []APISyncHistoryPoint `json:"history"`
	Summary APISyncSummary        `json:"summary"`
}

// APISyncSummary represents aggregate sync statistics.
type APISyncSummary struct {
	TotalSyncs      int     `json:"total_syncs"`
	SuccessRate     float64 `json:"success_rate"`
	TotalCreated    int     `json:"total_created"`
	TotalUpdated    int     `json:"total_updated"`
	TotalDeleted    int     `json:"total_deleted"`
	AvgDurationSecs float64 `json:"avg_duration_secs"`
}

// APIAuthStatus represents auth status response.
type APIAuthStatus struct {
	Authenticated bool    `json:"authenticated"`
	User          *APIUser `json:"user,omitempty"`
}

// APIUser represents a user in JSON format.
type APIUser struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"`
}

// sourceToAPI converts a db.Source to APISource.
func sourceToAPI(s *db.Source) *APISource {
	api := &APISource{
		ID:               s.ID,
		Name:             s.Name,
		SourceType:       string(s.SourceType),
		SourceURL:        s.SourceURL,
		SourceUsername:   s.SourceUsername,
		DestURL:          s.DestURL,
		DestUsername:     s.DestUsername,
		SyncInterval:     s.SyncInterval,
		SyncDirection:    string(s.SyncDirection),
		ConflictStrategy: string(s.ConflictStrategy),
		Enabled:          s.Enabled,
		SyncStatus:       string(s.LastSyncStatus),
		CreatedAt:        s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        s.UpdatedAt.Format(time.RFC3339),
	}
	if s.LastSyncAt != nil {
		ts := s.LastSyncAt.Format(time.RFC3339)
		api.LastSyncAt = &ts
	}
	return api
}

// syncLogToAPI converts a db.SyncLog to APISyncLog.
func syncLogToAPI(l *db.SyncLog) *APISyncLog {
	api := &APISyncLog{
		ID:              l.ID,
		SourceID:        l.SourceID,
		Status:          string(l.Status),
		Message:         l.Message,
		EventsCreated:   l.EventsCreated,
		EventsUpdated:   l.EventsUpdated,
		EventsDeleted:   l.EventsDeleted,
		EventsSkipped:   l.EventsSkipped,
		CalendarsSynced: l.CalendarsSynced,
		EventsProcessed: l.EventsProcessed,
		CreatedAt:       l.CreatedAt.Format(time.RFC3339),
	}
	if l.Details != "" {
		api.Details = &l.Details
	}
	if l.Duration > 0 {
		dur := l.Duration.Seconds()
		api.Duration = &dur
	}
	return api
}

// APIAuthStatus returns the authentication status.
func (h *Handlers) APIAuthStatus(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusOK, APIAuthStatus{Authenticated: false})
		return
	}

	c.JSON(http.StatusOK, APIAuthStatus{
		Authenticated: true,
		User: &APIUser{
			ID:     session.UserID,
			Email:  session.Email,
			Name:   session.Name,
			Avatar: session.Picture,
		},
	})
}

// APILogout logs out the user.
func (h *Handlers) APILogout(c *gin.Context) {
	if err := h.session.Clear(c.Writer, c.Request); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to logout"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Logged out"})
}

// APIDashboardStats returns dashboard statistics.
func (h *Handlers) APIDashboardStats(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sources, err := h.db.GetSourcesByUserID(session.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load sources"})
		return
	}

	stats := APIDashboardStats{
		TotalSources: len(sources),
	}

	for _, s := range sources {
		if s.Enabled {
			stats.ActiveSources++
		}
		if s.LastSyncStatus == db.SyncStatusError {
			stats.FailedSyncs++
		}
	}

	// Count syncs today
	today := time.Now().Truncate(24 * time.Hour)
	for _, s := range sources {
		logs, _ := h.db.GetSyncLogs(s.ID, 100)
		for _, l := range logs {
			if l.CreatedAt.After(today) {
				stats.SyncsToday++
			}
		}
	}

	c.JSON(http.StatusOK, stats)
}

// APISyncHistory returns sync history for charts.
func (h *Handlers) APISyncHistory(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	// Get number of days from query param (default 7)
	days := 7
	if d := c.Query("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 30 {
			days = parsed
		}
	}

	sources, err := h.db.GetSourcesByUserID(session.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load sources"})
		return
	}

	// Collect all logs for all sources
	var allLogs []*db.SyncLog
	for _, s := range sources {
		logs, _ := h.db.GetSyncLogs(s.ID, 500)
		allLogs = append(allLogs, logs...)
	}

	// Build daily aggregates for the past N days
	now := time.Now()
	startDate := now.AddDate(0, 0, -days+1).Truncate(24 * time.Hour)

	// Initialize history points
	historyMap := make(map[string]*APISyncHistoryPoint)
	for i := 0; i < days; i++ {
		date := startDate.AddDate(0, 0, i)
		dateStr := date.Format("Jan 02")
		historyMap[dateStr] = &APISyncHistoryPoint{Date: dateStr}
	}

	// Aggregate stats
	var totalSyncs, successCount int
	var totalDuration time.Duration
	summary := APISyncSummary{}

	for _, log := range allLogs {
		logDate := log.CreatedAt.Truncate(24 * time.Hour)
		if logDate.Before(startDate) {
			continue
		}

		dateStr := log.CreatedAt.Format("Jan 02")
		point, ok := historyMap[dateStr]
		if !ok {
			continue
		}

		totalSyncs++
		totalDuration += log.Duration
		summary.TotalCreated += log.EventsCreated
		summary.TotalUpdated += log.EventsUpdated
		summary.TotalDeleted += log.EventsDeleted
		point.EventsCreated += log.EventsCreated
		point.EventsUpdated += log.EventsUpdated
		point.EventsDeleted += log.EventsDeleted

		switch log.Status {
		case db.SyncStatusSuccess:
			point.Success++
			successCount++
		case db.SyncStatusPartial:
			point.Partial++
			successCount++ // Partial counts as success for rate calculation
		case db.SyncStatusError:
			point.Error++
		}
	}

	// Build ordered history array
	history := make([]APISyncHistoryPoint, 0, days)
	for i := 0; i < days; i++ {
		date := startDate.AddDate(0, 0, i)
		dateStr := date.Format("Jan 02")
		if point, ok := historyMap[dateStr]; ok {
			history = append(history, *point)
		}
	}

	summary.TotalSyncs = totalSyncs
	if totalSyncs > 0 {
		summary.SuccessRate = float64(successCount) / float64(totalSyncs) * 100
		summary.AvgDurationSecs = totalDuration.Seconds() / float64(totalSyncs)
	}

	c.JSON(http.StatusOK, APISyncHistory{
		History: history,
		Summary: summary,
	})
}

// APIListSources returns all sources for the user.
func (h *Handlers) APIListSources(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sources, err := h.db.GetSourcesByUserID(session.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load sources"})
		return
	}

	apiSources := make([]*APISource, len(sources))
	for i, s := range sources {
		apiSources[i] = sourceToAPI(s)
	}

	c.JSON(http.StatusOK, apiSources)
}

// APIGetSource returns a single source.
func (h *Handlers) APIGetSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sourceID := c.Param("id")
	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	log.Printf("APIGetSource: returning sync_direction='%s' for source %s", source.SyncDirection, sourceID)
	c.JSON(http.StatusOK, sourceToAPI(source))
}

// APICreateSourceRequest represents the request body for creating a source.
type APICreateSourceRequest struct {
	Name             string `json:"name"`
	SourceType       string `json:"source_type"`
	SourceURL        string `json:"source_url"`
	SourceUsername   string `json:"source_username"`
	SourcePassword   string `json:"source_password"`
	DestURL          string `json:"dest_url"`
	DestUsername     string `json:"dest_username"`
	DestPassword     string `json:"dest_password"`
	SyncInterval     int    `json:"sync_interval"`
	ConflictStrategy string `json:"conflict_strategy"`
}

// APICreateSource creates a new source.
func (h *Handlers) APICreateSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req APICreateSourceRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Name == "" || req.SourceURL == "" || req.SourceUsername == "" || req.SourcePassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required fields"})
		return
	}

	// Test source connection
	ctx := c.Request.Context()
	if err := h.syncEngine.TestConnection(ctx, req.SourceURL, req.SourceUsername, req.SourcePassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to connect to source: " + err.Error()})
		return
	}

	// Test destination if provided
	if req.DestURL != "" && req.DestUsername != "" && req.DestPassword != "" {
		if err := h.syncEngine.TestConnection(ctx, req.DestURL, req.DestUsername, req.DestPassword); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to connect to destination: " + err.Error()})
			return
		}
	}

	// Encrypt passwords
	encSourcePwd, err := h.encryptor.Encrypt(req.SourcePassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt credentials"})
		return
	}

	encDestPwd, err := h.encryptor.Encrypt(req.DestPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt credentials"})
		return
	}

	syncInterval := req.SyncInterval
	if syncInterval < h.cfg.Sync.MinInterval || syncInterval > h.cfg.Sync.MaxInterval {
		syncInterval = 300
	}

	source := &db.Source{
		UserID:           session.UserID,
		Name:             req.Name,
		SourceType:       db.SourceType(req.SourceType),
		SourceURL:        req.SourceURL,
		SourceUsername:   req.SourceUsername,
		SourcePassword:   encSourcePwd,
		DestURL:          req.DestURL,
		DestUsername:     req.DestUsername,
		DestPassword:     encDestPwd,
		SyncInterval:     syncInterval,
		ConflictStrategy: db.ConflictStrategy(req.ConflictStrategy),
		Enabled:          true,
	}

	if err := h.db.CreateSource(source); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create source"})
		return
	}

	h.scheduler.AddJob(source.ID, time.Duration(source.SyncInterval)*time.Second)

	c.JSON(http.StatusCreated, sourceToAPI(source))
}

// APIUpdateSourceRequest represents the request body for updating a source.
type APIUpdateSourceRequest struct {
	Name             string `json:"name"`
	SourceType       string `json:"source_type"`
	SourceURL        string `json:"source_url"`
	SourceUsername   string `json:"source_username"`
	SourcePassword   string `json:"source_password,omitempty"`
	DestURL          string `json:"dest_url"`
	DestUsername     string `json:"dest_username"`
	DestPassword     string `json:"dest_password,omitempty"`
	SyncInterval     int    `json:"sync_interval"`
	SyncDirection    string `json:"sync_direction"`
	ConflictStrategy string `json:"conflict_strategy"`
}

// APIUpdateSource updates an existing source.
func (h *Handlers) APIUpdateSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sourceID := c.Param("id")
	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	var req APIUpdateSourceRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	log.Printf("APIUpdateSource: received sync_direction='%s' for source %s", req.SyncDirection, sourceID)

	// Update fields
	source.Name = req.Name
	source.SourceType = db.SourceType(req.SourceType)
	source.SourceURL = req.SourceURL
	source.SourceUsername = req.SourceUsername
	source.DestURL = req.DestURL
	source.DestUsername = req.DestUsername
	source.SyncDirection = db.SyncDirection(req.SyncDirection)
	source.ConflictStrategy = db.ConflictStrategy(req.ConflictStrategy)
	if req.SyncInterval > 0 {
		source.SyncInterval = req.SyncInterval
	}

	// Update passwords if provided
	if req.SourcePassword != "" {
		encPassword, err := h.encryptor.Encrypt(req.SourcePassword)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt credentials"})
			return
		}
		source.SourcePassword = encPassword
	}

	if req.DestPassword != "" {
		encPassword, err := h.encryptor.Encrypt(req.DestPassword)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt credentials"})
			return
		}
		source.DestPassword = encPassword
	}

	if err := h.db.UpdateSource(source); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update source"})
		return
	}

	log.Printf("APIUpdateSource: saved sync_direction='%s' for source %s", source.SyncDirection, sourceID)
	h.scheduler.UpdateJobInterval(source.ID, time.Duration(source.SyncInterval)*time.Second)

	c.JSON(http.StatusOK, sourceToAPI(source))
}

// APIDeleteSource deletes a source.
func (h *Handlers) APIDeleteSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sourceID := c.Param("id")
	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	h.scheduler.RemoveJob(sourceID)

	if err := h.db.DeleteSource(sourceID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete source"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Source deleted"})
}

// APIToggleSource toggles a source's enabled status.
func (h *Handlers) APIToggleSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sourceID := c.Param("id")
	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	source.Enabled = !source.Enabled
	if err := h.db.UpdateSource(source); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update source"})
		return
	}

	if source.Enabled {
		h.scheduler.AddJob(source.ID, time.Duration(source.SyncInterval)*time.Second)
	} else {
		h.scheduler.RemoveJob(source.ID)
	}

	c.JSON(http.StatusOK, sourceToAPI(source))
}

// APITriggerSync triggers a sync for a source.
func (h *Handlers) APITriggerSync(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sourceID := c.Param("id")
	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	h.scheduler.TriggerSync(sourceID)

	c.JSON(http.StatusOK, gin.H{"message": "Sync triggered"})
}

// APIGetSourceLogs returns logs for a source.
func (h *Handlers) APIGetSourceLogs(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	sourceID := c.Param("id")
	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	page := 1
	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	limit := 20
	offset := (page - 1) * limit

	logs, err := h.db.GetSyncLogs(sourceID, 1000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load logs"})
		return
	}

	totalPages := (len(logs) + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}

	// Paginate
	start := offset
	end := offset + limit
	if start > len(logs) {
		start = len(logs)
	}
	if end > len(logs) {
		end = len(logs)
	}
	paginatedLogs := logs[start:end]

	apiLogs := make([]*APISyncLog, len(paginatedLogs))
	for i, l := range paginatedLogs {
		apiLogs[i] = syncLogToAPI(l)
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":        apiLogs,
		"page":        page,
		"total_pages": totalPages,
	})
}

// APIMalformedEvent represents a malformed event in API responses.
type APIMalformedEvent struct {
	ID           string `json:"id"`
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name"`
	EventPath    string `json:"event_path"`
	ErrorMessage string `json:"error_message"`
	DiscoveredAt string `json:"discovered_at"`
}

// malformedEventToAPI converts a db.MalformedEvent to API format.
func malformedEventToAPI(e *db.MalformedEvent) *APIMalformedEvent {
	return &APIMalformedEvent{
		ID:           e.ID,
		SourceID:     e.SourceID,
		SourceName:   e.SourceName,
		EventPath:    e.EventPath,
		ErrorMessage: e.ErrorMessage,
		DiscoveredAt: e.DiscoveredAt.Format(time.RFC3339),
	}
}

// APIGetMalformedEvents returns all malformed events for the current user.
func (h *Handlers) APIGetMalformedEvents(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	events, err := h.db.GetMalformedEvents(session.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get malformed events"})
		return
	}

	apiEvents := make([]*APIMalformedEvent, len(events))
	for i, e := range events {
		apiEvents[i] = malformedEventToAPI(e)
	}

	c.JSON(http.StatusOK, apiEvents)
}

// APIDeleteMalformedEvent deletes a malformed event record and optionally the event from the source.
func (h *Handlers) APIDeleteMalformedEvent(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	eventID := c.Param("id")

	// Get the malformed event
	event, err := h.db.GetMalformedEventByID(eventID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Malformed event not found"})
		return
	}

	// Get the source to verify ownership
	source, err := h.db.GetSourceByID(event.SourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
		return
	}

	if source.UserID != session.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Try to delete the event from the source calendar
	sourcePassword, err := h.encryptor.Decrypt(source.SourcePassword)
	if err == nil {
		client, err := caldav.NewClient(source.SourceURL, source.SourceUsername, sourcePassword)
		if err == nil {
			ctx := c.Request.Context()
			if err := client.DeleteEvent(ctx, event.EventPath); err != nil {
				log.Printf("Failed to delete malformed event from source: %v", err)
				// Continue to delete the record anyway
			} else {
				log.Printf("Deleted malformed event from source: %s", event.EventPath)
			}
		}
	}

	// Delete the malformed event record
	if err := h.db.DeleteMalformedEvent(eventID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete malformed event"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Malformed event deleted"})
}
