package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/config"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
	"github.com/macjediwizard/calbridgesync/internal/health"
	"github.com/macjediwizard/calbridgesync/internal/scheduler"
)

// Handlers contains all HTTP handlers and their dependencies.
type Handlers struct {
	cfg        *config.Config
	db         *db.DB
	oidc       *auth.OIDCProvider
	session    *auth.SessionManager
	encryptor  *crypto.Encryptor
	syncEngine *caldav.SyncEngine
	scheduler  *scheduler.Scheduler
	health     *health.Checker
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(
	cfg *config.Config,
	database *db.DB,
	oidc *auth.OIDCProvider,
	session *auth.SessionManager,
	encryptor *crypto.Encryptor,
	syncEngine *caldav.SyncEngine,
	sched *scheduler.Scheduler,
	healthChecker *health.Checker,
) *Handlers {
	return &Handlers{
		cfg:        cfg,
		db:         database,
		oidc:       oidc,
		session:    session,
		encryptor:  encryptor,
		syncEngine: syncEngine,
		scheduler:  sched,
		health:     healthChecker,
	}
}

// HealthCheck returns a full health report.
func (h *Handlers) HealthCheck(c *gin.Context) {
	report := h.health.Check(c.Request.Context())
	status := http.StatusOK
	if report.Status == health.StatusUnhealthy {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, report)
}

// Liveness returns a simple liveness check.
func (h *Handlers) Liveness(c *gin.Context) {
	report := h.health.Liveness()
	c.JSON(http.StatusOK, report)
}

// Readiness checks all dependencies.
func (h *Handlers) Readiness(c *gin.Context) {
	report := h.health.Check(c.Request.Context())
	if report.Status == health.StatusUnhealthy {
		c.JSON(http.StatusServiceUnavailable, report)
		return
	}
	c.JSON(http.StatusOK, report)
}

// LoginPage renders the login page.
func (h *Handlers) LoginPage(c *gin.Context) {
	c.HTML(http.StatusOK, "login.html", gin.H{
		"title": "Sign In - CalBridgeSync",
	})
}

// Login initiates OIDC authentication.
func (h *Handlers) Login(c *gin.Context) {
	state, err := auth.GenerateState()
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to generate state",
		})
		return
	}

	if err := h.session.SetOAuthState(c.Writer, c.Request, state); err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to save state",
		})
		return
	}

	authURL := h.oidc.AuthCodeURL(state)
	c.Redirect(http.StatusFound, authURL)
}

// Callback handles the OIDC callback.
func (h *Handlers) Callback(c *gin.Context) {
	// Verify state
	state := c.Query("state")
	savedState, err := h.session.GetOAuthState(c.Writer, c.Request)
	if err != nil || state != savedState {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error": "Invalid state parameter",
		})
		return
	}

	// Check for error from OIDC provider
	if errParam := c.Query("error"); errParam != "" {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error": "Authentication failed: " + errParam,
		})
		return
	}

	// Exchange code for token
	code := c.Query("code")
	token, err := h.oidc.Exchange(c.Request.Context(), code)
	if err != nil {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error": "Failed to exchange code",
		})
		return
	}

	// Verify ID token and get claims
	claims, err := h.oidc.VerifyIDToken(c.Request.Context(), token)
	if err != nil {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error": "Failed to verify token",
		})
		return
	}

	// Get or create user
	user, err := h.db.GetOrCreateUser(claims.Email, claims.Name)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to create user",
		})
		return
	}

	// Create session
	sessionData := &auth.SessionData{
		UserID:  user.ID,
		Email:   user.Email,
		Name:    user.Name,
		Picture: claims.AvatarURL,
	}
	if err := h.session.Set(c.Writer, c.Request, sessionData); err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to create session",
		})
		return
	}

	// Check for redirect cookie with validation to prevent open redirect
	redirectURL := "/"
	if cookie, err := c.Cookie("redirect_after_login"); err == nil && cookie != "" {
		// Only use redirect URL if it's safe (relative path, no protocol)
		if IsSafeRedirectURL(cookie) {
			redirectURL = cookie
		}
		c.SetCookie("redirect_after_login", "", -1, "/", "", h.cfg.IsProduction(), true)
	}

	c.Redirect(http.StatusFound, redirectURL)
}

