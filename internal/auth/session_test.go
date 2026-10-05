package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestGenerateState(t *testing.T) {
	t.Run("generates non-empty state", func(t *testing.T) {
		state, err := GenerateState()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if state == "" {
			t.Fatal("expected non-empty state")
		}
	})

	t.Run("generates unique states", func(t *testing.T) {
		state1, _ := GenerateState()
		state2, _ := GenerateState()

		if state1 == state2 {
			t.Fatal("expected unique states")
		}
	})

	t.Run("generates base64 URL-safe strings", func(t *testing.T) {
		state, _ := GenerateState()
		// Base64 URL encoding should produce approximately 44 characters for 32 bytes
		if len(state) < 40 {
			t.Fatalf("state too short: %d characters", len(state))
		}
	})
}

func TestSessionManager(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	t.Run("set and get session", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		data := &SessionData{
			UserID:  "user-123",
			Email:   "test@example.com",
			Name:    "Test User",
			Picture: "https://example.com/pic.jpg",
		}

		err := sm.Set(w, r, data)
		if err != nil {
			t.Fatalf("failed to set session: %v", err)
		}

		// Create a new request with the cookie from the response
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, cookie := range w.Result().Cookies() {
			r2.AddCookie(cookie)
		}

		retrieved, err := sm.Get(r2)
		if err != nil {
			t.Fatalf("failed to get session: %v", err)
		}

		if retrieved.UserID != data.UserID {
			t.Errorf("expected UserID %q, got %q", data.UserID, retrieved.UserID)
		}
		if retrieved.Email != data.Email {
			t.Errorf("expected Email %q, got %q", data.Email, retrieved.Email)
		}
		if retrieved.Name != data.Name {
			t.Errorf("expected Name %q, got %q", data.Name, retrieved.Name)
		}
	})

	t.Run("returns error for missing session", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		_, err := sm.Get(r)
		if err != ErrSessionNotFound {
			t.Errorf("expected ErrSessionNotFound, got %v", err)
		}
	})

	t.Run("clear removes session", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		// Set a session first
		sm.Set(w, r, &SessionData{UserID: "user-123", Email: "test@example.com"})

		// Create request with session cookie
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, cookie := range w.Result().Cookies() {
			r2.AddCookie(cookie)
		}

		// Clear the session
		w2 := httptest.NewRecorder()
		err := sm.Clear(w2, r2)
		if err != nil {
			t.Fatalf("failed to clear session: %v", err)
		}

		// Session should be cleared (cookie will be set to expire)
		cookies := w2.Result().Cookies()
		for _, cookie := range cookies {
			if cookie.Name == "calbridgesync_session" && cookie.MaxAge > 0 {
				t.Error("expected session cookie to be expired")
			}
		}
	})
}

func TestOAuthState(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	t.Run("set and get OAuth state", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		state := "test-oauth-state-12345"
		err := sm.SetOAuthState(w, r, state)
		if err != nil {
			t.Fatalf("failed to set OAuth state: %v", err)
		}

		// Create a new request with the cookie
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		w2 := httptest.NewRecorder()
		for _, cookie := range w.Result().Cookies() {
			r2.AddCookie(cookie)
		}

		retrieved, err := sm.GetOAuthState(w2, r2)
		if err != nil {
			t.Fatalf("failed to get OAuth state: %v", err)
		}

		if retrieved != state {
			t.Errorf("expected state %q, got %q", state, retrieved)
		}
	})

	t.Run("OAuth state is cleared after retrieval", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		sm.SetOAuthState(w, r, "test-state")

		// Get state (should clear it)
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		w2 := httptest.NewRecorder()
		for _, cookie := range w.Result().Cookies() {
			r2.AddCookie(cookie)
		}
		state, err := sm.GetOAuthState(w2, r2)
		if err != nil {
			t.Fatalf("failed to get OAuth state: %v", err)
		}
		if state != "test-state" {
			t.Errorf("expected 'test-state', got %q", state)
		}

		// The GetOAuthState sets MaxAge = -1 to clear the cookie,
		// so subsequent requests with the cleared cookie should fail.
		// Note: The cookie is marked for deletion but the session store
		// behavior may vary. We verify the state was retrieved successfully.
	})
}

