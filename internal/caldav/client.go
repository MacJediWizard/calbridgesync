package caldav

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"golang.org/x/oauth2"
)

var (
	ErrConnectionFailed = errors.New("connection failed")
	ErrAuthFailed       = errors.New("authentication failed")
	ErrNotFound         = errors.New("resource not found")
	ErrInvalidResponse  = errors.New("invalid server response")
	ErrMalformedContent = errors.New("malformed calendar content")
	// ErrEventSkipped indicates that PutEvent intentionally did NOT write
	// the event to the destination (empty data, missing UID, etc.). This
	// is distinct from a connection/auth/write failure. Callers that
	// treat all non-nil errors as failures will show these events in
	// warnings, which is wrong — they are skips, not errors. Use
	// errors.Is(err, ErrEventSkipped) to distinguish.
	ErrEventSkipped = errors.New("event skipped")
)

const (
	defaultTimeout = 300 * time.Second // CALDAV_REQUEST_TIMEOUT default: 5 minutes for slow CalDAV servers like iCloud
	minTLSVersion  = tls.VersionTLS12

	// maxCalDAVResponseSize caps every io.ReadAll over a CalDAV
	// response body so a misbehaving or malicious CalDAV server
	// cannot force the daemon to allocate unbounded memory. The
	// cap is generous (200 MB) — real PROPFIND / REPORT responses
	// for a 5000-event calendar are typically in the low single-
	// digit MB range, so this is five to ten orders of magnitude
	// larger than any legitimate response we'd expect. The point
	// is to catch the runaway case (infinite response, slowloris-
	// style trickle, zip bomb) without false-flagging any real
	// user workload. Matches the existing maxICSResponseSize
	// convention in ics_client.go (which uses 50 MB for ICS feeds). (#119)
	maxCalDAVResponseSize = 200 * 1024 * 1024
)

// requestTimeoutNanos holds the per-request HTTP timeout used by
// NewClient, NewOAuthClient and NewICSClient. It is set once at startup
// from CALDAV_REQUEST_TIMEOUT (SetRequestTimeout) and read when each
// client is built; atomic because sync goroutines build clients. (#239)
var requestTimeoutNanos atomic.Int64

func init() { requestTimeoutNanos.Store(int64(defaultTimeout)) }

// SetRequestTimeout sets the HTTP timeout for CalDAV and ICS clients
// created after the call. Non-positive values are ignored.
func SetRequestTimeout(d time.Duration) {
	if d > 0 {
		requestTimeoutNanos.Store(int64(d))
	}
}

func requestTimeout() time.Duration { return time.Duration(requestTimeoutNanos.Load()) }

// Calendar represents a CalDAV calendar.
type Calendar struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

// Event represents a calendar event.
type Event struct {
	Path      string `json:"path"`
	ETag      string `json:"etag"`
	Data      string `json:"data"` // iCalendar data
	UID       string `json:"uid"`
	Summary   string `json:"summary"`
	StartTime string `json:"start_time"` // DTSTART value for deduplication
}

// DedupeKey returns a key for deduplication based on summary and start time.
func (e *Event) DedupeKey() string {
	return e.Summary + "|" + e.StartTime
}

// MalformedEventInfo contains information about a corrupted calendar event.
type MalformedEventInfo struct {
	Path         string
	ErrorMessage string
}

// MalformedEventCollector collects malformed events during sync operations.
type MalformedEventCollector struct {
	events []MalformedEventInfo
}

// NewMalformedEventCollector creates a new collector.
func NewMalformedEventCollector() *MalformedEventCollector {
	return &MalformedEventCollector{
		events: make([]MalformedEventInfo, 0),
	}
}

// Add records a malformed event.
func (c *MalformedEventCollector) Add(path, errorMessage string) {
	c.events = append(c.events, MalformedEventInfo{
		Path:         path,
		ErrorMessage: errorMessage,
	})
}

// GetEvents returns all collected malformed events.
func (c *MalformedEventCollector) GetEvents() []MalformedEventInfo {
	return c.events
}

// Count returns the number of collected malformed events.
func (c *MalformedEventCollector) Count() int {
	return len(c.events)
}

// UnreadableKind says why a listed calendar object could not be read.
type UnreadableKind string

const (
	// UnreadableTransient: the fetch itself failed (5xx, timeout,
	// connection reset). The object may be perfectly fine.
	UnreadableTransient UnreadableKind = "transient"
	// UnreadableMalformed: the server returned the object but its
	// iCalendar body was malformed or empty.
	UnreadableMalformed UnreadableKind = "malformed"
)

// UnreadableObject is a calendar object the server listed but that
// could not be turned into an Event. (#206)
type UnreadableObject struct {
	Path string
	Kind UnreadableKind
	// UID recovered from a raw "UID:" line of a malformed or empty
	// object, when one exists. Always empty for transient failures.
	UID string
	Err string
}

// FetchReport describes what a GetEventsWithReport call saw but could
// not return. The sync engine uses it so an object that failed to load
// is never mistaken for one that was deleted. (#206)
type FetchReport struct {
	Unreadable []UnreadableObject
}

func (r *FetchReport) add(path string, kind UnreadableKind, reason string) {
	if r == nil {
		return
	}
	r.Unreadable = append(r.Unreadable, UnreadableObject{Path: path, Kind: kind, Err: reason})
}

// Client provides CalDAV operations.
type Client struct {
	baseURL      string
	username     string
	password     string
	httpClient   *http.Client
	caldavClient *caldav.Client
	// tokenSource is set only for OAuth clients (NewOAuthClient). It is
	// the same ReuseTokenSource the HTTP transport uses, so calling
	// Token() on it to probe credentials does not cause an extra
	// refresh on the next request. (#192)
	tokenSource oauth2.TokenSource
}

