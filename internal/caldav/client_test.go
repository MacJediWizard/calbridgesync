package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

func TestEventDedupeKey(t *testing.T) {
	testCases := []struct {
		name      string
		event     Event
		expected  string
	}{
		{
			name: "combines summary and start time",
			event: Event{
				Summary:   "Team Meeting",
				StartTime: "20240115T140000Z",
			},
			expected: "Team Meeting|20240115T140000Z",
		},
		{
			name: "handles empty summary",
			event: Event{
				Summary:   "",
				StartTime: "20240115T140000Z",
			},
			expected: "|20240115T140000Z",
		},
		{
			name: "handles empty start time",
			event: Event{
				Summary:   "Team Meeting",
				StartTime: "",
			},
			expected: "Team Meeting|",
		},
		{
			name: "handles both empty",
			event: Event{
				Summary:   "",
				StartTime: "",
			},
			expected: "|",
		},
		{
			name: "handles special characters in summary",
			event: Event{
				Summary:   "Meeting: Q1 Review & Planning",
				StartTime: "20240115T140000Z",
			},
			expected: "Meeting: Q1 Review & Planning|20240115T140000Z",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.event.DedupeKey()
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestMalformedEventCollector(t *testing.T) {
	t.Run("creates empty collector", func(t *testing.T) {
		collector := NewMalformedEventCollector()

		if collector == nil {
			t.Fatal("expected non-nil collector")
		}

		if collector.Count() != 0 {
			t.Errorf("expected count 0, got %d", collector.Count())
		}

		events := collector.GetEvents()
		if len(events) != 0 {
			t.Errorf("expected empty events, got %d", len(events))
		}
	})

	t.Run("adds and retrieves events", func(t *testing.T) {
		collector := NewMalformedEventCollector()

		collector.Add("/calendar/event1.ics", "Invalid VCALENDAR format")
		collector.Add("/calendar/event2.ics", "Missing DTSTART")

		if collector.Count() != 2 {
			t.Errorf("expected count 2, got %d", collector.Count())
		}

		events := collector.GetEvents()
		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}

		if events[0].Path != "/calendar/event1.ics" {
			t.Errorf("expected path '/calendar/event1.ics', got %q", events[0].Path)
		}
		if events[0].ErrorMessage != "Invalid VCALENDAR format" {
			t.Errorf("unexpected error message: %q", events[0].ErrorMessage)
		}

		if events[1].Path != "/calendar/event2.ics" {
			t.Errorf("expected path '/calendar/event2.ics', got %q", events[1].Path)
		}
	})

	t.Run("preserves order of events", func(t *testing.T) {
		collector := NewMalformedEventCollector()

		collector.Add("/first.ics", "error 1")
		collector.Add("/second.ics", "error 2")
		collector.Add("/third.ics", "error 3")

		events := collector.GetEvents()

		expectedPaths := []string{"/first.ics", "/second.ics", "/third.ics"}
		for i, path := range expectedPaths {
			if events[i].Path != path {
				t.Errorf("expected path %q at index %d, got %q", path, i, events[i].Path)
			}
		}
	})
}

func TestCalendarStruct(t *testing.T) {
	t.Run("calendar struct has expected fields", func(t *testing.T) {
		cal := Calendar{
			Path:        "/dav/calendars/user/default/",
			Name:        "Default Calendar",
			Description: "My default calendar",
			Color:       "#FF5733",
			SyncToken:   "sync-token-123",
			CTag:        "ctag-456",
		}

		if cal.Path != "/dav/calendars/user/default/" {
			t.Error("Path not set correctly")
		}
		if cal.Name != "Default Calendar" {
			t.Error("Name not set correctly")
		}
		if cal.Description != "My default calendar" {
			t.Error("Description not set correctly")
		}
		if cal.Color != "#FF5733" {
			t.Error("Color not set correctly")
		}
		if cal.SyncToken != "sync-token-123" {
			t.Error("SyncToken not set correctly")
		}
		if cal.CTag != "ctag-456" {
			t.Error("CTag not set correctly")
		}
	})
}

func TestEventStruct(t *testing.T) {
	t.Run("event struct has expected fields", func(t *testing.T) {
		event := Event{
			Path:      "/calendar/event.ics",
			ETag:      "etag-123",
			Data:      "BEGIN:VCALENDAR\nEND:VCALENDAR",
			UID:       "unique-id@example.com",
			Summary:   "Test Event",
			StartTime: "20240115T140000Z",
		}

		if event.Path != "/calendar/event.ics" {
			t.Error("Path not set correctly")
		}
		if event.ETag != "etag-123" {
			t.Error("ETag not set correctly")
		}
		if event.UID != "unique-id@example.com" {
			t.Error("UID not set correctly")
		}
		if event.Summary != "Test Event" {
			t.Error("Summary not set correctly")
		}
	})
}

func TestErrorConstants(t *testing.T) {
	t.Run("error constants are not nil", func(t *testing.T) {
		errors := []error{
			ErrConnectionFailed,
			ErrAuthFailed,
			ErrNotFound,
			ErrInvalidResponse,
			ErrMalformedContent,
		}

		for _, err := range errors {
			if err == nil {
				t.Error("expected error constant to be non-nil")
			}
		}
	})

	t.Run("error messages are descriptive", func(t *testing.T) {
		if ErrConnectionFailed.Error() != "connection failed" {
			t.Errorf("unexpected error message: %q", ErrConnectionFailed.Error())
		}
		if ErrAuthFailed.Error() != "authentication failed" {
			t.Errorf("unexpected error message: %q", ErrAuthFailed.Error())
		}
		if ErrNotFound.Error() != "resource not found" {
			t.Errorf("unexpected error message: %q", ErrNotFound.Error())
		}
	})
}

func TestMalformedEventInfo(t *testing.T) {
	t.Run("struct holds path and error message", func(t *testing.T) {
		info := MalformedEventInfo{
			Path:         "/calendar/broken.ics",
			ErrorMessage: "invalid iCalendar format",
		}

		if info.Path != "/calendar/broken.ics" {
			t.Errorf("expected path '/calendar/broken.ics', got %q", info.Path)
		}
		if info.ErrorMessage != "invalid iCalendar format" {
			t.Errorf("expected error message, got %q", info.ErrorMessage)
		}
	})
}

func TestNewClient(t *testing.T) {
	t.Run("returns error for empty URL", func(t *testing.T) {
		_, err := NewClient("", "user", "pass")
		if err == nil {
			t.Error("expected error for empty URL")
		}
		if !errors.Is(err, ErrConnectionFailed) {
			t.Errorf("expected ErrConnectionFailed, got %v", err)
		}
	})

	t.Run("creates client with valid URL", func(t *testing.T) {
		client, err := NewClient("https://caldav.example.com", "user", "pass")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client == nil {
			t.Fatal("expected non-nil client")
		}
		if client.baseURL != "https://caldav.example.com" {
			t.Errorf("expected baseURL to be set, got %q", client.baseURL)
		}
		if client.username != "user" {
			t.Errorf("expected username 'user', got %q", client.username)
		}
	})
}

func TestClientGetCalendarPath(t *testing.T) {
	testCases := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{
			name:     "extracts path from full URL",
			baseURL:  "https://caldav.example.com/dav/calendars/user/default/",
			expected: "/dav/calendars/user/default/",
		},
		{
			name:     "returns root for URL without path",
			baseURL:  "https://caldav.example.com",
			expected: "/",
		},
		{
			name:     "handles URL with port",
			baseURL:  "https://caldav.example.com:8443/calendars/",
			expected: "/calendars/",
		},
		{
			name:     "handles URL with query string",
			baseURL:  "https://caldav.example.com/cal?user=test",
			expected: "/cal?user=test",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{baseURL: tc.baseURL}
			result := client.GetCalendarPath()
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestClientBuildURL(t *testing.T) {
	testCases := []struct {
		name     string
		baseURL  string
		path     string
		expected string
	}{
		{
			name:     "returns baseURL for empty path",
			baseURL:  "https://caldav.example.com/cal",
			path:     "",
			expected: "https://caldav.example.com/cal",
		},
		{
			name:     "handles absolute path",
			baseURL:  "https://caldav.example.com/cal",
			path:     "/dav/calendars/event.ics",
			expected: "https://caldav.example.com/dav/calendars/event.ics",
		},
		{
			name:     "handles relative path",
			baseURL:  "https://caldav.example.com/cal",
			path:     "event.ics",
			expected: "https://caldav.example.com/cal/event.ics",
		},
		{
			name:     "removes trailing slash from baseURL for relative path",
			baseURL:  "https://caldav.example.com/cal/",
			path:     "event.ics",
			expected: "https://caldav.example.com/cal/event.ics",
		},
		{
			name:     "handles baseURL without path for absolute path",
			baseURL:  "https://caldav.example.com",
			path:     "/calendars/event.ics",
			expected: "https://caldav.example.com/calendars/event.ics",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{baseURL: tc.baseURL}
			result := client.buildURL(tc.path)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestIsMalformedError(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "returns true for ErrMalformedContent",
			err:      ErrMalformedContent,
			expected: true,
		},
		{
			name:     "returns true for wrapped ErrMalformedContent",
			err:      errors.New("malformed calendar data"),
			expected: true,
		},
		{
			name:     "returns true for missing colon error",
			err:      errors.New("line 5: missing colon"),
			expected: true,
		},
		{
			name:     "returns true for invalid ical error",
			err:      errors.New("invalid ical format"),
			expected: true,
		},
		{
			name:     "returns false for connection error",
			err:      ErrConnectionFailed,
			expected: false,
		},
		{
			name:     "returns false for generic error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsMalformedError(tc.err)
			if result != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestParseEventPaths(t *testing.T) {
	t.Run("parses event paths from multistatus response", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/calendars/user/default/</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/calendars/user/default/event1.ics</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/calendars/user/default/event2.ics</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		paths := parseEventPaths([]byte(xmlBody), "/calendars/user/default/")

		if len(paths) != 2 {
			t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
		}
		if paths[0] != "/calendars/user/default/event1.ics" {
			t.Errorf("expected first path to be event1.ics, got %q", paths[0])
		}
		if paths[1] != "/calendars/user/default/event2.ics" {
			t.Errorf("expected second path to be event2.ics, got %q", paths[1])
		}
	})

	t.Run("skips non-ics files", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/calendars/user/default/event.ics</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/calendars/user/default/readme.txt</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/plain</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		paths := parseEventPaths([]byte(xmlBody), "/calendars/user/default/")

		if len(paths) != 1 {
			t.Fatalf("expected 1 path, got %d: %v", len(paths), paths)
		}
	})

	t.Run("handles URL-encoded paths", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/calendars/user/default/event%20with%20spaces.ics</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		paths := parseEventPaths([]byte(xmlBody), "/calendars/user/default/")

		if len(paths) != 1 {
			t.Fatalf("expected 1 path, got %d", len(paths))
		}
		// Should be URL-decoded
		if paths[0] != "/calendars/user/default/event with spaces.ics" {
			t.Errorf("expected decoded path, got %q", paths[0])
		}
	})

	t.Run("returns empty slice for invalid XML", func(t *testing.T) {
		paths := parseEventPaths([]byte("not valid xml"), "/cal/")
		if paths != nil && len(paths) != 0 {
			t.Errorf("expected empty slice, got %v", paths)
		}
	})
}

func TestParseICalendar(t *testing.T) {
	t.Run("parses valid iCalendar data", func(t *testing.T) {
		data := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:test-uid@example.com
DTSTART:20240115T140000Z
DTEND:20240115T150000Z
SUMMARY:Test Event
END:VEVENT
END:VCALENDAR`

		cal, err := parseICalendar(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cal == nil {
			t.Fatal("expected non-nil calendar")
		}

		events := cal.Events()
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
	})

	t.Run("returns error for invalid data", func(t *testing.T) {
		_, err := parseICalendar("not valid icalendar")
		if err == nil {
			t.Error("expected error for invalid data")
		}
	})

	t.Run("returns error for empty data", func(t *testing.T) {
		_, err := parseICalendar("")
		if err == nil {
			t.Error("expected error for empty data")
		}
	})
}

// Note: encodeCalendar tests are omitted because the go-ical library
// encoder behavior varies and the function is tested through integration.

func TestNormalizeStartTime(t *testing.T) {
	t.Run("handles UTC time", func(t *testing.T) {
		prop := &ical.Prop{
			Name:  ical.PropDateTimeStart,
			Value: "20240115T140000Z",
		}

		result := normalizeStartTime(prop)

		if result != "20240115T140000Z" {
			t.Errorf("expected '20240115T140000Z', got %q", result)
		}
	})

	t.Run("handles nil prop", func(t *testing.T) {
		result := normalizeStartTime(nil)
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("handles local time without TZID", func(t *testing.T) {
		prop := &ical.Prop{
			Name:  ical.PropDateTimeStart,
			Value: "20240115T140000",
		}

		result := normalizeStartTime(prop)

		// Should return some value (behavior depends on go-ical library)
		if result == "" {
			t.Error("expected non-empty result")
		}
	})
}

func TestParseGMTOffset(t *testing.T) {
	testCases := []struct {
		name           string
		tzid           string
		expectedOffset int // in seconds
		expectedNil    bool
	}{
		{
			name:           "GMT alone returns UTC",
			tzid:           "GMT",
			expectedOffset: 0,
		},
		{
			name:           "UTC alone returns UTC",
			tzid:           "UTC",
			expectedOffset: 0,
		},
		{
			name:           "GMT-0400",
			tzid:           "GMT-0400",
			expectedOffset: -4 * 3600,
		},
		{
			name:           "GMT+0530",
			tzid:           "GMT+0530",
			expectedOffset: 5*3600 + 30*60,
		},
		{
			name:           "UTC+05:30",
			tzid:           "UTC+05:30",
			expectedOffset: 5*3600 + 30*60,
		},
		{
			name:           "GMT-5 (single digit)",
			tzid:           "GMT-5",
			expectedOffset: -5 * 3600,
		},
		{
			name:           "GMT+10 (two digits)",
			tzid:           "GMT+10",
			expectedOffset: 10 * 3600,
		},
		{
			name:        "invalid format returns nil",
			tzid:        "America/New_York",
			expectedNil: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			loc := parseGMTOffset(tc.tzid)

			if tc.expectedNil {
				if loc != nil {
					t.Errorf("expected nil, got %v", loc)
				}
				return
			}

			if loc == nil {
				t.Fatal("expected non-nil location")
			}

			// Test the offset by checking a specific time
			testTime := time.Date(2024, 1, 15, 12, 0, 0, 0, loc)
			_, offset := testTime.Zone()
			if offset != tc.expectedOffset {
				t.Errorf("expected offset %d, got %d", tc.expectedOffset, offset)
			}
		})
	}
}

func TestSyncResult(t *testing.T) {
	t.Run("struct has expected fields", func(t *testing.T) {
		result := SyncResult{
			Success:           true,
			Message:           "Sync completed",
			Created:           5,
			Updated:           3,
			Deleted:           2,
			Skipped:           1,
			DuplicatesRemoved: 0,
			CalendarsSynced:   2,
			EventsProcessed:   10,
			Errors:            []string{},
			Warnings:          []string{"warning 1"},
			Duration:          5 * time.Second,
		}

		if !result.Success {
			t.Error("expected Success to be true")
		}
		if result.Created != 5 {
			t.Errorf("expected Created 5, got %d", result.Created)
		}
		if result.Updated != 3 {
			t.Errorf("expected Updated 3, got %d", result.Updated)
		}
		if result.Deleted != 2 {
			t.Errorf("expected Deleted 2, got %d", result.Deleted)
		}
		if result.CalendarsSynced != 2 {
			t.Errorf("expected CalendarsSynced 2, got %d", result.CalendarsSynced)
		}
		if len(result.Warnings) != 1 {
			t.Errorf("expected 1 warning, got %d", len(result.Warnings))
		}
	})
}

func TestSanitizeLogDetails(t *testing.T) {
	t.Run("returns empty string for empty input", func(t *testing.T) {
		result := sanitizeLogDetails("")
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("returns short strings unchanged", func(t *testing.T) {
		input := "Synced 10 events successfully"
		result := sanitizeLogDetails(input)
		if result != input {
			t.Errorf("expected %q, got %q", input, result)
		}
	})

	t.Run("truncates very long strings", func(t *testing.T) {
		input := strings.Repeat("a", 3000)
		result := sanitizeLogDetails(input)

		if len(result) > 2100 { // 2000 + some buffer for "... (truncated)"
			t.Errorf("expected truncated result, got length %d", len(result))
		}
		if !strings.Contains(result, "(truncated)") {
			t.Error("expected truncation marker")
		}
	})
}

func TestNewSyncEngine(t *testing.T) {
	t.Run("creates sync engine with nil dependencies", func(t *testing.T) {
		engine := NewSyncEngine(nil, nil)

		if engine == nil {
			t.Fatal("expected non-nil engine")
		}
	})
}

func TestSyncItem(t *testing.T) {
	t.Run("struct has expected fields", func(t *testing.T) {
		item := SyncItem{
			Path: "/calendars/event.ics",
			ETag: "etag-123",
			Data: "BEGIN:VCALENDAR...",
		}

		if item.Path != "/calendars/event.ics" {
			t.Error("Path not set correctly")
		}
		if item.ETag != "etag-123" {
			t.Error("ETag not set correctly")
		}
		if item.Data != "BEGIN:VCALENDAR..." {
			t.Error("Data not set correctly")
		}
	})
}

func TestSyncResponse(t *testing.T) {
	t.Run("struct has expected fields", func(t *testing.T) {
		resp := SyncResponse{
			SyncToken: "sync-token-123",
			Changed:   []SyncItem{{Path: "/event1.ics"}},
			Deleted:   []string{"/event2.ics"},
		}

		if resp.SyncToken != "sync-token-123" {
			t.Error("SyncToken not set correctly")
		}
		if len(resp.Changed) != 1 {
			t.Errorf("expected 1 changed item, got %d", len(resp.Changed))
		}
		if len(resp.Deleted) != 1 {
			t.Errorf("expected 1 deleted item, got %d", len(resp.Deleted))
		}
	})
}

func TestBuildSyncCollectionRequest(t *testing.T) {
	t.Run("builds request without sync token", func(t *testing.T) {
		result := buildSyncCollectionRequest("")

		if !strings.Contains(result, "<D:sync-token/>") {
			t.Error("expected empty sync-token element")
		}
		if !strings.Contains(result, "<D:sync-collection") {
			t.Error("expected sync-collection element")
		}
		if !strings.Contains(result, "<D:getetag/>") {
			t.Error("expected getetag element")
		}
		if !strings.Contains(result, "<C:calendar-data/>") {
			t.Error("expected calendar-data element")
		}
	})

	t.Run("builds request with sync token", func(t *testing.T) {
		result := buildSyncCollectionRequest("http://example.com/sync/token123")

		if !strings.Contains(result, "<D:sync-token>http://example.com/sync/token123</D:sync-token>") {
			t.Error("expected sync-token with value")
		}
	})

	t.Run("escapes special characters in sync token", func(t *testing.T) {
		result := buildSyncCollectionRequest("token<>&'\"")

		if strings.Contains(result, "token<>") {
			t.Error("expected special characters to be escaped")
		}
		if !strings.Contains(result, "&lt;") {
			t.Error("expected < to be escaped")
		}
		if !strings.Contains(result, "&gt;") {
			t.Error("expected > to be escaped")
		}
	})
}

func TestParseSyncResponse(t *testing.T) {
	t.Run("parses response with changed items", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token>http://example.com/sync/token456</D:sync-token>
  <D:response>
    <D:href>/calendars/event1.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-123"</D:getetag>
        <C:calendar-data>BEGIN:VCALENDAR...</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		resp, err := parseSyncResponse([]byte(xmlBody))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if resp.SyncToken != "http://example.com/sync/token456" {
			t.Errorf("expected sync token, got %q", resp.SyncToken)
		}
		if len(resp.Changed) != 1 {
			t.Fatalf("expected 1 changed item, got %d", len(resp.Changed))
		}
		if resp.Changed[0].Path != "/calendars/event1.ics" {
			t.Errorf("expected path, got %q", resp.Changed[0].Path)
		}
	})

	t.Run("parses response with deleted items", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:sync-token>token123</D:sync-token>
  <D:response>
    <D:href>/calendars/deleted-event.ics</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
</D:multistatus>`

		resp, err := parseSyncResponse([]byte(xmlBody))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(resp.Deleted) != 1 {
			t.Fatalf("expected 1 deleted item, got %d", len(resp.Deleted))
		}
		if resp.Deleted[0] != "/calendars/deleted-event.ics" {
			t.Errorf("expected deleted path, got %q", resp.Deleted[0])
		}
	})

	t.Run("returns error for invalid XML", func(t *testing.T) {
		_, err := parseSyncResponse([]byte("not valid xml"))
		if err == nil {
			t.Error("expected error for invalid XML")
		}
	})
}

func TestXmlEscape(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "escapes ampersand",
			input:    "a & b",
			expected: "a &amp; b",
		},
		{
			name:     "escapes less than",
			input:    "a < b",
			expected: "a &lt; b",
		},
		{
			name:     "escapes greater than",
			input:    "a > b",
			expected: "a &gt; b",
		},
		{
			name:     "escapes single quote",
			input:    "it's",
			expected: "it&apos;s",
		},
		{
			name:     "escapes double quote",
			input:    `say "hello"`,
			expected: "say &quot;hello&quot;",
		},
		{
			name:     "escapes all special characters",
			input:    `<tag attr='val' & "test">`,
			expected: "&lt;tag attr=&apos;val&apos; &amp; &quot;test&quot;&gt;",
		},
		{
			name:     "returns empty string unchanged",
			input:    "",
			expected: "",
		},
		{
			name:     "returns plain text unchanged",
			input:    "plain text",
			expected: "plain text",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := xmlEscape(tc.input)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	t.Run("defaultTimeout is 30 seconds", func(t *testing.T) {
		if defaultTimeout != 30*time.Second {
			t.Errorf("expected defaultTimeout to be 30s, got %v", defaultTimeout)
		}
	})

	t.Run("minTLSVersion is TLS 1.2", func(t *testing.T) {
		// TLS 1.2 is 0x0303 = 771
		if minTLSVersion != 771 {
			t.Errorf("expected minTLSVersion to be 771 (TLS 1.2), got %d", minTLSVersion)
		}
	})
}

func TestEncodeCalendar(t *testing.T) {
	t.Run("round trip encodes parsed calendar", func(t *testing.T) {
		// Use parseICalendar to get a valid calendar, then encode it back
		// Note: DTSTAMP is required by RFC 5545 for VEVENT
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Test//EN\r\nBEGIN:VEVENT\r\nUID:test-uid@example.com\r\nDTSTAMP:20240115T120000Z\r\nDTSTART:20240115T140000Z\r\nDTEND:20240115T150000Z\r\nSUMMARY:Test Event\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

		cal, err := parseICalendar(data)
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		result := encodeCalendar(cal)
		if result == "" {
			t.Error("expected non-empty result")
		}

		// Should contain key elements
		if !strings.Contains(result, "VCALENDAR") {
			t.Error("expected VCALENDAR in output")
		}
		if !strings.Contains(result, "VEVENT") {
			t.Error("expected VEVENT in output")
		}
		if !strings.Contains(result, "test-uid@example.com") {
			t.Error("expected UID in output")
		}
	})

	t.Run("returns empty string when encoding fails", func(t *testing.T) {
		// Create a calendar missing required DTSTAMP - encoding should fail
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Test//EN\r\nBEGIN:VEVENT\r\nUID:test-uid@example.com\r\nDTSTART:20240115T140000Z\r\nSUMMARY:Test Event\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

		cal, err := parseICalendar(data)
		if err != nil {
			t.Fatalf("failed to parse: %v", err)
		}

		result := encodeCalendar(cal)
		if result != "" {
			t.Error("expected empty string when encoding fails due to missing DTSTAMP")
		}
	})
}

func TestNormalizeStartTimeWithTZID(t *testing.T) {
	t.Run("handles known IANA timezone", func(t *testing.T) {
		prop := &ical.Prop{
			Name:   ical.PropDateTimeStart,
			Value:  "20240115T140000",
			Params: ical.Params{"TZID": []string{"America/New_York"}},
		}

		result := normalizeStartTime(prop)
		// America/New_York is UTC-5, so 14:00 local = 19:00 UTC
		if result != "20240115T190000Z" {
			t.Errorf("expected '20240115T190000Z', got %q", result)
		}
	})

	t.Run("handles GMT offset timezone", func(t *testing.T) {
		prop := &ical.Prop{
			Name:   ical.PropDateTimeStart,
			Value:  "20240115T140000",
			Params: ical.Params{"TZID": []string{"GMT-0500"}},
		}

		result := normalizeStartTime(prop)
		// GMT-5, so 14:00 local = 19:00 UTC
		if result != "20240115T190000Z" {
			t.Errorf("expected '20240115T190000Z', got %q", result)
		}
	})

	t.Run("handles invalid UTC format gracefully", func(t *testing.T) {
		prop := &ical.Prop{
			Name:  ical.PropDateTimeStart,
			Value: "invalid-dateZ", // Ends with Z but not valid datetime
		}

		result := normalizeStartTime(prop)
		// Should return original value when parsing fails
		if result != "invalid-dateZ" {
			t.Errorf("expected 'invalid-dateZ', got %q", result)
		}
	})

	t.Run("handles invalid timezone with invalid datetime", func(t *testing.T) {
		prop := &ical.Prop{
			Name:   ical.PropDateTimeStart,
			Value:  "invalid-date",
			Params: ical.Params{"TZID": []string{"InvalidTimezone/NoExist"}},
		}

		result := normalizeStartTime(prop)
		// When both IANA and GMT fail, go-ical tries, and if that fails, returns raw value
		if result == "" {
			t.Error("expected non-empty result")
		}
	})

	t.Run("handles datetime without timezone info", func(t *testing.T) {
		prop := &ical.Prop{
			Name:  ical.PropDateTimeStart,
			Value: "20240115T140000", // No Z suffix, no TZID
		}

		result := normalizeStartTime(prop)
		// go-ical will try to parse this as local time and convert to UTC
		if result == "" {
			t.Error("expected non-empty result")
		}
	})

	t.Run("handles valid datetime with invalid TZID fallback to go-ical", func(t *testing.T) {
		prop := &ical.Prop{
			Name:   ical.PropDateTimeStart,
			Value:  "20240115T140000",
			Params: ical.Params{"TZID": []string{"Custom/Timezone"}},
		}

		result := normalizeStartTime(prop)
		// Invalid TZID but valid datetime - go-ical should parse it
		if result == "" {
			t.Error("expected non-empty result")
		}
	})
}

func TestParseGMTOffsetEdgeCases(t *testing.T) {
	testCases := []struct {
		name           string
		tzid           string
		expectedOffset int
		expectedNil    bool
	}{
		{
			name:           "Etc/GMT format",
			tzid:           "Etc/GMT",
			expectedOffset: 0,
		},
		{
			name:           "GMT+0 explicit",
			tzid:           "GMT+0",
			expectedOffset: 0,
		},
		{
			name:           "GMT+100 (three digit)",
			tzid:           "GMT+100",
			expectedOffset: 1*3600 + 0*60,
		},
		{
			name:           "GMT-1",
			tzid:           "GMT-1",
			expectedOffset: -1 * 3600,
		},
		{
			name:           "UTC+12",
			tzid:           "UTC+12",
			expectedOffset: 12 * 3600,
		},
		{
			name:           "UTC-12",
			tzid:           "UTC-12",
			expectedOffset: -12 * 3600,
		},
		{
			name:        "invalid offset length returns nil",
			tzid:        "GMT+12345",
			expectedNil: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			loc := parseGMTOffset(tc.tzid)

			if tc.expectedNil {
				if loc != nil {
					t.Errorf("expected nil, got %v", loc)
				}
				return
			}

			if loc == nil {
				t.Fatal("expected non-nil location")
			}

			testTime := time.Date(2024, 1, 15, 12, 0, 0, 0, loc)
			_, offset := testTime.Zone()
			if offset != tc.expectedOffset {
				t.Errorf("expected offset %d, got %d", tc.expectedOffset, offset)
			}
		})
	}
}

func TestClientStruct(t *testing.T) {
	t.Run("client struct has expected fields after creation", func(t *testing.T) {
		client, err := NewClient("https://caldav.example.com/dav", "user", "pass")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if client.baseURL != "https://caldav.example.com/dav" {
			t.Errorf("expected baseURL to be set")
		}
		if client.username != "user" {
			t.Errorf("expected username to be set")
		}
		if client.password != "pass" {
			t.Errorf("expected password to be set")
		}
		if client.httpClient == nil {
			t.Error("expected httpClient to be set")
		}
		if client.caldavClient == nil {
			t.Error("expected caldavClient to be set")
		}
	})
}

func TestParseEventPathsEdgeCases(t *testing.T) {
	t.Run("skips collection with trailing slash variation", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/calendars/user/default</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/calendars/user/default/event.ics</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		paths := parseEventPaths([]byte(xmlBody), "/calendars/user/default/")

		if len(paths) != 1 {
			t.Fatalf("expected 1 path, got %d: %v", len(paths), paths)
		}
		if paths[0] != "/calendars/user/default/event.ics" {
			t.Errorf("expected event.ics path, got %q", paths[0])
		}
	})

	t.Run("handles empty response", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
</D:multistatus>`

		paths := parseEventPaths([]byte(xmlBody), "/calendars/")

		if len(paths) != 0 {
			t.Errorf("expected 0 paths, got %d", len(paths))
		}
	})

	t.Run("handles calendar content type without .ics extension", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/calendars/event-without-extension</D:href>
    <D:propstat>
      <D:prop><D:getcontenttype>text/calendar; charset=utf-8</D:getcontenttype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		paths := parseEventPaths([]byte(xmlBody), "/calendars/")

		if len(paths) != 1 {
			t.Fatalf("expected 1 path for calendar content type, got %d", len(paths))
		}
	})
}

func TestSanitizeLogDetailsEdgeCases(t *testing.T) {
	t.Run("handles string at max length", func(t *testing.T) {
		input := strings.Repeat("a", 2000)
		result := sanitizeLogDetails(input)
		if result != input {
			t.Error("expected string at max length to be unchanged")
		}
	})

	t.Run("handles string just over max length", func(t *testing.T) {
		input := strings.Repeat("a", 2001)
		result := sanitizeLogDetails(input)

		if len(result) > 2020 {
			t.Errorf("expected result to be truncated, got length %d", len(result))
		}
		if !strings.Contains(result, "(truncated)") {
			t.Error("expected truncation marker")
		}
	})
}

func TestSyncEngineTestConnection(t *testing.T) {
	t.Run("returns error for invalid URL", func(t *testing.T) {
		engine := NewSyncEngine(nil, nil)

		err := engine.TestConnection(context.Background(), "", "user", "pass")
		if err == nil {
			t.Error("expected error for empty URL")
		}
		if !errors.Is(err, ErrConnectionFailed) {
			t.Errorf("expected ErrConnectionFailed, got %v", err)
		}
	})
}

func TestParseSyncResponseEdgeCases(t *testing.T) {
	t.Run("handles response with propstat but no 200 status", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token>token123</D:sync-token>
  <D:response>
    <D:href>/calendars/event.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-123"</D:getetag>
      </D:prop>
      <D:status>HTTP/1.1 403 Forbidden</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		resp, err := parseSyncResponse([]byte(xmlBody))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should not include items with non-200 status
		if len(resp.Changed) != 0 {
			t.Errorf("expected 0 changed items for 403 status, got %d", len(resp.Changed))
		}
	})

	t.Run("handles mixed changed and deleted items", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token>token456</D:sync-token>
  <D:response>
    <D:href>/calendars/event1.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-1"</D:getetag>
        <C:calendar-data>DATA1</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/calendars/deleted.ics</D:href>
    <D:status>HTTP/1.1 404 Not Found</D:status>
  </D:response>
  <D:response>
    <D:href>/calendars/event2.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-2"</D:getetag>
        <C:calendar-data>DATA2</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

		resp, err := parseSyncResponse([]byte(xmlBody))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(resp.Changed) != 2 {
			t.Errorf("expected 2 changed items, got %d", len(resp.Changed))
		}
		if len(resp.Deleted) != 1 {
			t.Errorf("expected 1 deleted item, got %d", len(resp.Deleted))
		}
	})
}

func TestBuildSyncCollectionRequestFormat(t *testing.T) {
	t.Run("request has correct XML structure", func(t *testing.T) {
		result := buildSyncCollectionRequest("")

		if !strings.Contains(result, `xmlns:D="DAV:"`) {
			t.Error("expected DAV namespace")
		}
		if !strings.Contains(result, `xmlns:C="urn:ietf:params:xml:ns:caldav"`) {
			t.Error("expected CalDAV namespace")
		}
		if !strings.Contains(result, `<D:sync-level>1</D:sync-level>`) {
			t.Error("expected sync-level element")
		}
	})
}

func TestIsMalformedErrorEdgeCases(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "lowercase malformed matches",
			err:      errors.New("malformed data"),
			expected: true,
		},
		{
			name:     "uppercase MALFORMED does not match (case sensitive)",
			err:      errors.New("MALFORMED data"),
			expected: false,
		},
		{
			name:     "partial match for missing colon",
			err:      errors.New("line 10: missing colon in value"),
			expected: true,
		},
		{
			name:     "invalid without ical",
			err:      errors.New("invalid data format"),
			expected: false,
		},
		{
			name:     "invalid with ical",
			err:      errors.New("invalid ical data"),
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsMalformedError(tc.err)
			if result != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestMalformedEventCollectorMultipleOperations(t *testing.T) {
	t.Run("handles large number of events", func(t *testing.T) {
		collector := NewMalformedEventCollector()

		for i := 0; i < 100; i++ {
			collector.Add(fmt.Sprintf("/calendar/event%d.ics", i), fmt.Sprintf("Error %d", i))
		}

		if collector.Count() != 100 {
			t.Errorf("expected 100 events, got %d", collector.Count())
		}

		events := collector.GetEvents()
		if len(events) != 100 {
			t.Errorf("expected 100 events from GetEvents, got %d", len(events))
		}
	})
}

func TestSyncResultFields(t *testing.T) {
	t.Run("all fields can be set and retrieved", func(t *testing.T) {
		result := SyncResult{
			Success:           false,
			Message:           "Sync failed",
			Created:           10,
			Updated:           5,
			Deleted:           3,
			Skipped:           2,
			DuplicatesRemoved: 1,
			CalendarsSynced:   4,
			EventsProcessed:   20,
			Errors:            []string{"error1", "error2"},
			Warnings:          []string{"warning1"},
			Duration:          10 * time.Second,
		}

		if result.Success {
			t.Error("expected Success to be false")
		}
		if result.Message != "Sync failed" {
			t.Error("Message not set correctly")
		}
		if result.Created != 10 {
			t.Error("Created not set correctly")
		}
		if result.DuplicatesRemoved != 1 {
			t.Error("DuplicatesRemoved not set correctly")
		}
		if len(result.Errors) != 2 {
			t.Error("Errors not set correctly")
		}
		if len(result.Warnings) != 1 {
			t.Error("Warnings not set correctly")
		}
		if result.Duration != 10*time.Second {
			t.Error("Duration not set correctly")
		}
	})
}

func TestParseICalendarEdgeCases(t *testing.T) {
	t.Run("parses calendar with multiple events", func(t *testing.T) {
		data := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
BEGIN:VEVENT
UID:event1@example.com
DTSTART:20240115T140000Z
SUMMARY:Event 1
END:VEVENT
BEGIN:VEVENT
UID:event2@example.com
DTSTART:20240116T140000Z
SUMMARY:Event 2
END:VEVENT
END:VCALENDAR`

		cal, err := parseICalendar(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		events := cal.Events()
		if len(events) != 2 {
			t.Errorf("expected 2 events, got %d", len(events))
		}
	})

	t.Run("parses calendar with no events", func(t *testing.T) {
		data := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
END:VCALENDAR`

		cal, err := parseICalendar(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		events := cal.Events()
		if len(events) != 0 {
			t.Errorf("expected 0 events, got %d", len(events))
		}
	})
}

func TestEventStructMethods(t *testing.T) {
	t.Run("DedupeKey with pipe in summary", func(t *testing.T) {
		event := Event{
			Summary:   "Meeting | Important",
			StartTime: "20240115T140000Z",
		}

		key := event.DedupeKey()
		expected := "Meeting | Important|20240115T140000Z"
		if key != expected {
			t.Errorf("expected %q, got %q", expected, key)
		}
	})
}

func TestCalendarStructJSON(t *testing.T) {
	t.Run("struct can be used for JSON marshaling", func(t *testing.T) {
		cal := Calendar{
			Path:        "/dav/cal/",
			Name:        "Work",
			Description: "Work calendar",
			Color:       "#0000FF",
			SyncToken:   "token",
			CTag:        "ctag",
		}

		// Verify all fields are accessible
		if cal.Path == "" || cal.Name == "" {
			t.Error("expected fields to be set")
		}
	})
}

func TestObjectsToEvents(t *testing.T) {
	// Create a minimal test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	t.Run("converts empty objects slice", func(t *testing.T) {
		events := client.objectsToEvents(nil)
		if len(events) != 0 {
			t.Errorf("expected 0 events, got %d", len(events))
		}
	})

	t.Run("converts object without data", func(t *testing.T) {
		objects := []caldav.CalendarObject{
			{
				Path: "/calendars/user/default/event1.ics",
				ETag: "etag123",
			},
		}

		events := client.objectsToEvents(objects)
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}

		if events[0].Path != "/calendars/user/default/event1.ics" {
			t.Errorf("expected path, got %q", events[0].Path)
		}
		if events[0].ETag != "etag123" {
			t.Errorf("expected etag, got %q", events[0].ETag)
		}
		if events[0].Data != "" {
			t.Errorf("expected empty data, got %q", events[0].Data)
		}
	})

	t.Run("converts object with valid calendar data", func(t *testing.T) {
		// Create a calendar with a valid event
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//Test//EN\r\nBEGIN:VEVENT\r\nUID:test-uid@example.com\r\nDTSTAMP:20240115T120000Z\r\nDTSTART:20240115T140000Z\r\nDTEND:20240115T150000Z\r\nSUMMARY:Test Meeting\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		cal, err := parseICalendar(data)
		if err != nil {
			t.Fatalf("failed to parse calendar: %v", err)
		}

		objects := []caldav.CalendarObject{
			{
				Path: "/calendars/user/default/event1.ics",
				ETag: "etag123",
				Data: cal,
			},
		}

		events := client.objectsToEvents(objects)
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}

		event := events[0]
		if event.UID != "test-uid@example.com" {
			t.Errorf("expected UID 'test-uid@example.com', got %q", event.UID)
		}
		if event.Summary != "Test Meeting" {
			t.Errorf("expected Summary 'Test Meeting', got %q", event.Summary)
		}
		if event.StartTime == "" {
			t.Error("expected StartTime to be set")
		}
		if event.Data == "" {
			t.Error("expected Data to be set")
		}
	})

	t.Run("converts multiple objects", func(t *testing.T) {
		data1 := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:uid1@test.com\r\nDTSTAMP:20240115T120000Z\r\nDTSTART:20240115T100000Z\r\nSUMMARY:Event 1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		data2 := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:uid2@test.com\r\nDTSTAMP:20240115T120000Z\r\nDTSTART:20240116T100000Z\r\nSUMMARY:Event 2\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

		cal1, _ := parseICalendar(data1)
		cal2, _ := parseICalendar(data2)

		objects := []caldav.CalendarObject{
			{Path: "/event1.ics", ETag: "e1", Data: cal1},
			{Path: "/event2.ics", ETag: "e2", Data: cal2},
		}

		events := client.objectsToEvents(objects)
		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}

		if events[0].UID != "uid1@test.com" {
			t.Errorf("event 0 UID mismatch: %q", events[0].UID)
		}
		if events[1].UID != "uid2@test.com" {
			t.Errorf("event 1 UID mismatch: %q", events[1].UID)
		}
	})
}

func TestClientWithTestServer(t *testing.T) {
	t.Run("TestConnection returns error for server returning 401", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "user", "wrongpass")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		err = client.TestConnection(context.Background())
		if err == nil {
			t.Error("expected error for unauthorized response")
		}
	})

	t.Run("TestConnection returns error for server returning 500", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "user", "pass")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		err = client.TestConnection(context.Background())
		if err == nil {
			t.Error("expected error for server error response")
		}
	})

	t.Run("FindCalendars returns error for invalid response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "user", "pass")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		_, err = client.FindCalendars(context.Background())
		if err == nil {
			t.Error("expected error for server error response")
		}
	})
}