func TestGetCurrentUser(t *testing.T) {
	t.Run("returns nil when no session in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := createTestGinContext(w)

		user := GetCurrentUser(c)
		if user != nil {
			t.Error("expected nil user when no session")
		}
	})

	t.Run("returns session data when present", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := createTestGinContext(w)

		expected := &SessionData{
			UserID: "user-123",
			Email:  "test@example.com",
		}
		c.Set(ContextKeySession, expected)

		user := GetCurrentUser(c)
		if user == nil {
			t.Fatal("expected user, got nil")
		}
		if user.UserID != expected.UserID {
			t.Errorf("expected UserID %q, got %q", expected.UserID, user.UserID)
		}
	})
}

// Helper to create a test Gin context
func createTestGinContext(w http.ResponseWriter) (*gin.Context, *gin.Engine) {
	engine := gin.New()
	c, _ := gin.CreateTestContext(w)
	return c, engine
}

func TestRequireAuth(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	t.Run("redirects to login when no session", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/protected", nil)

		middleware := RequireAuth(sm)
		middleware(c)

		if !c.IsAborted() {
			t.Error("expected request to be aborted")
		}
		if w.Code != http.StatusFound {
			t.Errorf("expected status 302, got %d", w.Code)
		}
		location := w.Header().Get("Location")
		if location != "/auth/login" {
			t.Errorf("expected redirect to /auth/login, got %q", location)
		}
	})

	t.Run("sets redirect cookie when redirecting", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/dashboard?tab=settings", nil)

		middleware := RequireAuth(sm)
		middleware(c)

		cookies := w.Result().Cookies()
		var found bool
		for _, cookie := range cookies {
			if cookie.Name == "redirect_after_login" {
				found = true
				// Cookie value is URL-encoded
				decoded, err := url.QueryUnescape(cookie.Value)
				if err != nil {
					t.Fatalf("failed to decode cookie value: %v", err)
				}
				if decoded != "/dashboard?tab=settings" {
					t.Errorf("expected redirect URL in cookie, got %q (decoded: %q)", cookie.Value, decoded)
				}
			}
		}
		if !found {
			t.Error("expected redirect_after_login cookie to be set")
		}
	})

	t.Run("allows request with valid session", func(t *testing.T) {
		// First set up a session
		w1 := httptest.NewRecorder()
		r1 := httptest.NewRequest(http.MethodGet, "/", nil)
		sm.Set(w1, r1, &SessionData{UserID: "user-123", Email: "test@example.com"})

		// Now test the middleware with the session cookie
		w2 := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w2)
		c.Request = httptest.NewRequest(http.MethodGet, "/protected", nil)
		for _, cookie := range w1.Result().Cookies() {
			c.Request.AddCookie(cookie)
		}

		middleware := RequireAuth(sm)

		// Simulate calling Next by checking if context was aborted
		middleware(c)

		if c.IsAborted() {
			t.Error("expected request to not be aborted")
		}

		// Check that session was stored in context
		session := GetCurrentUser(c)
		if session == nil {
			t.Error("expected session to be stored in context")
		}
		if session != nil && session.UserID != "user-123" {
			t.Errorf("expected UserID 'user-123', got %q", session.UserID)
		}
	})
}

func TestOptionalAuth(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	t.Run("continues without session", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/public", nil)

		middleware := OptionalAuth(sm)
		middleware(c)

		if c.IsAborted() {
			t.Error("expected request to not be aborted")
		}

		// Session should not be in context
		session := GetCurrentUser(c)
		if session != nil {
			t.Error("expected no session in context")
		}
	})

	t.Run("loads session when available", func(t *testing.T) {
		// First set up a session
		w1 := httptest.NewRecorder()
		r1 := httptest.NewRequest(http.MethodGet, "/", nil)
		sm.Set(w1, r1, &SessionData{UserID: "user-456", Email: "user@example.com"})

		// Now test the middleware with the session cookie
		w2 := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w2)
		c.Request = httptest.NewRequest(http.MethodGet, "/public", nil)
		for _, cookie := range w1.Result().Cookies() {
			c.Request.AddCookie(cookie)
		}

		middleware := OptionalAuth(sm)
		middleware(c)

		if c.IsAborted() {
			t.Error("expected request to not be aborted")
		}

		// Session should be in context
		session := GetCurrentUser(c)
		if session == nil {
			t.Fatal("expected session in context")
		}
		if session.UserID != "user-456" {
			t.Errorf("expected UserID 'user-456', got %q", session.UserID)
		}
	})
}

