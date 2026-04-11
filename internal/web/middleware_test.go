package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestSecurityHeaders(t *testing.T) {
	t.Run("sets security headers", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		handler := SecurityHeaders()
		handler(c)

		headers := w.Header()
		if headers.Get("X-Content-Type-Options") != "nosniff" {
			t.Error("expected X-Content-Type-Options header")
		}
		if headers.Get("X-Frame-Options") != "DENY" {
			t.Error("expected X-Frame-Options header")
		}
		if headers.Get("X-XSS-Protection") != "1; mode=block" {
			t.Error("expected X-XSS-Protection header")
		}
		if headers.Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
			t.Error("expected Referrer-Policy header")
		}
		if headers.Get("Permissions-Policy") == "" {
			t.Error("expected Permissions-Policy header")
		}
		if headers.Get("Content-Security-Policy") == "" {
			t.Error("expected Content-Security-Policy header")
		}
	})

	t.Run("sets HSTS header for HTTPS", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("X-Forwarded-Proto", "https")

		handler := SecurityHeaders()
		handler(c)

		if w.Header().Get("Strict-Transport-Security") == "" {
			t.Error("expected HSTS header for HTTPS requests")
		}
	})

	t.Run("does not set HSTS for HTTP", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		handler := SecurityHeaders()
		handler(c)

		if w.Header().Get("Strict-Transport-Security") != "" {
			t.Error("should not set HSTS header for HTTP requests")
		}
	})
}

func TestRateLimiter(t *testing.T) {
	t.Run("allows requests within limit", func(t *testing.T) {
		limiter := RateLimiter(10, 10) // 10 req/s, burst 10

		for i := 0; i < 5; i++ {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

			limiter(c)

			if c.IsAborted() {
				t.Errorf("request %d should not be aborted", i)
			}
		}
	})

	t.Run("blocks requests exceeding limit", func(t *testing.T) {
		limiter := RateLimiter(1, 1) // 1 req/s, burst 1

		// First request should pass
		w1 := httptest.NewRecorder()
		c1, _ := gin.CreateTestContext(w1)
		c1.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		limiter(c1)

		if c1.IsAborted() {
			t.Error("first request should not be aborted")
		}

		// Second request should be rate limited
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		limiter(c2)

		if !c2.IsAborted() {
			t.Error("second request should be rate limited")
		}
		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("expected status 429, got %d", w2.Code)
		}
	})
}

func TestRequireJSONContentType(t *testing.T) {
	t.Run("allows GET requests without content-type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		handler := RequireJSONContentType()
		handler(c)

		if c.IsAborted() {
			t.Error("GET request should not be aborted")
		}
	})

	t.Run("allows POST with JSON content-type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Content-Type", "application/json")

		handler := RequireJSONContentType()
		handler(c)

		if c.IsAborted() {
			t.Error("POST with JSON content-type should not be aborted")
		}
	})

	t.Run("allows POST with JSON charset content-type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")

		handler := RequireJSONContentType()
		handler(c)

		if c.IsAborted() {
			t.Error("POST with JSON charset content-type should not be aborted")
		}
	})

	t.Run("allows POST with empty content-type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

		handler := RequireJSONContentType()
		handler(c)

		if c.IsAborted() {
			t.Error("POST with empty content-type should not be aborted")
		}
	})

	t.Run("rejects POST with non-JSON content-type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Content-Type", "text/plain")

		handler := RequireJSONContentType()
		handler(c)

		if !c.IsAborted() {
			t.Error("POST with non-JSON content-type should be aborted")
		}
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("expected status 415, got %d", w.Code)
		}
	})

	t.Run("validates PUT requests", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/", nil)
		c.Request.Header.Set("Content-Type", "application/xml")

		handler := RequireJSONContentType()
		handler(c)

		if !c.IsAborted() {
			t.Error("PUT with non-JSON content-type should be aborted")
		}
	})

	t.Run("validates PATCH requests", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPatch, "/", nil)
		c.Request.Header.Set("Content-Type", "text/html")

		handler := RequireJSONContentType()
		handler(c)

		if !c.IsAborted() {
			t.Error("PATCH with non-JSON content-type should be aborted")
		}
	})
}