// NewClient creates a new CalDAV client.
func NewClient(baseURL, username, password string) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("%w: base URL is required", ErrConnectionFailed)
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: minTLSVersion,
		},
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         dialCalDAV, // SSRF guard (#200)
	}

	httpClient := &http.Client{
		Timeout:   requestTimeout(),
		Transport: transport,
	}

	caldavClient, err := caldav.NewClient(
		webdav.HTTPClientWithBasicAuth(httpClient, username, password),
		baseURL,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create CalDAV client: %w", ErrConnectionFailed, err)
	}

	return &Client{
		baseURL:      baseURL,
		username:     username,
		password:     password,
		httpClient:   httpClient,
		caldavClient: caldavClient,
	}, nil
}

// TestConnection tests the connection to the CalDAV server.
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.caldavClient.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	return nil
}

// TestConnectionGoogle tests a Google CalDAV connection by listing
// calendars directly, since Google doesn't support the standard
// FindCurrentUserPrincipal PROPFIND. (#160)
//
// For OAuth clients it first forces a token refresh, because
// FindCalendarsGoogle makes no network call: without this probe a
// revoked refresh token was only discovered inside GetEvents, where
// it was flattened into a generic per-calendar error and never
// recognized as an auth failure. (#192)
func (c *Client) TestConnectionGoogle(ctx context.Context) error {
	if c.tokenSource != nil {
		if _, err := c.tokenSource.Token(); err != nil {
			return classifyTokenError(err)
		}
	}
	_, err := c.FindCalendarsGoogle(ctx)
	return err
}

// FindCalendars discovers all calendars for the current user.
func (c *Client) FindCalendars(ctx context.Context) ([]Calendar, error) {
	principal, err := c.caldavClient.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to find principal: %w", ErrConnectionFailed, err)
	}

	homeSet, err := c.caldavClient.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to find home set: %w", ErrConnectionFailed, err)
	}

	cals, err := c.caldavClient.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to find calendars: %w", ErrConnectionFailed, err)
	}

	calendars := make([]Calendar, 0, len(cals))
	for _, cal := range cals {
		calendars = append(calendars, Calendar{
			Path:        cal.Path,
			Name:        cal.Name,
			Description: cal.Description,
		})
	}

	return calendars, nil
}

// FindCalendarsGoogle returns the known calendar for a Google CalDAV source.
// Google doesn't support standard CalDAV discovery (neither principal nor
// calendar home set PROPFIND), so we construct the calendar path directly.
// Google's primary calendar is always at /caldav/v2/{email}/events/.
//
// The baseURL is expected to be:
//
//	https://apidata.googleusercontent.com/caldav/v2/{email}/user
//
// We derive the events path by replacing "/user" with "/events/". (#160)
func (c *Client) FindCalendarsGoogle(ctx context.Context) ([]Calendar, error) {
	// Extract just the path portion from the full base URL.
	// baseURL: https://apidata.googleusercontent.com/caldav/v2/{email}/user
	// We need: /caldav/v2/{email}/events/
	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to parse base URL: %w", ErrConnectionFailed, err)
	}
	eventsPath := strings.TrimSuffix(parsed.Path, "/user")
	eventsPath = strings.TrimSuffix(eventsPath, "/")
	eventsPath += "/events/"

	return []Calendar{{
		Path: eventsPath,
		Name: "Google Calendar",
	}}, nil
}

// GetEvents retrieves all events from a calendar.
// If collector is provided, malformed events will be recorded there.
// Objects that could not be read are left out; callers that make
// deletion decisions must use GetEventsWithReport instead. (#206)
func (c *Client) GetEvents(ctx context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, error) {
	events, _, err := c.GetEventsWithReport(ctx, calendarPath, collector)
	return events, err
}

// GetEventsWithReport retrieves all events from a calendar and reports
// every listed object it could not read (transient fetch failure, or
// malformed/empty content). An object in the report is NOT absent from
// the calendar, so it must never be treated as deleted. (#206)
func (c *Client) GetEventsWithReport(ctx context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, FetchReport, error) {
	// Try the standard calendar-query first
	var report FetchReport
	events, err := c.getEventsViaQuery(ctx, calendarPath, &report)
	if err != nil || len(events) == 0 {
		// If query failed (412, etc.) OR returned 0 events, fall back to PROPFIND
		// Some servers (like SOGo) may return empty results from REPORT but have events accessible via PROPFIND
		if err != nil {
			log.Printf("Calendar query failed, trying PROPFIND fallback: %v", err)
		} else {
			log.Printf("Calendar query returned 0 events, trying PROPFIND fallback for path: %s", calendarPath)
		}
		report = FetchReport{}
		events, err = c.getEventsViaPropfind(ctx, calendarPath, collector, &report)
		if err != nil {
			return nil, FetchReport{}, err
		}
	}

	// A malformed or empty object has no parsed UID. Try a raw "UID:"
	// line so the sync engine can still tell which event it is. If that
	// raw GET fails, nothing is known about the object (the first
	// response may not even have been the real object), so it is
	// reported as transient, which blocks deletions this cycle.
	for i := range report.Unreadable {
		u := &report.Unreadable[i]
		if u.Kind != UnreadableMalformed {
			continue
		}
		raw, rawErr := c.fetchRawEvent(ctx, u.Path)
		if rawErr != nil {
			u.Kind = UnreadableTransient
			u.Err = fmt.Sprintf("%s; raw re-fetch failed: %v", u.Err, rawErr)
			continue
		}
		u.UID = extractUIDFromICS(raw)
	}
	return events, report, nil
}

