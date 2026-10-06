package caldav

import (
	"reflect"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Forward updates PUT with If-Match set to the destination ETag read this
// cycle; creates send no precondition. A destination edit that lands
// between the read and the PUT makes the PUT fail with 412: that counts
// as Skipped (no warning), keeps the tracking row with its previous
// ETags, leaves the destination edit in place, and the next cycle
// retries against the fresh destination copy.
func TestSyncFlow_ForwardUpdateIfMatch412KeepsTracking(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	for _, uid := range []string{"A", "B"} {
		h.src.seed(flowSrcCal, uid, "Event "+uid, flowStart)
	}

	r := h.cycle()
	assertNoWarnings(t, "cycle1", r)
	assertCounts(t, "cycle1", r, counts{Created: 2, EventsProcessed: 2})
	if got, want := h.dst.ifMatchLog(), []string{"", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("cycle1: create If-Match values = %q, want %q", got, want)
	}
	h.cycle() // steady: records dest ETags
	prev := *h.rows()["A"]
	if prev.DestETag == "" {
		t.Fatalf("cycle2: row A has no DestETag")
	}

	// Cycle 3: source edits A, and a destination edit to A lands after
	// the sync read the destination but before its PUT.
	h.src.edit(srcPath("A"), "Event A (source edit)")
	readETag := prev.DestETag
	h.dst.editBeforePut(destPath("A"), "Event A (dest edit)")
	r = h.cycle()
	assertNoWarnings(t, "cycle3", r)
	assertCounts(t, "cycle3", r, counts{Skipped: 1, EventsProcessed: 2})
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog())
	if got, want := h.dst.ifMatchLog(), []string{readETag}; !reflect.DeepEqual(got, want) {
		t.Errorf("cycle3: If-Match values = %q, want %q (dest ETag read this cycle)", got, want)
	}
	if got, _ := h.dst.get(destPath("A")); got.Summary != "Event A (dest edit)" {
		t.Errorf("cycle3: dest A summary = %q, want the concurrent dest edit preserved", got.Summary)
	}
	rows := h.rows()
	assertRowUIDs(t, "cycle3", rows, "A", "B")
	if rows["A"].SourceETag != prev.SourceETag || rows["A"].DestETag != prev.DestETag {
		t.Errorf("cycle3: row A ETags = (%q, %q), want previous (%q, %q)",
			rows["A"].SourceETag, rows["A"].DestETag, prev.SourceETag, prev.DestETag)
	}

	// Cycle 4: the source change is still pending, so the update is
	// retried with If-Match = the destination's current ETag.
	dstA, _ := h.dst.get(destPath("A"))
	r = h.cycle()
	assertNoWarnings(t, "cycle4", r)
	assertCounts(t, "cycle4", r, counts{Updated: 1, EventsProcessed: 2})
	if got, want := h.dst.ifMatchLog(), []string{dstA.ETag}; !reflect.DeepEqual(got, want) {
		t.Errorf("cycle4: If-Match values = %q, want %q", got, want)
	}
	if got, _ := h.dst.get(destPath("A")); got.Summary != "Event A (source edit)" {
		t.Errorf("cycle4: dest A summary = %q, want source edit", got.Summary)
	}
	srcA, _ := h.src.get(srcPath("A"))
	if got := h.rows()["A"].SourceETag; got != srcA.ETag {
		t.Errorf("cycle4: row A SourceETag = %q, want %q", got, srcA.ETag)
	}
}
