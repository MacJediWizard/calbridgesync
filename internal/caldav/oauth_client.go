package caldav

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-webdav/caldav"
	"golang.org/x/oauth2"
)

// NewOAuthClient creates a CalDAV Client that authenticates using
// OAuth2 Bearer tokens instead of HTTP Basic Auth. It is used for
// source types where the server requires OAuth2 — currently only
// Google Calendar (#70).
//
// The caller provides an oauth2.Config (with ClientID/ClientSecret
// and the provider endpoint already set) and an *oauth2.Token that
// holds a non-empty RefreshToken. Access tokens are refreshed
// automatically by oauth2.Transport when they expire; the caller does
// NOT need to check expiry.
//
// ctx is stored inside the returned TokenSource and used for token
// refreshes, so it must remain valid for the lifetime of the Client.
// Pass context.Background() for long-lived use (the sync engine).
func NewOAuthClient(ctx context.Context, baseURL string, oauthConfig *oauth2.Config, token *oauth2.Token) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("%w: base URL is required", ErrConnectionFailed)
	}
	if oauthConfig == nil {
		return nil, fmt.Errorf("%w: oauth config is required", ErrConnectionFailed)
	}
	if token == nil || token.RefreshToken == "" {
		return nil, fmt.Errorf("%w: refresh token is required", ErrAuthFailed)
	}

	// Base transport — matches NewClient's TLS/timeouts exactly so
	// OAuth requests and non-OAuth requests behave identically at the
	// network layer. Any change to TLS/timeout policy here MUST be
	// mirrored in NewClient (and vice versa).
	baseTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: minTLSVersion,
		},
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         dialCalDAV, // SSRF guard (#200)
	}

	// oauth2.Transport wraps baseTransport and injects the bearer
	// token into every request. When the access token expires, the
	// underlying ReuseTokenSource calls oauthConfig.TokenSource(ctx,
	// token).Token() which performs a refresh against the provider's
	// TokenURL (google.Endpoint.TokenURL for Google).
	tokenSource := oauthConfig.TokenSource(ctx, token)
	oauthTransport := &oauth2.Transport{
		Base:   baseTransport,
		Source: tokenSource,
	}

	httpClient := &http.Client{
		Timeout:   requestTimeout(),
		Transport: oauthTransport,
	}

	// caldav.NewClient accepts anything that implements webdav.HTTPClient,
	// and *http.Client satisfies that interface via its Do method. We
	// pass the oauth-wrapped client directly — no basic-auth wrapper.
	caldavClient, err := caldav.NewClient(httpClient, baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create CalDAV client: %w", ErrConnectionFailed, err)
	}

	return &Client{
		baseURL:      baseURL,
		username:     "", // OAuth clients don't carry a username/password
		password:     "",
		httpClient:   httpClient,
		caldavClient: caldavClient,
		tokenSource:  tokenSource,
	}, nil
}

// GoogleAuthExpiredMessage is the source status message recorded when
// Google rejects the stored refresh token (invalid_grant). It tells the
// user exactly what to do; the SPA also keys its "Reconnect" prompt off
// this message. (#192)
const GoogleAuthExpiredMessage = "Google authorization expired or was revoked. Reconnect the Google account (Edit source > Reconnect Google account)."

// oauthGrantRevokedCode is the RFC 6749 §5.2 error code Google returns
// when a refresh token is expired, revoked, or was issued to an OAuth
// app whose consent screen is still in "Testing" mode (7-day expiry).
const oauthGrantRevokedCode = "invalid_grant"

// classifyTokenError wraps an error from an OAuth2 token refresh. A 4xx
// response from the provider's token endpoint means the provider
// rejected our credentials (revoked grant, deleted client, ...), which
// is an ErrAuthFailed. Anything else (network error, 5xx) is a
// transient ErrConnectionFailed so a provider outage is not reported
// as expired credentials. (#192)
func classifyTokenError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil &&
		re.Response.StatusCode >= 400 && re.Response.StatusCode < 500 {
		return fmt.Errorf("%w: %w", ErrAuthFailed, err)
	}
	return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
}

// IsOAuthGrantRevoked reports whether err is the OAuth2 token endpoint
// rejecting the refresh token with invalid_grant. That condition only
// clears when the user re-authorizes the source. (#192)
func IsOAuthGrantRevoked(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && re.ErrorCode == oauthGrantRevokedCode
}

// ErrorTextIsOAuthGrantRevoked is the string form of IsOAuthGrantRevoked
// for errors that have already been flattened into SyncResult.Errors.
// oauth2.RetrieveError renders as `oauth2: "invalid_grant" "..."`. (#192)
func ErrorTextIsOAuthGrantRevoked(text string) bool {
	return strings.Contains(text, `"`+oauthGrantRevokedCode+`"`)
}
