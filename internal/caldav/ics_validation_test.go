package caldav

import (
	"strings"
	"testing"
)

// TestValidateICSFeedURL covers the scheme + host block rules from
// #127. The validator is intentionally narrower than the webhook
// validator (private IPs are still allowed for LAN calendar
// servers) so this table documents exactly which inputs pass and
// which don't.
func TestValidateICSFeedURL(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantErr    bool
		wantErrSub string // optional substring check on the error
	}{
		// Happy path — public HTTPS feeds
		{"public https Google calendar", "https://calendar.google.com/calendar/ical/abc/basic.ics", false, ""},
		{"public https university", "https://schedule.example.edu/events.ics", false, ""},
		{"public http sports schedule", "http://sports.example.com/team.ics", false, ""},

		// LAN / private IP — intentionally allowed (Nextcloud,
		// Radicale, DavMail, etc.)
		{"private 10.0.0.1", "https://10.0.0.1/calendar.ics", false, ""},
		{"private 192.168.1.5", "https://192.168.1.5/cal.ics", false, ""},
		{"private 172.16.0.1", "http://172.16.0.1/cal.ics", false, ""},
		{"RFC 6598 CGNAT 100.64.0.1", "https://100.64.0.1/cal.ics", false, ""},
		{"public 8.8.8.8 via IP", "https://8.8.8.8/cal.ics", false, ""},

		// Scheme rejection
		{"file scheme rejected", "file:///etc/passwd", true, "scheme must be http or https"},
		{"gopher scheme rejected", "gopher://example.com/", true, "scheme must be http or https"},
		{"ftp scheme rejected", "ftp://example.com/cal.ics", true, "scheme must be http or https"},
		{"data scheme rejected", "data:text/plain;base64,SGVsbG8=", true, "scheme must be http or https"},
		{"dict scheme rejected", "dict://example.com/", true, "scheme must be http or https"},
		{"javascript scheme rejected", "javascript:alert(1)", true, "scheme must be http or https"},

		// Localhost rejection — not private, specifically the
		// loopback-or-name cases that are almost always operator
		// typos.
		{"localhost name rejected", "https://localhost/cal.ics", true, "localhost"},
		{"127.0.0.1 rejected", "http://127.0.0.1/cal.ics", true, "localhost"},
		{"IPv6 loopback rejected", "http://[::1]/cal.ics", true, "localhost"},
		{"LOCALHOST uppercase rejected", "https://LOCALHOST/cal.ics", true, "localhost"},

		// mDNS / intranet suffixes
		{".local rejected", "https://server.local/cal.ics", true, ".local"},
		{".internal rejected", "https://api.internal/cal.ics", true, ".internal"},
		{".local uppercase rejected", "https://SERVER.LOCAL/cal.ics", true, ".local"},

		// Malformed
		{"empty URL rejected", "", true, "required"},
		{"no scheme no host", "not-a-url", true, "scheme"},
		{"scheme only", "http://", true, "host"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateICSFeedURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got nil", tc.url)
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSub)
				}
			} else {
				if err != nil {
					t.Errorf("want no error for %q, got: %v", tc.url, err)
				}
			}
		})
	}
}

// TestNewICSClient_RejectsInvalidURLs verifies the validator
// runs at constructor time so callers fail fast with a clear
// error message rather than discovering the problem at HTTP-
// send time.
func TestNewICSClient_RejectsInvalidURLs(t *testing.T) {
	cases := []string{
		"",
		"file:///etc/passwd",
		"http://localhost/cal.ics",
		"https://server.local/cal.ics",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			_, err := NewICSClient(u, "", "")
			if err == nil {
				t.Errorf("NewICSClient(%q) should fail validation", u)
			}
		})
	}
}

// TestNewICSClient_AcceptsLegitimateURLs is the positive
// counterpart: real-world ICS feed URLs must not be rejected.
func TestNewICSClient_AcceptsLegitimateURLs(t *testing.T) {
	cases := []string{
		"https://calendar.google.com/calendar/ical/example/basic.ics",
		"https://nextcloud.example.com/remote.php/dav/public-calendars/abc/?export",
		"http://192.168.1.10:5232/user/calendar.ics", // Radicale on LAN
		"https://10.0.5.2/caldav/personal/export",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			_, err := NewICSClient(u, "", "")
			if err != nil {
				t.Errorf("NewICSClient(%q) should succeed, got error: %v", u, err)
			}
		})
	}
}
