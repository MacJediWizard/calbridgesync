package caldav

import (
	"context"
	"net"
	"strings"
	"testing"
)

// TestIsICSBlockedIP covers the ICS-specific IP classifier from
// #129. The ICS block-list is intentionally narrower than the
// webhook block-list (notify.isBlockedIP): private IPs are
// allowed because LAN calendar servers are a real use case. Only
// loopback, unspecified, and link-local are refused — anything
// that is ALWAYS an operator mistake for an ICS feed URL.
func TestIsICSBlockedIP(t *testing.T) {
	cases := []struct {
		name       string
		ip         string
		wantBlock  bool
		wantReason string
	}{
		// Happy path — public IPs pass.
		{"public IPv4 8.8.8.8", "8.8.8.8", false, ""},
		{"public IPv4 1.1.1.1", "1.1.1.1", false, ""},
		{"public IPv6 Google DNS", "2001:4860:4860::8888", false, ""},

		// Private / LAN — ALLOWED (intentional difference from
		// notify.isBlockedIP). LAN Nextcloud, Radicale, DavMail
		// all live on private ranges.
		{"private 10.0.0.1 allowed", "10.0.0.1", false, ""},
		{"private 192.168.1.1 allowed", "192.168.1.1", false, ""},
		{"private 172.16.0.1 allowed", "172.16.0.1", false, ""},
		{"private 172.31.255.254 allowed", "172.31.255.254", false, ""},
		{"CGNAT 100.64.0.1 allowed", "100.64.0.1", false, ""},
		{"IPv6 unique-local fc00::1 allowed", "fc00::1", false, ""},

		// Loopback — blocked (operator typo).
		{"loopback 127.0.0.1", "127.0.0.1", true, "loopback"},
		{"loopback 127.0.0.2", "127.0.0.2", true, "loopback"},
		{"loopback 127.1.2.3", "127.1.2.3", true, "loopback"},
		{"IPv6 loopback ::1", "::1", true, "loopback"},

		// Link-local — blocked (includes cloud IMDS).
		{"link-local AWS IMDS 169.254.169.254", "169.254.169.254", true, "link-local"},
		{"link-local 169.254.1.1", "169.254.1.1", true, "link-local"},
		{"IPv6 link-local fe80::1", "fe80::1", true, "link-local"},

		// Unspecified — blocked.
		{"unspecified 0.0.0.0", "0.0.0.0", true, "unspecified"},
		{"IPv6 unspecified ::", "::", true, "unspecified"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("test setup: unparseable IP literal %q", tc.ip)
			}
			blocked, reason := isICSBlockedIP(ip)
			if blocked != tc.wantBlock {
				t.Errorf("isICSBlockedIP(%s) = blocked:%v reason:%q, want blocked:%v", tc.ip, blocked, reason, tc.wantBlock)
			}
			if tc.wantBlock && tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Errorf("isICSBlockedIP(%s) reason %q does not contain expected substring %q", tc.ip, reason, tc.wantReason)
			}
			if !tc.wantBlock && reason != "" {
				t.Errorf("isICSBlockedIP(%s) unexpectedly returned reason %q for non-blocked IP", tc.ip, reason)
			}
		})
	}
}

// TestIsICSBlockedIP_NilFailsClosed ensures the defensive nil
// branch fails closed rather than allowing a nil IP through.
func TestIsICSBlockedIP_NilFailsClosed(t *testing.T) {
	blocked, reason := isICSBlockedIP(nil)
	if !blocked {
		t.Error("nil IP must block (fail-closed)")
	}
	if reason == "" {
		t.Error("nil IP must return a diagnostic reason")
	}
}

// TestICSLoopbackOnlyDialContext_BlocksLiteralLoopback verifies
// the dial helper refuses a literal loopback IP address even if
// the hostname itself would pass validation (e.g., because it's
// an IP rather than a name). This is the dial-time half of the
// defense in PR #128 — the validator catches "localhost" as a
// string, and this dial catches 127.0.0.x post-resolution.
func TestICSLoopbackOnlyDialContext_BlocksLiteralLoopback(t *testing.T) {
	_, err := icsLoopbackOnlyDialContext(context.Background(), "tcp", "127.0.0.1:12345")
	if err == nil {
		t.Fatal("127.0.0.1 must be blocked at dial time")
	}
	if !strings.Contains(err.Error(), "blocked destination") {
		t.Errorf("error should mention 'blocked destination', got: %v", err)
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error should classify as loopback, got: %v", err)
	}
}

// TestICSLoopbackOnlyDialContext_BlocksLiteralIMDS is the
// headline cloud-metadata exfiltration defense. A malicious DNS
// answer returning 169.254.169.254 must be refused even though
// the hostname passed static validation.
func TestICSLoopbackOnlyDialContext_BlocksLiteralIMDS(t *testing.T) {
	_, err := icsLoopbackOnlyDialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatal("cloud IMDS 169.254.169.254 must be blocked")
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error should classify as link-local (IMDS), got: %v", err)
	}
}

// TestICSLoopbackOnlyDialContext_InvalidAddress verifies the
// helper returns a clear error for malformed addresses instead
// of hanging on DNS.
func TestICSLoopbackOnlyDialContext_InvalidAddress(t *testing.T) {
	_, err := icsLoopbackOnlyDialContext(context.Background(), "tcp", "not-a-valid-addr")
	if err == nil {
		t.Fatal("malformed addr must error")
	}
	if !strings.Contains(err.Error(), "invalid address") {
		t.Errorf("error should mention 'invalid address', got: %v", err)
	}
}

// TestICSLoopbackOnlyDialContext_BlocksLocalhostByName verifies
// that a NAME (not an IP literal) resolving to loopback is
// blocked at dial time. This is the DNS-rebinding defense — the
// hostname itself might have passed static validation at save
// time but resolves to 127.0.0.1 at fetch time.
//
// "localhost" resolves to 127.0.0.1 on virtually every system,
// making it a reliable test input for the resolve-then-classify
// path.
func TestICSLoopbackOnlyDialContext_BlocksLocalhostByName(t *testing.T) {
	_, err := icsLoopbackOnlyDialContext(context.Background(), "tcp", "localhost:12345")
	if err == nil {
		t.Fatal("localhost must resolve to loopback and be blocked")
	}
	if !strings.Contains(err.Error(), "blocked destination") {
		t.Errorf("error should mention 'blocked destination', got: %v", err)
	}
}
