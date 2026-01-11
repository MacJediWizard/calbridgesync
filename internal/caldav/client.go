package caldav

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

var (
	ErrConnectionFailed  = errors.New("connection failed")
	ErrAuthFailed        = errors.New("authentication failed")
	ErrNotFound          = errors.New("resource not found")
	ErrInvalidResponse   = errors.New("invalid server response")
	ErrMalformedContent  = errors.New("malformed calendar content")
)

const (
	defaultTimeout = 30 * time.Second
	minTLSVersion  = tls.VersionTLS12
)

// Calendar represents a CalDAV calendar.
type Calendar struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	SyncToken   string `json:"sync_token"`
	CTag        string `json:"ctag"`
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

// Client provides CalDAV operations.
type Client struct {
	baseURL      string
	username     string
	password     string
	httpClient   *http.Client
	caldavClient *caldav.Client
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
	}

	httpClient := &http.Client{
		Timeout:   defaultTimeout,
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

// GetEvents retrieves all events from a calendar.
// If collector is provided, malformed events will be recorded there.
func (c *Client) GetEvents(ctx context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, error) {
	// Try the standard calendar-query first
	events, err := c.getEventsViaQuery(ctx, calendarPath)
	if err == nil {
		return events, nil
	}

	// If query failed (412, etc.), fall back to multiget via PROPFIND
	log.Printf("Calendar query failed, trying PROPFIND fallback: %v", err)
	return c.getEventsViaPropfind(ctx, calendarPath, collector)
}

// getEventsViaQuery uses REPORT calendar-query to get events.
func (c *Client) getEventsViaQuery(ctx context.Context, calendarPath string) ([]Event, error) {
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

	return c.objectsToEvents(objects), nil
}

// getEventsViaPropfind uses PROPFIND to list calendar objects, then fetches each one.
func (c *Client) getEventsViaPropfind(ctx context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, error) {
	// Go directly to PROPFIND list since MultiGetCalendar requires specific paths
	return c.getEventsViaList(ctx, calendarPath, collector)
}

// getEventsViaList lists calendar contents and fetches each event individually.
func (c *Client) getEventsViaList(ctx context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, error) {
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

	// Parse the multistatus response to get event paths
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	eventPaths := parseEventPaths(body, calendarPath)

	// Fetch each event individually
	events := make([]Event, 0, len(eventPaths))
	skippedMalformed := 0
	skippedEmpty := 0
	for _, path := range eventPaths {
		event, err := c.GetEvent(ctx, path)
		if err != nil {
			if IsMalformedError(err) {
				// Record malformed event if collector is provided
				if collector != nil {
					collector.Add(path, err.Error())
				}
				skippedMalformed++
				continue
			}
			log.Printf("Failed to fetch event %s: %v", path, err)
			continue
		}
		// Check for empty event data (corrupted or deleted events)
		if event.Data == "" {
			if collector != nil {
				collector.Add(path, "empty iCalendar data - event may be corrupted or deleted")
			}
			skippedEmpty++
			continue
		}
		events = append(events, *event)
	}
	if skippedMalformed > 0 {
		log.Printf("Skipped %d malformed events (corrupted at source)", skippedMalformed)
	}
	if skippedEmpty > 0 {
		log.Printf("Skipped %d empty events (no iCalendar data)", skippedEmpty)
	}

	return events, nil
}

// objectsToEvents converts CalDAV objects to Events.
func (c *Client) objectsToEvents(objects []caldav.CalendarObject) []Event {
	events := make([]Event, 0, len(objects))
	for _, obj := range objects {
		event := Event{
			Path: obj.Path,
			ETag: obj.ETag,
		}

		if obj.Data != nil {
			// Encode the calendar to string
			event.Data = encodeCalendar(obj.Data)

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
		return nil
	}

	paths := make([]string, 0)
	for _, resp := range ms.Responses {
		// Skip the collection itself
		if resp.Href == basePath || resp.Href+"/" == basePath || basePath+"/" == resp.Href {
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
		}
	}
	return paths
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
		// Check for malformed content errors from the iCal parser
		errStr := err.Error()
		if strings.Contains(errStr, "malformed") ||
			strings.Contains(errStr, "missing colon") ||
			strings.Contains(errStr, "invalid") && strings.Contains(errStr, "ical") {
			return nil, fmt.Errorf("%w: %s", ErrMalformedContent, eventPath)
		}
		return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
	}

	event := &Event{
		Path: obj.Path,
		ETag: obj.ETag,
	}

	if obj.Data != nil {
		event.Data = encodeCalendar(obj.Data)

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

// PutEvent creates or updates an event.
func (c *Client) PutEvent(ctx context.Context, calendarPath string, event *Event) error {
	// Skip events with empty data
	if event.Data == "" {
		log.Printf("PutEvent: skipping event with empty data (UID: %s, summary: %s)", event.UID, event.Summary)
		return nil
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
			path = strings.TrimSuffix(calendarPath, "/") + "/" + event.UID + ".ics"
		} else {
			// Skip events without UID - can't create a valid path
			log.Printf("PutEvent: skipping event without UID (summary: %s)", event.Summary)
			return nil
		}
	}

	log.Printf("PutEvent: putting to path %s", path)
	_, err = c.caldavClient.PutCalendarObject(ctx, path, cal)
	if err != nil {
		return fmt.Errorf("%w: failed to put event: %w", ErrConnectionFailed, err)
	}

	return nil
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

// encodeCalendar encodes a calendar object to iCalendar string.
func encodeCalendar(cal *ical.Calendar) string {
	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return ""
	}
	return buf.String()
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
