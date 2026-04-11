package caldav

import (
	"sort"
	"testing"
)

// fingerprintKey is a stable (UID,Reason) tuple we sort on so test
// assertions are order-independent. FindZombieMasters emits
// findings in map-iteration order, which is not deterministic in
// Go; the tests compare sorted slices to avoid flakiness.
type fingerprintKey struct {
	UID    string
	Reason string
}

func keysOf(findings []ZombieFingerprint) []fingerprintKey {
	out := make([]fingerprintKey, 0, len(findings))
	for _, f := range findings {
		out = append(out, fingerprintKey{UID: f.UID, Reason: f.Reason})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UID != out[j].UID {
			return out[i].UID < out[j].UID
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// TestFindZombieMasters_EmptyAndNil covers the trivial edge cases
// so the detector never panics when called on an empty source
// calendar. This is the "source returned zero events" path —
// combined with the empty-source guard from #80 it should be a
// no-op, not a crash.
func TestFindZombieMasters_EmptyAndNil(t *testing.T) {
	if got := FindZombieMasters(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
	if got := FindZombieMasters([]Event{}); got != nil {
		t.Errorf("empty slice: want nil, got %v", got)
	}
}

// TestFindZombieMasters_CleanSeriesIsIgnored covers the happy-path
// regression: a normal recurring series (master with RRULE + a
// regular override rescheduling one occurrence) must NOT be
// flagged. If the detector fires on healthy data it becomes
// worthless because operators will tune it out.
func TestFindZombieMasters_CleanSeriesIsIgnored(t *testing.T) {
	events := []Event{
		{
			Path: "/cal/weekly-standup-master.ics",
			UID:  "weekly-standup",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:weekly-standup\r\nSUMMARY:Weekly Standup\r\nDTSTART:20260413T150000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
		{
			Path: "/cal/weekly-standup-exception.ics",
			UID:  "weekly-standup",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:weekly-standup\r\nSUMMARY:Weekly Standup (rescheduled)\r\nRECURRENCE-ID:20260420T150000Z\r\nDTSTART:20260421T150000Z\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
	}

	got := FindZombieMasters(events)
	if len(got) != 0 {
		t.Errorf("clean series triggered %d false positives: %v", len(got), got)
	}
}

// TestFindZombieMasters_FakedMasterDetected is the WOS zombie
// fingerprint exactly as it appeared on William's instance: a
// faked master stub and an orphaned override sharing one UID.
// The X-MOZ-FAKED-MASTER marker is authoritative so only ONE
// finding should be emitted (fakedMaster reason); the orphan
// pass should NOT double-report this UID under both reasons.
func TestFindZombieMasters_FakedMasterDetected(t *testing.T) {
	events := []Event{
		{
			Path: "/cal/wos-master-stub.ics",
			UID:  "040000-WOS-UID",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:040000-WOS-UID\r\nSUMMARY:Untitled\r\nDTSTART:20260403T110000Z\r\nX-MOZ-FAKED-MASTER:1\r\nX-MOZ-GENERATION:4\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
		{
			Path: "/cal/wos-override.ics",
			UID:  "040000-WOS-UID",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:040000-WOS-UID\r\nSUMMARY:WOS Tech Team and MacJedi - Hodinkee Mac migration weekly catch up\r\nRECURRENCE-ID:20260403T110000Z\r\nDTSTART:20260402T110000Z\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
	}

	got := FindZombieMasters(events)
	if len(got) != 1 {
		t.Fatalf("want 1 finding (faked master authoritative), got %d: %v", len(got), got)
	}
	if got[0].UID != "040000-WOS-UID" {
		t.Errorf("unexpected UID: %q", got[0].UID)
	}
	if got[0].Reason != ZombieReasonFakedMaster {
		t.Errorf("want reason %q, got %q", ZombieReasonFakedMaster, got[0].Reason)
	}
	if got[0].EventPath == "" {
		t.Errorf("expected non-empty EventPath so operators can follow up")
	}
}

// TestFindZombieMasters_OrphanedOverride covers the case where the
// X-MOZ-FAKED-MASTER marker is absent but the master VEVENT is
// also absent — e.g. a CalDAV server that deleted the master but
// left the override in place. Apple Calendar drops orphaned
// overrides silently, which is exactly the user-visible effect we
// want to detect and warn on.
func TestFindZombieMasters_OrphanedOverride(t *testing.T) {
	events := []Event{
		{
			Path: "/cal/override-only.ics",
			UID:  "lost-master-uid",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:lost-master-uid\r\nRECURRENCE-ID:20260420T150000Z\r\nSUMMARY:Meeting override\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
	}

	got := FindZombieMasters(events)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Reason != ZombieReasonOrphanedOverride {
		t.Errorf("want reason %q, got %q", ZombieReasonOrphanedOverride, got[0].Reason)
	}
}

// TestFindZombieMasters_MultipleDistinctZombies verifies that the
// detector reports each defective UID exactly once even when the
// event list contains several unrelated zombies (which can happen
// after a partial recovery or when more than one recurring series
// was affected by the same bug).
func TestFindZombieMasters_MultipleDistinctZombies(t *testing.T) {
	events := []Event{
		// Zombie A: faked master.
		{Path: "/a-stub.ics", UID: "a", Data: "BEGIN:VEVENT\r\nUID:a\r\nX-MOZ-FAKED-MASTER:1\r\nEND:VEVENT"},
		{Path: "/a-override.ics", UID: "a", Data: "BEGIN:VEVENT\r\nUID:a\r\nRECURRENCE-ID:20260101T000000Z\r\nEND:VEVENT"},
		// Zombie B: orphaned override only.
		{Path: "/b-override.ics", UID: "b", Data: "BEGIN:VEVENT\r\nUID:b\r\nRECURRENCE-ID:20260101T000000Z\r\nEND:VEVENT"},
		// Clean series C: master with RRULE + its override.
		{Path: "/c-master.ics", UID: "c", Data: "BEGIN:VEVENT\r\nUID:c\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT"},
		{Path: "/c-override.ics", UID: "c", Data: "BEGIN:VEVENT\r\nUID:c\r\nRECURRENCE-ID:20260101T000000Z\r\nEND:VEVENT"},
	}

	got := keysOf(FindZombieMasters(events))
	want := []fingerprintKey{
		{UID: "a", Reason: ZombieReasonFakedMaster},
		{UID: "b", Reason: ZombieReasonOrphanedOverride},
	}

	if len(got) != len(want) {
		t.Fatalf("want %d findings, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding[%d]: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// TestFindZombieMasters_RRULEInSummaryDoesNotFalsePositive guards
// against the most obvious regression from the substring-based
// detector design: if the text "RRULE" appears inside a SUMMARY,
// DESCRIPTION, or LOCATION property (e.g. a meeting named "RRULE
// discussion"), the detector must NOT consider it a live master.
// The same applies to "RECURRENCE-ID" appearing in a DESCRIPTION
// block (e.g. a paste from documentation). containsProperty
// anchors on `\nNAME:` / `\nNAME;` exactly to prevent this.
func TestFindZombieMasters_RRULEInSummaryDoesNotFalsePositive(t *testing.T) {
	events := []Event{
		// Override-only event; the string "RRULE" appears inside
		// the SUMMARY and DESCRIPTION but should NOT count as a
		// live master. Result: the UID still looks orphaned.
		{
			Path: "/cal/false-positive-test.ics",
			UID:  "tricky-uid",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:tricky-uid\r\nSUMMARY:RRULE semantics discussion (recurring)\r\nDESCRIPTION:A meeting where we will talk about RRULE rules and how RECURRENCE-ID works.\r\nRECURRENCE-ID:20260101T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
	}

	got := FindZombieMasters(events)
	if len(got) != 1 {
		t.Fatalf("want 1 finding (orphaned override), got %d: %v", len(got), got)
	}
	if got[0].Reason != ZombieReasonOrphanedOverride {
		t.Errorf("want %q, got %q — detector false-positive'd on RRULE/RECURRENCE-ID appearing inside SUMMARY or DESCRIPTION", ZombieReasonOrphanedOverride, got[0].Reason)
	}
}

// TestFindZombieMasters_RRULEWithParameters verifies that a
// master VEVENT whose RRULE property has parameters (e.g.
// `RRULE;X-VENDOR-TAG=foo:FREQ=WEEKLY`) is still recognized as
// having a live master. Property parameters are legal in iCalendar
// and the detector must not miss them.
func TestFindZombieMasters_RRULEWithParameters(t *testing.T) {
	events := []Event{
		{
			Path: "/cal/master-with-params.ics",
			UID:  "param-uid",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:param-uid\r\nSUMMARY:Weekly with params\r\nRRULE;X-VENDOR-TAG=foo:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
		{
			Path: "/cal/override.ics",
			UID:  "param-uid",
			Data: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:param-uid\r\nRECURRENCE-ID:20260101T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR",
		},
	}

	got := FindZombieMasters(events)
	if len(got) != 0 {
		t.Errorf("parameterized RRULE triggered false positive: %v", got)
	}
}

// TestFindZombieMasters_EmptyUIDSkipped covers the defensive case
// where an Event has empty Data or empty UID. These are garbage
// inputs that should be ignored without panicking.
func TestFindZombieMasters_EmptyUIDSkipped(t *testing.T) {
	events := []Event{
		{Path: "/cal/empty-data.ics", UID: "zeroed-uid", Data: ""},
		{Path: "/cal/empty-uid.ics", UID: "", Data: "BEGIN:VEVENT\r\nX-MOZ-FAKED-MASTER:1\r\nEND:VEVENT"},
	}

	got := FindZombieMasters(events)
	if len(got) != 0 {
		t.Errorf("want no findings for zero-UID / empty-Data events, got %v", got)
	}
}

// TestContainsProperty covers the anchor logic directly. This is
// the subroutine that makes or breaks false-positive safety, so
// it's worth the dedicated coverage.
func TestContainsProperty(t *testing.T) {
	cases := []struct {
		name string
		data string
		prop string
		want bool
	}{
		{"empty data", "", "RRULE", false},
		{"empty prop", "BEGIN:VEVENT\r\n", "", false},
		{"direct colon match", "BEGIN:VEVENT\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT", "RRULE", true},
		{"parameterized match", "BEGIN:VEVENT\r\nRRULE;X-VENDOR-TAG=foo:FREQ=WEEKLY\r\nEND:VEVENT", "RRULE", true},
		{"substring in SUMMARY false", "BEGIN:VEVENT\r\nSUMMARY:RRULE talk\r\nEND:VEVENT", "RRULE", false},
		{"substring in DESCRIPTION false", "BEGIN:VEVENT\r\nDESCRIPTION:see RRULE docs\r\nEND:VEVENT", "RRULE", false},
		{"multiple matches", "BEGIN:VEVENT\r\nRRULE:FREQ=WEEKLY\r\nRRULE:FREQ=DAILY\r\nEND:VEVENT", "RRULE", true},
		{"different property", "BEGIN:VEVENT\r\nDTSTART:20260101T000000Z\r\nEND:VEVENT", "RRULE", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsProperty(tc.data, tc.prop); got != tc.want {
				t.Errorf("containsProperty(%q, %q): want %v, got %v", tc.data, tc.prop, tc.want, got)
			}
		})
	}
}
