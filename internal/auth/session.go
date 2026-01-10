package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/gorilla/sessions"
)

const (
	sessionName     = "calbridge_session"
	oauthStateName  = "calbridge_oauth_state"
	sessionMaxAge   = 7 * 24 * 60 * 60 // 7 days in seconds
	csrfTokenLength = 32
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrInvalidSession  = errors.New("invalid session data")
)

// SessionData represents the data stored in a user session.
type SessionData struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	CSRFToken string `json:"csrf_token"`
}

// SessionManager manages user sessions.
type SessionManager struct {
	store  *sessions.CookieStore
	secure bool
}

// NewSessionManager creates a new session manager.
func NewSessionManager(secret string, secure bool) *SessionManager {
	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}

	return &SessionManager{
		store:  store,
		secure: secure,
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
	var email, name, csrfToken string
	if v, ok := session.Values["email"].(string); ok {
		email = v
	}
	if v, ok := session.Values["name"].(string); ok {
		name = v
	}
	if v, ok := session.Values["csrf_token"].(string); ok {
		csrfToken = v
	}

	return &SessionData{
		UserID:    userID,
		Email:     email,
		Name:      name,
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
	session.Options.MaxAge = 600 // 10 minutes

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
