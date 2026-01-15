package web

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
)

// SetupRoutes configures all application routes.
//
// Rate Limiting Strategy:
// - Auth endpoints: 5 req/s, burst 10 - Strict to prevent credential brute-force
// - General API: 30 req/s, burst 60 - Allows normal UI usage with headroom for page loads
// - Expensive ops: 2 req/s, burst 5 - Very strict for operations that make external network calls
//
// These values balance security with usability. Adjust via code if needed for your deployment.
func SetupRoutes(r *gin.Engine, h *Handlers, sm *auth.SessionManager) {
	// Health endpoints (no auth, no rate limit) - must always be accessible for orchestration
	r.GET("/health", h.HealthCheck)
	r.GET("/healthz", h.Liveness)
	r.GET("/ready", h.Readiness)

	// Auth endpoints with strict rate limiting to prevent brute force attacks on OIDC flow
	// 5 req/s allows normal login flow but prevents automated attacks
	authRateLimiter := RateLimiter(5, 10)
	authGroup := r.Group("/auth")
	authGroup.Use(authRateLimiter)
	{
		authGroup.GET("/login", h.Login)
		authGroup.POST("/login", h.Login)
		authGroup.GET("/callback", h.Callback)
		authGroup.POST("/logout", h.Logout)
	}

	// General API routes - 30 req/s handles typical SPA usage (page loads fetch multiple endpoints)
	apiRateLimiter := RateLimiter(30, 60)
	apiGroup := r.Group("/api")
	apiGroup.Use(apiRateLimiter)
	apiGroup.Use(auth.OptionalAuth(sm))
	{
		apiGroup.GET("/auth/status", h.APIAuthStatus)
		apiGroup.POST("/auth/logout", h.APILogout)
	}

	// Protected API routes with rate limiting, origin validation, and content-type validation
	protectedAPI := r.Group("/api")
	protectedAPI.Use(apiRateLimiter)
	protectedAPI.Use(auth.RequireAuth(sm))
	protectedAPI.Use(ValidateOrigin())         // CSRF protection via origin check
	protectedAPI.Use(RequireJSONContentType()) // Validate Content-Type header
	{
		protectedAPI.GET("/dashboard/stats", h.APIDashboardStats)
		protectedAPI.GET("/dashboard/sync-history", h.APISyncHistory)
		protectedAPI.GET("/sources", h.APIListSources)
		protectedAPI.GET("/sources/:id", h.APIGetSource)
		protectedAPI.PUT("/sources/:id", h.APIUpdateSource)
		protectedAPI.DELETE("/sources/:id", h.APIDeleteSource)
		protectedAPI.POST("/sources/:id/toggle", h.APIToggleSource)
		protectedAPI.POST("/sources/:id/sync", h.APITriggerSync)
		protectedAPI.GET("/sources/:id/logs", h.APIGetSourceLogs)
		protectedAPI.GET("/malformed-events", h.APIGetMalformedEvents)
		protectedAPI.DELETE("/malformed-events", h.APIDeleteAllMalformedEvents)
		protectedAPI.DELETE("/malformed-events/:id", h.APIDeleteMalformedEvent)
		protectedAPI.GET("/settings/alerts", h.APIGetAlertPreferences)
		protectedAPI.PUT("/settings/alerts", h.APIUpdateAlertPreferences)
		protectedAPI.GET("/activity", h.APIGetActivity)
	}

	// Expensive operations - 2 req/s prevents abuse of network-intensive operations
	// These endpoints make external CalDAV connections which are slow and resource-intensive
	expensiveRateLimiter := RateLimiter(2, 5)
	expensiveAPI := r.Group("/api")
	expensiveAPI.Use(expensiveRateLimiter)
	expensiveAPI.Use(auth.RequireAuth(sm))
	expensiveAPI.Use(ValidateOrigin())
	expensiveAPI.Use(RequireJSONContentType())
	{
		expensiveAPI.POST("/sources", h.APICreateSource)                      // Tests connections to CalDAV servers
		expensiveAPI.POST("/calendars/discover", h.APIDiscoverCalendars)      // Discovers calendars via network
		expensiveAPI.POST("/settings/alerts/test-webhook", h.APITestWebhook)  // Tests webhook via network
	}

	// Serve React app static files
	setupReactApp(r)
}

// setupReactApp configures serving of the React frontend.
func setupReactApp(r *gin.Engine) {
	// Check if React build exists
	webDistPath := "web/dist"
	if _, err := os.Stat(webDistPath); os.IsNotExist(err) {
		// In development or React app not built yet
		return
	}

	// Serve static assets
	r.Static("/assets", filepath.Join(webDistPath, "assets"))

	// Serve other static files
	r.StaticFile("/vite.svg", filepath.Join(webDistPath, "vite.svg"))
	r.StaticFile("/logo.png", filepath.Join(webDistPath, "logo.png"))

	// SPA fallback - serve index.html for all unmatched routes
	r.NoRoute(func(c *gin.Context) {
		// Don't serve index.html for API routes
		if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}
		// Don't serve index.html for auth routes
		if len(c.Request.URL.Path) >= 5 && c.Request.URL.Path[:5] == "/auth" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}
		// Don't serve index.html for health routes
		if c.Request.URL.Path == "/health" || c.Request.URL.Path == "/healthz" || c.Request.URL.Path == "/ready" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}

		c.File(filepath.Join(webDistPath, "index.html"))
	})
}
