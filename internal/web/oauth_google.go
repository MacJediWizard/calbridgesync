package web

// OAuth2 flow for Google Calendar source_type. (#70)
//
// Flow:
//  1. User picks "Google" in the add-source form (React SPA).
//  2. React POSTs the form (sans source credentials) to
//     POST /api/sources/google/prepare, which validates the form,
//     encrypts the dest password, stashes it in a short-lived
//     session cookie, and returns {redirect_url}.
//  3. React does window.location.href = redirect_url, landing on
//     GET /auth/oauth/google/start.
//  4. /start generates an OAuth state, stores it in the OAuth state
//     cookie, and redirects to Google's consent screen with the
//     calendar + userinfo.email scopes and access_type=offline so
//     Google returns a refresh_token.
//  5. Google redirects back to GET /auth/oauth/google/callback with
//     code + state.
//  6. /callback validates the state, exchanges the code for a token,
//     fetches the user's primary Google email, reads the pending
//     form from the session cookie, encrypts the refresh token,
//     creates the real Source row (SourceURL is built from the
//     email), and redirects to /sources.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

// googleOAuthStatePrefix namespaces the OAuth state cookie so a Google
// OAuth state can never be confused with an OIDC login state, even
// though they share the same underlying cookie.
const googleOAuthStatePrefix = "google:"

// googleUserinfoURL is the Google API endpoint used to fetch the
// authenticated user's primary email after OAuth consent. Scoped by
// "https://www.googleapis.com/auth/userinfo.email".
const googleUserinfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

// googleUserinfo is the subset of Google's /userinfo response that
// we care about.
type googleUserinfo struct {
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email"`
	Name          string `json:"name"`
}

// buildGoogleOAuthConfig assembles an oauth2.Config from per-request
// credentials plus the instance-level redirect URL. As of #79 the
// client ID and client secret are NOT stored in the global config —
// each user provides their own when adding a Google source — so the
// helper takes them as explicit parameters and the caller is
// responsible for sourcing them (from the form during prepare, from
// the pending session cookie during callback, or from the source row
// during sync).
//
// Returns nil if the redirect URL is unset (the feature is fully
// disabled on this instance) or if either credential is empty.
func (h *Handlers) buildGoogleOAuthConfig(clientID, clientSecret string) *oauth2.Config {
	if !h.cfg.GoogleOAuth.Enabled() || clientID == "" || clientSecret == "" {
		return nil
	}
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  h.cfg.GoogleOAuth.RedirectURL,
		Endpoint:     google.Endpoint,
		Scopes: []string{
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/userinfo.email",
		},
	}
}

// APIPrepareGoogleSourceRequest is the payload the React SPA sends to
// kick off a Google OAuth source. It contains only the fields the user
// sets in the form — source_url/username/password are intentionally
// omitted because they're filled in after OAuth completes. As of #79
// the user must also provide their own google_client_id and
// google_client_secret from their personal Google Cloud project.
type APIPrepareGoogleSourceRequest struct {
	Name               string `json:"name"`
	SyncInterval       int    `json:"sync_interval"`
	SyncDaysPast       int    `json:"sync_days_past"`
	SyncDirection      string `json:"sync_direction"`
	ConflictStrategy   string `json:"conflict_strategy"`
	DestURL            string `json:"dest_url"`
	DestUsername       string `json:"dest_username"`
	DestPassword       string `json:"dest_password"`
	GoogleClientID     string `json:"google_client_id"`
	GoogleClientSecret string `json:"google_client_secret"`
}

// APIPrepareGoogleSourceResponse tells the SPA where to send the user
// next (Google's consent page, via our /start handler).
type APIPrepareGoogleSourceResponse struct {
	RedirectURL string `json:"redirect_url"`
}

