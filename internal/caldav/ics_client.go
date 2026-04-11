package caldav

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

const (
	maxICSResponseSize = 50 * 1024 * 1024 // 50MB limit for ICS feed responses
)

// icsLoopbackOnlyDialContext is the dial-time DNS-rebinding defense
// for ICS feed subscriptions. It's analogous to the notify package's
// safeDialContext but intentionally NARROWER: the webhook dial
// rejects ALL private IPs (10/8, 172.16/12, 192.168/16, CGNAT,
// link-local, etc.) because webhooks target external services, but
// ICS feed URLs legitimately point at LAN calendar servers
// (Nextcloud on 192.168.x, Radicale on 10.x, DavMail export to a
// LAN host). Blocking private IPs would break real-world LAN
// configurations.
//
// So this dial-time check only refuses the narrow set that is
// ALWAYS an operator mistake for an ICS feed URL:
//
//  1. Loopback (127.0.0.0/8, ::1, ::ffff:127.0.0.1) — subscribing
//     the calbridgesync instance's own listener to itself is
//     operator typo, never legitimate.
//
//  2. Unspecified (0.0.0.0, ::) — routes to loopback on many
//     systems, same category of mistake.
//
//  3. Link-local unicast / multicast (169.254.0.0/16, fe80::/10) —
//     includes cloud metadata endpoints (AWS / GCP / Azure IMDS).
//     Critical SSRF defense against credential exfiltration via
//     a malicious DNS answer pointing at 169.254.169.254.
//
// RFC 1918 private, carrier NAT, unique-local IPv6 (fc00::/7)
// remain allowed so LAN use cases work.
//
// Validation-time hostname checks (PR #128) catch the obvious
// static cases at save time. This dial-time check catches DNS
// rebinding: a hostname that resolved to a public IP at save time
// but now resolves to 127.0.0.1 (or 169.254.169.254) at fetch
// time. Same architecture as notify package's safeDialContext. (#129)
//
// Like the webhook dial, this resolves the host and dials the
// resolved IP directly to close a last-mile TOCTOU window between
// check and connect.
func icsLoopbackOnlyDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed for %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPs resolved for %s", host)
	}

	// Reject if ANY resolved IP is in the narrow block-list. A
	// DNS answer of [public-ip, 127.0.0.1] must not slip through
	// just because Go's dialer might pick the public one — same
	// defensive posture as the webhook dial.
	for _, ip := range ips {
		if blocked, reason := isICSBlockedIP(ip); blocked {
			return nil, fmt.Errorf("blocked destination: %s resolves to %s (%s)", host, ip.String(), reason)
		}
	}

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	// Dial the first resolved IP directly to prevent a second
	// resolver lookup from returning a different answer.
	dialAddr := net.JoinHostPort(ips[0].String(), port)
	return dialer.DialContext(ctx, network, dialAddr)
}

// isICSBlockedIP is the ICS-specific block classifier. Narrower
// than notify.isBlockedIP: private IPs are allowed. See
// icsLoopbackOnlyDialContext for the rationale. (#129)
func isICSBlockedIP(ip net.IP) (bool, string) {
	if ip == nil {
		return true, "unparseable IP"
	}
	if ip.IsLoopback() {
		return true, "loopback"
	}
	if ip.IsUnspecified() {
		return true, "unspecified"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true, "link-local (includes cloud IMDS)"
	}
	return false, ""
}

// icsDialContext is the dial function the ICS http.Client uses.
// Package-level variable so future tests that spin up an
// httptest.NewServer (which binds to 127.0.0.1) can swap in a
// permissive dialer. Not currently needed by existing ICS tests
// but keeping the pattern consistent with notify/webhookDialContext
// avoids retrofitting if httptest-based ICS tests get added. (#129)
var icsDialContext = icsLoopbackOnlyDialContext

// ICSClient fetches and parses ICS calendar feeds over HTTP.
type ICSClient struct {
	feedURL    string
	username   string
	password   string
	httpClient *http.Client
}

// validateICSFeedURL rejects obviously unsafe ICS feed URLs. The
// threat model for ICS subscriptions sits between webhook URLs
// (strictly external HTTPS, private IPs rejected) and CalDAV
// source URLs (LAN servers are common and legitimate). ICS feeds
// are most often public calendar subscription URLs (Google
// Calendar public feeds, university schedules, sports calendars)
// but can also legitimately point at a LAN calendar with ICS
// export. So this validator is deliberately narrower than
// validateWebhookURL:
//
//  1. Scheme must be http or https. Rejects file://, gopher://,
//     dict://, data://, and other schemes that would let a
//     crafted feed URL exfiltrate local files or pivot to
//     non-HTTP protocols. This is the primary SSRF defense — the
//     Go HTTP client would technically try to dial a file:// URL
//     as a relative path lookup, which is almost always the
//     wrong behavior.
//
//  2. Hostname cannot be localhost / 127.0.0.1 / ::1. A feed URL
//     pointing at the calbridgesync instance's own HTTP listener
//     is almost always an operator typo — nobody legitimately
//     subscribes their own instance to itself, and accepting it
//     gives an attacker (or a confused user) the ability to
//     trigger recursive fetches or probe internal endpoints.
//
//  3. Hostname cannot end in `.local` or `.internal`. Catches
//     mDNS and intranet-style suffixes that are almost always
//     operator typos for the non-suffixed form, and that leak
//     intent about the internal network when they fail.
//
// **Not** blocked here: RFC 1918 private ranges (10.0.0.0/8,
// 172.16.0.0/12, 192.168.0.0/16) and link-local (169.254/16).
// Legitimate LAN ICS subscriptions exist (Nextcloud, Radicale,
// DavMail exporting to a LAN address) and blocking them would
// break real configurations on home / small-team deployments.
// The webhook validator blocks private IPs because webhook
// alerts are strictly external; ICS feeds are not.
//
// The second-pass audit flagged this gap after it observed that
// PR #116 hardened the webhook path but left ICS completely
// unvalidated. (#127)
func validateICSFeedURL(feedURL string) error {
	if feedURL == "" {
		return fmt.Errorf("ICS feed URL is required")
	}
	parsed, err := url.Parse(feedURL)
	if err != nil {
		return fmt.Errorf("invalid ICS feed URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("ICS feed URL scheme must be http or https, got %q", scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("ICS feed URL is missing a host")
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return fmt.Errorf("ICS feed URL cannot point to localhost")
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("ICS feed URL cannot point to .local or .internal hosts")
	}
	return nil
}

// NewICSClient creates a new ICS feed client.
func NewICSClient(feedURL, username, password string) (*ICSClient, error) {
	if err := validateICSFeedURL(feedURL); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}

	transport := &http.Transport{
		DialContext: icsDialContext,
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