// Logout clears the session.
func (h *Handlers) Logout(c *gin.Context) {
	if err := h.session.Clear(c.Writer, c.Request); err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to logout",
		})
		return
	}
	c.Redirect(http.StatusFound, "/auth/login")
}

// Dashboard renders the main dashboard.
func (h *Handlers) Dashboard(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.Redirect(http.StatusFound, "/auth/login")
		return
	}

	sources, err := h.db.GetSourcesByUserID(session.UserID)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to load sources",
		})
		return
	}

	c.HTML(http.StatusOK, "dashboard.html", gin.H{
		"title":     "Dashboard - CalBridgeSync",
		"user":      session,
		"sources":   sources,
		"csrfToken": session.CSRFToken,
		"presets":   db.SourcePresets,
	})
}

// ListSources returns the list of sources.
func (h *Handlers) ListSources(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sources, err := h.db.GetSourcesByUserID(session.UserID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to load sources")
		return
	}

	if isHTMX(c) {
		c.HTML(http.StatusOK, "sources/list.html", gin.H{
			"sources":   sources,
			"csrfToken": session.CSRFToken,
		})
		return
	}

	c.JSON(http.StatusOK, sources)
}

// AddSourcePage renders the add source form.
func (h *Handlers) AddSourcePage(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	c.HTML(http.StatusOK, "sources/add.html", gin.H{
		"title":       "Add Source - CalBridgeSync",
		"user":        session,
		"csrfToken":   session.CSRFToken,
		"presets":     db.SourcePresets,
		"defaultDest": h.cfg.CalDAV.DefaultDestURL,
	})
}

// sourceFormData holds parsed form data for source creation/update.
type sourceFormData struct {
	Name             string
	SourceType       db.SourceType
	SourceURL        string
	SourceUsername   string
	SourcePassword   string
	DestURL          string
	DestUsername     string
	DestPassword     string
	ConflictStrategy db.ConflictStrategy
	SyncInterval     int
}

// parseSourceForm extracts source form data from the request.
func (h *Handlers) parseSourceForm(c *gin.Context) *sourceFormData {
	syncInterval, err := strconv.Atoi(c.PostForm("sync_interval"))
	if err != nil || syncInterval < h.cfg.Sync.MinInterval || syncInterval > h.cfg.Sync.MaxInterval {
		syncInterval = 300
	}

	return &sourceFormData{
		Name:             c.PostForm("name"),
		SourceType:       db.SourceType(c.PostForm("source_type")),
		SourceURL:        c.PostForm("source_url"),
		SourceUsername:   c.PostForm("source_username"),
		SourcePassword:   c.PostForm("source_password"),
		DestURL:          c.PostForm("dest_url"),
		DestUsername:     c.PostForm("dest_username"),
		DestPassword:     c.PostForm("dest_password"),
		ConflictStrategy: db.ConflictStrategy(c.PostForm("conflict_strategy")),
		SyncInterval:     syncInterval,
	}
}

// validateSourceForm checks required fields are present.
func (f *sourceFormData) validate() bool {
	return f.Name != "" && f.SourceURL != "" && f.SourceUsername != "" && f.SourcePassword != ""
}

// hasDestCredentials checks if destination credentials are provided.
func (f *sourceFormData) hasDestCredentials() bool {
	return f.DestURL != "" && f.DestUsername != "" && f.DestPassword != ""
}

// AddSource creates a new source.
func (h *Handlers) AddSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	form := h.parseSourceForm(c)

	if !form.validate() {
		h.respondError(c, http.StatusBadRequest, "Missing required fields")
		return
	}

	// Test and create source
	if err := h.testAndCreateSource(c, session.UserID, form); err != nil {
		return // Error already sent
	}

	if isHTMX(c) {
		c.Header("HX-Redirect", "/")
		c.Status(http.StatusOK)
		return
	}
	c.Redirect(http.StatusFound, "/")
}