// extractUIDFromICS returns the value of the first UID property in raw
// iCalendar text, or "" if there is none. It does not need the rest of
// the object to parse, so it works on content go-ical rejects. (#206)
func extractUIDFromICS(data string) string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	// Unfold continuation lines (RFC 5545 section 3.1).
	data = strings.ReplaceAll(data, "\n ", "")
	data = strings.ReplaceAll(data, "\n\t", "")
	for _, line := range strings.Split(data, "\n") {
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		name := line[:colon]
		if semi := strings.Index(name, ";"); semi >= 0 {
			name = name[:semi]
		}
		if strings.EqualFold(strings.TrimSpace(name), "UID") {
			return strings.TrimSpace(line[colon+1:])
		}
	}
	return ""
}

// httpStatusCode returns the status of a go-webdav HTTPError anywhere
// in err's chain. That type is internal, so it is matched by its
// Error() format, "<code> <status text>[: <cause>]". The cause can
// include up to 1KB of a text/* response body. (#206)
func httpStatusCode(err error) (int, bool) {
	for ; err != nil; err = errors.Unwrap(err) {
		s := err.Error()
		if len(s) < 4 || s[3] != ' ' {
			continue
		}
		code, convErr := strconv.Atoi(s[:3])
		if convErr != nil || code < 100 || code > 599 {
			continue
		}
		prefix := fmt.Sprintf("%d %s", code, http.StatusText(code))
		if s == prefix || strings.HasPrefix(s, prefix+": ") {
			return code, true
		}
	}
	return 0, false
}

// isTransportError reports whether err came from the network or the
// request context rather than from the response content.
func isTransportError(err error) bool {
	var urlErr *url.Error
	var netErr net.Error
	return errors.As(err, &urlErr) ||
		errors.As(err, &netErr) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// isMalformedBodyError reports whether a GetCalendarObject error that
// is neither an HTTP status nor a transport failure describes the body
// of a 200 response: an empty body (go-ical returns io.EOF), a missing
// or wrong Content-Type, or an iCalendar parse error.
func isMalformedBodyError(err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	errStr := err.Error()
	return strings.HasPrefix(errStr, "mime: ") ||
		strings.Contains(errStr, "expected Content-Type") ||
		strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "missing colon") ||
		(strings.Contains(errStr, "invalid") && strings.Contains(errStr, "ical"))
}

// getEventsViaQuery uses REPORT calendar-query to get events.
func (c *Client) getEventsViaQuery(ctx context.Context, calendarPath string, report *FetchReport) ([]Event, error) {
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR",
			Comps: []caldav.CalendarCompRequest{
				{Name: "VEVENT"},
			},
		},
	}

	objects, err := c.caldavClient.QueryCalendar(ctx, calendarPath, query)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to query calendar: %w", ErrConnectionFailed, err)
	}

	return c.objectsToEvents(objects, report), nil
}

// getEventsViaPropfind uses PROPFIND to list calendar objects, then fetches each one.
func (c *Client) getEventsViaPropfind(ctx context.Context, calendarPath string, collector *MalformedEventCollector, report *FetchReport) ([]Event, error) {
	// Go directly to PROPFIND list since MultiGetCalendar requires specific paths
	return c.getEventsViaList(ctx, calendarPath, collector, report)
}

