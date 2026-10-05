package caldav

import (
	"strings"
	"testing"
)

// TestIsStrictICSHTTPS covers the env var parsing for the
// STRICT_ICS_HTTPS flag. Uses t.Setenv so each case starts from
// a clean environment regardless of what the host has set. (#131)
func TestIsStrictICSHTTPS(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"false", false},
		{"no", false},
		{"0", false},
		{"off", false},     // not recognized as truthy
		{"garbage", false}, // not recognized
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"1", true},
		{"yes", true},
		{"YES", true},
		{" true ", true}, // whitespace tolerated via TrimSpace
	}
	for _, tc := range cases {
		name := tc.env
		if name == "" {
			name = "(unset)"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("STRICT_ICS_HTTPS", tc.env)
			if got := isStrictICSHTTPS(); got != tc.want {
				t.Errorf("STRICT_ICS_HTTPS=%q → isStrictICSHTTPS() = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestValidateICSFeedURL_StrictHTTPS covers the end-to-end
// behavior of the #131 strict mode: when STRICT_ICS_HTTPS is
// truthy, http:// URLs are rejected; when unset/false, http://
// passes the scheme check.
func TestValidateICSFeedURL_StrictHTTPS(t *testing.T) {
	t.Run("http allowed by default (LAN compatibility)", func(t *testing.T) {
		t.Setenv("STRICT_ICS_HTTPS", "")
		if err := validateICSFeedURL("http://sports.example.com/team.ics"); err != nil {
			t.Errorf("http:// must be allowed by default: %v", err)
		}
	})

	t.Run("http rejected when strict mode is on", func(t *testing.T) {
		t.Setenv("STRICT_ICS_HTTPS", "true")
		err := validateICSFeedURL("http://sports.example.com/team.ics")
		if err == nil {
			t.Fatal("http:// must be rejected when STRICT_ICS_HTTPS=true")
		}
		if !strings.Contains(err.Error(), "STRICT_ICS_HTTPS") {
			t.Errorf("error should mention STRICT_ICS_HTTPS for operator discoverability, got: %v", err)
		}
	})

	t.Run("https always allowed", func(t *testing.T) {
		t.Setenv("STRICT_ICS_HTTPS", "true")
		if err := validateICSFeedURL("https://calendar.google.com/ical/example.ics"); err != nil {
			t.Errorf("https:// must always pass: %v", err)
		}
	})

	t.Run("other rejection rules still fire under strict mode", func(t *testing.T) {
		// Even with strict mode on, the other checks (localhost,
		// .local, file://) should still produce their own errors.
		t.Setenv("STRICT_ICS_HTTPS", "true")

		cases := []string{
			"https://localhost/cal.ics",    // still rejected as localhost
			"https://server.local/cal.ics", // still rejected as .local
			"file:///etc/passwd",           // still rejected as non-http scheme
		}
		for _, u := range cases {
			if err := validateICSFeedURL(u); err == nil {
				t.Errorf("%q must still be rejected under strict mode", u)
			}
		}
	})
}

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

// icsVTimezoneFeed has one event in Europe/Berlin (with a VALARM and a
// RECURRENCE-ID exception whose DTSTART is in America/New_York), one
// UTC-only event, and an unreferenced Asia/Tokyo VTIMEZONE. (#248)
const icsVTimezoneFeed = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:Europe/Berlin\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19701025T030000\r\n" +
	"TZOFFSETFROM:+0200\r\n" +
	"TZOFFSETTO:+0100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\n" +
	"END:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\n" +
	"DTSTART:19700329T020000\r\n" +
	"TZOFFSETFROM:+0100\r\n" +
	"TZOFFSETTO:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n" +
	"END:DAYLIGHT\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:Asia/Tokyo\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19700101T000000\r\n" +
	"TZOFFSETFROM:+0900\r\n" +
	"TZOFFSETTO:+0900\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:America/New_York\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19701101T020000\r\n" +
	"TZOFFSETFROM:-0400\r\n" +
	"TZOFFSETTO:-0500\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:berlin@example.com\r\n" +
	"DTSTAMP:20261001T000000Z\r\n" +
	"DTSTART;TZID=Europe/Berlin:20261012T090000\r\n" +
	"DTEND;TZID=Europe/Berlin:20261012T100000\r\n" +
	"RRULE:FREQ=WEEKLY\r\n" +
	"SUMMARY:Berlin standup\r\n" +
	"BEGIN:VALARM\r\n" +
	"ACTION:DISPLAY\r\n" +
	"DESCRIPTION:Reminder\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"END:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:utc@example.com\r\n" +
	"DTSTAMP:20261001T000000Z\r\n" +
	"DTSTART:20261013T090000Z\r\n" +
	"DTEND:20261013T100000Z\r\n" +
	"SUMMARY:UTC only\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:berlin@example.com\r\n" +
	"DTSTAMP:20261001T000000Z\r\n" +
	"RECURRENCE-ID;TZID=Europe/Berlin:20261019T090000\r\n" +
	"DTSTART;TZID=America/New_York:20261019T090000\r\n" +
	"DTEND;TZID=America/New_York:20261019T100000\r\n" +
	"SUMMARY:Berlin standup (from NY)\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// TestICSFetchEvents_CopiesReferencedVTimezones checks that splitting a
// feed per UID keeps the VTIMEZONE definitions each object references,
// places them before the VEVENTs, and does not copy unreferenced ones.
// Before #248 every object held only VEVENTs, so TZID references
// dangled and strict servers mis-timed or rejected the event.
func TestICSFetchEvents_CopiesReferencedVTimezones(t *testing.T) {
	events := fetchICSForTest(t, icsVTimezoneFeed)
	byUID := make(map[string]Event, len(events))
	for _, e := range events {
		byUID[e.UID] = e
	}

	berlin, ok := byUID["berlin@example.com"]
	if !ok {
		t.Fatalf("berlin@example.com missing from %d events", len(events))
	}
	for _, want := range []string{"TZID:Europe/Berlin", "TZID:America/New_York", "BEGIN:DAYLIGHT", "BEGIN:VALARM"} {
		if !strings.Contains(berlin.Data, want) {
			t.Errorf("berlin object missing %q:\n%s", want, berlin.Data)
		}
	}
	if strings.Contains(berlin.Data, "Asia/Tokyo") {
		t.Errorf("berlin object contains unreferenced Asia/Tokyo VTIMEZONE:\n%s", berlin.Data)
	}
	if got := strings.Count(berlin.Data, "BEGIN:VTIMEZONE"); got != 2 {
		t.Errorf("berlin object has %d VTIMEZONEs, want 2", got)
	}
	if tz, ev := strings.Index(berlin.Data, "BEGIN:VTIMEZONE"), strings.Index(berlin.Data, "BEGIN:VEVENT"); tz < 0 || tz > ev {
		t.Errorf("VTIMEZONE must precede VEVENT (tz=%d, vevent=%d)", tz, ev)
	}
	if got := strings.Count(berlin.Data, "BEGIN:VEVENT"); got != 2 {
		t.Errorf("berlin object has %d VEVENTs, want 2 (master + exception)", got)
	}

	utc, ok := byUID["utc@example.com"]
	if !ok {
		t.Fatalf("utc@example.com missing from %d events", len(events))
	}
	if strings.Contains(utc.Data, "BEGIN:VTIMEZONE") {
		t.Errorf("UTC-only object should carry no VTIMEZONE:\n%s", utc.Data)
	}
}
