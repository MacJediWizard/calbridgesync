package caldav

import (
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Sync-flow tests for #206: an object that was listed but could not be
// read must never be treated as deleted.

var errFake500 = fakeErr("500 Internal Server Error")

// oneWaySteady seeds source events at <uid>.ics (or at the given path
// override) and runs two cycles so every UID is tracked with both ETags.
func oneWaySteady(t *testing.T, at map[string]string, uids ...string) *flowHarness {
	t.Helper()
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	for _, uid := range uids {
		if p, ok := at[uid]; ok {
			h.src.seedAt(p, uid, "Event "+uid, flowStart)
		} else {
			h.src.seed(flowSrcCal, uid, "Event "+uid, flowStart)
		}
	}
	h.cycle()
	r := h.cycle()
	assertNoWarnings(t, "steady", r)
	return h
}

func hasWarning(r *SyncResult, substr string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// (a) A source GET fails with 500 at <uid>.ics. The tracked destination
// copy survives, and a real orphan is still deleted.
func TestUnreadable_OneWay_TransientAtUIDPathIsExcluded(t *testing.T) {
	h := oneWaySteady(t, nil, "A", "B", "C", "D")

	h.src.failReadOn(srcPath("A"), errFake500)
	h.src.remove(srcPath("C"))
	r := h.cycle()

	assertPaths(t, "cycle3", "dest DELETEs", h.dst.deleteLog(), destPath("C"))
	if _, ok := h.dst.get(destPath("A")); !ok {
		t.Errorf("cycle3: dest A was deleted although source A was only unreadable")
	}
	if !hasWarning(r, "unreadable") {
		t.Errorf("cycle3: warnings = %v, want an unreadable-object warning", r.Warnings)
	}
}

// (b) A source GET fails with 500 at a path that does not name a
// tracked UID. Nothing can say which event it is, so no deletion pass
// runs this cycle, and a Warning says so.
func TestUnreadable_OneWay_TransientAtArbitraryPathBlocksDeletion(t *testing.T) {
	opaque := flowSrcCal + "opaque-1.ics"
	h := oneWaySteady(t, map[string]string{"A": opaque}, "A", "B", "C", "D")

	h.src.failReadOn(opaque, errFake500)
	h.src.remove(srcPath("C"))
	r := h.cycle()

	assertPaths(t, "cycle3", "dest DELETEs", h.dst.deleteLog())
	assertPaths(t, "cycle3", "dest contents", h.dst.paths(),
		destPath("A"), destPath("B"), destPath("C"), destPath("D"))
	if !hasWarning(r, "skipping all deletion passes") {
		t.Errorf("cycle3: warnings = %v, want a skipped-deletion warning", r.Warnings)
	}

	// Next cycle the object reads again: the real orphan C is deleted.
	h.src.failReadOn(opaque, nil)
	r = h.cycle()
	assertNoWarnings(t, "cycle4", r)
	assertPaths(t, "cycle4", "dest DELETEs", h.dst.deleteLog(), destPath("C"))
}

// (c) A malformed source object has a raw UID: line. That UID is
// excluded even though its path does not name it.
func TestUnreadable_OneWay_MalformedWithRawUIDIsExcluded(t *testing.T) {
	h := oneWaySteady(t, nil, "A", "B", "C", "D")

	// A becomes malformed (go-ical can no longer parse it); only a raw
	// UID: line survives. Its listed path is not A.ics.
	h.src.remove(srcPath("A"))
	h.src.addMalformed(flowSrcCal+"opaque-9.ics", "A")
	h.src.remove(srcPath("C"))
	r := h.cycle()

	assertPaths(t, "cycle3", "dest DELETEs", h.dst.deleteLog(), destPath("C"))
	if _, ok := h.dst.get(destPath("A")); !ok {
		t.Errorf("cycle3: dest A was deleted although source A was only malformed")
	}
	if !hasWarning(r, "unreadable") {
		t.Errorf("cycle3: warnings = %v, want an unreadable-object warning", r.Warnings)
	}
}

// (d) A malformed object with no recoverable UID does not block
// deletions, but it is warned about.
func TestUnreadable_OneWay_UnmappedMalformedWarnsButDeletionsRun(t *testing.T) {
	h := oneWaySteady(t, nil, "A", "B", "C", "D")

	h.src.addMalformed(flowSrcCal+"junk.ics", "")
	h.src.remove(srcPath("C"))
	r := h.cycle()

	assertPaths(t, "cycle3", "dest DELETEs", h.dst.deleteLog(), destPath("C"))
	if !hasWarning(r, "could not be matched to an event UID") {
		t.Errorf("cycle3: warnings = %v, want an unmatched-malformed warning", r.Warnings)
	}
}

// Two-way: an unreadable destination object must not delete the
// user's source event, and an unreadable source object must not
// delete the destination copy or its tracking row.
func TestUnreadable_TwoWay_NeitherSideCountsAsDeleted(t *testing.T) {
	h := twoWaySetup(t, 0) // SyncInterval 0: outside the CreatedAt safety window

	h.dst.failReadOn(destPath("B"), errFake500)
	h.src.failReadOn(srcPath("C"), errFake500)
	r := h.cycle()

	assertPaths(t, "cycle3", "source DELETEs", h.src.deleteLog())
	assertPaths(t, "cycle3", "dest DELETEs", h.dst.deleteLog())
	assertRowUIDs(t, "cycle3", h.rows(), "A", "B", "C", "D", "X")
	if !hasWarning(r, "unreadable") {
		t.Errorf("cycle3: warnings = %v, want an unreadable-object warning", r.Warnings)
	}
}

// Two-way "deleted from both" cleanup must not drop the tracking row
// of an event that is merely unreadable on source.
func TestUnreadable_TwoWay_RowKeptWhenUnreadableOnSourceAndGoneFromDest(t *testing.T) {
	h := twoWaySetup(t, 0)

	h.src.failReadOn(srcPath("D"), errFake500)
	h.dst.remove(destPath("D"))
	h.cycle()

	assertPaths(t, "cycle3", "source DELETEs", h.src.deleteLog())
	if _, ok := h.rows()["D"]; !ok {
		t.Errorf("cycle3: tracking row D was removed although source D was only unreadable")
	}
}

func TestUIDFromObjectPath(t *testing.T) {
	tests := map[string]string{
		"/cal/abc.ics":                   "abc",
		"/cal/abc.ICS":                   "abc",
		"/cal/abc%40example.com.ics":     "abc@example.com",
		"/cal/abc.ics/":                  "abc",
		"/cal/abc":                       "",
		"/cal/.ics":                      "",
		"/cal/sub/E1F2-uid@host.org.ics": "E1F2-uid@host.org",
	}
	for in, want := range tests {
		if got := uidFromObjectPath(in); got != want {
			t.Errorf("uidFromObjectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanUnreadableExclusions_UntrackedBasenameIsUnmapped(t *testing.T) {
	tracked := map[string]*db.SyncedEvent{"A": {EventUID: "A"}}
	// Basename "B" is not tracked: the object could be any event, so a
	// transient failure there must block deletions.
	ex, block, _ := planUnreadableExclusions(
		[]UnreadableObject{{Path: "/cal/B.ics", Kind: UnreadableTransient}}, nil, tracked)
	if !block || len(ex) != 0 {
		t.Errorf("untracked basename: exclude=%v block=%v, want none and blocked", ex, block)
	}
	ex, block, _ = planUnreadableExclusions(nil,
		[]UnreadableObject{{Path: "/dest/A.ics", Kind: UnreadableTransient}}, tracked)
	if block || !ex["A"] {
		t.Errorf("tracked basename: exclude=%v block=%v, want A excluded, not blocked", ex, block)
	}
}
