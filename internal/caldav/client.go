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
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

var (
	ErrConnectionFailed = errors.New("connection failed")
	ErrAuthFailed       = errors.New("authentication failed")
	ErrNotFound         = errors.New("resource not found")
	ErrInvalidResponse  = errors.New("invalid server response")
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
	Path    string `json:"path"`
	ETag    string `json:"etag"`
	Data    string `json:"data"` // iCalendar data
	UID     string `json:"uid"`
	Summary string `json:"summary"`
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
func (c *Client) GetEvents(ctx context.Context, calendarPath string) ([]Event, error) {
	// Try the standard calendar-query first
	events, err := c.getEventsViaQuery(ctx, calendarPath)
	if err == nil {
		return events, nil
	}

	// If query failed (412, etc.), fall back to multiget via PROPFIND
	log.Printf("Calendar query failed, trying PROPFIND fallback: %v", err)
	return c.getEventsViaPropfind(ctx, calendarPath)
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
func (c *Client) getEventsViaPropfind(ctx context.Context, calendarPath string) ([]Event, error) {
	// Go directly to PROPFIND list since MultiGetCalendar requires specific paths
	return c.getEventsViaList(ctx, calendarPath)
}

// getEventsViaList lists calendar contents and fetches each event individually.
func (c *Client) getEventsViaList(ctx context.Context, calendarPath string) ([]Event, error) {
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
	for _, path := range eventPaths {
		event, err := c.GetEvent(ctx, path)
		if err != nil {
			log.Printf("Failed to fetch event %s: %v", path, err)
			continue
		}
		events = append(events, *event)
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

			// Extract UID and Summary from events
			for _, evt := range obj.Data.Events() {
				if uid, err := evt.Props.Text(ical.PropUID); err == nil {
					event.UID = uid
				}
				if summary, err := evt.Props.Text(ical.PropSummary); err == nil {
					event.Summary = summary
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
			paths = append(paths, resp.Href)
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

// GetEvent retrieves a single event by path.
func (c *Client) GetEvent(ctx context.Context, eventPath string) (*Event, error) {
	obj, err := c.caldavClient.GetCalendarObject(ctx, eventPath)
	if err != nil {
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
		}
	}

	return event, nil
}

// PutEvent creates or updates an event.
func (c *Client) PutEvent(ctx context.Context, calendarPath string, event *Event) error {
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
			path = calendarPath
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