func TestValidateOrigin(t *testing.T) {
	// Reset cache before tests
	allowedOriginsCache = nil
	allowedOriginsCacheInit = false

	t.Run("allows GET requests without origin", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		handler := ValidateOrigin()
		handler(c)

		if c.IsAborted() {
			t.Error("GET request should not be aborted")
		}
	})

	t.Run("allows HEAD requests without origin", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodHead, "/", nil)

		handler := ValidateOrigin()
		handler(c)

		if c.IsAborted() {
			t.Error("HEAD request should not be aborted")
		}
	})

	t.Run("allows OPTIONS requests without origin", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodOptions, "/", nil)

		handler := ValidateOrigin()
		handler(c)

		if c.IsAborted() {
			t.Error("OPTIONS request should not be aborted")
		}
	})

	t.Run("rejects POST without origin", func(t *testing.T) {
		// Reset cache
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

		handler := ValidateOrigin()
		handler(c)

		if !c.IsAborted() {
			t.Error("POST without origin should be aborted")
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", w.Code)
		}
	})

	t.Run("allows POST with valid origin", func(t *testing.T) {
		// Reset cache and set allowed origins
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false
		os.Setenv("ALLOWED_ORIGINS", "http://localhost:8080")
		defer os.Unsetenv("ALLOWED_ORIGINS")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Origin", "http://localhost:8080")

		handler := ValidateOrigin()
		handler(c)

		if c.IsAborted() {
			t.Error("POST with valid origin should not be aborted")
		}
	})

	t.Run("rejects POST with invalid origin", func(t *testing.T) {
		// Reset cache and set allowed origins
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false
		os.Setenv("ALLOWED_ORIGINS", "http://localhost:8080")
		defer os.Unsetenv("ALLOWED_ORIGINS")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Origin", "http://evil.com")

		handler := ValidateOrigin()
		handler(c)

		if !c.IsAborted() {
			t.Error("POST with invalid origin should be aborted")
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", w.Code)
		}
	})

	t.Run("extracts origin from referer", func(t *testing.T) {
		// Reset cache and set allowed origins
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false
		os.Setenv("ALLOWED_ORIGINS", "http://localhost:8080")
		defer os.Unsetenv("ALLOWED_ORIGINS")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Referer", "http://localhost:8080/page")

		handler := ValidateOrigin()
		handler(c)

		if c.IsAborted() {
			t.Error("POST with valid referer should not be aborted")
		}
	})
}

func TestIsValidOrigin(t *testing.T) {
	testCases := []struct {
		name     string
		origin   string
		expected bool
	}{
		{"valid http origin", "http://example.com", true},
		{"valid https origin", "https://example.com", true},
		{"valid with port", "http://localhost:8080", true},
		{"empty origin", "", false},
		{"origin with trailing slash", "http://example.com/", false},
		{"origin without protocol", "example.com", false},
		{"http without host", "http://", false},
		{"https without host", "https://", false},
		{"ftp protocol", "ftp://example.com", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isValidOrigin(tc.origin)
			if result != tc.expected {
				t.Errorf("isValidOrigin(%q) = %v, expected %v", tc.origin, result, tc.expected)
			}
		})
	}
}

func TestIsSafeRedirectURL(t *testing.T) {
	testCases := []struct {
		name     string
		url      string
		expected bool
	}{
		{"valid relative path", "/dashboard", true},
		{"valid nested path", "/api/sources", true},
		{"empty url", "", false},
		{"protocol-relative url", "//evil.com", false},
		{"absolute url", "http://evil.com", false},
		{"encoded double slash", "/path%2f%2ftest", false},
		{"backslash", "/path\\test", false},
		{"just root", "/", true},
		{"path with query", "/dashboard?tab=settings", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsSafeRedirectURL(tc.url)
			if result != tc.expected {
				t.Errorf("IsSafeRedirectURL(%q) = %v, expected %v", tc.url, result, tc.expected)
			}
		})
	}
}

