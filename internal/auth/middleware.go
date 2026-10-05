package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// ContextKeySession is the key used to store session data in the Gin context.
	ContextKeySession = "session"
)

// RequireAuth is a middleware that requires authentication.
// Unauthenticated API requests (/api and /api/*) get a 401 JSON response so
// the SPA sees a clean auth failure; other requests are redirected to
// /auth/login. The redirect_after_login cookie is only set for non-API
// requests so a later login never lands the user on a raw JSON endpoint.
func RequireAuth(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := sm.Get(c.Request)
		if err != nil {
			if isAPIPath(c.Request.URL.Path) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
				return
			}
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

// isAPIPath reports whether path is the /api root or below it.
func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
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
