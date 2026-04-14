package caldavproxy

import (
	"strings"
	"testing"
)

func TestRewriteMultistatus_SOGoCalendarResponse(t *testing.T) {
	// Real SOGo response with problematic resourcetype elements
	input := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:a="urn:ietf:params:xml:ns:caldav"><D:response><D:href>/SOGo/dav/user@example.com/Calendar/personal/</D:href><D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/><calendar xmlns="urn:ietf:params:xml:ns:caldav"/><vevent-collection xmlns="http://groupdav.org/"/><vtodo-collection xmlns="http://groupdav.org/"/><schedule-outbox xmlns="urn:ietf:params:xml:ns:caldav"/></D:resourcetype></D:prop></D:propstat></D:response></D:multistatus>`

	result := string(RewriteMultistatus([]byte(input)))

	// Should NOT contain GroupDAV elements
	if strings.Contains(result, "groupdav.org") {
		t.Error("result still contains groupdav.org elements")
	}

	// Should NOT contain schedule-outbox
	if strings.Contains(result, "schedule-outbox") {
		t.Error("result still contains schedule-outbox")
	}

	// Should still contain collection
	if !strings.Contains(result, "collection") {
		t.Error("result is missing collection element")
	}

	// Should still contain calendar
	if !strings.Contains(result, "calendar") {
		t.Error("result is missing calendar element")
	}

	// Should still be valid XML with multistatus
	if !strings.Contains(result, "multistatus") {
		t.Error("result is missing multistatus root")
	}
}

func TestRewriteMultistatus_PassthroughNonCalendar(t *testing.T) {
	// A plain collection with no calendar elements should pass through
	input := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/SOGo/dav/user@example.com/Calendar/</D:href><D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>Calendar</D:displayname></D:prop></D:propstat></D:response></D:multistatus>`

	result := string(RewriteMultistatus([]byte(input)))

	if !strings.Contains(result, "collection") {
		t.Error("result is missing collection element")
	}
	if !strings.Contains(result, "Calendar") {
		t.Error("result is missing displayname")
	}
}

func TestRewriteMultistatus_MalformedXML(t *testing.T) {
	// Malformed XML should be returned unchanged
	input := `this is not xml at all`
	result := string(RewriteMultistatus([]byte(input)))
	if result != input {
		t.Errorf("malformed input was modified: got %q", result)
	}
}

func TestRewriteMultistatus_EmptyBody(t *testing.T) {
	result := RewriteMultistatus([]byte{})
	if len(result) != 0 {
		t.Errorf("empty input produced non-empty output: %q", result)
	}
}

func TestRewriteMultistatus_PreservesOtherProperties(t *testing.T) {
	input := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/" xmlns:C="http://apple.com/ns/ical/"><D:response><D:href>/SOGo/dav/user@example.com/Calendar/personal/</D:href><D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/><calendar xmlns="urn:ietf:params:xml:ns:caldav"/><vevent-collection xmlns="http://groupdav.org/"/></D:resourcetype><D:displayname>My Calendar</D:displayname><CS:getctag>12345</CS:getctag><C:calendar-color>#FF0000</C:calendar-color></D:prop></D:propstat></D:response></D:multistatus>`

	result := string(RewriteMultistatus([]byte(input)))

	// Properties outside resourcetype should be preserved
	if !strings.Contains(result, "My Calendar") {
		t.Error("displayname was lost")
	}
	if !strings.Contains(result, "12345") {
		t.Error("getctag was lost")
	}
	if !strings.Contains(result, "#FF0000") {
		t.Error("calendar-color was lost")
	}
	// GroupDAV should be gone
	if strings.Contains(result, "groupdav") {
		t.Error("groupdav elements were not removed")
	}
}
