package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// fakeIdP is a minimal in-process OIDC provider: discovery, JWKS and a
// token endpoint that enforces PKCE (S256) and issues an RS256 ID token
// carrying whatever nonce the test configures.
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	client string

	mu            sync.Mutex
	challenge     string // code_challenge seen on the authorize URL
	gotVerifier   string // code_verifier sent to the token endpoint
	idTokenNonce  string // nonce to embed in the issued ID token ("" = omit)
	emailVerified bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f := &fakeIdP{t: t, key: key, client: "calbridgesync-test", emailVerified: true}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		pub := f.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": "test",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.gotVerifier = r.PostForm.Get("code_verifier")
		sum := sha256.Sum256([]byte(f.gotVerifier))
		if f.challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PKCE verification failed"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     f.signIDToken(),
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) signIDToken() string {
	now := time.Now()
	claims := map[string]any{
		"iss":            f.srv.URL,
		"sub":            "user-123",
		"aud":            f.client,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
		"email":          "user@example.com",
		"email_verified": f.emailVerified,
		"name":           "Test User",
	}
	if f.idTokenNonce != "" {
		claims["nonce"] = f.idTokenNonce
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// startLogin builds the authorize URL the way Handlers.Login does and
// records the PKCE challenge the IdP would have seen. It returns the
// parsed authorize query.
func (f *fakeIdP) startLogin(t *testing.T, p *OIDCProvider, state, nonce, verifier string) url.Values {
	t.Helper()
	u, err := url.Parse(p.AuthCodeURL(state, nonce, verifier))
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	q := u.Query()
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	return q
}

func newTestProvider(t *testing.T, f *fakeIdP) *OIDCProvider {
	t.Helper()
	p, err := NewOIDCProvider(context.Background(), f.srv.URL, f.client, "secret", "https://app.example/auth/callback")
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	return p
}

func TestOIDCAuthCodeURLSendsNonceAndS256Challenge(t *testing.T) {
	f := newFakeIdP(t)
	p := newTestProvider(t, f)

	q := f.startLogin(t, p, "st", "the-nonce", "the-verifier-0123456789012345678901234567890123")

	if got := q.Get("state"); got != "st" {
		t.Errorf("state = %q, want st", got)
	}
	if got := q.Get("nonce"); got != "the-nonce" {
		t.Errorf("nonce = %q, want the-nonce", got)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	sum := sha256.Sum256([]byte("the-verifier-0123456789012345678901234567890123"))
	if got, want := q.Get("code_challenge"), base64.RawURLEncoding.EncodeToString(sum[:]); got != want {
		t.Errorf("code_challenge = %q, want %q", got, want)
	}
}

func TestOIDCLoginRoundTripWithNonceAndPKCE(t *testing.T) {
	f := newFakeIdP(t)
	p := newTestProvider(t, f)
	ctx := context.Background()

	nonce, verifier, err := GenerateOIDCLoginSecrets()
	if err != nil {
		t.Fatalf("GenerateOIDCLoginSecrets: %v", err)
	}
	f.startLogin(t, p, "st", nonce, verifier)
	f.idTokenNonce = nonce

	tok, err := p.Exchange(ctx, "code", verifier)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if f.gotVerifier != verifier {
		t.Errorf("token endpoint got code_verifier %q, want %q", f.gotVerifier, verifier)
	}

	claims, err := p.VerifyIDToken(ctx, tok, nonce)
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	if claims.Email != "user@example.com" || claims.Subject != "user-123" {
		t.Errorf("unexpected claims: %+v", claims)
	}
}

func TestOIDCExchangeRejectsWrongVerifier(t *testing.T) {
	f := newFakeIdP(t)
	p := newTestProvider(t, f)

	_, verifier, _ := GenerateOIDCLoginSecrets()
	f.startLogin(t, p, "st", "n", verifier)

	_, otherVerifier, _ := GenerateOIDCLoginSecrets()
	if _, err := p.Exchange(context.Background(), "code", otherVerifier); !errors.Is(err, ErrTokenExchange) {
		t.Fatalf("Exchange with wrong verifier: err = %v, want ErrTokenExchange", err)
	}
}

func TestOIDCVerifyIDTokenRejectsNonceMismatch(t *testing.T) {
	f := newFakeIdP(t)
	p := newTestProvider(t, f)
	ctx := context.Background()

	_, verifier, _ := GenerateOIDCLoginSecrets()
	f.startLogin(t, p, "st", "expected-nonce", verifier)
	f.idTokenNonce = "attacker-nonce"

	tok, err := p.Exchange(ctx, "code", verifier)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if _, err := p.VerifyIDToken(ctx, tok, "expected-nonce"); !errors.Is(err, ErrNonceMismatch) {
		t.Fatalf("VerifyIDToken: err = %v, want ErrNonceMismatch", err)
	}
}

func TestOIDCVerifyIDTokenRejectsMissingNonce(t *testing.T) {
	f := newFakeIdP(t)
	p := newTestProvider(t, f)
	ctx := context.Background()

	_, verifier, _ := GenerateOIDCLoginSecrets()
	f.startLogin(t, p, "st", "expected-nonce", verifier)
	f.idTokenNonce = "" // IdP omits the nonce claim

	tok, err := p.Exchange(ctx, "code", verifier)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if _, err := p.VerifyIDToken(ctx, tok, "expected-nonce"); !errors.Is(err, ErrNonceMismatch) {
		t.Fatalf("VerifyIDToken: err = %v, want ErrNonceMismatch", err)
	}
	// An empty expected nonce must never match an absent claim.
	if _, err := p.VerifyIDToken(ctx, tok, ""); !errors.Is(err, ErrNonceMismatch) {
		t.Fatalf("VerifyIDToken with empty expected nonce: err = %v, want ErrNonceMismatch", err)
	}
}
