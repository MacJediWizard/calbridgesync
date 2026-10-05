package caldav

import (
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// End-to-end coverage for the per-source "Ignore alarms" flag (#174)
// through fullSync, using the sync-flow harness.

// testICSWithAlarm is testICS plus one well-formed VALARM.
func testICSWithAlarm(uid, summary, start string) string {
	return "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//calbridgesync//fake//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:" + start + "\r\n" +
		"SUMMARY:" + summary + "\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

// seedWithAlarm stores an event carrying a VALARM.
func (f *fakeCalendarClient) seedWithAlarm(calendarPath, uid, summary, start string) {
	e := f.seed(calendarPath, uid, summary, start)
	f.mu.Lock()
	defer f.mu.Unlock()
	e.Data = testICSWithAlarm(uid, summary, start)
	f.events[e.Path] = e
}

// touch simulates an out-of-band edit that keeps the rest of the
// object intact (only SUMMARY changes) and bumps the ETag.
func (f *fakeCalendarClient) touch(path, newSummary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.events[path]
	if !ok {
		panic("fakeCalendarClient.touch: no event at " + path)
	}
	e.Data = strings.Replace(e.Data, "SUMMARY:"+e.Summary, "SUMMARY:"+newSummary, 1)
	e.Summary = newSummary
	e.ETag = f.nextETag()
	f.events[path] = e
}

func hasAlarm(t *testing.T, c *fakeCalendarClient, path string) bool {
	t.Helper()
	e, ok := c.get(path)
	if !ok {
		t.Fatalf("no event at %s", path)
	}
	return strings.Contains(e.Data, "BEGIN:VALARM")
}

// Two-way + dest_wins + strip_alarms: the destination copy has no
// alarms, so the reverse pass wrote that alarm-less copy back over
// the source event and erased the user's own alarms on the source.
// Stripping must not apply to two-way syncs.
func TestSyncFlow_TwoWay_StripAlarmsDoesNotEraseSourceAlarms(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionTwoWay, db.ConflictDestWins, 3600)
	h.source.StripAlarms = true
	h.src.seedWithAlarm(flowSrcCal, "A", "Event A", flowStart)

	h.cycle() // create on dest
	h.cycle() // steady; records dest ETag

	// User edits the event on the destination (e.g. moves it in the
	// SOGo web UI). dest_wins propagates that edit back to the source.
	h.dst.touch(destPath("A"), "Event A (edited on dest)")
	h.cycle()

	if got, _ := h.src.get(srcPath("A")); got.Summary != "Event A (edited on dest)" {
		t.Fatalf("dest edit did not propagate to source; source summary = %q", got.Summary)
	}
	if !hasAlarm(t, h.src, srcPath("A")) {
		t.Error("two-way strip_alarms erased the VALARM from the SOURCE event")
	}
}

// Toggling "Ignore alarms" on an existing one-way source must apply to
// events that were already synced, not only to events whose source
// copy changes later. Toggling it off must restore the alarms.
func TestSyncFlow_OneWay_StripAlarmsToggleAppliesToSyncedEvents(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	h.src.seedWithAlarm(flowSrcCal, "A", "Event A", flowStart)

	h.cycle()
	h.cycle()
	if !hasAlarm(t, h.dst, destPath("A")) {
		t.Fatal("setup: dest A should carry the alarm while strip_alarms is off")
	}

	// Toggle on.
	h.source.StripAlarms = true
	r := h.cycle()
	assertCounts(t, "toggle on", r, counts{Updated: 1, EventsProcessed: 1})
	if hasAlarm(t, h.dst, destPath("A")) {
		t.Error("toggle on: dest A still carries the alarm")
	}

	// Steady: no re-PUT loop.
	r = h.cycle()
	assertCounts(t, "steady on", r, counts{EventsProcessed: 1})
	assertPaths(t, "steady on", "dest PUTs", h.dst.putLog())

	// Toggle off restores the alarm.
	h.source.StripAlarms = false
	r = h.cycle()
	assertCounts(t, "toggle off", r, counts{Updated: 1, EventsProcessed: 1})
	if !hasAlarm(t, h.dst, destPath("A")) {
		t.Error("toggle off: dest A alarm was not restored")
	}

	r = h.cycle()
	assertCounts(t, "steady off", r, counts{EventsProcessed: 1})
	assertPaths(t, "steady off", "dest PUTs", h.dst.putLog())
}

func TestStripAlarmsETag(t *testing.T) {
	if got := stripAlarmsETag(""); got != "" {
		t.Errorf("empty ETag must stay empty (legacy/ICS semantics), got %q", got)
	}
	once := stripAlarmsETag(`"etag-1"`)
	if once == `"etag-1"` {
		t.Error("non-empty ETag must be marked")
	}
	if twice := stripAlarmsETag(once); twice != once {
		t.Errorf("marking must be idempotent: %q -> %q", once, twice)
	}
}
