package caldav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Client-side tests for #206: objects the server lists but that cannot
// be read must be reported, never silently dropped.

const reportCal = "/cal/"

type stubObject struct {
	status      int    // GET status; 0 means 200
	body        string // GET body (and MULTIGET calendar-data)
	inMG        bool   // returned by MULTIGET
	errBody     string // GET body on a non-200 status; default "stub failure"
	contentType string // GET Content-Type on a 200; default text/calendar
	// okGets, when > 0, serves only the first okGets GETs normally;
	// every later GET of the object gets a 503.
	okGets int
}

// newReportStub serves a minimal CalDAV calendar at /cal/:
//   - REPORT calendar-query fails (500), forcing the PROPFIND path;
//   - PROPFIND lists every object;
//   - REPORT calendar-multiget returns only the objects with inMG;
//   - GET returns each object's status and body.
func newReportStub(t *testing.T, objects map[string]stubObject) *Client {
	t.Helper()
	var mu sync.Mutex
	gets := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.Method {
		case "REPORT":
			if !strings.Contains(string(body), "calendar-multiget") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var sb strings.Builder
			sb.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`)
			for p, o := range objects {
				if !o.inMG {
					continue
				}
				fmt.Fprintf(&sb, `<D:response><D:href>%s</D:href><D:propstat><D:prop><D:getetag>"mg"</D:getetag><C:calendar-data>%s</C:calendar-data></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, p, o.body)
			}
			sb.WriteString(`</D:multistatus>`)
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, sb.String())
		case "PROPFIND":
			var sb strings.Builder
			sb.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
			fmt.Fprintf(&sb, `<D:response><D:href>%s</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, reportCal)
			for p := range objects {
				fmt.Fprintf(&sb, `<D:response><D:href>%s</D:href><D:propstat><D:prop><D:getcontenttype>text/calendar</D:getcontenttype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, p)
			}
			sb.WriteString(`</D:multistatus>`)
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, sb.String())
		case http.MethodGet:
			o, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			mu.Lock()
			gets[r.URL.Path]++
			n := gets[r.URL.Path]
			mu.Unlock()
			if o.okGets > 0 && n > o.okGets {
				o.status = http.StatusServiceUnavailable
			}
			if o.status != 0 && o.status != http.StatusOK {
				errBody := o.errBody
				if errBody == "" {
					errBody = "stub failure"
				}
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(o.status)
				_, _ = io.WriteString(w, errBody)
				return
			}
			contentType := o.contentType
			if contentType == "" {
				contentType = "text/calendar"
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("ETag", `"get"`)
			_, _ = io.WriteString(w, o.body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL+reportCal, "u", "p")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func eventUIDs(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.UID)
	}
	sort.Strings(out)
	return out
}

// (e) MULTIGET omits a path and the single GET returns 200: the
// recovered event must be in the results.
func TestGetEvents_MultiGetDroppedPathRecoveredByProbeIsReturned(t *testing.T) {
	allowLoopbackDial(t)
	c := newReportStub(t, map[string]stubObject{
		"/cal/a.ics": {body: testICS("a", "A", flowStart), inMG: true},
		"/cal/b.ics": {body: testICS("b", "B", flowStart), inMG: false},
	})
	events, err := c.GetEvents(context.Background(), reportCal, nil)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if got := eventUIDs(events); strings.Join(got, ",") != "a,b" {
		t.Fatalf("GetEvents UIDs = %v, want [a b] (probe-recovered b must not be dropped)", got)
	}
	for _, e := range events {
		if e.UID == "b" && (e.StartTime == "" || e.Summary != "B" || e.Data == "") {
			t.Errorf("recovered event b not fully extracted: %+v", e)
		}
	}
}

