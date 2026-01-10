package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	// ContextKeySession is the key used to store session data in the Gin context.
	ContextKeySession = "session"
)

// RequireAuth is a middleware that requires authentication.
// It redirects to /auth/login if the user is not authenticated.
func RequireAuth(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := sm.Get(c.Request)
		if err != nil {
			// Store the original URL to redirect back after login
			c.SetCookie("redirect_after_login", c.Request.URL.String(), 600, "/", "", sm.secure, true)
			c.Redirect(http.StatusFound, "/auth/login")
			c.Abort()
			return
		}

		// Store session data in context for handlers to use
		c.Set(ContextKeySession, session)
		c.Next()
	}
}

// GetCurrentUser retrieves the current user's session data from the Gin context.
func GetCurrentUser(c *gin.Context) *SessionData {
	session, exists := c.Get(ContextKeySession)
	if !exists {
		return nil
	}

	sessionData, ok := session.(*SessionData)
	if !ok {
		return nil
	}

	return sessionData
}

// ValidateCSRF is a middleware that validates CSRF tokens for non-safe methods.
// It skips validation for GET, HEAD, OPTIONS methods and HTMX requests.
func ValidateCSRF(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip for safe methods
		if c.Request.Method == http.MethodGet ||
			c.Request.Method == http.MethodHead ||
			c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		// Skip for HTMX requests (they include their own CSRF protection via headers)
		if c.GetHeader("HX-Request") == "true" {
			c.Next()
			return
		}

		// Get session
		session, err := sm.Get(c.Request)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "session required"})
			return
		}

		// Get CSRF token from request
		csrfToken := c.PostForm("csrf_token")
		if csrfToken == "" {
			csrfToken = c.GetHeader("X-CSRF-Token")
		}

		// Validate token
		if csrfToken == "" || csrfToken != session.CSRFToken {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid CSRF token"})
			return
		}

		c.Next()
	}
}

// OptionalAuth is a middleware that loads session data if available but doesn't require it.
func OptionalAuth(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := sm.Get(c.Request)
		if err == nil {
			c.Set(ContextKeySession, session)
		}
		c.Next()
	}
}
