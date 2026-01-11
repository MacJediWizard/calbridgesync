package web

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
)

// SetupRoutes configures all application routes.
func SetupRoutes(r *gin.Engine, h *Handlers, sm *auth.SessionManager) {
	// Health endpoints (no auth, no rate limit)
	r.GET("/health", h.HealthCheck)
	r.GET("/healthz", h.Liveness)
	r.GET("/ready", h.Readiness)

	// Auth endpoints with rate limiting to prevent brute force attacks
	authRateLimiter := RateLimiter(5, 10) // 5 requests/sec, burst of 10
	authGroup := r.Group("/auth")
	authGroup.Use(authRateLimiter)
	{
		authGroup.GET("/login", h.Login)
		authGroup.POST("/login", h.Login)
		authGroup.GET("/callback", h.Callback)
		authGroup.POST("/logout", h.Logout)
	}

	// API routes for React frontend with rate limiting
	apiRateLimiter := RateLimiter(30, 60) // 30 requests/sec, burst of 60
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
		protectedAPI.DELETE("/malformed-events/:id", h.APIDeleteMalformedEvent)
	}

	// Expensive operations with stricter rate limiting (network calls, credential testing)
	expensiveRateLimiter := RateLimiter(2, 5) // 2 requests/sec, burst of 5
	expensiveAPI := r.Group("/api")
	expensiveAPI.Use(expensiveRateLimiter)
	expensiveAPI.Use(auth.RequireAuth(sm))
	expensiveAPI.Use(ValidateOrigin())
	expensiveAPI.Use(RequireJSONContentType())
	{
		expensiveAPI.POST("/sources", h.APICreateSource)           // Tests connections
		expensiveAPI.POST("/calendars/discover", h.APIDiscoverCalendars) // Network call
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