func TestGetEventsWithReport_TransientProbeFailureIsReported(t *testing.T) {
	allowLoopbackDial(t)
	c := newReportStub(t, map[string]stubObject{
		"/cal/a.ics": {body: testICS("a", "A", flowStart), inMG: true},
		"/cal/b.ics": {status: http.StatusInternalServerError, inMG: false},
	})
	events, report, err := c.GetEventsWithReport(context.Background(), reportCal, NewMalformedEventCollector())
	if err != nil {
		t.Fatalf("GetEventsWithReport: %v", err)
	}
	if got := eventUIDs(events); strings.Join(got, ",") != "a" {
		t.Fatalf("events = %v, want [a]", got)
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("report.Unreadable = %+v, want exactly /cal/b.ics", report.Unreadable)
	}
	u := report.Unreadable[0]
	if u.Path != "/cal/b.ics" || u.Kind != UnreadableTransient || u.UID != "" {
		t.Errorf("unreadable = %+v, want transient /cal/b.ics with no UID", u)
	}
}

func TestGetEventsWithReport_MalformedObjectReportsRawUID(t *testing.T) {
	allowLoopbackDial(t)
	broken := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:raw-uid@example.com\r\nthis line has no colon\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	c := newReportStub(t, map[string]stubObject{
		"/cal/a.ics":      {body: testICS("a", "A", flowStart), inMG: true},
		"/cal/opaque.ics": {body: broken, inMG: false},
	})
	_, report, err := c.GetEventsWithReport(context.Background(), reportCal, nil)
	if err != nil {
		t.Fatalf("GetEventsWithReport: %v", err)
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("report.Unreadable = %+v, want exactly /cal/opaque.ics", report.Unreadable)
	}
	u := report.Unreadable[0]
	if u.Path != "/cal/opaque.ics" || u.Kind != UnreadableMalformed || u.UID != "raw-uid@example.com" {
		t.Errorf("unreadable = %+v, want malformed /cal/opaque.ics with UID raw-uid@example.com", u)
	}
}

// A malformed object whose raw follow-up GET fails has no recoverable
// UID. Nothing is known about it, so it must be reported as transient
// (which blocks deletion passes), not as malformed-unmapped (which
// does not). (#206 review)
func TestGetEventsWithReport_MalformedObjectWithFailedRawFetchIsTransient(t *testing.T) {
	allowLoopbackDial(t)
	broken := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:raw-uid@example.com\r\nthis line has no colon\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	c := newReportStub(t, map[string]stubObject{
		"/cal/a.ics":      {body: testICS("a", "A", flowStart), inMG: true},
		"/cal/opaque.ics": {body: broken, inMG: false, okGets: 1},
	})
	_, report, err := c.GetEventsWithReport(context.Background(), reportCal, nil)
	if err != nil {
		t.Fatalf("GetEventsWithReport: %v", err)
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("report.Unreadable = %+v, want exactly /cal/opaque.ics", report.Unreadable)
	}
	u := report.Unreadable[0]
	if u.Path != "/cal/opaque.ics" || u.Kind != UnreadableTransient || u.UID != "" {
		t.Errorf("unreadable = %+v, want transient /cal/opaque.ics with no UID", u)
	}
}

func TestGetEvent_OnlyA404IsErrNotFound(t *testing.T) {
	allowLoopbackDial(t)
	c := newReportStub(t, map[string]stubObject{
		"/cal/down.ics": {status: http.StatusServiceUnavailable},
	})
	_, err := c.GetEvent(context.Background(), "/cal/down.ics")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetEvent on 503: err = %v, want a non-ErrNotFound error", err)
	}
	_, err = c.GetEvent(context.Background(), "/cal/missing.ics")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEvent on 404: err = %v, want ErrNotFound", err)
	}
}

