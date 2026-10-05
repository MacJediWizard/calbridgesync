package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ICS feeds carry no ETag, so before this fix every ICS event was
// tracked with an empty SourceETag and shouldUpdateDestFromSource
// returned false forever: feed edits never reached the destination.
// FetchEvents now derives a synthetic ETag from the encoded data with
// DTSTAMP removed (many feeds regenerate DTSTAMP on every request).

const icsETagFeedTemplate = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Test//EN\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:series-1@example.com\r\n" +
	"DTSTAMP:{{STAMP}}\r\n" +
	"DTSTART:20260101T100000Z\r\n" +
	"DTEND:20260101T110000Z\r\n" +
	"RRULE:FREQ=WEEKLY\r\n" +
	"SUMMARY:{{SUMMARY}}\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:single-2@example.com\r\n" +
	"DTSTAMP;VALUE=DATE-TIME:{{STAMP}}\r\n" +
	"DTSTART:20260105T100000Z\r\n" +
	"DTEND:20260105T110000Z\r\n" +
	"SUMMARY:Standalone\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:series-1@example.com\r\n" +
	"RECURRENCE-ID:20260108T100000Z\r\n" +
	"DTSTAMP:{{STAMP}}\r\n" +
	"DTSTART:20260108T120000Z\r\n" +
	"DTEND:20260108T130000Z\r\n" +
	"SUMMARY:Moved occurrence\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func icsETagFeed(stamp, summary string) string {
	return strings.NewReplacer("{{STAMP}}", stamp, "{{SUMMARY}}", summary).Replace(icsETagFeedTemplate)
}

// fetchICSForTest serves body from an httptest server and runs
// FetchEvents against it. The client is built in-package so the
// loopback SSRF dial guard in NewICSClient does not apply.
func fetchICSForTest(t *testing.T, body string) []Event {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &ICSClient{feedURL: srv.URL, httpClient: srv.Client()}
	events, err := c.FetchEvents(context.Background(), nil)
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	return events
}

func etagsByUID(events []Event) map[string]string {
	m := make(map[string]string, len(events))
	for _, e := range events {
		m[e.UID] = e.ETag
	}
	return m
}

func TestICSFetchEvents_SyntheticETagIsSet(t *testing.T) {
	events := fetchICSForTest(t, icsETagFeed("20261001T000000Z", "Weekly"))
	for _, e := range events {
		if !strings.HasPrefix(e.ETag, "ics-") || len(e.ETag) != len("ics-")+64 {
			t.Errorf("UID %s: ETag = %q, want ics-<sha256 hex>", e.UID, e.ETag)
		}
	}
}

func TestICSFetchEvents_DTSTAMPChangeKeepsETag(t *testing.T) {
	a := etagsByUID(fetchICSForTest(t, icsETagFeed("20261001T000000Z", "Weekly")))
	b := etagsByUID(fetchICSForTest(t, icsETagFeed("20261005T123456Z", "Weekly")))
	for uid, etag := range a {
		if b[uid] != etag {
			t.Errorf("UID %s: ETag changed on DTSTAMP-only change (%q -> %q)", uid, etag, b[uid])
		}
	}
}

func TestICSFetchEvents_SummaryChangeChangesETag(t *testing.T) {
	a := etagsByUID(fetchICSForTest(t, icsETagFeed("20261001T000000Z", "Weekly")))
	b := etagsByUID(fetchICSForTest(t, icsETagFeed("20261001T000000Z", "Weekly (renamed)")))
	if a["series-1@example.com"] == b["series-1@example.com"] {
		t.Error("changed SUMMARY must change the series ETag")
	}
	if a["single-2@example.com"] != b["single-2@example.com"] {
		t.Error("an unrelated UID's ETag must not change")
	}
}

// Grouping is a protected region (commit 92e1458): master and
// RECURRENCE-ID exceptions stay in one object, in feed order.
func TestICSFetchEvents_GroupingUnchanged(t *testing.T) {
	events := fetchICSForTest(t, icsETagFeed("20261001T000000Z", "Weekly"))
	var uids []string
	for _, e := range events {
		uids = append(uids, e.UID)
	}
	want := []string{"series-1@example.com", "single-2@example.com"}
	if strings.Join(uids, ",") != strings.Join(want, ",") {
		t.Fatalf("UIDs = %v, want %v", uids, want)
	}
	if n := strings.Count(events[0].Data, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("series object has %d VEVENTs, want 2 (master + exception)", n)
	}
	if !strings.Contains(events[0].Data, "DTSTAMP:") {
		t.Error("DTSTAMP must be stripped only from the hash input, not from Data")
	}
	if events[0].Path != "series-1@example.com.ics" {
		t.Errorf("Path = %q, want unchanged uid+.ics", events[0].Path)
	}
}