// testAndCreateSource validates connections, encrypts passwords, and creates the source.
func (h *Handlers) testAndCreateSource(c *gin.Context, userID string, form *sourceFormData) error {
	ctx := c.Request.Context()

	// Test source connection
	if err := h.syncEngine.TestConnection(ctx, form.SourceURL, form.SourceUsername, form.SourcePassword); err != nil {
		h.respondError(c, http.StatusBadRequest, "Failed to connect to source: "+err.Error())
		return err
	}

	// Test destination if provided
	if form.hasDestCredentials() {
		if err := h.syncEngine.TestConnection(ctx, form.DestURL, form.DestUsername, form.DestPassword); err != nil {
			h.respondError(c, http.StatusBadRequest, "Failed to connect to destination: "+err.Error())
			return err
		}
	}

	// Encrypt passwords
	encSourcePwd, err := h.encryptor.Encrypt(form.SourcePassword)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to encrypt credentials")
		return err
	}

	encDestPwd, err := h.encryptor.Encrypt(form.DestPassword)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to encrypt credentials")
		return err
	}

	source := &db.Source{
		UserID:           userID,
		Name:             form.Name,
		SourceType:       form.SourceType,
		SourceURL:        form.SourceURL,
		SourceUsername:   form.SourceUsername,
		SourcePassword:   encSourcePwd,
		DestURL:          form.DestURL,
		DestUsername:     form.DestUsername,
		DestPassword:     encDestPwd,
		SyncInterval:     form.SyncInterval,
		ConflictStrategy: form.ConflictStrategy,
		Enabled:          true,
	}

	if err := h.db.CreateSource(source); err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to create source")
		return err
	}

	h.scheduler.AddJob(source.ID, time.Duration(source.SyncInterval)*time.Second)
	return nil
}

// EditSourcePage renders the edit source form.
func (h *Handlers) EditSourcePage(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sourceID := c.Param("id")

	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error": "Source not found",
		})
		return
	}

	// Verify ownership
	if source.UserID != session.UserID {
		c.HTML(http.StatusForbidden, "error.html", gin.H{
			"error": "Access denied",
		})
		return
	}

	c.HTML(http.StatusOK, "sources/edit.html", gin.H{
		"title":       "Edit Source - CalBridgeSync",
		"user":        session,
		"source":      source,
		"csrfToken":   session.CSRFToken,
		"presets":     db.SourcePresets,
		"defaultDest": h.cfg.CalDAV.DefaultDestURL,
	})
}

// UpdateSource updates an existing source.
func (h *Handlers) UpdateSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sourceID := c.Param("id")

	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		h.respondError(c, http.StatusNotFound, "Source not found")
		return
	}

	// Verify ownership
	if source.UserID != session.UserID {
		h.respondError(c, http.StatusForbidden, "Access denied")
		return
	}

	// Update fields
	source.Name = c.PostForm("name")
	source.SourceType = db.SourceType(c.PostForm("source_type"))
	source.SourceURL = c.PostForm("source_url")
	source.SourceUsername = c.PostForm("source_username")
	source.DestURL = c.PostForm("dest_url")
	source.DestUsername = c.PostForm("dest_username")
	source.ConflictStrategy = db.ConflictStrategy(c.PostForm("conflict_strategy"))

	syncIntervalStr := c.PostForm("sync_interval")
	if syncInterval, err := strconv.Atoi(syncIntervalStr); err == nil {
		source.SyncInterval = syncInterval
	}

	// Update passwords if provided
	if newSourcePassword := c.PostForm("source_password"); newSourcePassword != "" {
		encPassword, err := h.encryptor.Encrypt(newSourcePassword)
		if err != nil {
			h.respondError(c, http.StatusInternalServerError, "Failed to encrypt credentials")
			return
		}
		source.SourcePassword = encPassword
	}

	if newDestPassword := c.PostForm("dest_password"); newDestPassword != "" {
		encPassword, err := h.encryptor.Encrypt(newDestPassword)
		if err != nil {
			h.respondError(c, http.StatusInternalServerError, "Failed to encrypt credentials")
			return
		}
		source.DestPassword = encPassword
	}

	if err := h.db.UpdateSource(source); err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to update source")
		return
	}

	// Update scheduler
	h.scheduler.UpdateJobInterval(source.ID, time.Duration(source.SyncInterval)*time.Second)

	if isHTMX(c) {
		c.Header("HX-Redirect", "/")
		c.Status(http.StatusOK)
		return
	}

	c.Redirect(http.StatusFound, "/")
}

