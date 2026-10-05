package caldav

import (
	"fmt"
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// dest_wins flow tests (PR-11). They drive fullSync against the
// in-memory fakes from sync_flow_test.go with conflict_strategy set
// to dest_wins.

// destWinsSetup creates event E on the source, syncs it to the
// destination (cycle 1), then runs a steady cycle so both ETags are
// recorded in synced_events (cycle 2).
func destWinsSetup(t *testing.T) *flowHarness {
	t.Helper()
	h := newFlowHarness(t, db.SyncDirectionTwoWay, db.ConflictDestWins, 0)
	h.src.seed(flowSrcCal, "E", "Original", flowStart)

	r := h.cycle()
	assertNoWarnings(t, "setup cycle1", r)
	assertCounts(t, "setup cycle1", r, counts{Created: 1, EventsProcessed: 1})

	r = h.cycle()
	assertNoWarnings(t, "setup cycle2", r)
	assertCounts(t, "setup cycle2", r, counts{EventsProcessed: 1})
	assertPaths(t, "setup cycle2", "dest PUTs", h.dst.putLog())
	assertPaths(t, "setup cycle2", "source PUTs", h.src.putLog())
	row := h.rows()["E"]
	if row == nil || row.SourceETag == "" || row.DestETag == "" {
		t.Fatalf("setup cycle2: row E = %+v, want both ETags set", row)
	}
	return h
}

func assertSummary(t *testing.T, step, side string, f *fakeCalendarClient, path, want string) {
	t.Helper()
	e, ok := f.get(path)
	if !ok {
		t.Fatalf("%s: %s has no event at %s", step, side, path)
	}
	if e.Summary != want {
		t.Errorf("%s: %s summary = %q, want %q", step, side, e.Summary, want)
	}
}

func assertNoSourceWinnerConflict(t *testing.T, step string, r *SyncResult) {
	t.Helper()
	for _, w := range r.Warnings {
		if strings.Contains(w, `"winner":"source"`) {
			t.Errorf("%s: dest_wins logged a source-winner conflict: %s", step, w)
		}
	}
}

// A destination-only edit under dest_wins goes back to the source and
// the destination is left alone.
func TestSyncFlow_DestWins_DestEditPropagatesToSource(t *testing.T) {
	h := destWinsSetup(t)

	h.dst.edit(destPath("E"), "Dest edit")
	r := h.cycle()
	assertNoWarnings(t, "cycle3", r)
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle3", "source PUTs", h.src.putLog(), srcPath("E"))
	assertSummary(t, "cycle3", "dest", h.dst, destPath("E"), "Dest edit")
	assertSummary(t, "cycle3", "source", h.src, srcPath("E"), "Dest edit")
}

// Both sides edited between cycles under dest_wins: the destination
// edit must win. Before PR-11 the forward loop PUT the source copy over
// the destination edit, and the dest_wins pass then PUT the old
// destination copy onto the source, so the two sides swapped content
// and a "winner":"source" conflict was logged.
func TestSyncFlow_DestWins_ConflictKeepsDestEdit(t *testing.T) {
	h := destWinsSetup(t)

	h.src.edit(srcPath("E"), "Source edit")
	h.dst.edit(destPath("E"), "Dest edit")
	r := h.cycle()
	assertNoSourceWinnerConflict(t, "cycle3", r)
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle3", "source PUTs", h.src.putLog(), srcPath("E"))
	assertSummary(t, "cycle3", "dest", h.dst, destPath("E"), "Dest edit")
	assertSummary(t, "cycle3", "source", h.src, srcPath("E"), "Dest edit")
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], `"winner":"dest"`) {
		t.Errorf("cycle3: warnings = %v, want exactly one dest-winner conflict", r.Warnings)
	}

	// The next cycles settle: no PUTs either way, and both sides
	// still hold the destination edit.
	for _, step := range []string{"cycle4", "cycle5"} {
		r = h.cycle()
		assertNoWarnings(t, step, r)
		assertPaths(t, step, "dest PUTs", h.dst.putLog())
		assertPaths(t, step, "source PUTs", h.src.putLog())
		assertSummary(t, step, "dest", h.dst, destPath("E"), "Dest edit")
		assertSummary(t, step, "source", h.src, srcPath("E"), "Dest edit")
	}
}

