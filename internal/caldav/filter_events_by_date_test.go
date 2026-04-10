package caldav

import (
	"fmt"
	"testing"
	"time"
)

// TestFilterEventsByDate_RRULEAlwaysIncluded is the characterization
// test for William's fix in commit 58d890f. Recurring events have a
// DTSTART equal to the FIRST occurrence, which can be years in the
// past. Without the RRULE short-circuit, filterEventsByDate would
// drop recurring events with old DTSTART even though the event
// recurs into the future.
//
// This test locks in the short-circuit so a future refactor cannot
// silently regress the bug. If this test ever fails, the commit
// comment on 58d890f ("Fix date filter skipping recurring events
// with old DTSTART") explains why the behavior must be preserved.
func TestFilterEventsByDate_RRULEAlwaysIncluded(t *testing.T) {
	// Cutoff: events older than 30 days ago should normally be filtered
	cutoff := time.Now().AddDate(0, 0, -30)

	// Old start date (years in the past)
	oldStart := "20200101T120000Z"

	recurringEvent := Event{
		UID:       "recurring-weekly",
		Summary:   "Weekly Team Meeting",
		StartTime: oldStart,
		// RRULE indicates this event recurs, so it must survive the filter
		// even though its DTSTART is from 2020.
		Data: "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\n" +
			"UID:recurring-weekly\r\nDTSTART:20200101T120000Z\r\nDTEND:20200101T130000Z\r\n" +
			"RRULE:FREQ=WEEKLY;BYDAY=MO\r\nSUMMARY:Weekly Team Meeting\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	nonRecurringOldEvent := Event{
		UID:       "one-off-old",
		Summary:   "Old meeting",
		StartTime: oldStart,
		// No RRULE — this is a one-off event. The filter should drop it
		// because its DTSTART is before the cutoff.
		Data: "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\n" +
			"UID:one-off-old\r\nDTSTART:20200101T120000Z\r\nDTEND:20200101T130000Z\r\n" +
			"SUMMARY:Old meeting\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	filtered := filterEventsByDate([]Event{recurringEvent, nonRecurringOldEvent}, cutoff)

	if len(filtered) != 1 {
		t.Fatalf("expected exactly 1 event after filter (recurring only), got %d", len(filtered))
	}
	if filtered[0].UID != "recurring-weekly" {
		t.Errorf("expected recurring event to survive the filter, got %q", filtered[0].UID)
	}
}

// TestFilterEventsByDate_RecentEventsIncluded verifies the normal
// case: a non-recurring event with a DTSTART newer than the cutoff
// is included.
func TestFilterEventsByDate_RecentEventsIncluded(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)

	// Event from yesterday — clearly within the 30-day window
	recent := time.Now().AddDate(0, 0, -1).UTC().Format("20060102T150405Z")

	event := Event{
		UID:       "recent-event",
		StartTime: recent,
		Data:      "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:recent-event\r\nDTSTART:" + recent + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	filtered := filterEventsByDate([]Event{event}, cutoff)
	if len(filtered) != 1 {
		t.Errorf("expected recent event to be included, got %d events after filter", len(filtered))
	}
}

// TestFilterEventsByDate_OldEventsDropped verifies the normal case
// in the other direction: a non-recurring event older than the cutoff
// is dropped.
func TestFilterEventsByDate_OldEventsDropped(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)

	event := Event{
		UID:       "old-event",
		StartTime: "20200101T120000Z",
		Data: "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:old-event\r\n" +
			"DTSTART:20200101T120000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	filtered := filterEventsByDate([]Event{event}, cutoff)
	if len(filtered) != 0 {
		t.Errorf("expected old non-recurring event to be filtered out, got %d events", len(filtered))
	}
}

// TestFilterEventsByDate_FutureEventsAlwaysIncluded locks in the
// second "always include" case: events with a future DTSTART are
// always kept regardless of the cutoff. This catches the case where
// cutoff is somehow in the future (test clock skew, config error).
func TestFilterEventsByDate_FutureEventsAlwaysIncluded(t *testing.T) {
	// Cutoff is 1 hour from now
	cutoff := time.Now().Add(time.Hour)

	// Event is 2 hours from now — after the cutoff
	future := time.Now().Add(2 * time.Hour).UTC().Format("20060102T150405Z")

	event := Event{
		UID:       "future-event",
		StartTime: future,
		Data:      "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:future-event\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	filtered := filterEventsByDate([]Event{event}, cutoff)
	if len(filtered) != 1 {
		t.Errorf("expected future event to be included, got %d", len(filtered))
	}
}

// TestFilterEventsByDate_EmptyStartTimeIncluded verifies the "include
// if unparseable" safety rule: events without a StartTime (e.g., tasks,
// events the parser couldn't extract) are always included to avoid
// accidental data loss.
func TestFilterEventsByDate_EmptyStartTimeIncluded(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)

	event := Event{
		UID:       "no-start-time",
		StartTime: "", // unparsed
		Data:      "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:no-start-time\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	filtered := filterEventsByDate([]Event{event}, cutoff)
	if len(filtered) != 1 {
		t.Errorf("expected event with empty StartTime to be included (safety), got %d", len(filtered))
	}
}

// TestFilterEventsByDate_UnparseableStartTimeIncluded verifies the
// other safety rule: if a StartTime is present but doesn't match any
// of the supported iCalendar formats, the event is still included
// rather than being silently dropped.
func TestFilterEventsByDate_UnparseableStartTimeIncluded(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)

	event := Event{
		UID:       "weird-format",
		StartTime: "not-a-real-date-format",
		Data:      "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:weird-format\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}

	filtered := filterEventsByDate([]Event{event}, cutoff)
	if len(filtered) != 1 {
		t.Errorf("expected event with unparseable StartTime to be included (safety), got %d", len(filtered))
	}
}

// TestFilterEventsByDate_SupportedDateFormats exercises the iCalendar
// format parser to ensure each of the 5 supported formats produces
// the expected filter result. If a new format is added, a test case
// here lets us verify it works without digging through sync.go.
func TestFilterEventsByDate_SupportedDateFormats(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)
	old := time.Now().AddDate(0, 0, -365) // 1 year ago — should be filtered out

	tests := []struct {
		name       string
		format     string
		shouldDrop bool
	}{
		{"UTC datetime Z suffix", old.UTC().Format("20060102T150405Z"), true},
		{"local datetime no suffix", old.UTC().Format("20060102T150405"), true},
		{"date only compact", old.UTC().Format("20060102"), true},
		{"ISO with dashes", old.UTC().Format("2006-01-02T15:04:05Z"), true},
		{"ISO date only with dashes", old.UTC().Format("2006-01-02"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := Event{
				UID:       "test-" + tt.name,
				StartTime: tt.format,
				Data:      "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
			}
			filtered := filterEventsByDate([]Event{event}, cutoff)
			if tt.shouldDrop && len(filtered) != 0 {
				t.Errorf("expected old event in format %q to be dropped, but it was kept", tt.format)
			}
		})
	}
}

// TestFilterEventsByDate_MixedBatch verifies the filter handles a
// realistic mixed batch: recurring events, recent non-recurring,
// old non-recurring, events with missing start times. Each class
// lands on the correct side of the filter.
func TestFilterEventsByDate_MixedBatch(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)
	recent := time.Now().AddDate(0, 0, -5).UTC().Format("20060102T150405Z")
	old := "20200101T120000Z"

	events := []Event{
		{
			UID: "recurring-old", StartTime: old,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
		{
			UID: "recent-oneoff", StartTime: recent,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
		{
			UID: "old-oneoff", StartTime: old,
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
		{
			UID: "no-time", StartTime: "",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
	}

	filtered := filterEventsByDate(events, cutoff)

	// Expected to survive: recurring-old (RRULE), recent-oneoff (within window),
	// no-time (empty StartTime safety)
	// Expected to drop: old-oneoff
	if len(filtered) != 3 {
		t.Fatalf("expected 3 surviving events, got %d", len(filtered))
	}

	got := make(map[string]bool, len(filtered))
	for _, e := range filtered {
		got[e.UID] = true
	}
	want := []string{"recurring-old", "recent-oneoff", "no-time"}
	for _, uid := range want {
		if !got[uid] {
			t.Errorf("expected %q to survive, not found", uid)
		}
	}
	if got["old-oneoff"] {
		t.Error("old-oneoff should have been dropped by the date filter")
	}
}

// TestFilterEventsByDate_PreservesOrder verifies that filtering
// preserves the input order. Downstream code that pairs events with
// indexes, progress tracking, or logs would break silently if the
// order changed.
func TestFilterEventsByDate_PreservesOrder(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -30)
	recent := time.Now().AddDate(0, 0, -1).UTC().Format("20060102T150405Z")

	events := make([]Event, 10)
	for i := 0; i < 10; i++ {
		events[i] = Event{
			UID:       fmt.Sprintf("event-%d", i),
			StartTime: recent,
			Data:      "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		}
	}

	filtered := filterEventsByDate(events, cutoff)
	if len(filtered) != 10 {
		t.Fatalf("expected all 10 to survive, got %d", len(filtered))
	}
	for i, e := range filtered {
		want := fmt.Sprintf("event-%d", i)
		if e.UID != want {
			t.Errorf("filter changed order at index %d: got %q, want %q", i, e.UID, want)
		}
	}
}

// TestFilterEventsByDate_EmptyInput is the degenerate edge case.
func TestFilterEventsByDate_EmptyInput(t *testing.T) {
	filtered := filterEventsByDate(nil, time.Now())
	if len(filtered) != 0 {
		t.Errorf("expected empty result for nil input, got %d", len(filtered))
	}
	filtered = filterEventsByDate([]Event{}, time.Now())
	if len(filtered) != 0 {
		t.Errorf("expected empty result for empty slice, got %d", len(filtered))
	}
}
