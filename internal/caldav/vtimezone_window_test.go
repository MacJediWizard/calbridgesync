package caldav

import (
	"strings"
	"testing"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// berlinVTimezone is a real-world style DST VTIMEZONE: its STANDARD and
// DAYLIGHT sub-components carry RRULE:FREQ=YEARLY lines, as Google,
// Outlook and tzurl feeds do.
const berlinVTimezone = "BEGIN:VTIMEZONE\r\n" +
	"TZID:Europe/Berlin\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19701025T030000\r\n" +
	"TZOFFSETFROM:+0200\r\n" +
	"TZOFFSETTO:+0100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\n" +
	"END:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\n" +
	"DTSTART:19700329T020000\r\n" +
	"TZOFFSETFROM:+0100\r\n" +
	"TZOFFSETTO:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n" +
	"END:DAYLIGHT\r\n" +
	"END:VTIMEZONE\r\n"

// withBerlinVTimezone inserts berlinVTimezone right after the
// VCALENDAR header lines of a testICS body.
func withBerlinVTimezone(ics string) string {
	return strings.Replace(ics, "BEGIN:VEVENT\r\n", berlinVTimezone+"BEGIN:VEVENT\r\n", 1)
}

const icsWindowFeed = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Test//EN\r\n" +
	berlinVTimezone +
	"BEGIN:VEVENT\r\n" +
	"UID:old-oneoff@example.com\r\n" +
	"DTSTAMP:20200101T000000Z\r\n" +
	"DTSTART;TZID=Europe/Berlin:20200110T090000\r\n" +
	"DTEND;TZID=Europe/Berlin:20200110T100000\r\n" +
	"SUMMARY:Old one-off\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:old-weekly@example.com\r\n" +
	"DTSTAMP:20200101T000000Z\r\n" +
	"DTSTART;TZID=Europe/Berlin:20200106T090000\r\n" +
	"DTEND;TZID=Europe/Berlin:20200106T100000\r\n" +
	"RRULE:FREQ=WEEKLY\r\n" +
	"SUMMARY:Old weekly\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:future-oneoff@example.com\r\n" +
	"DTSTAMP:20200101T000000Z\r\n" +
	"DTSTART;TZID=Europe/Berlin:20300110T090000\r\n" +
	"DTEND;TZID=Europe/Berlin:20300110T100000\r\n" +
	"SUMMARY:Future one-off\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// TestFilterEventsInWindow_IgnoresVTimezoneRRULE: once FetchEvents
// copies VTIMEZONEs into each object (#248), a one-off event in a DST
// zone carries "RRULE:" inside its VTIMEZONE. The sync_days_past
// window must still drop it; only a VEVENT RRULE marks a series.
func TestFilterEventsInWindow_IgnoresVTimezoneRRULE(t *testing.T) {
	events := fetchICSForTest(t, icsWindowFeed)
	byUID := make(map[string]Event, len(events))
	for _, e := range events {
		byUID[e.UID] = e
	}
	old, ok := byUID["old-oneoff@example.com"]
	if !ok {
		t.Fatalf("old-oneoff missing from %d events", len(events))
	}
	// Premise: the copied VTIMEZONE puts "RRULE:" into a one-off object.
	if !strings.Contains(old.Data, "RRULE:FREQ=YEARLY") {
		t.Fatalf("premise: old one-off should carry the VTIMEZONE RRULE:\n%s", old.Data)
	}

	cutoff := time.Now().AddDate(0, 0, -30)
	got := map[string]bool{}
	for _, e := range filterEventsInWindow(events, cutoff) {
		got[e.UID] = true
	}
	want := map[string]bool{"old-weekly@example.com": true, "future-oneoff@example.com": true}
	if len(got) != len(want) || !got["old-weekly@example.com"] || !got["future-oneoff@example.com"] {
		t.Errorf("filterEventsInWindow kept %v, want %v", got, want)
	}

	// Kept events are returned unmodified, VTIMEZONE included.
	for _, e := range filterEventsInWindow(events, cutoff) {
		if e.Data != byUID[e.UID].Data {
			t.Errorf("%s: Data was modified by the filter", e.UID)
		}
	}
}

func TestStripVTimezones(t *testing.T) {
	in := withBerlinVTimezone(testICS("u", "S", "20200101T000000Z"))
	out := stripVTimezones(in)
	if out != testICS("u", "S", "20200101T000000Z") {
		t.Errorf("stripVTimezones left:\n%s", out)
	}
	plain := testICS("u", "S", "20200101T000000Z")
	if stripVTimezones(plain) != plain {
		t.Error("stripVTimezones changed a body with no VTIMEZONE")
	}
}

// TestSyncFlow_DateWindowIgnoresVTimezoneRRULE drives fullSync with
// sync_days_past set. Neither side of the date filter may treat a
// VTIMEZONE RRULE as a recurring event:
//   - source side: an old one-off that was never synced must not be
//     created (mass create of feed history, PR #249 review);
//   - destination side: an old tracked one-off whose destination copy
//     carries a VTIMEZONE but whose source copy does not must not look
//     destination-only, or one-way orphan deletion removes it.
func TestSyncFlow_DateWindowIgnoresVTimezoneRRULE(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	const oldStart = "20200110T080000Z"
	for _, uid := range []string{"A", "B", "C"} {
		h.src.seed(flowSrcCal, uid, "Event "+uid, flowStart)
	}
	h.src.seed(flowSrcCal, "AGED", "Aged one-off", oldStart)

	// Cycle 1, no window: everything is created and tracked.
	r := h.cycle()
	assertCounts(t, "cycle1", r, counts{Created: 4, EventsProcessed: 4})

	// The destination copy of AGED now carries a VTIMEZONE (written by
	// a VTIMEZONE-copying release, or added by the server).
	setFakeData(h.dst, destPath("AGED"), withBerlinVTimezone(testICS("AGED", "Aged one-off", oldStart)))
	// A never-synced old one-off appears in the source with a VTIMEZONE.
	setFakeData(h.src, h.src.seed(flowSrcCal, "HISTORY", "Old history", oldStart).Path,
		withBerlinVTimezone(testICS("HISTORY", "Old history", oldStart)))

	h.source.SyncDaysPast = 30
	r = h.cycle()
	assertNoWarnings(t, "cycle2", r)
	assertPaths(t, "cycle2", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle2", "dest DELETEs", h.dst.deleteLog())
	if _, ok := h.dst.get(destPath("AGED")); !ok {
		t.Error("cycle2: AGED was deleted from the destination")
	}
}

func setFakeData(f *fakeCalendarClient, path, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.events[path]
	if !ok {
		panic("setFakeData: no event at " + path)
	}
	e.Data = data
	f.events[path] = e
}

// TestFindZombieMasters_VTimezoneRRULEIsNotAMaster: an orphaned
// RECURRENCE-ID override whose object also carries a DST VTIMEZONE is
// still an orphan. The VTIMEZONE's RRULE is not a live master.
func TestFindZombieMasters_VTimezoneRRULEIsNotAMaster(t *testing.T) {
	events := []Event{{
		Path: "/cal/orphan.ics",
		UID:  "orphan",
		Data: "BEGIN:VCALENDAR\r\n" + berlinVTimezone +
			"BEGIN:VEVENT\r\nUID:orphan\r\nRECURRENCE-ID;TZID=Europe/Berlin:20260420T090000\r\n" +
			"DTSTART;TZID=Europe/Berlin:20260421T090000\r\nSUMMARY:Override\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}}
	got := keysOf(FindZombieMasters(events))
	if len(got) != 1 || got[0] != (fingerprintKey{UID: "orphan", Reason: ZombieReasonOrphanedOverride}) {
		t.Errorf("FindZombieMasters = %v, want orphaned override for UID orphan", got)
	}
}
