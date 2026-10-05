package caldav

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Sync-flow characterization tests (#177).
//
// These drive fullSync end to end against two in-memory
// fakeCalendarClients and a real temp SQLite database, across several
// cycles. They pin TODAY's observable behavior (result counters, the
// PUT/DELETE calls each side receives, and the synced_events rows)
// so later behavior fixes to sync.go can be written test-first and
// show exactly which pinned value they change. A pinned value here is
// not an endorsement: where today's behavior is questionable the
// assertion says so in a comment.

const (
	flowSrcCal  = "/src/cal/"
	flowDestCal = "/dest/cal/"
	flowStart   = "20300101T100000Z"
)

type flowHarness struct {
	t      *testing.T
	db     *db.DB
	se     *SyncEngine
	src    *fakeCalendarClient
	dst    *fakeCalendarClient
	source *db.Source
	cal    Calendar
}

func newFlowHarness(t *testing.T, direction db.SyncDirection, strategy db.ConflictStrategy, syncInterval int) *flowHarness {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	user, err := database.GetOrCreateUser("flow@example.com", "Flow Test")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	source := &db.Source{
		UserID:           user.ID,
		Name:             "flow",
		SourceType:       db.SourceTypeCalDAV,
		SourceURL:        "https://source.example.com" + flowSrcCal,
		DestURL:          "https://dest.example.com" + flowDestCal,
		SyncInterval:     syncInterval,
		SyncDaysPast:     0, // no date filtering
		SyncDirection:    direction,
		ConflictStrategy: strategy,
		Enabled:          true,
	}
	if err := database.CreateSource(source); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	return &flowHarness{
		t:      t,
		db:     database,
		se:     NewSyncEngine(database, nil),
		src:    newFakeCalendarClient(flowSrcCal),
		dst:    newFakeCalendarClient(flowDestCal),
		source: source,
		cal:    Calendar{Path: flowSrcCal, Name: "Work"},
	}
}

// cycle clears the fakes' call logs and runs one fullSync pass.
func (h *flowHarness) cycle() *SyncResult {
	h.t.Helper()
	h.src.resetLog()
	h.dst.resetLog()
	res := h.se.fullSync(context.Background(), h.source, h.src, h.dst, h.cal, 1)
	if len(res.Errors) > 0 {
		h.t.Fatalf("fullSync returned errors: %v", res.Errors)
	}
	return res
}

func (h *flowHarness) rows() map[string]*db.SyncedEvent {
	h.t.Helper()
	rows, err := h.db.GetSyncedEvents(h.source.ID, h.cal.Path)
	if err != nil {
		h.t.Fatalf("GetSyncedEvents: %v", err)
	}
	out := make(map[string]*db.SyncedEvent, len(rows))
	for _, r := range rows {
		out[r.EventUID] = r
	}
	return out
}

type counts struct {
	Created, Updated, Deleted, Skipped, DuplicatesRemoved, EventsProcessed int
}

func countsOf(r *SyncResult) counts {
	return counts{r.Created, r.Updated, r.Deleted, r.Skipped, r.DuplicatesRemoved, r.EventsProcessed}
}

func assertCounts(t *testing.T, step string, r *SyncResult, want counts) {
	t.Helper()
	if got := countsOf(r); got != want {
		t.Errorf("%s: counts = %+v, want %+v (warnings: %v)", step, got, want, r.Warnings)
	}
}

func assertPaths(t *testing.T, step, what string, got []string, want ...string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: %s = %v, want %v", step, what, got, want)
	}
}