// go-webdav's HTTPError.Error() includes the text/* response body, so
// a 5xx body can contain "malformed", "missing colon" or "invalid" +
// "ical". The status must win: anything but a 404 is transient. (#206)
func TestGetEventsWithReport_5xxBodyWithMalformedWordsIsTransient(t *testing.T) {
	allowLoopbackDial(t)
	bodies := []string{
		"backend unavailable: critical invalid state",
		"malformed upstream response",
		"missing colon in proxy config",
	}
	objects := map[string]stubObject{
		"/cal/a.ics": {body: testICS("a", "A", flowStart), inMG: true},
	}
	for i, b := range bodies {
		objects[fmt.Sprintf("/cal/x%d.ics", i)] = stubObject{status: http.StatusServiceUnavailable, errBody: b}
	}
	c := newReportStub(t, objects)

	for i := range bodies {
		path := fmt.Sprintf("/cal/x%d.ics", i)
		_, err := c.GetEvent(context.Background(), path)
		if err == nil || errors.Is(err, ErrMalformedContent) || errors.Is(err, ErrNotFound) {
			t.Errorf("GetEvent(%s) on 503 = %v, want a transient error (not malformed, not not-found)", path, err)
		}
	}

	_, report, err := c.GetEventsWithReport(context.Background(), reportCal, NewMalformedEventCollector())
	if err != nil {
		t.Fatalf("GetEventsWithReport: %v", err)
	}
	if len(report.Unreadable) != len(bodies) {
		t.Fatalf("report.Unreadable = %+v, want %d entries", report.Unreadable, len(bodies))
	}
	for _, u := range report.Unreadable {
		if u.Kind != UnreadableTransient {
			t.Errorf("unreadable %s kind = %s, want transient (err %q)", u.Path, u.Kind, u.Err)
		}
	}
}

// A 200 whose body is empty (go-ical returns io.EOF) or whose
// Content-Type is not text/calendar is a deterministic property of the
// object, not a transient fault: it must be reported as malformed so
// it does not block every deletion pass on every cycle. (#206)
func TestGetEventsWithReport_EmptyOrWrongTypeBodyIsMalformed(t *testing.T) {
	allowLoopbackDial(t)
	c := newReportStub(t, map[string]stubObject{
		"/cal/a.ics":     {body: testICS("a", "A", flowStart), inMG: true},
		"/cal/empty.ics": {body: ""},
		"/cal/html.ics":  {body: "<html>oops</html>", contentType: "text/html"},
	})
	for _, p := range []string{"/cal/empty.ics", "/cal/html.ics"} {
		_, err := c.GetEvent(context.Background(), p)
		if !errors.Is(err, ErrMalformedContent) {
			t.Errorf("GetEvent(%s) = %v, want ErrMalformedContent", p, err)
		}
	}

	_, report, err := c.GetEventsWithReport(context.Background(), reportCal, nil)
	if err != nil {
		t.Fatalf("GetEventsWithReport: %v", err)
	}
	if len(report.Unreadable) != 2 {
		t.Fatalf("report.Unreadable = %+v, want empty.ics and html.ics", report.Unreadable)
	}
	for _, u := range report.Unreadable {
		if u.Kind != UnreadableMalformed {
			t.Errorf("unreadable %s kind = %s, want malformed (err %q)", u.Path, u.Kind, u.Err)
		}
	}
}

func TestExtractUIDFromICS(t *testing.T) {
	tests := []struct {
		name, data, want string
	}{
		{"plain", "BEGIN:VEVENT\r\nUID:abc\r\nEND:VEVENT\r\n", "abc"},
		{"lf only", "BEGIN:VEVENT\nUID:abc\nEND:VEVENT\n", "abc"},
		{"folded", "BEGIN:VEVENT\r\nUID:abc\r\n def\r\nEND:VEVENT\r\n", "abcdef"},
		{"param", "UID;X-FOO=bar:abc\r\n", "abc"},
		{"lowercase name", "uid:abc\r\n", "abc"},
		{"not a prefix match", "UIDX:abc\r\n", ""},
		{"none", "BEGIN:VEVENT\r\nSUMMARY:x\r\n", ""},
		{"empty value", "UID:\r\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractUIDFromICS(tt.data); got != tt.want {
				t.Errorf("extractUIDFromICS = %q, want %q", got, tt.want)
			}
		})
	}
}