// getEventsViaList lists calendar contents and fetches events using batch MULTIGET.
func (c *Client) getEventsViaList(ctx context.Context, calendarPath string, collector *MalformedEventCollector, report *FetchReport) ([]Event, error) {
	// Build the full URL - calendarPath might be absolute or relative
	fullURL := c.buildURL(calendarPath)

	// Make a simple PROPFIND request to list contents
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", fullURL, strings.NewReader(`<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:getetag/>
    <D:getcontenttype/>
  </D:prop>
</D:propfind>`))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrInvalidResponse, resp.StatusCode)
	}

	// Parse the multistatus response to get event paths. Wrapped
	// in io.LimitReader so a malicious or misbehaving CalDAV
	// server cannot force an unbounded allocation. (#119)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCalDAVResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("PROPFIND response for %s: status=%d, body_len=%d", fullURL, resp.StatusCode, len(body))
	eventPaths := parseEventPaths(body, calendarPath)
	log.Printf("Parsed %d event paths from PROPFIND response (calendarPath=%s)", len(eventPaths), calendarPath)

	if len(eventPaths) == 0 {
		return []Event{}, nil
	}

	// Use batch MULTIGET to fetch events efficiently (50 events per batch)
	const batchSize = 50
	events := make([]Event, 0, len(eventPaths))
	skippedMalformed := 0
	skippedEmpty := 0
	total := len(eventPaths)

	for batchStart := 0; batchStart < total; batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > total {
			batchEnd = total
		}
		batchPaths := eventPaths[batchStart:batchEnd]

		log.Printf("Fetching events batch: %d-%d of %d (%.0f%%)", batchStart+1, batchEnd, total, float64(batchEnd)/float64(total)*100)

		// Try MULTIGET for this batch
		batchEvents, malformed, empty, err := c.getEventsBatch(ctx, calendarPath, batchPaths, collector, report)
		if err != nil {
			// If MULTIGET fails, fall back to individual fetches for this batch
			log.Printf("MULTIGET failed, falling back to individual fetches: %v", err)
			batchEvents, malformed, empty = c.getEventsIndividually(ctx, batchPaths, collector, report)
		}

		events = append(events, batchEvents...)
		skippedMalformed += malformed
		skippedEmpty += empty
	}

	log.Printf("Fetched %d events complete", len(events))
	if skippedMalformed > 0 {
		log.Printf("Skipped %d malformed events (corrupted at source)", skippedMalformed)
	}
	if skippedEmpty > 0 {
		log.Printf("Skipped %d empty events (no iCalendar data)", skippedEmpty)
	}

	return events, nil
}

// normalizeMultiGetPath returns a canonical form of a CalDAV object path
// suitable for equality comparison between a request path (which may be
// URL-decoded by parseEventPaths) and a response path (which may be
// URL-encoded, or have a trailing slash added by some servers).
//
// The comparison is intentionally loose: percent-escapes are decoded,
// trailing slashes are trimmed. Case is preserved since CalDAV paths
// can be case-sensitive depending on the server.
func normalizeMultiGetPath(p string) string {
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return strings.TrimRight(p, "/")
}

// findDroppedMultiGetPaths returns the subset of requestedPaths that do
// not appear in returnedPaths, using normalized comparison. Used to
// detect paths that the go-webdav library silently dropped during a
// MultiGetCalendar call — typically because the library could not parse
// that entry's response and discarded it without reporting.
//
// Exposed (unexported but free function) so it can be unit-tested
// without touching the real CalDAV client.
func findDroppedMultiGetPaths(requestedPaths, returnedPaths []string) []string {
	if len(requestedPaths) == 0 {
		return nil
	}
	returned := make(map[string]bool, len(returnedPaths))
	for _, p := range returnedPaths {
		returned[normalizeMultiGetPath(p)] = true
	}
	var dropped []string
	for _, p := range requestedPaths {
		if !returned[normalizeMultiGetPath(p)] {
			dropped = append(dropped, p)
		}
	}
	return dropped
}

// getEventsBatch fetches a batch of events using MULTIGET.
//
// Beyond the obvious "fetch these paths", this function also detects and
// reports paths that the go-webdav library silently drops from the
// response. The library occasionally discards entries it cannot parse,
// and those paths vanish without any error or log — they're simply
// missing from the returned objects slice. Before this function detected
// that, malformed events at the protocol level were completely invisible
// to users: the dashboard's "Corrupted Events" card stayed empty while
// real corruption was silently losing data.
//
// For each dropped path, we fall back to an individual GetEvent call,
// which returns a concrete error that we can record in the
// MalformedEventCollector. That's one extra HTTP round-trip per dropped
// path, but only for paths that are genuinely problematic — in normal
// operation the fallback loop doesn't execute at all.
func (c *Client) getEventsBatch(ctx context.Context, calendarPath string, paths []string, collector *MalformedEventCollector, report *FetchReport) ([]Event, int, int, error) {
	multiGet := &caldav.CalendarMultiGet{
		Paths: paths,
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR",
			Comps: []caldav.CalendarCompRequest{
				{Name: "VEVENT"},
			},
		},
	}

	objects, err := c.caldavClient.MultiGetCalendar(ctx, calendarPath, multiGet)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("MULTIGET failed: %w", err)
	}

	events := make([]Event, 0, len(objects))
	skippedMalformed := 0
	skippedEmpty := 0

	for _, obj := range objects {
		event := Event{
			Path: obj.Path,
			ETag: obj.ETag,
		}

		if obj.Data == nil {
			if collector != nil {
				collector.Add(obj.Path, "nil iCalendar data - event may be corrupted or deleted")
			}
			report.add(obj.Path, UnreadableMalformed, "nil iCalendar data")
			skippedEmpty++
			continue
		}

		data, encErr := encodeCalendar(obj.Data)
		if encErr != nil {
			// Encode failure is a form of malformed content — the go-ical
			// encoder rejected calendar data that the library itself had
			// just parsed. Track it distinctly from the "empty data" case
			// so the dashboard can surface the real cause.
			if collector != nil {
				collector.Add(obj.Path, fmt.Sprintf("failed to encode event: %v", encErr))
			}
			report.add(obj.Path, UnreadableMalformed, fmt.Sprintf("failed to encode event: %v", encErr))
			skippedMalformed++
			continue
		}
		event.Data = data

		for _, evt := range obj.Data.Events() {
			if uid, err := evt.Props.Text(ical.PropUID); err == nil {
				event.UID = uid
			}
			if summary, err := evt.Props.Text(ical.PropSummary); err == nil {
				event.Summary = summary
			}
			if dtstart := evt.Props.Get(ical.PropDateTimeStart); dtstart != nil {
				event.StartTime = normalizeStartTime(dtstart)
			}
		}

		events = append(events, event)
	}

	// Detect paths that MULTIGET silently dropped and probe them
	// individually. See the function doc comment for the full rationale.
	returnedPaths := make([]string, 0, len(objects))
	for _, obj := range objects {
		returnedPaths = append(returnedPaths, obj.Path)
	}
	dropped := findDroppedMultiGetPaths(paths, returnedPaths)
	if len(dropped) > 0 {
		log.Printf("MULTIGET response missing %d of %d requested paths; probing individually to classify", len(dropped), len(paths))
	}
	for _, missingPath := range dropped {
		recovered, probeErr := c.GetEvent(ctx, missingPath)
		if probeErr != nil {
			// Got a concrete error from the individual fetch. Record it
			// in the collector so the user sees it on the dashboard.
			if collector != nil {
				collector.Add(missingPath, fmt.Sprintf("MULTIGET silently dropped this event; individual fetch returned: %v", probeErr))
			}
			reportFetchError(report, missingPath, probeErr)
			skippedMalformed++
			continue
		}
		if recovered.Data == "" {
			if collector != nil {
				collector.Add(missingPath, "MULTIGET silently dropped this event; individual fetch returned empty iCalendar data")
			}
			report.add(missingPath, UnreadableMalformed, "empty iCalendar data")
			skippedEmpty++
			continue
		}
		// Individual fetch succeeded where MULTIGET didn't. Return the
		// recovered event: GetEvent already ran the same UID, Summary
		// and StartTime extraction. Dropping it made the event look
		// deleted for a whole cycle. (#206)
		log.Printf("MULTIGET dropped %s but individual GetEvent succeeded; using the individually fetched copy", missingPath)
		events = append(events, *recovered)
	}

	return events, skippedMalformed, skippedEmpty, nil
}

// reportFetchError records a failed single-object GET in report. A 404
// means the object really is gone (deleted after the PROPFIND listing),
// so it is not reported. (#206)
func reportFetchError(report *FetchReport, path string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		return
	case errors.Is(err, ErrMalformedContent):
		// Sentinel only: IsMalformedError also matches on error text,
		// which for an HTTP error can be the response body. (#206)
		report.add(path, UnreadableMalformed, err.Error())
	default:
		report.add(path, UnreadableTransient, err.Error())
	}
}

// getEventsIndividually fetches events one by one (fallback for servers that don't support MULTIGET).
func (c *Client) getEventsIndividually(ctx context.Context, paths []string, collector *MalformedEventCollector, report *FetchReport) ([]Event, int, int) {
	events := make([]Event, 0, len(paths))
	skippedMalformed := 0
	skippedEmpty := 0

	for _, path := range paths {
		event, err := c.GetEvent(ctx, path)
		if err != nil {
			if errors.Is(err, ErrMalformedContent) {
				if collector != nil {
					collector.Add(path, err.Error())
				}
				reportFetchError(report, path, err)
				skippedMalformed++
				continue
			}
			log.Printf("Failed to fetch event %s: %v", path, err)
			reportFetchError(report, path, err)
			continue
		}
		if event.Data == "" {
			if collector != nil {
				collector.Add(path, "empty iCalendar data - event may be corrupted or deleted")
			}
			report.add(path, UnreadableMalformed, "empty iCalendar data")
			skippedEmpty++
			continue
		}
		events = append(events, *event)
	}

	return events, skippedMalformed, skippedEmpty
}

// objectsToEvents converts CalDAV objects to Events. Events that fail to
// encode (or have nil data) are skipped and logged — the returned slice
// may have fewer entries than the input. This is safer than the prior
// behavior, which silently stored Event{Data: ""} values that then flowed
// into the sync engine as if they were valid events.
func (c *Client) objectsToEvents(objects []caldav.CalendarObject, report *FetchReport) []Event {
	events := make([]Event, 0, len(objects))
	for _, obj := range objects {
		if obj.Data == nil {
			log.Printf("objectsToEvents: skipping %s with nil data", obj.Path)
			report.add(obj.Path, UnreadableMalformed, "nil iCalendar data")
			continue
		}

		data, encErr := encodeCalendar(obj.Data)
		if encErr != nil {
			log.Printf("objectsToEvents: skipping %s, encode failed: %v", obj.Path, encErr)
			report.add(obj.Path, UnreadableMalformed, fmt.Sprintf("failed to encode event: %v", encErr))
			continue
		}

		event := Event{
			Path: obj.Path,
			ETag: obj.ETag,
			Data: data,
		}

		// Extract UID, Summary, and StartTime from events
		for _, evt := range obj.Data.Events() {
			if uid, err := evt.Props.Text(ical.PropUID); err == nil {
				event.UID = uid
			}
			if summary, err := evt.Props.Text(ical.PropSummary); err == nil {
				event.Summary = summary
			}
			// Extract start time for deduplication (normalized to UTC)
			if dtstart := evt.Props.Get(ical.PropDateTimeStart); dtstart != nil {
				event.StartTime = normalizeStartTime(dtstart)
			}
		}

		events = append(events, event)
	}
	return events
}

// parseEventPaths extracts .ics file paths from a PROPFIND multistatus response.
func parseEventPaths(body []byte, basePath string) []string {
	type propfindResponse struct {
		XMLName   xml.Name `xml:"DAV: multistatus"`
		Responses []struct {
			Href     string `xml:"href"`
			PropStat struct {
				Prop struct {
					ContentType string `xml:"getcontenttype"`
				} `xml:"prop"`
				Status string `xml:"status"`
			} `xml:"propstat"`
		} `xml:"response"`
	}

	var ms propfindResponse
	if err := xml.Unmarshal(body, &ms); err != nil {
		log.Printf("parseEventPaths: XML unmarshal failed: %v", err)
		// Try to log a snippet of the response for debugging
		if len(body) > 500 {
			log.Printf("parseEventPaths: response body (first 500 bytes): %s", string(body[:500]))
		} else {
			log.Printf("parseEventPaths: response body: %s", string(body))
		}
		return nil
	}

	log.Printf("parseEventPaths: found %d responses in multistatus (basePath=%s)", len(ms.Responses), basePath)
	paths := make([]string, 0)
	for _, resp := range ms.Responses {
		// Skip the collection itself
		if resp.Href == basePath || resp.Href+"/" == basePath || basePath+"/" == resp.Href {
			log.Printf("parseEventPaths: skipping collection path: %s", resp.Href)
			continue
		}
		// Check if it's a calendar object (ends with .ics or has calendar content type)
		if strings.HasSuffix(resp.Href, ".ics") ||
			strings.Contains(resp.PropStat.Prop.ContentType, "calendar") {
			// URL-decode the path to avoid double-encoding when making requests
			decodedPath, err := url.PathUnescape(resp.Href)
			if err != nil {
				// If decoding fails, use original path
				decodedPath = resp.Href
			}
			paths = append(paths, decodedPath)
		} else {
			log.Printf("parseEventPaths: skipping non-event: href=%s contentType=%s", resp.Href, resp.PropStat.Prop.ContentType)
		}
	}
	return paths
}

// IsGoogleURL reports whether a CalDAV base URL points at Google's
// CalDAV endpoint. Google uses a non-standard discovery flow and a
// different write path (/events/ instead of /user), so callers must
// branch on this.
func IsGoogleURL(baseURL string) bool {
	return strings.Contains(baseURL, "googleusercontent.com/caldav/")
}

// GetCalendarPath returns the path portion of the client's base URL.
// This is useful when the client is configured for a specific calendar.
func (c *Client) GetCalendarPath() string {
	if idx := strings.Index(c.baseURL, "://"); idx != -1 {
		rest := c.baseURL[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
			return rest[slashIdx:]
		}
	}
	return "/"
}

// buildURL constructs the full URL for a path.
// If path is absolute (starts with /), extract host from baseURL and combine.
// Otherwise, append path to baseURL.
func (c *Client) buildURL(path string) string {
	if path == "" {
		return c.baseURL
	}

	// If path is absolute, use just the scheme+host from baseURL
	if strings.HasPrefix(path, "/") {
		// Parse baseURL to get scheme and host
		if idx := strings.Index(c.baseURL, "://"); idx != -1 {
			rest := c.baseURL[idx+3:]
			if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
				// baseURL has a path, use scheme://host + path
				return c.baseURL[:idx+3] + rest[:slashIdx] + path
			}
		}
		// baseURL is just scheme://host, append path
		return strings.TrimSuffix(c.baseURL, "/") + path
	}

	// Relative path - append to baseURL
	return strings.TrimSuffix(c.baseURL, "/") + "/" + path
}

// IsMalformedError checks if an error is a malformed content error.
func IsMalformedError(err error) bool {
	if errors.Is(err, ErrMalformedContent) {
		return true
	}
	// Also check error string for malformed patterns (in case of wrapped errors)
	errStr := err.Error()
	return strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "missing colon") ||
		(strings.Contains(errStr, "invalid") && strings.Contains(errStr, "ical"))
}

// GetEvent retrieves a single event by path.
func (c *Client) GetEvent(ctx context.Context, eventPath string) (*Event, error) {
	obj, err := c.caldavClient.GetCalendarObject(ctx, eventPath)
	if err != nil {
		// Classify by HTTP status first. Only a 404 means the object
		// is gone; any other status (5xx, auth failure) says nothing
		// about the object and is transient. The status check must
		// come before any string matching, because the error text can
		// carry the response body. (#206)
		if code, ok := httpStatusCode(err); ok {
			if code == http.StatusNotFound {
				return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
			}
			return nil, fmt.Errorf("failed to fetch event %s: %w", eventPath, err)
		}
		if isTransportError(err) {
			return nil, fmt.Errorf("failed to fetch event %s: %w", eventPath, err)
		}
		// A 200 whose body cannot be decoded is a property of the
		// object, not a passing fault.
		if isMalformedBodyError(err) {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedContent, eventPath, err)
		}
		return nil, fmt.Errorf("failed to fetch event %s: %w", eventPath, err)
	}

	event := &Event{
		Path: obj.Path,
		ETag: obj.ETag,
	}

	if obj.Data != nil {
		data, encErr := encodeCalendar(obj.Data)
		if encErr != nil {
			// Encode failure is a form of malformed content. Return an
			// error so getEventsIndividually's existing malformed-error
			// detection classifies this correctly and records it in the
			// MalformedEventCollector, rather than silently returning an
			// Event{Data: ""} that downstream code would treat as valid.
			return nil, encErr
		}
		event.Data = data

		for _, evt := range obj.Data.Events() {
			if uid, err := evt.Props.Text(ical.PropUID); err == nil {
				event.UID = uid
			}
			if summary, err := evt.Props.Text(ical.PropSummary); err == nil {
				event.Summary = summary
			}
			// Extract start time for deduplication (normalized to UTC)
			if dtstart := evt.Props.Get(ical.PropDateTimeStart); dtstart != nil {
				event.StartTime = normalizeStartTime(dtstart)
			}
		}
	}

	return event, nil
}

// objectFilenameForUID returns the object filename PutEvent uses when it
// has to build a destination path from an event UID (the create path).
//
// Ordinary UIDs keep uid + ".ics" exactly, so existing destination objects
// stay addressable. go-webdav already escapes '?', '#', '%' and spaces when
// it builds the request URL, so those must NOT be pre-escaped here.
//
// A UID containing '/', ".." or a control character would produce a path in
// a sub-collection, a traversal out of the calendar collection, or an
// invalid request line, and go-webdav cannot send '/' as %2F. Those UIDs get
// a deterministic hashed filename instead. The UID inside the iCalendar
// data is unchanged; CalDAV servers key events by that, not by filename.
func objectFilenameForUID(uid string) string {
	unsafe := strings.Contains(uid, "/") || strings.Contains(uid, "..") ||
		strings.ContainsFunc(uid, unicode.IsControl)
	if !unsafe {
		return uid + ".ics"
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:])[:32] + ".ics"
}

// PutEvent creates or updates an event on the destination calendar.
//
// Return values:
//   - nil: the event was successfully written.
//   - wrapped ErrEventSkipped: the event was intentionally NOT written
//     because it had empty data or no extractable UID. Callers should
//     count these as "skipped" in the sync result, NOT as "created" or
//     "updated". Use errors.Is(err, ErrEventSkipped) to detect.
//   - any other non-nil error: a real failure (parse, connection, auth,
//     write). Callers should surface these in result.Warnings or
//     result.Errors as appropriate.
func (c *Client) PutEvent(ctx context.Context, calendarPath string, event *Event) error {
	// Skip events with empty data. This is NOT a success — we did not
	// write anything. Previously this returned nil, which made the
	// caller's `result.Created++` bookkeeping lie.
	if event.Data == "" {
		log.Printf("PutEvent: skipping event with empty data (UID: %s, summary: %s)", event.UID, event.Summary)
		return fmt.Errorf("%w: empty iCalendar data (UID: %s, summary: %s)",
			ErrEventSkipped, event.UID, event.Summary)
	}

	// Parse the iCalendar data
	cal, err := parseICalendar(event.Data)
	if err != nil {
		return fmt.Errorf("failed to parse iCalendar data: %w", err)
	}

	// Determine the path for this event on this server
	// If event.Path is from a different server (doesn't start with calendarPath),
	// we need to construct a new path using the UID
	path := event.Path
	if path == "" || !strings.HasPrefix(path, calendarPath) {
		// Construct path from calendar path and UID
		if event.UID == "" {
			// Try to extract UID from calendar data
			for _, evt := range cal.Events() {
				if uid, err := evt.Props.Text(ical.PropUID); err == nil {
					event.UID = uid
					break
				}
			}
		}
		if event.UID != "" {
			path = strings.TrimSuffix(calendarPath, "/") + "/" + objectFilenameForUID(event.UID)
		} else {
			// Skip events without UID — can't construct a valid path. Same
			// honesty contract as the empty-data case above: return a
			// wrapped sentinel so the caller counts this as a skip.
			log.Printf("PutEvent: skipping event without UID (summary: %s)", event.Summary)
			return fmt.Errorf("%w: no UID extractable from event data (summary: %s)",
				ErrEventSkipped, event.Summary)
		}
	}

	log.Printf("PutEvent: putting to path %s", path)
	_, err = c.caldavClient.PutCalendarObject(ctx, path, cal)
	if err != nil {
		// SOGo (and other RFC-5546-strict servers) reject a PUT when the
		// incoming SEQUENCE is lower than what they already have stored
		// for this UID, returning 403 with body `<D:error>sequences don't
		// match</D:error>`. Google Calendar emits SEQUENCE:0 for nearly
		// every event, so any event that was previously written to the
		// destination at SEQUENCE >= 1 (by another client or an earlier
		// calbridgesync run) will be refused every cycle forever.
		//
		// On 403 we try once to recover: GET the existing event at the
		// same path, extract its SEQUENCE, rewrite our outbound payload
		// to SEQUENCE = existing + 1, and retry the PUT. If the retry
		// succeeds we return nil; if it doesn't, we surface the original
		// error so real auth/ACL 403s still bubble up. (#167)
		if strings.Contains(err.Error(), "403") {
			if retryErr := c.retryPutWithBumpedSequence(ctx, path, cal); retryErr == nil {
				log.Printf("PutEvent: recovered from 403 via SEQUENCE bump on %s", path)
				return nil
			}
		}
		return fmt.Errorf("%w: failed to put event: %w", ErrConnectionFailed, err)
	}

	return nil
}

