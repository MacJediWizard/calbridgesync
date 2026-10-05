package caldav

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/db"
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

// TestDialVettedIPs_FallsBackToNextIP verifies that the SSRF-guarded
// dial tries every vetted address in order, like net.Dialer does for a
// hostname, instead of failing when only the first one is unreachable.
// Here the first address (::1) has no listener and the second
// (127.0.0.1) does. (#200 review)
func TestDialVettedIPs_FallsBackToNextIP(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ips := []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}
	conn, err := dialVettedIPs(context.Background(), "tcp", ips, port)
	if err != nil {
		t.Fatalf("expected fallback to the second IP to succeed, got %v", err)
	}
	defer conn.Close()
	if got := conn.RemoteAddr().String(); got != ln.Addr().String() {
		t.Errorf("connected to %s, want %s", got, ln.Addr())
	}
}

// TestDialVettedIPs_AllFailReturnsFirstError mirrors net.Dialer: when
// every address fails, the first address's error is returned. (#200 review)
func TestDialVettedIPs_AllFailReturnsFirstError(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()

	_, err = dialVettedIPs(context.Background(), "tcp", []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, port)
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Addr == nil || opErr.Addr.String() != net.JoinHostPort("127.0.0.1", port) {
		t.Fatalf("expected the first address's OpError, got %v", err)
	}
}

// TestScrubInternalDialErrors verifies that dial failures to
// non-public addresses lose the refused/timeout/unreachable detail
// that would otherwise let a user map the server's private network
// through the sync log, while dial failures to public addresses and
// unrelated errors are left alone. (#200 review)
func TestScrubInternalDialErrors(t *testing.T) {
	const scrubbed = "could not connect to the server"
	cases := []struct {
		in        string
		wantScrub bool
	}{
		{`connection failed: Propfind "http://10.0.0.5:22/": dial tcp 10.0.0.5:22: connect: connection refused`, true},
		{`connection failed: Propfind "http://10.0.0.5:23/": dial tcp 10.0.0.5:23: i/o timeout`, true},
		{`Failed to get source events: Report "http://h/": dial tcp 192.168.1.20:8443: connect: no route to host`, true},
		{`dial tcp [fd00::1]:443: connect: connection refused`, true},
		{`dial tcp 100.64.0.1:443: connect: network is unreachable`, true},
		{`dial tcp 127.0.0.1:8080: connect: connection refused`, true},
		{`dial tcp 8.8.8.8:443: connect: connection refused`, false},
		{`dial tcp [2001:4860:4860::8888]:443: i/o timeout`, false},
		{`401 Unauthorized`, false},
		{`blocked destination: internal.test resolves to 169.254.169.254 (link-local (includes cloud IMDS))`, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := scrubInternalDialErrors([]string{tc.in})[0]
			if tc.wantScrub {
				for _, leak := range []string{"refused", "timeout", "unreachable", "no route"} {
					if strings.Contains(got, leak) {
						t.Errorf("scrubbed text still contains %q: %q", leak, got)
					}
				}
				if !strings.Contains(got, scrubbed) {
					t.Errorf("got %q, want it to contain %q", got, scrubbed)
				}
			} else if got != tc.in {
				t.Errorf("unexpected rewrite: %q -> %q", tc.in, got)
			}
		})
	}

	// Refused and timed-out dials to the same private host must read
	// identically once scrubbed.
	a := scrubInternalDialErrors([]string{`dial tcp 10.0.0.5:22: connect: connection refused`})[0]
	b := scrubInternalDialErrors([]string{`dial tcp 10.0.0.5:22: i/o timeout`})[0]
	if a != b {
		t.Errorf("refused and timeout still distinguishable: %q vs %q", a, b)
	}
}

// TestFinishSync_ScrubsInternalDialErrors verifies the scrub is applied
// at the single choke point that feeds sync_logs.Details (returned by
// GET /sources/:id/logs), the activity tracker and scheduler alerts. (#200 review)
func TestFinishSync_ScrubsInternalDialErrors(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	result := &SyncResult{
		Message: "Source connection test failed",
		Errors: []string{
			`connection failed: Propfind "http://10.0.0.5:22/": dial tcp 10.0.0.5:22: connect: connection refused`,
		},
		Warnings: []string{`dial tcp 10.0.0.6:22: i/o timeout`},
	}
	h.se.finishSync(h.source.ID, result)

	logs, err := h.db.GetSyncLogs(h.source.ID, 1)
	if err != nil || len(logs) != 1 {
		t.Fatalf("GetSyncLogs: %v (n=%d)", err, len(logs))
	}
	all := append(append([]string{}, result.Errors...), result.Warnings...)
	for _, leak := range []string{"connection refused", "i/o timeout"} {
		if strings.Contains(logs[0].Details, leak) {
			t.Errorf("sync log details leak %q: %q", leak, logs[0].Details)
		}
		for _, e := range all {
			if strings.Contains(e, leak) {
				t.Errorf("result (tracker/alert input) leaks %q: %q", leak, e)
			}
		}
	}
}