// DeleteSource deletes a source.
func (h *Handlers) DeleteSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sourceID := c.Param("id")

	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		h.respondError(c, http.StatusNotFound, "Source not found")
		return
	}

	// Verify ownership
	if source.UserID != session.UserID {
		h.respondError(c, http.StatusForbidden, "Access denied")
		return
	}

	// Remove from scheduler
	h.scheduler.RemoveJob(sourceID)

	if err := h.db.DeleteSource(sourceID); err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to delete source")
		return
	}

	if isHTMX(c) {
		c.Header("HX-Refresh", "true")
		c.Status(http.StatusOK)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Source deleted"})
}

// TriggerSync manually triggers a sync.
func (h *Handlers) TriggerSync(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sourceID := c.Param("id")

	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		h.respondError(c, http.StatusNotFound, "Source not found")
		return
	}

	// Verify ownership
	if source.UserID != session.UserID {
		h.respondError(c, http.StatusForbidden, "Access denied")
		return
	}

	// Trigger async sync
	h.scheduler.TriggerSync(sourceID)

	if isHTMX(c) {
		c.HTML(http.StatusOK, "partials/sync_triggered.html", gin.H{
			"message": "Sync started",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Sync triggered"})
}

// ToggleSource enables or disables a source.
func (h *Handlers) ToggleSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sourceID := c.Param("id")

	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		h.respondError(c, http.StatusNotFound, "Source not found")
		return
	}

	// Verify ownership
	if source.UserID != session.UserID {
		h.respondError(c, http.StatusForbidden, "Access denied")
		return
	}

	// Toggle enabled status
	source.Enabled = !source.Enabled
	if err := h.db.UpdateSource(source); err != nil {
		h.respondError(c, http.StatusInternalServerError, "Failed to update source")
		return
	}

	// Update scheduler
	if source.Enabled {
		h.scheduler.AddJob(source.ID, time.Duration(source.SyncInterval)*time.Second)
	} else {
		h.scheduler.RemoveJob(source.ID)
	}

	if isHTMX(c) {
		c.Header("HX-Refresh", "true")
		c.Status(http.StatusOK)
		return
	}

	c.JSON(http.StatusOK, gin.H{"enabled": source.Enabled})
}

// ViewLogs shows sync logs for a source.
func (h *Handlers) ViewLogs(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	sourceID := c.Param("id")

	source, err := h.db.GetSourceByID(sourceID)
	if err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error": "Source not found",
		})
		return
	}

	// Verify ownership
	if source.UserID != session.UserID {
		c.HTML(http.StatusForbidden, "error.html", gin.H{
			"error": "Access denied",
		})
		return
	}

	logs, err := h.db.GetSyncLogs(sourceID, 100)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to load logs",
		})
		return
	}

	c.HTML(http.StatusOK, "logs.html", gin.H{
		"title":  "Sync Logs - " + source.Name,
		"user":   session,
		"source": source,
		"logs":   logs,
	})
}

// respondError sends an error response appropriate for the request type.
func (h *Handlers) respondError(c *gin.Context, status int, message string) {
	if isHTMX(c) {
		c.HTML(status, "partials/error.html", gin.H{
			"error": message,
		})
		return
	}
	c.JSON(status, gin.H{"error": message})
}

// isHTMX returns true if the request is an HTMX request.
func isHTMX(c *gin.Context) bool {
	return c.GetHeader("HX-Request") == "true"
}