// retryPutWithBumpedSequence attempts a second PUT after a 403 by reading
// the destination's current SEQUENCE for this UID and bumping the outbound
// payload to be strictly greater. Returns nil on successful retry, non-nil
// if the retry cannot be attempted (cannot read existing, no SEQUENCE to
// compare against) or the retry itself fails.
func (c *Client) retryPutWithBumpedSequence(ctx context.Context, path string, cal *ical.Calendar) error {
	existingData, err := c.fetchRawEvent(ctx, path)
	if err != nil {
		return err
	}
	existingSeq := extractSequenceFromICS(existingData)
	if existingSeq < 0 {
		return fmt.Errorf("no SEQUENCE on existing event at %s", path)
	}
	newSeq := existingSeq + 1
	if our := extractSequenceFromCalendar(cal); our > newSeq {
		newSeq = our + 1
	}
	rewriteSequenceInCalendar(cal, newSeq)
	_, err = c.caldavClient.PutCalendarObject(ctx, path, cal)
	return err
}

// fetchRawEvent does a plain HTTP GET against an event path and returns
// the raw iCalendar body. Used by retryPutWithBumpedSequence to read the
// current SEQUENCE without paying for a full MULTIGET.
func (c *Client) fetchRawEvent(ctx context.Context, path string) (string, error) {
	fullURL := c.buildURL(path)
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.username, c.password)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d fetching %s", resp.StatusCode, path)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCalDAVResponseSize))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// extractSequenceFromICS parses the SEQUENCE value from raw iCalendar data.
// Returns -1 if no SEQUENCE line is present. Only the first SEQUENCE found
// in the first VEVENT is used — master + overrides would all share the
// same SEQUENCE under RFC 5545 in practice.
func extractSequenceFromICS(data string) int {
	// Scan line-by-line so we don't allocate a full parse when the
	// caller only needs a single integer. Lines are CRLF or LF; handle
	// both.
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "SEQUENCE:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "SEQUENCE:"))
			n, err := strconv.Atoi(v)
			if err != nil {
				return -1
			}
			return n
		}
	}
	return -1
}

