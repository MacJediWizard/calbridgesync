package main

import (
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
)

// TestFindUIDInEvents_ParsedUIDWins covers the normal case where the
// iCalendar parser populated Event.UID correctly and we find the
// target on the first pass.
func TestFindUIDInEvents_ParsedUIDWins(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "other-uid-1", Data: ""},
		{Path: "/cal/b.ics", UID: "TARGET", Data: "BEGIN:VEVENT\nUID:TARGET\nEND:VEVENT"},
		{Path: "/cal/c.ics", UID: "other-uid-2", Data: ""},
	}

	got := findUIDInEvents(events, "TARGET")
	if got != "/cal/b.ics" {
		t.Errorf("expected /cal/b.ics, got %q", got)
	}
}

// TestFindUIDInEvents_NotFound covers the common no-match case. The
// function MUST return an empty string, not panic or return an
// arbitrary event's path.
func TestFindUIDInEvents_NotFound(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "uid-1"},
		{Path: "/cal/b.ics", UID: "uid-2"},
	}

	got := findUIDInEvents(events, "NOT-PRESENT")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestFindUIDInEvents_EmptyList covers the edge case where the
// calendar has zero events. Must not panic and must return empty.
func TestFindUIDInEvents_EmptyList(t *testing.T) {
	got := findUIDInEvents(nil, "anything")
	if got != "" {
		t.Errorf("expected empty string for nil slice, got %q", got)
	}
	got = findUIDInEvents([]caldav.Event{}, "anything")
	if got != "" {
		t.Errorf("expected empty string for empty slice, got %q", got)
	}
}

// TestFindUIDInEvents_RawFallback covers the zombie-recovery case:
// the parsed UID field is empty (parser dropped it) but the raw
// Event.Data still carries the UID property. The raw-substring pass
// should catch it. Pattern is exactly how the WOS zombie manifested
// during recovery: parser couldn't make sense of the corrupted
// VCALENDAR envelope and left Event.UID empty, but "UID:040000..."
// was still in Event.Data.
func TestFindUIDInEvents_RawFallback(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "", Data: "BEGIN:VEVENT\r\nUID:040000-TARGET\r\nSUMMARY:Untitled\r\nEND:VEVENT"},
	}

	got := findUIDInEvents(events, "040000-TARGET")
	if got != "/cal/a.ics" {
		t.Errorf("expected raw-fallback match at /cal/a.ics, got %q", got)
	}
}

// TestFindUIDInEvents_ParsedTakesPriorityOverRaw verifies the
// documented precedence: if a parsed UID matches in event A and a
// raw-data UID matches in event B, the parsed match (A) wins. This
// keeps the result deterministic when both the live form and a
// corrupted form of the same UID happen to coexist.
func TestFindUIDInEvents_ParsedTakesPriorityOverRaw(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/raw.ics", UID: "", Data: "BEGIN:VEVENT\r\nUID:TARGET\r\nEND:VEVENT"},
		{Path: "/cal/parsed.ics", UID: "TARGET", Data: ""},
	}

	got := findUIDInEvents(events, "TARGET")
	if got != "/cal/parsed.ics" {
		t.Errorf("expected parsed match /cal/parsed.ics to win, got %q", got)
	}
}

// TestFindUIDInEvents_SubstringSafety verifies that we don't
// accidentally return a false-positive match on a partial-UID
// substring. The needle is "UID:TARGET", so "UID:TARGETX" should
// also match (Contains is forgiving), but "UID:OTHER" that happens
// to contain the substring "TARGET" inside a different property
// (e.g. SUMMARY:TARGET) must NOT match. The raw-fallback guard
// against the specific "UID:" prefix is what provides this safety.
func TestFindUIDInEvents_SubstringSafety(t *testing.T) {
	events := []caldav.Event{
		// Has "TARGET" in the summary but a different UID.
		// Must NOT match because the needle is "UID:TARGET".
		{Path: "/cal/a.ics", UID: "different-uid", Data: "BEGIN:VEVENT\r\nUID:different-uid\r\nSUMMARY:Meet with TARGET team\r\nEND:VEVENT"},
	}

	got := findUIDInEvents(events, "TARGET")
	if got != "" {
		t.Errorf("expected no match (TARGET appears only in SUMMARY), got %q", got)
	}
}

// TestModeLabel covers the tiny mode-label helper for consistency
// between the startup banner and the summary footer.
func TestModeLabel(t *testing.T) {
	if got := modeLabel(true); got != "CONFIRM (will delete)" {
		t.Errorf("confirm=true: unexpected label %q", got)
	}
	if got := modeLabel(false); got != "DRY-RUN (read-only)" {
		t.Errorf("confirm=false: unexpected label %q", got)
	}
}
