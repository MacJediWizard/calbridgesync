package web

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/config"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
	"github.com/macjediwizard/calbridgesync/internal/health"
	"github.com/macjediwizard/calbridgesync/internal/notify"
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
	notifier   *notify.Notifier
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
	notifier *notify.Notifier,
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
		notifier:   notifier,
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

// Login initiates OIDC authentication.
// Note: Account lockout and brute-force protection are delegated to the OIDC provider
// (e.g., Authentik, Keycloak, Okta). Configure these protections in your identity provider.
func (h *Handlers) Login(c *gin.Context) {
	state, err := auth.GenerateState()
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to generate state",
		})
		return
	}

	nonce, verifier, err := auth.GenerateOIDCLoginSecrets()
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to generate state",
		})
		return
	}

	loginState := &auth.OIDCLoginState{State: state, Nonce: nonce, Verifier: verifier}
	if err := h.session.SetOIDCLoginState(c.Writer, c.Request, loginState); err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to save state",
		})
		return
	}

	authURL := h.oidc.AuthCodeURL(state, nonce, verifier)
	c.Redirect(http.StatusFound, authURL)
}

// Callback handles the OIDC callback.
func (h *Handlers) Callback(c *gin.Context) {
	// Verify state
	state := c.Query("state")
	loginState, err := h.session.GetOIDCLoginState(c.Writer, c.Request)
	if err != nil || state != loginState.State {
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
	token, err := h.oidc.Exchange(c.Request.Context(), code, loginState.Verifier)
	if err != nil {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error": "Failed to exchange code",
		})
		return
	}

	// Verify ID token and get claims
	claims, err := h.oidc.VerifyIDToken(c.Request.Context(), token, loginState.Nonce)
	if errors.Is(err, auth.ErrEmailNotVerified) {
		c.HTML(http.StatusForbidden, "error.html", gin.H{
			"error": "Your email address is not verified with the identity provider",
		})
		return
	}
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
