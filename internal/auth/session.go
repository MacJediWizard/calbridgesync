package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/sessions"
)

const (
	sessionName             = "calbridgesync_session"
	oauthStateName          = "calbridgesync_oauth_state"
	googlePendingSourceName = "calbridgesync_google_pending" // #70
	csrfTokenLength         = 32
	// pendingSourceMaxAge is how long a pending Google source can sit
	// in its cookie between form submit and OAuth callback. Long
	// enough for the user to go through Google's consent screen,
	// short enough that a stale cookie doesn't pile up. (#70)
	pendingSourceMaxAge = 900 // 15 minutes
)

// PendingGoogleSource holds the form data for a Google source that is
// mid-OAuth. It's stashed in a short-lived session cookie between the
// "prepare" API call and the OAuth callback, then read and cleared by
// the callback when it creates the real Source row. DestPassword is
// already encrypted via the application's AES-256-GCM Encryptor
// before it lands in this struct — the session cookie does NOT carry
// a plaintext password. (#70)
type PendingGoogleSource struct {
	State            string `json:"state"`
	Name             string `json:"name"`
	SyncInterval     int    `json:"sync_interval"`
	SyncDaysPast     int    `json:"sync_days_past"`
	SyncDirection    string `json:"sync_direction"`
	ConflictStrategy string `json:"conflict_strategy"`
	DestURL          string `json:"dest_url"`
	DestUsername     string `json:"dest_username"`
	DestPasswordEnc  string `json:"dest_password_enc"` // already encrypted
}

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrInvalidSession  = errors.New("invalid session data")
)

// SessionData represents the data stored in a user session.
type SessionData struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Picture   string `json:"picture"`
	CSRFToken string `json:"csrf_token"`
}

// SessionManager manages user sessions.
type SessionManager struct {
	store            *sessions.CookieStore
	secure           bool
	oauthStateMaxAge int // OAuth state timeout in seconds
}

// NewSessionManager creates a new session manager.
// sessionMaxAgeSecs: Session timeout in seconds (e.g., 86400 for 24 hours)
// oauthStateMaxAgeSecs: OAuth state timeout in seconds (e.g., 300 for 5 minutes)
func NewSessionManager(secret string, secure bool, sessionMaxAgeSecs, oauthStateMaxAgeSecs int) *SessionManager {
	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   sessionMaxAgeSecs,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		// Domain is intentionally NOT set - this is the secure default.
		// When domain is unset, the cookie is restricted to the exact host only,
		// preventing subdomain access. Setting a domain (e.g., ".example.com")
		// would actually make the cookie LESS secure by allowing subdomains to access it.
	}

	return &SessionManager{
		store:            store,
		secure:           secure,
		oauthStateMaxAge: oauthStateMaxAgeSecs,
	}
}

// Get retrieves the session data from the request.
func (sm *SessionManager) Get(r *http.Request) (*SessionData, error) {
	session, err := sm.store.Get(r, sessionName)
	if err != nil {
		return nil, ErrSessionNotFound
	}

	userID, ok := session.Values["user_id"].(string)
	if !ok || userID == "" {
		return nil, ErrSessionNotFound
	}

	// These type assertions are intentionally unchecked - missing values default to empty string
	var email, name, picture, csrfToken string
	if v, ok := session.Values["email"].(string); ok {
		email = v
	}
	if v, ok := session.Values["name"].(string); ok {
		name = v
	}
	if v, ok := session.Values["picture"].(string); ok {
		picture = v
	}
	if v, ok := session.Values["csrf_token"].(string); ok {
		csrfToken = v
	}

	return &SessionData{
		UserID:    userID,
		Email:     email,
		Name:      name,
		Picture:   picture,
		CSRFToken: csrfToken,
	}, nil
}