// Four cycles under dest_wins: a destination edit, then a source edit,
// then two idle cycles. PUT traffic must reach zero by the third cycle
// and stay there (no dest<->source ping-pong, #79 class).
//
// This is a settling check, not a check of the PR-11 forward guard: it
// never edits both sides in one cycle, so it also passes without the
// guard. TestSyncFlow_DestWins_ConflictKeepsDestEdit covers the guard.
func TestSyncFlow_DestWins_FourCycleSettles(t *testing.T) {
	h := destWinsSetup(t)

	// Cycle 1: destination edit goes to the source only.
	h.dst.edit(destPath("E"), "Dest edit")
	r := h.cycle()
	assertNoWarnings(t, "c1", r)
	assertPaths(t, "c1", "dest PUTs", h.dst.putLog())
	assertPaths(t, "c1", "source PUTs", h.src.putLog(), srcPath("E"))

	// Cycle 2: source edit. The dest_wins write in c1 stored an empty
	// source ETag, so this cycle takes the unchanged branch and only
	// re-records ETags: zero PUTs either way.
	//
	// Pinned, not endorsed: this means a source edit made in the cycle
	// right after a dest_wins write never reaches the destination (the
	// edited source ETag becomes the new baseline). That gap predates
	// PR-11 and is tracked separately.
	h.src.edit(srcPath("E"), "Source edit")
	r = h.cycle()
	assertNoWarnings(t, "c2", r)
	assertPaths(t, "c2", "dest PUTs", h.dst.putLog())
	assertPaths(t, "c2", "source PUTs", h.src.putLog())

	// Cycles 3 and 4: idle. Zero PUTs both ways.
	for _, step := range []string{"c3", "c4"} {
		r = h.cycle()
		assertNoWarnings(t, step, r)
		assertPaths(t, step, "dest PUTs", h.dst.putLog())
		assertPaths(t, step, "source PUTs", h.src.putLog())
	}
}

// A both-sides conflict under dest_wins where the source PUT fails for
// good (read-only source such as an iCloud subscribed calendar, a UID
// collision, or a refused event). The forward guard must not freeze
// the UID: the cycle records both current ETags and warns, and a later
// source edit still reaches the destination.
func TestSyncFlow_DestWins_ConflictPermanentSourceFailureRecovers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"forbidden", fakeErr("403 Forbidden")},
		{"already exists", fakeErr("409 Conflict")},
		{"skipped", fmt.Errorf("%w: refused", ErrEventSkipped)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := destWinsSetup(t)
			h.src.failOn(srcPath("E"), tc.err)

			h.src.edit(srcPath("E"), "Source edit")
			h.dst.edit(destPath("E"), "Dest edit")
			r := h.cycle()
			assertNoSourceWinnerConflict(t, "cycle3", r)
			assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog())
			assertPaths(t, "cycle3", "source PUTs", h.src.putLog())
			if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "could not write the destination copy back to the source") {
				t.Errorf("cycle3: warnings = %v, want one dest_wins write-back failure", r.Warnings)
			}
			src, _ := h.src.get(srcPath("E"))
			dst, _ := h.dst.get(destPath("E"))
			row := h.rows()["E"]
			if row == nil || row.SourceETag != src.ETag || row.DestETag != dst.ETag {
				t.Fatalf("cycle3: row E = %+v, want current ETags src=%s dst=%s", row, src.ETag, dst.ETag)
			}

			// Steady: the failure is not retried every cycle.
			r = h.cycle()
			assertNoWarnings(t, "cycle4", r)
			assertPaths(t, "cycle4", "dest PUTs", h.dst.putLog())
			assertPaths(t, "cycle4", "source PUTs", h.src.putLog())

			// A later source edit still propagates.
			h.src.failOn(srcPath("E"), nil)
			h.src.edit(srcPath("E"), "Source edit 2")
			r = h.cycle()
			assertNoWarnings(t, "cycle5", r)
			assertPaths(t, "cycle5", "dest PUTs", h.dst.putLog(), destPath("E"))
			assertPaths(t, "cycle5", "source PUTs", h.src.putLog())
			assertSummary(t, "cycle5", "dest", h.dst, destPath("E"), "Source edit 2")
		})
	}
}

// A one-off source PUT failure on a dest_wins conflict keeps the
// previous tracking row, so the next cycle retries and the destination
// edit still wins.
func TestSyncFlow_DestWins_ConflictTransientSourceFailureRetries(t *testing.T) {
	h := destWinsSetup(t)
	prev := *h.rows()["E"]
	h.src.failOn(srcPath("E"), fakeErr("500 Internal Server Error"))

	h.src.edit(srcPath("E"), "Source edit")
	h.dst.edit(destPath("E"), "Dest edit")
	r := h.cycle()
	assertNoSourceWinnerConflict(t, "cycle3", r)
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle3", "source PUTs", h.src.putLog())
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "Failed to update event on source") {
		t.Errorf("cycle3: warnings = %v, want one source update failure", r.Warnings)
	}
	if row := h.rows()["E"]; row == nil || row.SourceETag != prev.SourceETag || row.DestETag != prev.DestETag {
		t.Fatalf("cycle3: row E = %+v, want previous ETags %+v", row, prev)
	}

	h.src.failOn(srcPath("E"), nil)
	r = h.cycle()
	assertNoSourceWinnerConflict(t, "cycle4", r)
	assertPaths(t, "cycle4", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle4", "source PUTs", h.src.putLog(), srcPath("E"))
	assertSummary(t, "cycle4", "dest", h.dst, destPath("E"), "Dest edit")
	assertSummary(t, "cycle4", "source", h.src, srcPath("E"), "Dest edit")
}