func TestGetAllowedOrigins(t *testing.T) {
	t.Run("returns cached origins on subsequent calls", func(t *testing.T) {
		// Reset cache
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false
		os.Unsetenv("ALLOWED_ORIGINS")

		// First call initializes cache
		origins1 := getAllowedOrigins()
		// Second call should return cached value
		origins2 := getAllowedOrigins()

		if len(origins1) != len(origins2) {
			t.Error("expected same origins from cache")
		}
	})

	t.Run("parses ALLOWED_ORIGINS environment variable", func(t *testing.T) {
		// Reset cache
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false

		os.Setenv("ALLOWED_ORIGINS", "http://localhost:8080,https://example.com")
		defer os.Unsetenv("ALLOWED_ORIGINS")

		origins := getAllowedOrigins()

		if len(origins) != 2 {
			t.Errorf("expected 2 origins, got %d", len(origins))
		}

		found8080 := false
		foundExample := false
		for _, o := range origins {
			if o == "http://localhost:8080" {
				found8080 = true
			}
			if o == "https://example.com" {
				foundExample = true
			}
		}

		if !found8080 || !foundExample {
			t.Error("expected both origins to be parsed")
		}
	})

	t.Run("uses localhost defaults when env not set", func(t *testing.T) {
		// Reset cache
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false
		os.Unsetenv("ALLOWED_ORIGINS")

		origins := getAllowedOrigins()

		if len(origins) == 0 {
			t.Error("expected default localhost origins")
		}

		hasLocalhost := false
		for _, o := range origins {
			if o == "http://localhost:8080" || o == "http://localhost:5173" {
				hasLocalhost = true
				break
			}
		}

		if !hasLocalhost {
			t.Error("expected localhost in default origins")
		}
	})

	t.Run("ignores invalid origins in env", func(t *testing.T) {
		// Reset cache
		allowedOriginsCache = nil
		allowedOriginsCacheInit = false

		os.Setenv("ALLOWED_ORIGINS", "http://valid.com,invalid,http://also-valid.com")
		defer os.Unsetenv("ALLOWED_ORIGINS")

		origins := getAllowedOrigins()

		// Should have 2 valid origins
		if len(origins) != 2 {
			t.Errorf("expected 2 valid origins, got %d: %v", len(origins), origins)
		}
	})
}

func TestIsHTMX(t *testing.T) {
	t.Run("returns true for HTMX request", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("HX-Request", "true")

		if !isHTMX(c) {
			t.Error("expected isHTMX to return true")
		}
	})

	t.Run("returns false for non-HTMX request", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		if isHTMX(c) {
			t.Error("expected isHTMX to return false")
		}
	})

	t.Run("returns false for other HX-Request values", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.Header.Set("HX-Request", "false")

		if isHTMX(c) {
			t.Error("expected isHTMX to return false for 'false' value")
		}
	})
}

