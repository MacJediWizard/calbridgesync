package web

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// SecurityHeaders adds security headers to all responses.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Suppress Server header to avoid revealing technology stack
		c.Header("Server", "")

		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")

		// HSTS - force HTTPS for all future connections (1 year, include subdomains)
		// Only set in production (when request is already over HTTPS)
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		// Permissions-Policy - restrict browser features we don't need
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		// CSP - Content Security Policy
		// Note on 'unsafe-inline':
		// - style-src: Required for Tailwind CSS which uses inline styles
		// - script-src: Required for React/Vite which injects inline scripts for HMR in dev
		//   and may use inline event handlers. Removing this would require nonce-based CSP
		//   which adds complexity. The XSS risk is mitigated by React's automatic escaping.
		c.Header("Content-Security-Policy", "default-src 'self'; "+
			"script-src 'self' 'unsafe-inline' https://unpkg.com; "+
			"style-src 'self' 'unsafe-inline' https://cdn.tailwindcss.com https://fonts.googleapis.com; "+
			"img-src 'self' data: https://cdn.macjediwizard.com; "+
			"font-src 'self' https://fonts.gstatic.com; "+
			"connect-src 'self'; "+
			"form-action 'self'; "+
			"frame-ancestors 'none'; "+
			"base-uri 'self'")
		c.Next()
	}
}

// RateLimiter creates a rate limiting middleware.
func RateLimiter(rps float64, burst int) gin.HandlerFunc {
	limiter := rate.NewLimiter(rate.Limit(rps), burst)

	return func(c *gin.Context) {
		if !limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			return
		}
		c.Next()
	}
}

// RequestLogger logs HTTP requests without logging bodies or query strings (security).
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		// Only log path, NOT query string (c.Request.URL.RawQuery) - may contain sensitive data
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		duration := time.Since(start)
		status := c.Writer.Status()

		// Log request (NEVER log request bodies or query strings - may contain credentials)
		log.Printf("%s %s %d %v", method, path, status, duration)
	}
}

// RequireJSONContentType validates that POST/PUT/PATCH requests have JSON content type.
func RequireJSONContentType() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only validate for methods that typically have a body
		if c.Request.Method == "POST" || c.Request.Method == "PUT" || c.Request.Method == "PATCH" {
			contentType := c.GetHeader("Content-Type")
			// Allow empty content type for requests without body, or require JSON
			if contentType != "" && !strings.HasPrefix(contentType, "application/json") {
				c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{
					"error": "Content-Type must be application/json",
				})
				return
			}
		}
		c.Next()
	}
}

// ValidateOrigin validates the Origin header for CSRF protection.
// This provides an additional layer of protection beyond SameSite cookies.
func ValidateOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only validate state-changing methods
		if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}

		origin := c.GetHeader("Origin")
		referer := c.GetHeader("Referer")

		// If no Origin header, check Referer (some browsers send Referer instead)
		if origin == "" && referer != "" {
			// Extract origin from referer
			if idx := strings.Index(referer, "://"); idx != -1 {
				end := strings.Index(referer[idx+3:], "/")
				if end != -1 {
					origin = referer[:idx+3+end]
				} else {
					origin = referer
				}
			}
		}

		// If still no origin, reject the request (browser should send one)
		if origin == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "Missing Origin header",
			})
			return
		}

		// Get allowed origins from environment or use defaults
		allowedOrigins := getAllowedOrigins()

		// Validate origin
		originValid := false
		for _, allowed := range allowedOrigins {
			if origin == allowed {
				originValid = true
				break
			}
		}

		if !originValid {
			log.Printf("CSRF: rejected request from origin %s", origin)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "Invalid origin",
			})
			return
		}

		c.Next()
	}
}

// allowedOriginsCache caches the parsed origins so we don't re-parse
// ALLOWED_ORIGINS on every request. Initialization is guarded by
// allowedOriginsOnce so the parse happens exactly once even under
// concurrent first-request load. Previously this was a bool +
// unguarded slice pair, which was a data race: two goroutines hitting
// the first request simultaneously could read and write the slice
// with no synchronization, and under the Go memory model that's
// undefined behavior — in the worst case a half-initialized slice
// visible to one goroutine before the cache init flag flipped
// could cause a request to fail-open or fail-closed depending on
// scheduling. sync.Once gives us a lock-free fast path after the
// initialization completes, so the per-request cost stays trivial. (#91)
var (
	allowedOriginsCache []string
	allowedOriginsOnce  sync.Once
)

// getAllowedOrigins returns the list of allowed origins for CSRF validation.
// SECURITY: In production, always set ALLOWED_ORIGINS environment variable.
func getAllowedOrigins() []string {
	allowedOriginsOnce.Do(func() {
		origins := []string{}

		// Add from environment variable if set
		if env := os.Getenv("ALLOWED_ORIGINS"); env != "" {
			for _, o := range strings.Split(env, ",") {
				origin := strings.TrimSpace(o)
				if isValidOrigin(origin) {
					origins = append(origins, origin)
				} else {
					log.Printf("WARNING: Invalid origin in ALLOWED_ORIGINS ignored: %s", origin)
				}
			}
		}

		// Fall back to localhost origins for development only
		if len(origins) == 0 {
			// Log warning - this should not happen in production
			log.Printf("WARNING: ALLOWED_ORIGINS not set - using localhost defaults. Set ALLOWED_ORIGINS in production!")
			origins = []string{
				"http://localhost:8080",
				"http://localhost:5173",
				"http://127.0.0.1:8080",
				"http://127.0.0.1:5173",
			}
		}

		allowedOriginsCache = origins
	})
	return allowedOriginsCache
}

// isValidOrigin validates that an origin string is a proper URL format.
func isValidOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	// Must start with http:// or https://
	if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
		return false
	}
	// Must not end with a slash (origins don't have paths)
	if strings.HasSuffix(origin, "/") {
		return false
	}
	// Must have a host after the protocol
	if strings.HasPrefix(origin, "http://") && len(origin) <= 7 {
		return false
	}
	if strings.HasPrefix(origin, "https://") && len(origin) <= 8 {
		return false
	}
	return true
}

// IsSafeRedirectURL validates that a URL is safe for redirects (relative paths only).
func IsSafeRedirectURL(url string) bool {
	if url == "" {
		return false
	}
	// Must start with / (relative path)
	if !strings.HasPrefix(url, "/") {
		return false
	}
	// Must not be a protocol-relative URL (//evil.com)
	if strings.HasPrefix(url, "//") {
		return false
	}
	// Must not contain URL-encoded slashes that could bypass checks
	if strings.Contains(strings.ToLower(url), "%2f%2f") {
		return false
	}
	// Must not contain backslashes (IE compatibility)
	if strings.Contains(url, "\\") {
		return false
	}
	return true
}
