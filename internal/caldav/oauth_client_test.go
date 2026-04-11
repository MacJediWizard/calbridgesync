package caldav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// TestNewOAuthClient_RejectsEmptyBaseURL verifies the same empty-URL
// guard as NewClient. (#70)
func TestNewOAuthClient_RejectsEmptyBaseURL(t *testing.T) {
	cfg := &oauth2.Config{ClientID: "x", ClientSecret: "y", Endpoint: oauth2.Endpoint{TokenURL: "https://example.com/token"}}
	token := &oauth2.Token{RefreshToken: "refresh"}

	_, err := NewOAuthClient(context.Background(), "", cfg, token)
	if err == nil {
		t.Fatal("expected error for empty base URL")
	}
	if !errors.Is(err, ErrConnectionFailed) {
		t.Errorf("expected ErrConnectionFailed, got %v", err)
	}
}

// TestNewOAuthClient_RejectsNilConfig verifies that a nil oauth2.Config
// is rejected before we try to build anything. (#70)
func TestNewOAuthClient_RejectsNilConfig(t *testing.T) {
	token := &oauth2.Token{RefreshToken: "refresh"}
	_, err := NewOAuthClient(context.Background(), "https://example.com/", nil, token)
	if err == nil {
		t.Fatal("expected error for nil oauth config")
	}
	if !errors.Is(err, ErrConnectionFailed) {
		t.Errorf("expected ErrConnectionFailed, got %v", err)
	}
}

// TestNewOAuthClient_RejectsMissingRefreshToken verifies that callers
// cannot create a client without a refresh token. A refresh token is
// the only way the client can recover from access token expiry, so
// creating a client without one is a programming error that should
// fail loudly instead of silently producing a 401-later client. (#70)
func TestNewOAuthClient_RejectsMissingRefreshToken(t *testing.T) {
	cfg := &oauth2.Config{ClientID: "x", ClientSecret: "y", Endpoint: oauth2.Endpoint{TokenURL: "https://example.com/token"}}

	_, err := NewOAuthClient(context.Background(), "https://example.com/", cfg, nil)
	if err == nil {
		t.Fatal("expected error for nil token")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed for nil token, got %v", err)
	}

	_, err = NewOAuthClient(context.Background(), "https://example.com/", cfg, &oauth2.Token{})
	if err == nil {
		t.Fatal("expected error for empty refresh token")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed for empty refresh token, got %v", err)
	}
}

// TestOAuthClient_InjectsBearerToken verifies the core of the OAuth
// path: the CalDAV HTTP requests carry an Authorization: Bearer
// header derived from the configured TokenSource. We stand up a
// mock CalDAV server that inspects the header and a mock OAuth2
// token endpoint that returns a canned access token. (#70)
func TestOAuthClient_InjectsBearerToken(t *testing.T) {
	var caldavAuthHeader atomic.Value
	caldavServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caldavAuthHeader.Store(r.Header.Get("Authorization"))
		// Return a 401 so the client's subsequent call path is
		// irrelevant to this test — we only care that the header
		// was set correctly on the outgoing request.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer caldavServer.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"test-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	cfg := &oauth2.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  tokenServer.URL + "/auth",
			TokenURL: tokenServer.URL + "/token",
		},
	}
	// Token with an expired access token — the Transport must
	// refresh it via tokenServer before making any CalDAV call.
	token := &oauth2.Token{
		AccessToken:  "stale",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-1 * time.Hour),
	}

	client, err := NewOAuthClient(context.Background(), caldavServer.URL+"/", cfg, token)
	if err != nil {
		t.Fatalf("NewOAuthClient returned error: %v", err)
	}

	// TestConnection will fail (server returns 401) — we don't care
	// about the return value, only the header seen by the server.
	_ = client.TestConnection(context.Background())

	headerValue, _ := caldavAuthHeader.Load().(string)
	if headerValue == "" {
		t.Fatal("CalDAV server never saw an Authorization header")
	}
	if !strings.HasPrefix(headerValue, "Bearer ") {
		t.Errorf("expected Bearer token, got %q", headerValue)
	}
	if !strings.Contains(headerValue, "test-access-token") {
		t.Errorf("expected Bearer header to carry refreshed token, got %q", headerValue)
	}
}

// TestOAuthClient_RefreshHitsTokenEndpoint verifies that an expired
// access token triggers a refresh against the configured TokenURL
// with the stored refresh token. This is the happy-path scenario
// for long-running Google sources that sit idle between syncs. (#70)
func TestOAuthClient_RefreshHitsTokenEndpoint(t *testing.T) {
	var tokenHits atomic.Int32
	var receivedRefreshToken atomic.Value

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenHits.Add(1)
		// oauth2 package sends the refresh token as form body
		if err := r.ParseForm(); err == nil {
			receivedRefreshToken.Store(r.Form.Get("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	caldavServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer caldavServer.Close()

	cfg := &oauth2.Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenServer.URL,
		},
	}
	token := &oauth2.Token{
		AccessToken:  "stale",
		RefreshToken: "the-refresh-token",
		Expiry:       time.Now().Add(-1 * time.Hour),
	}

	client, err := NewOAuthClient(context.Background(), caldavServer.URL+"/", cfg, token)
	if err != nil {
		t.Fatalf("NewOAuthClient returned error: %v", err)
	}

	_ = client.TestConnection(context.Background())

	if got := tokenHits.Load(); got == 0 {
		t.Fatal("token endpoint was never hit — refresh did not occur")
	}
	if got, _ := receivedRefreshToken.Load().(string); got != "the-refresh-token" {
		t.Errorf("expected refresh token %q, got %q", "the-refresh-token", got)
	}
}
