package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SyncItem represents a changed or new item from a sync operation.
type SyncItem struct {
	Path string `json:"path"`
	ETag string `json:"etag"`
	Data string `json:"data,omitempty"`
}

// SyncResponse represents the result of a WebDAV-Sync operation.
type SyncResponse struct {
	SyncToken string     `json:"sync_token"`
	Changed   []SyncItem `json:"changed"`
	Deleted   []string   `json:"deleted"`
}

// XML structures for parsing WebDAV-Sync responses
type multistatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"response"`
	SyncToken string     `xml:"sync-token"`
}

type response struct {
	Href     string    `xml:"href"`
	PropStat *propstat `xml:"propstat"`
	Status   string    `xml:"status"`
}

type propstat struct {
	Prop   prop   `xml:"prop"`
	Status string `xml:"status"`
}

type prop struct {
	GetETag      string `xml:"getetag"`
	CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
}

// SyncCollection performs a WebDAV-Sync (RFC 6578) operation.
func (c *Client) SyncCollection(ctx context.Context, calendarPath, syncToken string) (*SyncResponse, error) {
	// Build the sync-collection REPORT request
	reqBody := buildSyncCollectionRequest(syncToken)

	req, err := http.NewRequestWithContext(ctx, "REPORT", c.baseURL+calendarPath, strings.NewReader(reqBody))
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

	if resp.StatusCode != http.StatusMultiStatus {
		// WebDAV-Sync not supported or invalid token
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotImplemented {
			return nil, fmt.Errorf("WebDAV-Sync not supported")
		}
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("%w: unexpected status %d", ErrInvalidResponse, resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: unexpected status %d: %s", ErrInvalidResponse, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return parseSyncResponse(body)
}

// SupportsWebDAVSync checks if the calendar supports WebDAV-Sync.
func (c *Client) SupportsWebDAVSync(ctx context.Context, calendarPath string) bool {
	// Try an OPTIONS request to check for sync-collection support
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, c.baseURL+calendarPath, nil)
	if err != nil {
		return false
	}

	req.SetBasicAuth(c.username, c.password)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Check DAV header for sync-collection
	dav := resp.Header.Get("DAV")
	return strings.Contains(dav, "sync-collection")
}

func buildSyncCollectionRequest(syncToken string) string {
	var tokenElement string
	if syncToken != "" {
		tokenElement = fmt.Sprintf("<D:sync-token>%s</D:sync-token>", xmlEscape(syncToken))
	} else {
		tokenElement = "<D:sync-token/>"
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8" ?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  %s
  <D:sync-level>1</D:sync-level>
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
</D:sync-collection>`, tokenElement)
}

func parseSyncResponse(body []byte) (*SyncResponse, error) {
	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	result := &SyncResponse{
		SyncToken: ms.SyncToken,
		Changed:   make([]SyncItem, 0),
		Deleted:   make([]string, 0),
	}

	for _, resp := range ms.Responses {
		// Check if this is a deleted item (404 status)
		if strings.Contains(resp.Status, "404") {
			result.Deleted = append(result.Deleted, resp.Href)
			continue
		}

		// Check propstat status
		if resp.PropStat != nil && strings.Contains(resp.PropStat.Status, "200") {
			item := SyncItem{
				Path: resp.Href,
				ETag: resp.PropStat.Prop.GetETag,
				Data: resp.PropStat.Prop.CalendarData,
			}
			result.Changed = append(result.Changed, item)
		}
	}

	return result, nil
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
