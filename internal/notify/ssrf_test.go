package notify

import (
	"context"
	"net"
	"strings"
	"testing"
)

// TestIsBlockedIP covers every classification rule in isBlockedIP
// so the SSRF block-list can't regress silently. If someone adds
// a new IP class later, add a row here too. (#117)
func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		name       string
		ip         string
		wantBlock  bool
		wantReason string
	}{
		// Happy path — public IPs pass.
		{"public IPv4 8.8.8.8", "8.8.8.8", false, ""},
		{"public IPv4 1.1.1.1", "1.1.1.1", false, ""},
		{"public IPv4 172.32.0.1 (NOT RFC 1918)", "172.32.0.1", false, ""},
		{"public IPv6 2606:4700::1111", "2606:4700::1111", false, ""},

		// Loopback — all of 127.0.0.0/8 plus IPv6 ::1.
		{"loopback 127.0.0.1", "127.0.0.1", true, "loopback"},
		{"loopback 127.0.0.2", "127.0.0.2", true, "loopback"},
		{"loopback 127.1.2.3", "127.1.2.3", true, "loopback"},
		{"loopback IPv6 ::1", "::1", true, "loopback"},

		// RFC 1918 private ranges.
		{"private 10.0.0.1", "10.0.0.1", true, "private"},
		{"private 10.255.255.255", "10.255.255.255", true, "private"},
		{"private 172.16.0.1", "172.16.0.1", true, "private"},
		{"private 172.31.255.254", "172.31.255.254", true, "private"},
		{"private 192.168.0.1", "192.168.0.1", true, "private"},
		{"private 192.168.255.254", "192.168.255.254", true, "private"},
		// IPv6 unique-local.
		{"private IPv6 fc00::1", "fc00::1", true, "private"},
		{"private IPv6 fd00::1", "fd00::1", true, "private"},

		// Link-local / cloud metadata.
		{"link-local AWS IMDS 169.254.169.254", "169.254.169.254", true, "link-local"},
		{"link-local 169.254.1.1", "169.254.1.1", true, "link-local"},
		{"IPv6 link-local fe80::1", "fe80::1", true, "link-local"},

		// Unspecified.
		{"unspecified 0.0.0.0", "0.0.0.0", true, "unspecified"},
		{"unspecified IPv6 ::", "::", true, "unspecified"},

		// Carrier-grade NAT.
		{"CGNAT 100.64.0.1", "100.64.0.1", true, "carrier-grade NAT"},
		{"CGNAT 100.127.255.254", "100.127.255.254", true, "carrier-grade NAT"},
		// Boundary: 100.63.x.x is NOT CGNAT, should pass. 100.128.x.x same.
		{"non-CGNAT 100.63.0.1", "100.63.0.1", false, ""},
		{"non-CGNAT 100.128.0.1", "100.128.0.1", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("test setup: unparseable IP literal %q", tc.ip)
			}
			blocked, reason := isBlockedIP(ip)
			if blocked != tc.wantBlock {
				t.Errorf("isBlockedIP(%s) = blocked:%v reason:%q, want blocked:%v", tc.ip, blocked, reason, tc.wantBlock)
			}
			if tc.wantBlock && !strings.Contains(reason, tc.wantReason) {
				t.Errorf("isBlockedIP(%s) reason %q does not contain expected substring %q", tc.ip, reason, tc.wantReason)
			}
			if !tc.wantBlock && reason != "" {
				t.Errorf("isBlockedIP(%s) unexpectedly returned reason %q for a non-blocked IP", tc.ip, reason)
			}
		})
	}
}

// TestIsBlockedIP_NilInput ensures the defensive nil-guard in
// isBlockedIP blocks rather than allows — better to fail closed
// if an internal caller accidentally passes nil.
func TestIsBlockedIP_NilInput(t *testing.T) {
	blocked, reason := isBlockedIP(nil)
	if !blocked {
		t.Error("nil IP must block (fail-closed)")
	}
	if reason == "" {
		t.Error("nil IP must include a reason so the caller can diagnose")
	}
}

// TestSafeDialContext_InvalidAddress verifies the helper returns a
// clear error when given a malformed address, without hanging on a
// DNS lookup.
func TestSafeDialContext_InvalidAddress(t *testing.T) {
	_, err := safeDialContext(context.Background(), "tcp", "not-a-valid-addr")
	if err == nil {
		t.Fatal("want error for malformed addr, got nil")
	}
	if !strings.Contains(err.Error(), "invalid address") {
		t.Errorf("error should mention 'invalid address', got: %v", err)
	}
}

// TestSafeDialContext_BlocksLoopbackByHostname verifies that a
// hostname that resolves to loopback (via the OS resolver) is
// blocked at dial time even if it passed validation earlier. Uses
// "localhost" which resolves to 127.0.0.1 on virtually every system.
//
// This is the DNS-rebinding defense: even if an attacker's DNS
// returned a public IP at validation time, this dial-time check
// would catch a loopback answer at send time.
//
// Note: this test uses the production safeDialContext directly,
// not the package-level webhookDialContext variable (which
// testhelper_test.go sets to a permissive dialer). We want the
// strict version here specifically to verify its rejection logic.
func TestSafeDialContext_BlocksLoopbackByHostname(t *testing.T) {
	_, err := safeDialContext(context.Background(), "tcp", "localhost:12345")
	if err == nil {
		t.Fatal("localhost should resolve to loopback and be blocked")
	}
	if !strings.Contains(err.Error(), "blocked destination") && !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error should mention 'blocked destination' or 'loopback', got: %v", err)
	}
}

// TestSafeDialContext_BlocksLiteralIMDS verifies the cloud IMDS
// address is rejected. This is the headline SSRF win — AWS/GCP/
// Azure metadata services all live at 169.254.169.254 and leaking
// credentials via a misconfigured webhook URL is exactly what this
// defense prevents.
func TestSafeDialContext_BlocksLiteralIMDS(t *testing.T) {
	_, err := safeDialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatal("cloud IMDS should be blocked")
	}
	if !strings.Contains(err.Error(), "blocked destination") {
		t.Errorf("error should mention 'blocked destination', got: %v", err)
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error should classify the block as link-local (IMDS), got: %v", err)
	}
}