// Set stores the session data.
func (sm *SessionManager) Set(w http.ResponseWriter, r *http.Request, data *SessionData) error {
	session, err := sm.store.Get(r, sessionName)
	if err != nil {
		// Create a new session if the current one is invalid
		session, err = sm.store.New(r, sessionName)
		if err != nil {
			return err
		}
	}

	// Generate CSRF token if not present
	if data.CSRFToken == "" {
		csrfToken, err := generateCSRFToken()
		if err != nil {
			return err
		}
		data.CSRFToken = csrfToken
	}

	session.Values["user_id"] = data.UserID
	session.Values["email"] = data.Email
	session.Values["name"] = data.Name
	session.Values["picture"] = data.Picture
	session.Values["csrf_token"] = data.CSRFToken

	return session.Save(r, w)
}

// Clear removes the session.
func (sm *SessionManager) Clear(w http.ResponseWriter, r *http.Request) error {
	session, err := sm.store.Get(r, sessionName)
	if err != nil {
		return nil // Session doesn't exist, nothing to clear
	}

	session.Options.MaxAge = -1
	return session.Save(r, w)
}

// SetOAuthState stores the OAuth state for CSRF protection.
func (sm *SessionManager) SetOAuthState(w http.ResponseWriter, r *http.Request, state string) error {
	session, err := sm.store.Get(r, oauthStateName)
	if err != nil {
		session, err = sm.store.New(r, oauthStateName)
		if err != nil {
			return err
		}
	}

	session.Values["state"] = state
	session.Options.MaxAge = sm.oauthStateMaxAge // Configurable via OAUTH_STATE_MAX_AGE_SECS

	return session.Save(r, w)
}

// GetOAuthState retrieves and clears the OAuth state.
func (sm *SessionManager) GetOAuthState(w http.ResponseWriter, r *http.Request) (string, error) {
	session, err := sm.store.Get(r, oauthStateName)
	if err != nil {
		return "", err
	}

	state, ok := session.Values["state"].(string)
	if !ok || state == "" {
		return "", ErrInvalidSession
	}

	// Clear the state after reading
	session.Options.MaxAge = -1
	if err := session.Save(r, w); err != nil {
		return "", err
	}

	return state, nil
}

// SetPendingGoogleSource stores a pending Google source form in a
// short-lived session cookie. Used between the "prepare" API call
// (which validates the form) and the OAuth callback (which reads it
// back and creates the real Source row after Google returns the
// refresh token). (#70)
func (sm *SessionManager) SetPendingGoogleSource(w http.ResponseWriter, r *http.Request, data *PendingGoogleSource) error {
	session, err := sm.store.Get(r, googlePendingSourceName)
	if err != nil {
		session, err = sm.store.New(r, googlePendingSourceName)
		if err != nil {
			return err
		}
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}

	session.Values["pending"] = string(encoded)
	session.Options.MaxAge = pendingSourceMaxAge

	return session.Save(r, w)
}

// GetPendingGoogleSource retrieves and clears the pending Google
// source. The clear-on-read semantics mean a pending source can only
// be consumed once — replaying the OAuth callback will fail. (#70)
func (sm *SessionManager) GetPendingGoogleSource(w http.ResponseWriter, r *http.Request) (*PendingGoogleSource, error) {
	session, err := sm.store.Get(r, googlePendingSourceName)
	if err != nil {
		return nil, err
	}

	encoded, ok := session.Values["pending"].(string)
	if !ok || encoded == "" {
		return nil, ErrInvalidSession
	}

	var data PendingGoogleSource
	if err := json.Unmarshal([]byte(encoded), &data); err != nil {
		return nil, err
	}

	// Clear the cookie after reading
	session.Options.MaxAge = -1
	if err := session.Save(r, w); err != nil {
		return nil, err
	}

	return &data, nil
}

// GenerateState generates a random state string for OAuth.
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// generateCSRFToken generates a random CSRF token.
func generateCSRFToken() (string, error) {
	b := make([]byte, csrfTokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