// TestClientRateLimiters covers the per-IP bucket isolation from
// #123: one abusive client must not be able to exhaust another
// client's bucket. Runs with a deliberately tight rate (1 rps, 1
// burst) so the test can prove a second IP still gets through
// after the first has been saturated.
func TestClientRateLimiters(t *testing.T) {
	t.Run("different IPs get independent buckets", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		limiters := newClientRateLimiters(ctx, 1.0, 1)

		// Hammer IP A past its burst. First call succeeds, second
		// fails because burst = 1.
		if !limiters.getLimiter("1.2.3.4").Allow() {
			t.Fatal("first request from 1.2.3.4 should pass")
		}
		if limiters.getLimiter("1.2.3.4").Allow() {
			t.Error("second back-to-back request from 1.2.3.4 should be throttled (burst=1)")
		}

		// IP B must still be allowed — its own bucket is fresh.
		if !limiters.getLimiter("5.6.7.8").Allow() {
			t.Error("first request from 5.6.7.8 must pass — bucket must be isolated per IP")
		}
	})

	t.Run("same IP reuses the same limiter", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		limiters := newClientRateLimiters(ctx, 1.0, 1)

		a := limiters.getLimiter("10.0.0.1")
		b := limiters.getLimiter("10.0.0.1")
		if a != b {
			t.Error("same IP must return the same *rate.Limiter — otherwise the burst resets per request")
		}
	})

	t.Run("sweepIdle evicts stale entries", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		limiters := newClientRateLimiters(ctx, 1.0, 1)

		// Seed three IPs.
		limiters.getLimiter("1.1.1.1")
		limiters.getLimiter("2.2.2.2")
		limiters.getLimiter("3.3.3.3")

		// Backdate two of them past the idle cutoff.
		limiters.mu.Lock()
		limiters.entries["1.1.1.1"].lastSeen = time.Now().Add(-ipLimiterIdleEvict - time.Minute)
		limiters.entries["2.2.2.2"].lastSeen = time.Now().Add(-ipLimiterIdleEvict - time.Minute)
		limiters.mu.Unlock()

		removed := limiters.sweepIdle()
		if removed != 2 {
			t.Errorf("want 2 entries evicted, got %d", removed)
		}

		limiters.mu.Lock()
		defer limiters.mu.Unlock()
		if _, ok := limiters.entries["3.3.3.3"]; !ok {
			t.Error("fresh entry must be kept after sweepIdle")
		}
		if _, ok := limiters.entries["1.1.1.1"]; ok {
			t.Error("stale entry 1.1.1.1 must be evicted")
		}
	})

	t.Run("sweepIdle is safe on empty map", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		limiters := newClientRateLimiters(ctx, 1.0, 1)
		// No entries yet; sweepIdle must not panic and must return 0.
		if got := limiters.sweepIdle(); got != 0 {
			t.Errorf("want 0 removed on empty map, got %d", got)
		}
	})

	t.Run("cleanup goroutine exits on context cancel", func(t *testing.T) {
		// Smoke test: the cleanup goroutine should exit when its
		// context is canceled. We can't directly observe the
		// goroutine terminating, but we can at least verify that
		// cancellation doesn't panic and that the function returns.
		ctx, cancel := context.WithCancel(context.Background())
		_ = newClientRateLimiters(ctx, 1.0, 1)
		cancel()
		// Give the goroutine a moment to notice the cancellation.
		time.Sleep(10 * time.Millisecond)
	})
}

// TestRateLimiterPerIP is an integration-style test that exercises
// the RateLimiter middleware end-to-end and verifies the per-IP
// isolation visible from the handler layer.
func TestRateLimiterPerIP(t *testing.T) {
	t.Run("second IP still gets through after first is throttled", func(t *testing.T) {
		mw := RateLimiter(1.0, 1)

		runRequest := func(clientIP string) int {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Request.RemoteAddr = clientIP + ":1234"
			mw(c)
			return w.Code
		}

		// IP A's first request passes.
		if code := runRequest("9.9.9.1"); code != http.StatusOK {
			// Default test context returns 200 when not explicitly set
			// and middleware didn't abort. A 429 means throttled.
			if code == http.StatusTooManyRequests {
				t.Errorf("IP A first request was throttled (want allowed): %d", code)
			}
		}

		// IP A's second back-to-back request is throttled (burst=1).
		if code := runRequest("9.9.9.1"); code != http.StatusTooManyRequests {
			t.Errorf("IP A second request: want 429, got %d", code)
		}

		// IP B — fresh bucket — still allowed.
		if code := runRequest("9.9.9.2"); code == http.StatusTooManyRequests {
			t.Errorf("IP B first request was throttled even though its bucket is fresh — per-IP isolation broken (got 429)")
		}
	})
}