// extractSequenceFromCalendar reads the SEQUENCE property from the first
// VEVENT in a parsed calendar. Returns -1 if absent or unparseable.
func extractSequenceFromCalendar(cal *ical.Calendar) int {
	for _, evt := range cal.Events() {
		if prop := evt.Props.Get("SEQUENCE"); prop != nil {
			n, err := strconv.Atoi(strings.TrimSpace(prop.Value))
			if err == nil {
				return n
			}
		}
	}
	return -1
}

// rewriteSequenceInCalendar sets SEQUENCE=newSeq on every VEVENT in the
// calendar, creating the property if it was absent. Used by the retry
// path so the retried PUT carries a SEQUENCE that SOGo will accept as
// "newer" than what it has stored.
//
// We build the Prop directly instead of calling Props.SetText — SetText
// attaches a VALUE=TEXT parameter, which would emit `SEQUENCE;VALUE=TEXT:N`.
// RFC 5545 defines SEQUENCE as an INTEGER, and SOGo's parser rejects the
// VALUE=TEXT variant. Building the Prop bare produces the canonical
// `SEQUENCE:N` form.
func rewriteSequenceInCalendar(cal *ical.Calendar, newSeq int) {
	for _, evt := range cal.Events() {
		prop := ical.NewProp("SEQUENCE")
		prop.Value = strconv.Itoa(newSeq)
		evt.Props.Set(prop)
	}
}

