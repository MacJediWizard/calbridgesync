package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	ErrOIDCInit         = errors.New("OIDC initialization failed")
	ErrTokenExchange    = errors.New("token exchange failed")
	ErrTokenVerify      = errors.New("token verification failed")
	ErrMissingEmail     = errors.New("email claim is required")
	ErrEmailNotVerified = errors.New("email is not verified")
	ErrNonceMismatch    = errors.New("id token nonce does not match login request")
)

// OIDCClaims represents the claims extracted from an ID token.
type OIDCClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatarUrl"`
}

// OIDCProvider handles OIDC authentication.
type OIDCProvider struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	config   oauth2.Config
}

// NewOIDCProvider creates a new OIDC provider.
func NewOIDCProvider(ctx context.Context, issuer, clientID, clientSecret, redirectURL string) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create provider: %w", ErrOIDCInit, err)
	}

	config := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: clientID,
	})

	return &OIDCProvider{
		provider: provider,
		verifier: verifier,
		config:   config,
	}, nil
}

// GenerateOIDCLoginSecrets returns a fresh nonce and PKCE code verifier
// for one login attempt. Both must be stored alongside the OAuth state
// and handed back to Exchange / VerifyIDToken on the callback.
func GenerateOIDCLoginSecrets() (nonce, verifier string, err error) {
	nonce, err = GenerateState()
	if err != nil {
		return "", "", err
	}
	return nonce, oauth2.GenerateVerifier(), nil
}

// AuthCodeURL returns the URL to redirect the user to for authentication.
// The nonce is bound into the ID token by the IdP, and the verifier is
// sent as an S256 PKCE code_challenge.
func (p *OIDCProvider) AuthCodeURL(state, nonce, verifier string) string {
	return p.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// Exchange exchanges an authorization code for tokens, proving
// possession of the PKCE verifier used in AuthCodeURL.
func (p *OIDCProvider) Exchange(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	token, err := p.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTokenExchange, err)
	}
	return token, nil
}

// VerifyIDToken verifies the ID token, checks that its nonce matches the
// one issued for this login attempt, extracts claims, and requires
// email_verified=true.
func (p *OIDCProvider) VerifyIDToken(ctx context.Context, token *oauth2.Token, nonce string) (*OIDCClaims, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("%w: missing id_token", ErrTokenVerify)
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTokenVerify, err)
	}

	if nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return nil, ErrNonceMismatch
	}

	var claims OIDCClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: failed to parse claims: %w", ErrTokenVerify, err)
	}

	if claims.Email == "" {
		return nil, ErrMissingEmail
	}

	// Users are matched by email, so an address the IdP has not verified
	// must not log in. A missing claim decodes to false and is rejected too.
	if !claims.EmailVerified {
		return nil, ErrEmailNotVerified
	}

	return &claims, nil
}

// GetUserInfo retrieves user info from the userinfo endpoint.
func (p *OIDCProvider) GetUserInfo(ctx context.Context, token *oauth2.Token) (*OIDCClaims, error) {
	userInfo, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	var claims OIDCClaims
	if err := userInfo.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to parse user info: %w", err)
	}

	claims.Subject = userInfo.Subject
	claims.Email = userInfo.Email
	claims.EmailVerified = userInfo.EmailVerified

	if claims.Email == "" {
		return nil, ErrMissingEmail
	}

	return &claims, nil
}
