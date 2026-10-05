package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRequireAuthAPIReturns401 verifies that unauthenticated API requests
// get a 401 JSON response instead of a 302 to the login page, and that no
// redirect_after_login cookie is set for them (#238). Browser navigations
// keep the redirect.
func TestRequireAuthAPIReturns401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	for _, path := range []string{"/api/sources", "/api", "/api/sources/abc/sync?x=1"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)

			RequireAuth(sm)(c)

			if !c.IsAborted() {
				t.Error("expected request to be aborted")
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected status 401, got %d", w.Code)
			}
			if loc := w.Header().Get("Location"); loc != "" {
				t.Errorf("expected no Location header, got %q", loc)
			}
			if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
				t.Errorf("expected no Set-Cookie, got %v", sc)
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("expected JSON body, got %q: %v", w.Body.String(), err)
			}
			if body["error"] == "" {
				t.Errorf("expected error field in body, got %v", body)
			}
		})
	}

	// Paths that merely share the prefix are not API routes.
	for _, path := range []string{"/", "/apiary", "/sources"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)

			RequireAuth(sm)(c)

			if w.Code != http.StatusFound {
				t.Fatalf("expected status 302, got %d", w.Code)
			}
			if loc := w.Header().Get("Location"); loc != "/auth/login" {
				t.Errorf("expected redirect to /auth/login, got %q", loc)
			}
			var found bool
			for _, ck := range w.Result().Cookies() {
				if ck.Name == "redirect_after_login" {
					found = true
				}
			}
			if !found {
				t.Error("expected redirect_after_login cookie to be set")
			}
		})
	}
}
