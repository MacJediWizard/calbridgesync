package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/config"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

// fakeGoogle stands in for Google's token and userinfo endpoints so the
// OAuth callback can be exercised end to end without network access.
type fakeGoogle struct {
	refreshToken string // returned by the code exchange ("" = omit)
	email        string // returned by userinfo
}

func (f *fakeGoogle) install(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			body := map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 3600}
			if f.refreshToken != "" {
				body["refresh_token"] = f.refreshToken
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": f.email, "verified_email": true})
		default:
			http.NotFound(w, r)
		}
	}))
	oldEndpoint, oldUserinfo := googleOAuthEndpoint, googleUserinfoURL
	googleOAuthEndpoint = oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	googleUserinfoURL = srv.URL + "/userinfo"
	t.Cleanup(func() {
		googleOAuthEndpoint, googleUserinfoURL = oldEndpoint, oldUserinfo
		srv.Close()
	})
}

// setupGoogleOAuthHandlers extends setupTestHandlers with the pieces the
// Google OAuth handlers need: config, session manager and encryptor.
func setupGoogleOAuthHandlers(t *testing.T) *testHandlers {
	t.Helper()
	th := setupTestHandlers(t)
	t.Cleanup(th.cleanup)

	enc, err := crypto.NewEncryptor([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	th.handlers.encryptor = enc
	th.handlers.session = auth.NewSessionManager(strings.Repeat("s", 32), false, 3600, 600)
	th.handlers.cfg = &config.Config{
		GoogleOAuth: config.GoogleOAuthConfig{RedirectURL: "https://calbridge.test/auth/oauth/google/callback"},
	}
	return th
}

// createGoogleSource inserts a Google source for a new user and returns
// the user ID and the source.
func createGoogleSource(t *testing.T, th *testHandlers, userEmail, googleEmail string) (string, *db.Source) {
	t.Helper()
	user, err := th.db.GetOrCreateUser(userEmail, "Test User")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	encSecret, _ := th.handlers.encryptor.Encrypt("client-secret")
	encRefresh, _ := th.handlers.encryptor.Encrypt("old-refresh")
	src := &db.Source{
		UserID:             user.ID,
		Name:               "Google",
		SourceType:         db.SourceTypeGoogle,
		SourceURL:          googleCalDAVSourceURL(googleEmail),
		SourceUsername:     googleEmail,
		OAuthRefreshToken:  encRefresh,
		GoogleClientID:     "client-id.apps.googleusercontent.com",
		GoogleClientSecret: encSecret,
		DestURL:            "https://dest.test/caldav",
		DestUsername:       "dest",
		DestPassword:       "enc-dest",
		SyncInterval:       3600,
		SyncDaysPast:       30,
		SyncDirection:      db.SyncDirectionTwoWay,
		ConflictStrategy:   db.ConflictSourceWins,
		Enabled:            true,
	}
	if err := th.db.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	return user.ID, src
}

// startReconnect calls the reconnect endpoint as userID and returns the
// recorder (for status/body/cookies).
func startReconnect(th *testHandlers, userID, sourceID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/sources/"+sourceID+"/google/reconnect", nil)
	c.Params = gin.Params{{Key: "id", Value: sourceID}}
	setAuthContext(c, userID, "user@example.com")
	th.handlers.APIReconnectGoogleSource(c)
	return w
}

// runCallback replays the Google redirect back to the callback with the
// cookies the reconnect call set, authenticated as userID.
func runCallback(t *testing.T, th *testHandlers, start *httptest.ResponseRecorder, userID string) string {
	t.Helper()
	var resp APIPrepareGoogleSourceResponse
	if err := json.Unmarshal(start.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode reconnect response: %v", err)
	}
	u, err := url.Parse(resp.RedirectURL)
	if err != nil {
		t.Fatalf("parse redirect url: %v", err)
	}
	state := u.Query().Get("state")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/auth/oauth/google/callback?code=the-code&state="+url.QueryEscape(state), nil)
	for _, ck := range start.Result().Cookies() {
		req.AddCookie(ck)
	}
	c.Request = req
	setAuthContext(c, userID, "user@example.com")
	th.handlers.GoogleOAuthCallback(c)
	if w.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", w.Code)
	}
	return w.Header().Get("Location")
}

func storedRefreshToken(t *testing.T, th *testHandlers, sourceID string) string {
	t.Helper()
	src, err := th.db.GetSourceByID(sourceID)
	if err != nil {
		t.Fatalf("GetSourceByID: %v", err)
	}
	plain, err := th.handlers.encryptor.Decrypt(src.OAuthRefreshToken)
	if err != nil {
		t.Fatalf("stored refresh token is not decryptable (not encrypted at rest?): %v", err)
	}
	return plain
}

// TestAPIReconnectGoogleSource_ReturnsOfflineConsentURL verifies the
// reconnect endpoint builds a consent URL that always yields a refresh
// token (access_type=offline + prompt=consent) using the source's own
// stored client ID.
func TestAPIReconnectGoogleSource_ReturnsOfflineConsentURL(t *testing.T) {
	th := setupGoogleOAuthHandlers(t)
	userID, src := createGoogleSource(t, th, "owner@example.com", "owner@gmail.com")

	w := startReconnect(th, userID, src.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp APIPrepareGoogleSourceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(resp.RedirectURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Errorf("redirect URL must request offline access with prompt=consent, got %s", resp.RedirectURL)
	}
	if q.Get("client_id") != src.GoogleClientID {
		t.Errorf("client_id = %q, want the source's stored client id", q.Get("client_id"))
	}
	if q.Get("state") == "" {
		t.Error("redirect URL has no state")
	}
}

// TestAPIReconnectGoogleSource_Rejections covers ownership and type
// checks: another user's source is a 404 and a non-Google source is a
// 400, with no OAuth flow started.
func TestAPIReconnectGoogleSource_Rejections(t *testing.T) {
	th := setupGoogleOAuthHandlers(t)
	_, src := createGoogleSource(t, th, "owner@example.com", "owner@gmail.com")
	otherID, customSrc := createTestUserAndSource(t, th.db, "other@example.com", "CalDAV")

	if w := startReconnect(th, otherID, src.ID); w.Code != http.StatusNotFound {
		t.Errorf("other user's source: status = %d, want 404", w.Code)
	}
	if w := startReconnect(th, otherID, customSrc.ID); w.Code != http.StatusBadRequest {
		t.Errorf("non-google source: status = %d, want 400", w.Code)
	}
}

// TestGoogleOAuthCallback_ReconnectReplacesRefreshToken is the end to
// end reconnect: the callback replaces only the encrypted refresh token
// on the same source row and redirects back to the edit page.
func TestGoogleOAuthCallback_ReconnectReplacesRefreshToken(t *testing.T) {
	th := setupGoogleOAuthHandlers(t)
	(&fakeGoogle{refreshToken: "new-refresh", email: "owner@gmail.com"}).install(t)
	userID, src := createGoogleSource(t, th, "owner@example.com", "owner@gmail.com")
	if err := th.db.UpdateSourceSyncStatus(src.ID, db.SyncStatusError, caldav.GoogleAuthExpiredMessage); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	loc := runCallback(t, th, startReconnect(th, userID, src.ID), userID)

	if want := "/sources/" + src.ID + "/edit?google_oauth=reconnected"; loc != want {
		t.Errorf("redirect = %q, want %q", loc, want)
	}
	if got := storedRefreshToken(t, th, src.ID); got != "new-refresh" {
		t.Errorf("refresh token = %q, want new-refresh", got)
	}
	after, _ := th.db.GetSourceByID(src.ID)
	if after.OAuthRefreshToken == "new-refresh" {
		t.Error("refresh token stored in plaintext")
	}
	if after.SyncDirection != db.SyncDirectionTwoWay || after.DestURL != src.DestURL || after.UserID != userID {
		t.Errorf("reconnect changed unrelated settings: %+v", after)
	}
	if after.LastSyncMessage == caldav.GoogleAuthExpiredMessage {
		t.Error("expired-authorization status should be cleared after reconnect")
	}
	sources, _ := th.db.GetSourcesByUserID(userID)
	if len(sources) != 1 {
		t.Errorf("reconnect must not create a new source; user has %d", len(sources))
	}
}

// TestGoogleOAuthCallback_ReconnectRejectsDifferentAccount guards
// against pointing an existing source at a different Google account's
// calendar (which on a two-way source could delete events): the token
// is not replaced.
func TestGoogleOAuthCallback_ReconnectRejectsDifferentAccount(t *testing.T) {
	th := setupGoogleOAuthHandlers(t)
	(&fakeGoogle{refreshToken: "new-refresh", email: "someone-else@gmail.com"}).install(t)
	userID, src := createGoogleSource(t, th, "owner@example.com", "owner@gmail.com")

	loc := runCallback(t, th, startReconnect(th, userID, src.ID), userID)

	if !strings.Contains(loc, "/sources/"+src.ID+"/edit?error=google_account_mismatch") {
		t.Errorf("redirect = %q, want google_account_mismatch on the edit page", loc)
	}
	if got := storedRefreshToken(t, th, src.ID); got != "old-refresh" {
		t.Errorf("refresh token changed to %q on account mismatch", got)
	}
}

// TestGoogleOAuthCallback_ReconnectRequiresRefreshToken verifies a
// consent response without a refresh token is an error, not a silent
// success that leaves the dead token in place.
func TestGoogleOAuthCallback_ReconnectRequiresRefreshToken(t *testing.T) {
	th := setupGoogleOAuthHandlers(t)
	(&fakeGoogle{refreshToken: "", email: "owner@gmail.com"}).install(t)
	userID, src := createGoogleSource(t, th, "owner@example.com", "owner@gmail.com")

	loc := runCallback(t, th, startReconnect(th, userID, src.ID), userID)

	if !strings.Contains(loc, "/sources/"+src.ID+"/edit?error=no_refresh_token") {
		t.Errorf("redirect = %q, want no_refresh_token on the edit page", loc)
	}
	if got := storedRefreshToken(t, th, src.ID); got != "old-refresh" {
		t.Errorf("refresh token changed to %q without a new one", got)
	}
}

// TestGoogleOAuthCallback_ReconnectBoundToUser verifies the callback
// re-checks ownership: if a different signed-in user completes a flow
// started for someone else's source, nothing is written.
func TestGoogleOAuthCallback_ReconnectBoundToUser(t *testing.T) {
	th := setupGoogleOAuthHandlers(t)
	(&fakeGoogle{refreshToken: "new-refresh", email: "owner@gmail.com"}).install(t)
	ownerID, src := createGoogleSource(t, th, "owner@example.com", "owner@gmail.com")
	other, err := th.db.GetOrCreateUser("other@example.com", "Other")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}

	loc := runCallback(t, th, startReconnect(th, ownerID, src.ID), other.ID)

	if !strings.Contains(loc, "error=") {
		t.Errorf("redirect = %q, want an error", loc)
	}
	if got := storedRefreshToken(t, th, src.ID); got != "old-refresh" {
		t.Errorf("another user's callback replaced the token with %q", got)
	}
}

// TestSourceToAPI_NeedsReauth verifies the API exposes the last sync
// message and flags Google sources whose authorization expired, which
// drives the SPA's reconnect prompt.
func TestSourceToAPI_NeedsReauth(t *testing.T) {
	expired := &db.Source{SourceType: db.SourceTypeGoogle, LastSyncStatus: db.SyncStatusError, LastSyncMessage: caldav.GoogleAuthExpiredMessage}
	api := sourceToAPI(expired)
	if !api.NeedsReauth || api.LastSyncMessage != caldav.GoogleAuthExpiredMessage {
		t.Errorf("expired google source: needs_reauth=%v message=%q", api.NeedsReauth, api.LastSyncMessage)
	}

	otherErr := &db.Source{SourceType: db.SourceTypeGoogle, LastSyncStatus: db.SyncStatusError, LastSyncMessage: "Sync failed with 1 errors"}
	if sourceToAPI(otherErr).NeedsReauth {
		t.Error("a generic error must not ask for re-authorization")
	}
}
