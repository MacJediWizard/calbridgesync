package web

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// SecurityHeaders adds security headers to all responses.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
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

		// CSP - Note: 'unsafe-inline' for styles is required for Tailwind CSS
		// Scripts use 'unsafe-inline' only for Vite dev mode; in production builds it's not needed
		// but keeping it for compatibility
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

// getAllowedOrigins returns the list of allowed origins for CSRF validation.
func getAllowedOrigins() []string {
	origins := []string{}

	// Add from environment variable if set
	if env := os.Getenv("ALLOWED_ORIGINS"); env != "" {
		for _, o := range strings.Split(env, ",") {
			origins = append(origins, strings.TrimSpace(o))
		}
	}

	// Add default localhost origins for development
	if len(origins) == 0 {
		origins = []string{
			"http://localhost:8080",
			"http://localhost:5173",
			"http://127.0.0.1:8080",
			"http://127.0.0.1:5173",
		}
	}

	return origins
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
