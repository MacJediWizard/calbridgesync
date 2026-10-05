package caldav

import (
	"context"
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Two selected source calendars feeding one destination calendar,
// two-way (#244). Every source calendar syncs into the same
// destination calendar, so calendar A's reverse-create pass sees
// calendar B's synced events as never-synced destination-only events
// and copies them into A (and vice versa). The pass must be skipped
// with a warning in that configuration.

const flowSrcCal2 = "/src/cal2/"

func TestSyncFlow_TwoWay_SharedDestSkipsReverseCreate(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionTwoWay, db.ConflictSourceWins, 0)
	calA := Calendar{Path: flowSrcCal, Name: "Work"}
	calB := Calendar{Path: flowSrcCal2, Name: "Home"}
	h.src.seed(flowSrcCal, "A1", "Work event", flowStart)
	h.src.seed(flowSrcCal2, "B1", "Home event", "20300303T100000Z")

	runCycle := func(step string) []*SyncResult {
		h.src.resetLog()
		h.dst.resetLog()
		var out []*SyncResult
		for i, cal := range []Calendar{calA, calB} {
			r := h.se.fullSync(context.Background(), h.source, h.src, h.dst, cal, i+1, true)
			if len(r.Errors) > 0 {
				t.Fatalf("%s %s: errors: %v", step, cal.Name, r.Errors)
			}
			out = append(out, r)
		}
		return out
	}

	for _, step := range []string{"cycle1", "cycle2"} {
		results := runCycle(step)
		assertPaths(t, step, "source PUTs", h.src.putLog())
		assertPaths(t, step, "source DELETEs", h.src.deleteLog())
		assertPaths(t, step, "dest DELETEs", h.dst.deleteLog())
		assertPaths(t, step, "source contents", h.src.paths(), "/src/cal/A1.ics", "/src/cal2/B1.ics")
		assertPaths(t, step, "dest contents", h.dst.paths(), destPath("A1"), destPath("B1"))
		for i, r := range results {
			if !hasWarningContaining(r, "reverse-create skipped") {
				t.Errorf("%s calendar %d: want a reverse-create skipped warning, got %v", step, i+1, r.Warnings)
			}
		}
	}
}

// A single source calendar keeps reverse-create: a destination-only
// event is still uploaded to the source.
func TestSyncFlow_TwoWay_SingleCalendarStillReverseCreates(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionTwoWay, db.ConflictSourceWins, 0)
	h.dst.seed(flowDestCal, "D1", "Made on dest", flowStart)

	r := h.se.fullSync(context.Background(), h.source, h.src, h.dst, h.cal, 1, false)
	if len(r.Errors) > 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	assertPaths(t, "cycle1", "source PUTs", h.src.putLog(), srcPath("D1"))
	if hasWarningContaining(r, "reverse-create skipped") {
		t.Errorf("unexpected skip warning: %v", r.Warnings)
	}
}

func hasWarningContaining(r *SyncResult, sub string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