// APIPrepareGoogleSource is called by the React SPA when the user
// submits the add-source form with source_type=google. It:
//
//  1. Verifies Google OAuth is configured on this instance.
//  2. Validates the destination fields (source fields are not
//     required — those come from Google).
//  3. Tests the destination connection so we fail fast if SOGo
//     credentials are wrong.
//  4. Encrypts the dest password.
//  5. Generates an OAuth state and stashes it plus the form data in
//     a short-lived session cookie.
//  6. Returns the Google consent URL for the SPA to navigate to.
func (h *Handlers) APIPrepareGoogleSource(c *gin.Context) {
	session := auth.GetCurrentUser(c)
	if session == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if !h.cfg.GoogleOAuth.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Google OAuth is not configured on this server (BASE_URL / GOOGLE_OAUTH_REDIRECT_URL must be set).",
		})
		return
	}

	var req APIPrepareGoogleSourceRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Validate required destination fields. Source fields are filled
	// in from the OAuth response, not from the form.
	if req.Name == "" || req.DestURL == "" || req.DestUsername == "" || req.DestPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Name and destination URL/username/password are required",
		})
		return
	}

	// Per-source Google OAuth credentials are required as of #79.
	// Each user provides their own Google Cloud project credentials.
	if req.GoogleClientID == "" || req.GoogleClientSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Google OAuth client ID and client secret are required (from your Google Cloud project)",
		})
		return
	}

	// Build the per-request oauth2.Config from the form credentials.
	cfg := h.buildGoogleOAuthConfig(req.GoogleClientID, req.GoogleClientSecret)
	if cfg == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid Google OAuth credentials in request",
		})
		return
	}

	// Reuse the standard input validator. Source username/URL are
	// passed as empty because they aren't filled in yet.
	if validationErr := validateSourceInput(
		req.Name, string(db.SourceTypeGoogle), req.SyncDirection, req.ConflictStrategy,
		"", req.DestURL, "", req.DestUsername,
	); validationErr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": validationErr})
		return
	}

	if len(req.DestPassword) > maxPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Destination password is too long"})
		return
	}

	// Test destination before sending the user off to Google — if
	// SOGo credentials are wrong, we want to catch it now, not
	// after they've clicked through consent.
	if err := h.syncEngine.TestConnection(c.Request.Context(), req.DestURL, req.DestUsername, req.DestPassword); err != nil {
		log.Printf("Google source prepare: destination connection test failed for %s: %v", req.DestURL, err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Failed to connect to destination: " + categorizeConnectionError(err),
		})
		return
	}

	encDestPwd, err := h.encryptor.Encrypt(req.DestPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt credentials"})
		return
	}

	// Encrypt the per-source Google client secret before stashing it
	// in the session cookie. The cookie itself is signed (gorilla
	// sessions) but we still encrypt at the application layer so a
	// stolen cookie does not expose the plaintext secret. (#79)
	encGoogleClientSecret, err := h.encryptor.Encrypt(req.GoogleClientSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt Google client secret"})
		return
	}

	state, err := auth.GenerateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate state"})
		return
	}

	// Stash state + form data in separate cookies. We keep them
	// separate so a stale pending-source cookie from a previous flow
	// can't interfere with the OAuth state CSRF check.
	if err := h.session.SetOAuthState(c.Writer, c.Request, googleOAuthStatePrefix+state); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save OAuth state"})
		return
	}

	pending := &auth.PendingGoogleSource{
		State:                 state,
		Name:                  req.Name,
		SyncInterval:          req.SyncInterval,
		SyncDaysPast:          req.SyncDaysPast,
		SyncDirection:         req.SyncDirection,
		ConflictStrategy:      req.ConflictStrategy,
		DestURL:               req.DestURL,
		DestUsername:          req.DestUsername,
		DestPasswordEnc:       encDestPwd,
		GoogleClientID:        req.GoogleClientID,
		GoogleClientSecretEnc: encGoogleClientSecret,
	}
	if err := h.session.SetPendingGoogleSource(c.Writer, c.Request, pending); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save pending source"})
		return
	}

	// access_type=offline tells Google to return a refresh_token.
	// prompt=consent forces the consent screen to appear on every
	// re-authorization, which also forces a fresh refresh_token.
	// Without prompt=consent, Google may return no refresh_token on
	// re-authorization, which breaks the sync engine.
	redirectURL := cfg.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	c.JSON(http.StatusOK, APIPrepareGoogleSourceResponse{RedirectURL: redirectURL})
}

// GoogleOAuthStart is a convenience redirect that exists so operators
// can kick off the flow from the URL bar for debugging. In the
// normal flow, the SPA navigates directly to Google using the URL
// returned by APIPrepareGoogleSource, but this handler exists as a
// second entry point for the consent screen.
//
// It REQUIRES that a pending source already exist in the session —
// if the user navigates here directly without going through the
// prepare endpoint first, they'll be bounced back to /sources/add
// with an error.
func (h *Handlers) GoogleOAuthStart(c *gin.Context) {
	if !h.cfg.GoogleOAuth.Enabled() {
		c.Redirect(http.StatusFound, "/sources/add?error=google_not_configured")
		return
	}

	// We can't easily peek at the pending cookie without clearing
	// it, so the start endpoint just redirects to /sources/add with
	// an error if there's no valid prior prepare call. The normal
	// path is the SPA calling the AuthCodeURL returned by prepare,
	// which skips this handler entirely.
	c.Redirect(http.StatusFound, "/sources/add?error=start_via_prepare")
}

