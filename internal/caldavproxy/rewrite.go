package caldavproxy

import (
	"regexp"
)

// groupDAVRe matches any self-closing or open+close element from the
// http://groupdav.org/ namespace inside a <resourcetype> block.
// Examples it catches:
//   - <vevent-collection xmlns="http://groupdav.org/"/>
//   - <vtodo-collection xmlns="http://groupdav.org/"/>
var groupDAVRe = regexp.MustCompile(`<[^>]*xmlns="http://groupdav\.org/"[^>]*/?>(?:</[^>]+>)?`)

// scheduleOutboxRe matches the schedule-outbox element from the CalDAV
// namespace inside a <resourcetype> block.
// Examples:
//   - <schedule-outbox xmlns="urn:ietf:params:xml:ns:caldav"/>
//   - <n1:schedule-outbox xmlns:n1="urn:ietf:params:xml:ns:caldav"/>
var scheduleOutboxRe = regexp.MustCompile(`<[^>]*schedule-outbox[^>]*urn:ietf:params:xml:ns:caldav[^>]*/?>(?:</[^>]+>)?`)

// Also match the reverse attribute order (xmlns before element name)
var scheduleOutboxRe2 = regexp.MustCompile(`<[^>]*urn:ietf:params:xml:ns:caldav[^>]*schedule-outbox[^>]*/?>(?:</[^>]+>)?`)

// RewriteMultistatus cleans a WebDAV 207 Multi-Status XML response so that
// CalDAV clients like Fantastical can discover calendars from SOGo.
//
// SOGo includes non-standard elements in <resourcetype> that cause Fantastical
// to silently skip calendars:
//   - GroupDAV elements (http://groupdav.org/ namespace)
//   - schedule-outbox inside calendar collections
//
// This function uses regex-based surgery on the raw XML to remove those
// elements while preserving all other content byte-for-byte.
// If the input doesn't contain the problematic patterns, it's returned unchanged.
func RewriteMultistatus(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	result := body
	result = groupDAVRe.ReplaceAll(result, nil)
	result = scheduleOutboxRe.ReplaceAll(result, nil)
	result = scheduleOutboxRe2.ReplaceAll(result, nil)

	return result
}