func assertRowUIDs(t *testing.T, step string, rows map[string]*db.SyncedEvent, want ...string) {
	t.Helper()
	got := make(map[string]bool, len(rows))
	for uid := range rows {
		got[uid] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, uid := range want {
		wantSet[uid] = true
	}
	if !reflect.DeepEqual(got, wantSet) {
		t.Errorf("%s: synced_events UIDs = %v, want %v", step, got, wantSet)
	}
}

func assertNoWarnings(t *testing.T, step string, r *SyncResult) {
	t.Helper()
	if len(r.Warnings) > 0 {
		t.Errorf("%s: unexpected warnings: %v", step, r.Warnings)
	}
}

func destPath(uid string) string { return flowDestCal + uid + ".ics" }
func srcPath(uid string) string  { return flowSrcCal + uid + ".ics" }

func TestSyncFlow_OneWay_CreateUpdateOrphanDelete(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	for _, uid := range []string{"A", "B", "C"} {
		h.src.seed(flowSrcCal, uid, "Event "+uid, flowStart)
	}
	// Dest-only event this source never wrote (e.g. another source's
	// event, or one the user created directly on the destination).
	h.dst.seed(flowDestCal, "Z", "Someone else's", "20300202T100000Z")

	// Cycle 1: initial create.
	r := h.cycle()
	assertNoWarnings(t, "cycle1", r)
	assertCounts(t, "cycle1", r, counts{Created: 3, EventsProcessed: 3})
	assertPaths(t, "cycle1", "dest PUTs", h.dst.putLog(), destPath("A"), destPath("B"), destPath("C"))
	assertPaths(t, "cycle1", "dest DELETEs", h.dst.deleteLog())
	assertPaths(t, "cycle1", "source PUTs", h.src.putLog())
	rows := h.rows()
	assertRowUIDs(t, "cycle1", rows, "A", "B", "C")
	for _, uid := range []string{"A", "B", "C"} {
		srcEvt, _ := h.src.get(srcPath(uid))
		if rows[uid].SourceETag != srcEvt.ETag {
			t.Errorf("cycle1: row %s SourceETag = %q, want source ETag %q", uid, rows[uid].SourceETag, srcEvt.ETag)
		}
		// PutEvent returns no ETag, so the dest side is not known
		// until the next read.
		if rows[uid].DestETag != "" {
			t.Errorf("cycle1: row %s DestETag = %q, want empty after create", uid, rows[uid].DestETag)
		}
	}

	// Cycle 2: steady state, nothing written. Dest ETags get recorded.
	r = h.cycle()
	assertNoWarnings(t, "cycle2", r)
	assertCounts(t, "cycle2", r, counts{EventsProcessed: 3})
	assertPaths(t, "cycle2", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle2", "dest DELETEs", h.dst.deleteLog())
	rows = h.rows()
	for _, uid := range []string{"A", "B", "C"} {
		dstEvt, _ := h.dst.get(destPath(uid))
		if rows[uid].DestETag != dstEvt.ETag {
			t.Errorf("cycle2: row %s DestETag = %q, want dest ETag %q", uid, rows[uid].DestETag, dstEvt.ETag)
		}
	}

	// Cycle 3: source edit propagates as an update to the same dest path.
	h.src.edit(srcPath("A"), "Event A (edited)")
	r = h.cycle()
	assertNoWarnings(t, "cycle3", r)
	assertCounts(t, "cycle3", r, counts{Updated: 1, EventsProcessed: 3})
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog(), destPath("A"))
	if got, _ := h.dst.get(destPath("A")); got.Summary != "Event A (edited)" {
		t.Errorf("cycle3: dest A summary = %q, want edited summary", got.Summary)
	}
	srcA, _ := h.src.get(srcPath("A"))
	if got := h.rows()["A"].SourceETag; got != srcA.ETag {
		t.Errorf("cycle3: row A SourceETag = %q, want new source ETag %q", got, srcA.ETag)
	}

	// Cycle 4: source delete removes the tracked orphan from dest.
	// The untracked dest-only event Z is never touched.
	h.src.remove(srcPath("C"))
	r = h.cycle()
	assertNoWarnings(t, "cycle4", r)
	assertCounts(t, "cycle4", r, counts{Deleted: 1, EventsProcessed: 2})
	assertPaths(t, "cycle4", "dest DELETEs", h.dst.deleteLog(), destPath("C"))
	assertPaths(t, "cycle4", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle4", "dest contents", h.dst.paths(), destPath("A"), destPath("B"), destPath("Z"))
	// The orphan delete also drops C's tracking row (#181).
	assertRowUIDs(t, "cycle4", h.rows(), "A", "B")

	// Cycle 5: steady again, no further writes.
	r = h.cycle()
	assertNoWarnings(t, "cycle5", r)
	assertCounts(t, "cycle5", r, counts{EventsProcessed: 2})
	assertPaths(t, "cycle5", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle5", "dest DELETEs", h.dst.deleteLog())

	// Cycle 6: UID C shows up on the destination again, written by
	// someone else. This source no longer owns it, so it must not be
	// deleted (with a leaked row it was deleted again, #181).
	h.dst.seed(flowDestCal, "C", "Restored C", flowStart)
	r = h.cycle()
	assertNoWarnings(t, "cycle6", r)
	assertCounts(t, "cycle6", r, counts{EventsProcessed: 2})
	assertPaths(t, "cycle6", "dest DELETEs", h.dst.deleteLog())
	assertPaths(t, "cycle6", "dest contents", h.dst.paths(), destPath("A"), destPath("B"), destPath("C"), destPath("Z"))
	assertRowUIDs(t, "cycle6", h.rows(), "A", "B")
}

// twoWaySetup runs the first two cycles shared by the two-way tests:
// source A-D, dest-only X, then a steady cycle.
func twoWaySetup(t *testing.T, syncInterval int) *flowHarness {
	t.Helper()
	h := newFlowHarness(t, db.SyncDirectionTwoWay, db.ConflictSourceWins, syncInterval)
	for _, uid := range []string{"A", "B", "C", "D"} {
		h.src.seed(flowSrcCal, uid, "Event "+uid, flowStart)
	}
	h.dst.seed(flowDestCal, "X", "Dest only X", "20300303T100000Z")

	// Cycle 1: forward create A-D on dest, reverse-create X on source.
	r := h.cycle()
	assertNoWarnings(t, "setup cycle1", r)
	assertCounts(t, "setup cycle1", r, counts{Created: 5, EventsProcessed: 4})
	assertPaths(t, "setup cycle1", "dest PUTs", h.dst.putLog(),
		destPath("A"), destPath("B"), destPath("C"), destPath("D"))
	assertPaths(t, "setup cycle1", "source PUTs", h.src.putLog(), srcPath("X"))
	rows := h.rows()
	assertRowUIDs(t, "setup cycle1", rows, "A", "B", "C", "D", "X")
	// Forward creates record only the source ETag; the reverse create
	// records only the dest ETag it read before uploading.
	if rows["A"].SourceETag == "" || rows["A"].DestETag != "" {
		t.Errorf("setup cycle1: row A etags = (%q,%q), want (set,empty)", rows["A"].SourceETag, rows["A"].DestETag)
	}
	dstX, _ := h.dst.get(destPath("X"))
	if rows["X"].SourceETag != "" || rows["X"].DestETag != dstX.ETag {
		t.Errorf("setup cycle1: row X etags = (%q,%q), want (empty,%q)", rows["X"].SourceETag, rows["X"].DestETag, dstX.ETag)
	}

	// Cycle 2: steady. Both ETags now recorded for every row.
	r = h.cycle()
	assertNoWarnings(t, "setup cycle2", r)
	assertCounts(t, "setup cycle2", r, counts{EventsProcessed: 5})
	assertPaths(t, "setup cycle2", "dest PUTs", h.dst.putLog())
	assertPaths(t, "setup cycle2", "source PUTs", h.src.putLog())
	assertPaths(t, "setup cycle2", "dest DELETEs", h.dst.deleteLog())
	assertPaths(t, "setup cycle2", "source DELETEs", h.src.deleteLog())
	for uid, row := range h.rows() {
		if row.SourceETag == "" || row.DestETag == "" {
			t.Errorf("setup cycle2: row %s etags = (%q,%q), want both set", uid, row.SourceETag, row.DestETag)
		}
	}
	return h
}

func TestSyncFlow_TwoWay_CreateReverseCreateAndDeletes(t *testing.T) {
	// SyncInterval 0 puts every row outside the CreatedAt safety
	// window, so the source-side deletion is allowed to run.
	h := twoWaySetup(t, 0)

	// Cycle 3: deleted on source -> deleted from dest, row removed.
	h.src.remove(srcPath("A"))
	r := h.cycle()
	assertNoWarnings(t, "cycle3", r)
	assertCounts(t, "cycle3", r, counts{Deleted: 1, EventsProcessed: 4})
	assertPaths(t, "cycle3", "dest DELETEs", h.dst.deleteLog(), destPath("A"))
	assertPaths(t, "cycle3", "source DELETEs", h.src.deleteLog())
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle3", "source PUTs", h.src.putLog())
	assertRowUIDs(t, "cycle3", h.rows(), "B", "C", "D", "X")

	// Cycle 4: deleted on dest -> deleted from source.
	//
	// TODAY (audit 2026-10-03, remediation PR-08): the forward loop
	// iterates the sourceEvents slice, not sourceEventMap, so B is
	// deleted from source and then re-created on dest in the same
	// cycle. Its tracking row survives through the forward-pass upsert.
	h.dst.remove(destPath("B"))
	r = h.cycle()
	assertNoWarnings(t, "cycle4", r)
	assertCounts(t, "cycle4", r, counts{Created: 1, Deleted: 1, EventsProcessed: 4})
	assertPaths(t, "cycle4", "source DELETEs", h.src.deleteLog(), srcPath("B"))
	assertPaths(t, "cycle4", "dest DELETEs", h.dst.deleteLog())
	assertPaths(t, "cycle4", "dest PUTs", h.dst.putLog(), destPath("B"))
	assertPaths(t, "cycle4", "source PUTs", h.src.putLog())
	assertRowUIDs(t, "cycle4", h.rows(), "B", "C", "D", "X")
	assertPaths(t, "cycle4", "source contents", h.src.paths(), srcPath("C"), srcPath("D"), srcPath("X"))
	assertPaths(t, "cycle4", "dest contents", h.dst.paths(), destPath("B"), destPath("C"), destPath("D"), destPath("X"))

	// Cycle 5: TODAY the re-created dest copy of B is now "deleted on
	// source", so the dest-deletion pass removes it and the row goes.
	r = h.cycle()
	assertNoWarnings(t, "cycle5", r)
	assertCounts(t, "cycle5", r, counts{Deleted: 1, EventsProcessed: 3})
	assertPaths(t, "cycle5", "dest DELETEs", h.dst.deleteLog(), destPath("B"))
	assertPaths(t, "cycle5", "source DELETEs", h.src.deleteLog())
	assertPaths(t, "cycle5", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle5", "source PUTs", h.src.putLog())
	assertRowUIDs(t, "cycle5", h.rows(), "C", "D", "X")
	assertPaths(t, "cycle5", "dest contents", h.dst.paths(), destPath("C"), destPath("D"), destPath("X"))

	// Cycle 6: steady.
	r = h.cycle()
	assertNoWarnings(t, "cycle6", r)
	assertCounts(t, "cycle6", r, counts{EventsProcessed: 3})
	assertPaths(t, "cycle6", "dest PUTs", h.dst.putLog())
	assertPaths(t, "cycle6", "source PUTs", h.src.putLog())
	assertPaths(t, "cycle6", "dest DELETEs", h.dst.deleteLog())
	assertPaths(t, "cycle6", "source DELETEs", h.src.deleteLog())
}

func TestSyncFlow_TwoWay_SourceDeleteBlockedBySafetyThreshold(t *testing.T) {
	// SyncInterval 3600: every row was first synced seconds ago, so it
	// is inside the CreatedAt safety window (#72).
	h := twoWaySetup(t, 3600)

	h.dst.remove(destPath("B"))
	r := h.cycle()
	assertNoWarnings(t, "cycle3", r)
	// TODAY (#182): the source delete is skipped by the safety
	// threshold and the forward pass then re-creates B on dest, because
	// B is still on source and no longer on dest. The user's dest-side
	// delete is undone. The fix for #182 flips this to no dest PUT.
	assertCounts(t, "cycle3", r, counts{Created: 1, EventsProcessed: 5})
	assertPaths(t, "cycle3", "source DELETEs", h.src.deleteLog())
	assertPaths(t, "cycle3", "dest PUTs", h.dst.putLog(), destPath("B"))
	assertRowUIDs(t, "cycle3", h.rows(), "A", "B", "C", "D", "X")
	if _, ok := h.src.get(srcPath("B")); !ok {
		t.Errorf("cycle3: source B was deleted despite the safety threshold")
	}
}

func TestSyncFlow_OneWay_OrphanDeleteFailureWarnsAndKeepsEvent(t *testing.T) {
	h := newFlowHarness(t, db.SyncDirectionOneWay, db.ConflictSourceWins, 3600)
	for _, uid := range []string{"A", "B", "C"} {
		h.src.seed(flowSrcCal, uid, "Event "+uid, flowStart)
	}
	h.cycle()
	h.cycle()

	h.src.remove(srcPath("C"))
	h.dst.failOn(destPath("C"), errFake503)
	r := h.cycle()
	assertCounts(t, "cycle3", r, counts{EventsProcessed: 2})
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "Failed to delete orphan event") {
		t.Errorf("cycle3: warnings = %v, want one orphan-delete failure", r.Warnings)
	}
	if _, ok := h.dst.get(destPath("C")); !ok {
		t.Errorf("cycle3: dest C missing after a failed DELETE")
	}
}

var errFake503 = fakeErr("503 Service Unavailable")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// A typed-nil *Client passed as sourceClient must behave like the
// untyped nil the ICS path passes: the source-side passes are skipped
// instead of calling methods on a nil receiver.
func TestSyncEventsToDestination_TypedNilSourceClientIsTreatedAsNil(t *testing.T) {
	h := twoWaySetup(t, 0) // outside the safety window
	h.dst.remove(destPath("B"))
	h.src.resetLog()
	h.dst.resetLog()

	events, err := h.src.GetEvents(context.Background(), flowSrcCal, nil)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	var nilClient *Client
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("syncEventsToDestination panicked with a typed-nil source client: %v", p)
		}
	}()
	r := h.se.syncEventsToDestination(context.Background(), h.source, nilClient, h.dst, events, h.cal, 1, db.SyncDirectionTwoWay)
	if len(r.Errors) > 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	assertPaths(t, "typed-nil", "source DELETEs", h.src.deleteLog())
	assertPaths(t, "typed-nil", "source PUTs", h.src.putLog())
}