// GoogleOAuthCallback handles the redirect from Google after the user
// approves (or denies) the consent request. It validates state,
// exchanges the code for tokens, fetches the user's primary email,
// reads the pending form data from the session, creates the real
// Source row, and redirects back to /sources.
func (h *Handlers) GoogleOAuthCallback(c *gin.Context) {
	if !h.cfg.GoogleOAuth.Enabled() {
		c.Redirect(http.StatusFound, "/sources/add?error=google_not_configured")
		return
	}

	// Validate state BEFORE anything else — this is the CSRF check.
	// GetOAuthState atomically reads and clears the state cookie.
	// Compared constant-time because queryState is attacker-
	// controllable (comes from the Google redirect query string)
	// and savedState is secret (our server-generated random value
	// stashed in the session cookie). Same rationale as the CSRF
	// token comparison fix in #111. (#113)
	queryState := c.Query("state")
	savedState, err := h.session.GetOAuthState(c.Writer, c.Request)
	expectedState := googleOAuthStatePrefix + queryState
	if err != nil || savedState == "" || subtle.ConstantTimeCompare([]byte(savedState), []byte(expectedState)) != 1 {
		log.Printf("Google OAuth callback: state mismatch (saved=%q query=%q err=%v)", savedState, queryState, err)
		c.Redirect(http.StatusFound, "/sources/add?error=invalid_state")
		return
	}

	// Google can report its own error (user denied consent, etc.)
	if errParam := c.Query("error"); errParam != "" {
		log.Printf("Google OAuth callback: Google reported error: %s", errParam)
		// Clear any stashed pending source so we don't leave it
		// lying around to interfere with a retry.
		_, _ = h.session.GetPendingGoogleSource(c.Writer, c.Request)
		c.Redirect(http.StatusFound, "/sources/add?error=google_denied")
		return
	}

	code := c.Query("code")
	if code == "" {
		_, _ = h.session.GetPendingGoogleSource(c.Writer, c.Request)
		c.Redirect(http.StatusFound, "/sources/add?error=missing_code")
		return
	}

	// Read back the pending form data BEFORE exchanging the code, so
	// we have the per-source client_id and client_secret to build the
	// oauth2.Config that the exchange call needs. GetPendingGoogleSource
	// also clears the cookie (consume-once semantics).
	pending, err := h.session.GetPendingGoogleSource(c.Writer, c.Request)
	if err != nil {
		log.Printf("Google OAuth callback: no pending source in session: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=pending_expired")
		return
	}

	// Decrypt the client secret just long enough to build the
	// oauth2.Config. We re-encrypt it later for storage on the
	// Source row using h.encryptor.Encrypt.
	googleClientSecret, err := h.encryptor.Decrypt(pending.GoogleClientSecretEnc)
	if err != nil {
		log.Printf("Google OAuth callback: failed to decrypt pending Google client secret: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=encrypt_failed")
		return
	}
	cfg := h.buildGoogleOAuthConfig(pending.GoogleClientID, googleClientSecret)
	if cfg == nil {
		log.Printf("Google OAuth callback: pending source had invalid Google credentials")
		c.Redirect(http.StatusFound, "/sources/add?error=google_not_configured")
		return
	}

	// Exchange the authorization code for an access + refresh token.
	// This hits https://oauth2.googleapis.com/token with the client
	// secret; it MUST happen server-side, never in the browser.
	token, err := cfg.Exchange(c.Request.Context(), code)
	if err != nil {
		log.Printf("Google OAuth callback: code exchange failed: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=exchange_failed")
		return
	}

	if token.RefreshToken == "" {
		// If a user re-authorizes an existing client, Google may
		// omit the refresh_token. prompt=consent on the auth URL
		// should force a new one, but if it's still missing we
		// can't proceed — a source without a refresh token can't
		// sync past the first access token expiry.
		log.Printf("Google OAuth callback: Google did not return a refresh token")
		c.Redirect(http.StatusFound, "/sources/add?error=no_refresh_token")
		return
	}

	// Fetch the user's primary email. We need it to build the
	// per-calendar CalDAV URL because Google's CalDAV endpoints are
	// keyed by email, not by a discovery principal.
	email, err := fetchGoogleUserEmail(c.Request.Context(), cfg, token)
	if err != nil {
		log.Printf("Google OAuth callback: failed to fetch user email: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=userinfo_failed")
		return
	}

	// Cross-check: the state in the pending source cookie must
	// match the state we just validated. This is belt-and-braces on
	// top of the state cookie CSRF check. Constant-time comparison
	// for the same reason as above — queryState is attacker-
	// controllable, pending.State is server-side secret. (#113)
	if subtle.ConstantTimeCompare([]byte(pending.State), []byte(queryState)) != 1 {
		log.Printf("Google OAuth callback: pending state mismatch (pending=%q query=%q)", pending.State, queryState)
		c.Redirect(http.StatusFound, "/sources/add?error=state_mismatch")
		return
	}

	session := auth.GetCurrentUser(c)
	if session == nil {
		log.Printf("Google OAuth callback: no authenticated session; redirecting to /auth/login")
		c.Redirect(http.StatusFound, "/auth/login?error=session_lost")
		return
	}

	// Build the Google CalDAV URL for the user's primary calendar.
	// Google's documented format is:
	//   https://apidata.googleusercontent.com/caldav/v2/<email>/user
	// for principal discovery. The sync engine's FindCurrentUserPrincipal
	// call is happy with this as the base URL.
	sourceURL := fmt.Sprintf("https://apidata.googleusercontent.com/caldav/v2/%s/user", email)

	encRefreshToken, err := h.encryptor.Encrypt(token.RefreshToken)
	if err != nil {
		log.Printf("Google OAuth callback: failed to encrypt refresh token: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=encrypt_failed")
		return
	}

	// Defaults for the sync interval + days past — same clamping as
	// APICreateSource.
	syncInterval := pending.SyncInterval
	if syncInterval < h.cfg.Sync.MinInterval || syncInterval > h.cfg.Sync.MaxInterval {
		syncInterval = h.cfg.Sync.MinInterval
	}
	syncDaysPast := pending.SyncDaysPast
	if syncDaysPast <= 0 {
		syncDaysPast = 30
	}
	syncDirection := db.SyncDirection(pending.SyncDirection)
	if !syncDirection.IsValid() {
		syncDirection = db.SyncDirectionOneWay
	}
	conflictStrategy := db.ConflictStrategy(pending.ConflictStrategy)
	if !conflictStrategy.IsValid() {
		conflictStrategy = db.ConflictSourceWins
	}

	// Re-encrypt the Google client secret for storage on the Source
	// row. We held it in plaintext only for the duration of the
	// cfg.Exchange call above; the value-at-rest in synced_events
	// must always be encrypted.
	encGoogleClientSecret, err := h.encryptor.Encrypt(googleClientSecret)
	if err != nil {
		log.Printf("Google OAuth callback: failed to encrypt Google client secret for storage: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=encrypt_failed")
		return
	}

	source := &db.Source{
		UserID:             session.UserID,
		Name:               pending.Name,
		SourceType:         db.SourceTypeGoogle,
		SourceURL:          sourceURL,
		SourceUsername:     email, // Informational only; OAuth doesn't use it
		SourcePassword:     "",    // Intentionally empty for OAuth sources
		OAuthRefreshToken:  encRefreshToken,
		GoogleClientID:     pending.GoogleClientID,
		GoogleClientSecret: encGoogleClientSecret,
		DestURL:            pending.DestURL,
		DestUsername:       pending.DestUsername,
		DestPassword:       pending.DestPasswordEnc,
		SyncInterval:       syncInterval,
		SyncDaysPast:       syncDaysPast,
		SyncDirection:      syncDirection,
		ConflictStrategy:   conflictStrategy,
		Enabled:            true,
	}

	if err := h.db.CreateSource(source); err != nil {
		log.Printf("Google OAuth callback: failed to create source: %v", err)
		c.Redirect(http.StatusFound, "/sources/add?error=create_failed")
		return
	}

	h.scheduler.AddJob(source.ID, time.Duration(source.SyncInterval)*time.Second)
	log.Printf("Google OAuth callback: created source %s for %s", source.ID, email)

	// Full-page navigation back to the SPA, which will load /sources
	// and show the new source.
	c.Redirect(http.StatusFound, "/sources?google_oauth=success")
}

// fetchGoogleUserEmail makes a GET /userinfo call against Google using
// the access token we just obtained and returns the user's primary
// email. Separated into its own function to keep the callback handler
// readable and to make it easy to mock in tests.
func fetchGoogleUserEmail(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (string, error) {
	client := cfg.Client(ctx, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo returned HTTP %d", resp.StatusCode)
	}

	var info googleUserinfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.Email == "" {
		return "", fmt.Errorf("userinfo did not include an email")
	}
	return info.Email, nil
}