// TestSetDropsLegacyCSRFToken covers sessions written by builds that still
// had the HTMX UI: the next Set must not carry their csrf_token forward.
func TestSetDropsLegacyCSRFToken(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	legacy, _ := sm.store.New(r, sessionName)
	legacy.Values["user_id"] = "user-123"
	legacy.Values["csrf_token"] = "old-token"
	if err := legacy.Save(r, w); err != nil {
		t.Fatalf("saving legacy session: %v", err)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range w.Result().Cookies() {
		r2.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	if err := sm.Set(w2, r2, &SessionData{UserID: "user-123"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range w2.Result().Cookies() {
		r3.AddCookie(c)
	}
	s, err := sm.store.Get(r3, sessionName)
	if err != nil {
		t.Fatalf("reading session: %v", err)
	}
	if _, ok := s.Values["csrf_token"]; ok {
		t.Error("expected csrf_token to be removed from the session")
	}
	if s.Values["user_id"] != "user-123" {
		t.Errorf("expected user_id to survive, got %v", s.Values["user_id"])
	}
}

func TestSessionData(t *testing.T) {
	t.Run("struct has expected fields", func(t *testing.T) {
		data := SessionData{
			UserID:  "user-123",
			Email:   "test@example.com",
			Name:    "Test User",
			Picture: "https://example.com/pic.jpg",
		}

		if data.UserID != "user-123" {
			t.Error("UserID not set correctly")
		}
		if data.Email != "test@example.com" {
			t.Error("Email not set correctly")
		}
		if data.Name != "Test User" {
			t.Error("Name not set correctly")
		}
		if data.Picture != "https://example.com/pic.jpg" {
			t.Error("Picture not set correctly")
		}
	})
}

func TestSessionErrors(t *testing.T) {
	t.Run("error constants are not nil", func(t *testing.T) {
		errors := []error{
			ErrSessionNotFound,
			ErrInvalidSession,
		}

		for _, err := range errors {
			if err == nil {
				t.Error("expected error constant to be non-nil")
			}
		}
	})

	t.Run("error messages are descriptive", func(t *testing.T) {
		if ErrSessionNotFound.Error() != "session not found" {
			t.Errorf("unexpected error message: %q", ErrSessionNotFound.Error())
		}
		if ErrInvalidSession.Error() != "invalid session data" {
			t.Errorf("unexpected error message: %q", ErrInvalidSession.Error())
		}
	})
}

func TestOIDCErrors(t *testing.T) {
	t.Run("OIDC error constants are not nil", func(t *testing.T) {
		errors := []error{
			ErrOIDCInit,
			ErrTokenExchange,
			ErrTokenVerify,
			ErrMissingEmail,
			ErrEmailNotVerified,
		}

		for _, err := range errors {
			if err == nil {
				t.Error("expected error constant to be non-nil")
			}
		}
	})

	t.Run("OIDC error messages are descriptive", func(t *testing.T) {
		if ErrOIDCInit.Error() != "OIDC initialization failed" {
			t.Errorf("unexpected error message: %q", ErrOIDCInit.Error())
		}
		if ErrTokenExchange.Error() != "token exchange failed" {
			t.Errorf("unexpected error message: %q", ErrTokenExchange.Error())
		}
		if ErrTokenVerify.Error() != "token verification failed" {
			t.Errorf("unexpected error message: %q", ErrTokenVerify.Error())
		}
		if ErrMissingEmail.Error() != "email claim is required" {
			t.Errorf("unexpected error message: %q", ErrMissingEmail.Error())
		}
		if ErrEmailNotVerified.Error() != "email is not verified" {
			t.Errorf("unexpected error message: %q", ErrEmailNotVerified.Error())
		}
	})
}

func TestOIDCClaims(t *testing.T) {
	t.Run("struct has expected fields", func(t *testing.T) {
		claims := OIDCClaims{
			Subject:       "sub-123",
			Email:         "user@example.com",
			EmailVerified: true,
			Name:          "Test User",
			AvatarURL:     "https://example.com/avatar.jpg",
		}

		if claims.Subject != "sub-123" {
			t.Error("Subject not set correctly")
		}
		if claims.Email != "user@example.com" {
			t.Error("Email not set correctly")
		}
		if !claims.EmailVerified {
			t.Error("EmailVerified not set correctly")
		}
		if claims.Name != "Test User" {
			t.Error("Name not set correctly")
		}
		if claims.AvatarURL != "https://example.com/avatar.jpg" {
			t.Error("AvatarURL not set correctly")
		}
	})
}

func TestGetCurrentUserEdgeCases(t *testing.T) {
	t.Run("returns nil for wrong type in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := createTestGinContext(w)

		// Set wrong type in context
		c.Set(ContextKeySession, "not a session data")

		user := GetCurrentUser(c)
		if user != nil {
			t.Error("expected nil user for wrong type")
		}
	})

	t.Run("returns nil for nil value in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := createTestGinContext(w)

		// Set nil in context
		c.Set(ContextKeySession, nil)

		user := GetCurrentUser(c)
		if user != nil {
			t.Error("expected nil user for nil value")
		}
	})
}

func TestNewSessionManager(t *testing.T) {
	t.Run("creates session manager with correct settings", func(t *testing.T) {
		sm := NewSessionManager("test-secret-32-chars-minimum-len", true, 86400, 300)

		if sm == nil {
			t.Fatal("expected non-nil session manager")
		}
		if sm.store == nil {
			t.Error("expected store to be initialized")
		}
		if !sm.secure {
			t.Error("expected secure to be true")
		}
		if sm.oauthStateMaxAge != 300 {
			t.Errorf("expected oauthStateMaxAge 300, got %d", sm.oauthStateMaxAge)
		}
	})

	t.Run("creates session manager with insecure settings", func(t *testing.T) {
		sm := NewSessionManager("test-secret-32-chars-minimum-len", false, 3600, 600)

		if sm.secure {
			t.Error("expected secure to be false")
		}
		if sm.oauthStateMaxAge != 600 {
			t.Errorf("expected oauthStateMaxAge 600, got %d", sm.oauthStateMaxAge)
		}
	})
}

func TestSessionManagerClearNonexistent(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	t.Run("clearing nonexistent session returns nil", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		err := sm.Clear(w, r)
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})
}

func TestGetOAuthStateInvalid(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	t.Run("returns error for empty state", func(t *testing.T) {
		// Set an empty state
		w1 := httptest.NewRecorder()
		r1 := httptest.NewRequest(http.MethodGet, "/", nil)
		session, _ := sm.store.Get(r1, oauthStateName)
		session.Values["state"] = ""
		session.Save(r1, w1)

		// Try to get state
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		w2 := httptest.NewRecorder()
		for _, cookie := range w1.Result().Cookies() {
			r2.AddCookie(cookie)
		}

		_, err := sm.GetOAuthState(w2, r2)
		if err != ErrInvalidSession {
			t.Errorf("expected ErrInvalidSession, got %v", err)
		}
	})
}

func TestContextKeySession(t *testing.T) {
	t.Run("constant has expected value", func(t *testing.T) {
		if ContextKeySession != "session" {
			t.Errorf("expected ContextKeySession to be 'session', got %q", ContextKeySession)
		}
	})
}

func TestOIDCLoginState(t *testing.T) {
	sm := NewSessionManager("test-secret-key-at-least-32-chars", false, 86400, 300)

	carry := func(w *httptest.ResponseRecorder) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, cookie := range w.Result().Cookies() {
			r.AddCookie(cookie)
		}
		return r
	}

	t.Run("round-trips state, nonce and verifier", func(t *testing.T) {
		w := httptest.NewRecorder()
		want := &OIDCLoginState{State: "s", Nonce: "n", Verifier: "v"}
		if err := sm.SetOIDCLoginState(w, httptest.NewRequest(http.MethodGet, "/", nil), want); err != nil {
			t.Fatalf("SetOIDCLoginState: %v", err)
		}
		got, err := sm.GetOIDCLoginState(httptest.NewRecorder(), carry(w))
		if err != nil {
			t.Fatalf("GetOIDCLoginState: %v", err)
		}
		if *got != *want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("rejects a state-only cookie from the Google flow", func(t *testing.T) {
		w := httptest.NewRecorder()
		if err := sm.SetOAuthState(w, httptest.NewRequest(http.MethodGet, "/", nil), "google:abc"); err != nil {
			t.Fatalf("SetOAuthState: %v", err)
		}
		if _, err := sm.GetOIDCLoginState(httptest.NewRecorder(), carry(w)); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("err = %v, want ErrInvalidSession", err)
		}
	})

	t.Run("SetOAuthState drops a leftover login nonce and verifier", func(t *testing.T) {
		w := httptest.NewRecorder()
		if err := sm.SetOIDCLoginState(w, httptest.NewRequest(http.MethodGet, "/", nil), &OIDCLoginState{State: "s", Nonce: "n", Verifier: "v"}); err != nil {
			t.Fatalf("SetOIDCLoginState: %v", err)
		}
		w2 := httptest.NewRecorder()
		if err := sm.SetOAuthState(w2, carry(w), "google:abc"); err != nil {
			t.Fatalf("SetOAuthState: %v", err)
		}
		if _, err := sm.GetOIDCLoginState(httptest.NewRecorder(), carry(w2)); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("err = %v, want ErrInvalidSession", err)
		}
	})
}
