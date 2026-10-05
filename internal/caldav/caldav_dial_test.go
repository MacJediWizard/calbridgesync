package caldav

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// allowLoopbackDial swaps caldavDialContext for a plain dialer for the
// duration of the test, so httptest servers (bound to 127.0.0.1) are
// reachable. Tests in this package do not use t.Parallel, so swapping
// the package var is race-free. (#200)
func allowLoopbackDial(t *testing.T) {
	t.Helper()
	orig := caldavDialContext
	d := &net.Dialer{Timeout: 5 * time.Second}
	caldavDialContext = d.DialContext
	t.Cleanup(func() { caldavDialContext = orig })
}

// TestNewClient_RefusesBlockedAddresses verifies that the basic-auth
// CalDAV client refuses to connect to loopback (here a live httptest
// server that must never see a request) and to the cloud metadata
// address. (#200)
func TestNewClient_RefusesBlockedAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, baseURL := range []string{
		srv.URL + "/",                    // 127.0.0.1
		"http://169.254.169.254/latest/", // IMDS
		"http://[::1]:1/",                // IPv6 loopback
		"http://0.0.0.0:1/",              // unspecified
	} {
		t.Run(baseURL, func(t *testing.T) {
			client, err := NewClient(baseURL, "user", "pass")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = client.TestConnection(ctx)
			if !errors.Is(err, ErrBlockedDestination) {
				t.Fatalf("expected ErrBlockedDestination, got %v", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("loopback server received %d requests; dial should have been refused", n)
	}
}

// TestNewOAuthClient_RefusesBlockedAddresses is the OAuth-client
// counterpart. The access token is still valid, so no refresh (which
// uses its own HTTP client against the fixed provider endpoint) runs. (#200)
func TestNewOAuthClient_RefusesBlockedAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &oauth2.Config{ClientID: "id", ClientSecret: "secret"}
	token := &oauth2.Token{
		AccessToken:  "valid",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(time.Hour),
	}

	for _, baseURL := range []string{srv.URL + "/", "http://169.254.169.254/"} {
		t.Run(baseURL, func(t *testing.T) {
			client, err := NewOAuthClient(context.Background(), baseURL, cfg, token)
			if err != nil {
				t.Fatalf("NewOAuthClient: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = client.TestConnection(ctx)
			if !errors.Is(err, ErrBlockedDestination) {
				t.Fatalf("expected ErrBlockedDestination, got %v", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("loopback server received %d requests; dial should have been refused", n)
	}
}

// TestNewClient_RefusedDialKeepsOpError verifies that an ordinary
// refused dial surfaces a *net.OpError carrying the dialed address
// through the client's error wrapping. The web layer relies on this
// to give refused dials to non-public addresses the same message as
// blocked dials. (#200)
func TestNewClient_RefusedDialKeepsOpError(t *testing.T) {
	allowLoopbackDial(t)

	// Grab a free port, then close the listener so the dial is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	client, err := NewClient("http://"+addr+"/", "user", "pass")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = client.TestConnection(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected *net.OpError in chain, got %T: %v", err, err)
	}
	if opErr.Op != "dial" || opErr.Addr == nil || opErr.Addr.String() != addr {
		t.Errorf("unexpected OpError: op=%q addr=%v", opErr.Op, opErr.Addr)
	}
	if errors.Is(err, ErrBlockedDestination) {
		t.Errorf("refused dial must not be reported as blocked: %v", err)
	}
}
