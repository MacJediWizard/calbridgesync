package caldav

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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

	return events, nil
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

	// Use event path if available, otherwise use calendar path
	path := event.Path
	if path == "" {
		path = calendarPath
	}

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
