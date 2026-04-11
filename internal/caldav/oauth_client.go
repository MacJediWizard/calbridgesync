package caldav

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
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
		Timeout:   defaultTimeout,
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
	}, nil
}