// DeleteEvent deletes an event.
func (c *Client) DeleteEvent(ctx context.Context, eventPath string) error {
	err := c.caldavClient.RemoveAll(ctx, eventPath)
	if err != nil {
		return fmt.Errorf("%w: failed to delete event: %w", ErrConnectionFailed, err)
	}
	return nil
}

// parseICalendar parses iCalendar data string into a calendar object.
func parseICalendar(data string) (*ical.Calendar, error) {
	dec := ical.NewDecoder(strings.NewReader(data))
	cal, err := dec.Decode()
	if err != nil {
		return nil, err
	}
	return cal, nil
}

// encodeCalendar encodes a calendar object to iCalendar string form.
//
// Returns an error wrapping ErrMalformedContent if the go-ical encoder
// fails. Previously this function silently returned an empty string on
// encode failure, which led to three distinct forms of data corruption:
//
//  1. getEventsBatch misclassified encode failures as "empty iCalendar
//     data" in the MalformedEventCollector, hiding the real cause.
//  2. objectsToEvents silently stored Event{Data: ""} in its returned
//     slice and never checked, letting corrupt events flow into the
//     sync engine as if they were normal.
//  3. GetEvent returned a zero-Data Event with err == nil, so callers
//     believed the fetch had succeeded.
//
// All callers now MUST handle the error explicitly. Encode failures
// should be treated as malformed events — recorded in a collector where
// one is available, logged where one is not, and the event skipped.
func encodeCalendar(cal *ical.Calendar) (string, error) {
	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformedContent, err)
	}
	return buf.String(), nil
}

