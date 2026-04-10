package caldav

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

const (
	maxICSResponseSize = 50 * 1024 * 1024 // 50MB limit for ICS feed responses
)

// ICSClient fetches and parses ICS calendar feeds over HTTP.
type ICSClient struct {
	feedURL    string
	username   string
	password   string
	httpClient *http.Client
}

// NewICSClient creates a new ICS feed client.
func NewICSClient(feedURL, username, password string) (*ICSClient, error) {
	if feedURL == "" {
		return nil, fmt.Errorf("%w: feed URL is required", ErrConnectionFailed)
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

	return &ICSClient{
		feedURL:    feedURL,
		username:   username,
		password:   password,
		httpClient: httpClient,
	}, nil
}

// TestConnection validates the ICS feed URL is reachable.
func (c *ICSClient) TestConnection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.feedURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}

	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	// Some servers don't support HEAD, fall back to GET
	if resp.StatusCode == http.StatusMethodNotAllowed {
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL, nil)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrConnectionFailed, err)
		}
		if c.username != "" {
			req2.SetBasicAuth(c.username, c.password)
		}
		resp2, err := c.httpClient.Do(req2)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrConnectionFailed, err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: HTTP %d", ErrConnectionFailed, resp2.StatusCode)
		}
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrConnectionFailed, resp.StatusCode)
	}
	return nil
}

// FetchEvents fetches and parses events from the ICS feed.
func (c *ICSClient) FetchEvents(ctx context.Context, collector *MalformedEventCollector) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}

	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrConnectionFailed, resp.StatusCode)
	}

	// Read with size limit
	limitedReader := io.LimitReader(resp.Body, maxICSResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read ICS feed: %w", err)
	}

	log.Printf("ICS feed: fetched %d bytes from %s", len(body), c.feedURL)

	// Parse iCalendar data
	dec := ical.NewDecoder(strings.NewReader(string(body)))
	cal, err := dec.Decode()
	if err != nil {
		return nil, fmt.Errorf("failed to parse ICS feed: %w", err)
	}

	// Group VEVENTs by UID so recurring events (master + exceptions) stay together
	type uidGroup struct {
		summary   string
		startTime string
		vevents   []*ical.Component
	}
	groups := make(map[string]*uidGroup)
	var groupOrder []string

	for _, vevent := range cal.Events() {
		uid, _ := vevent.Props.Text(ical.PropUID)
		if uid == "" {
			if collector != nil {
				collector.Add("", "event missing UID")
			}
			continue
		}

		g, exists := groups[uid]
		if !exists {
			g = &uidGroup{}
			groups[uid] = g
			groupOrder = append(groupOrder, uid)
		}

		g.vevents = append(g.vevents, vevent.Component)

		// Use the master event (no RECURRENCE-ID) for summary and start time
		if vevent.Props.Get("RECURRENCE-ID") == nil {
			summary, _ := vevent.Props.Text(ical.PropSummary)
			g.summary = summary
			if dtstart := vevent.Props.Get(ical.PropDateTimeStart); dtstart != nil {
				g.startTime = normalizeStartTime(dtstart)
			}
		}
	}

	// Build events from groups
	var events []Event
	for _, uid := range groupOrder {
		g := groups[uid]

		singleCal := ical.NewCalendar()
		singleCal.Props.SetText(ical.PropVersion, "2.0")
		singleCal.Props.SetText(ical.PropProductID, "-//CalBridgeSync//EN")
		for _, vevent := range g.vevents {
			singleCal.Children = append(singleCal.Children, vevent)
		}

		data, encErr := encodeCalendar(singleCal)
		if encErr != nil {
			if collector != nil {
				collector.Add(uid, fmt.Sprintf("failed to encode event: %v", encErr))
			}
			continue
		}

		events = append(events, Event{
			Path:      uid + ".ics",
			UID:       uid,
			Summary:   g.summary,
			StartTime: g.startTime,
			Data:      data,
		})
	}

	log.Printf("ICS feed: parsed %d events (%d UIDs grouped from %d VEVENTs)", len(events), len(groups), len(cal.Events()))
	return events, nil
}