// normalizeStartTime converts a DTSTART property to a normalized UTC string for comparison.
// This handles different formats like "20260112T170000Z" (UTC) and "20260113T010000" with TZID.
func normalizeStartTime(prop *ical.Prop) string {
	if prop == nil {
		return ""
	}

	value := prop.Value

	// Check for UTC format (ends with Z)
	if strings.HasSuffix(value, "Z") {
		// Already UTC, parse and reformat to ensure consistent format
		t, err := time.Parse("20060102T150405Z", value)
		if err == nil {
			return t.Format("20060102T150405Z")
		}
		return value
	}

	// Check for TZID parameter
	if tzidParam := prop.Params.Get("TZID"); tzidParam != "" {
		// Try to load the timezone - first try standard IANA name
		loc, err := time.LoadLocation(tzidParam)
		if err != nil {
			// Try to parse GMT offset format (e.g., "GMT-0400", "GMT+0530")
			loc = parseGMTOffset(tzidParam)
			if loc == nil {
				// Try the go-ical library method as fallback
				t, err := prop.DateTime(time.UTC)
				if err == nil {
					return t.UTC().Format("20060102T150405Z")
				}
				return value
			}
		}

		// Parse the datetime in the specified timezone
		t, err := time.ParseInLocation("20060102T150405", value, loc)
		if err != nil {
			log.Printf("normalizeStartTime: failed to parse datetime %s: %v", value, err)
			return value
		}

		// Convert to UTC
		return t.UTC().Format("20060102T150405Z")
	}

	// Try the go-ical library method for other cases
	t, err := prop.DateTime(time.UTC)
	if err == nil {
		return t.UTC().Format("20060102T150405Z")
	}

	// Fall back to the raw value
	return value
}

// parseGMTOffset parses timezone strings like "GMT-0400", "GMT+0530", "UTC+05:30"
// and returns a fixed timezone location.
func parseGMTOffset(tzid string) *time.Location {
	// Remove common prefixes
	offset := tzid
	for _, prefix := range []string{"GMT", "UTC", "Etc/GMT"} {
		if strings.HasPrefix(offset, prefix) {
			offset = strings.TrimPrefix(offset, prefix)
			break
		}
	}

	if offset == "" {
		return time.UTC
	}

	// Parse the offset
	sign := 1
	if strings.HasPrefix(offset, "-") {
		sign = -1
		offset = offset[1:]
	} else if strings.HasPrefix(offset, "+") {
		offset = offset[1:]
	}

	// Handle formats: "0400", "04:00", "4", "04"
	offset = strings.ReplaceAll(offset, ":", "")

	var hours, minutes int
	switch len(offset) {
	case 1, 2:
		fmt.Sscanf(offset, "%d", &hours)
	case 3:
		fmt.Sscanf(offset, "%1d%2d", &hours, &minutes)
	case 4:
		fmt.Sscanf(offset, "%2d%2d", &hours, &minutes)
	default:
		return nil
	}

	totalSeconds := sign * (hours*3600 + minutes*60)
	return time.FixedZone(tzid, totalSeconds)
}
