package caldav

import (
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Issue #80 regression suite for planTwoWayDeletion.
//
// Background: William lost 748 events from his SOGo destination
// calendar in a single sync cycle. Root cause: the inline two-way
// deletion loop only consulted shouldSkipTwoWayDeletion, which checks
// destEventCount==0 but NOT sourceEventCount. When iCloud returned a
// partial response for one of his calendars (a few events instead of
// the expected ~748), every previously-synced UID not in the partial
// response was treated as "deleted from source" and propagated to
// the destination.
//
// planTwoWayDeletion now enforces three independent guards:
//
//   1. Empty-dest guard (was: shouldSkipTwoWayDeletion)
//   2. Empty-source guard (NEW — the direct fix for William's case)
//   3. Mass-deletion ratio guard (NEW — protects against partial
//      source responses that don't trigger the empty-side guards)
//
// These tests lock in each guard plus the happy paths that must NOT
// trigger them.

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func newPriorMap(uids ...string) map[string]*db.SyncedEvent {
	m := make(map[string]*db.SyncedEvent, len(uids))
	for _, uid := range uids {
		m[uid] = &db.SyncedEvent{EventUID: uid}
	}
	return m
}

func newEventMap(uids ...string) map[string]Event {
	m := make(map[string]Event, len(uids))
	for _, uid := range uids {
		m[uid] = Event{UID: uid, Path: "/cal/" + uid + ".ics"}
	}
	return m
}

// -----------------------------------------------------------------------------
// Rule 1: empty-dest guard (existing behavior, preserved)
// -----------------------------------------------------------------------------

// TestPlanTwoWayDeletion_EmptyDestWithPriorRefuses verifies the
// empty-destination guard. If destination returned 0 events but we
// have prior records, refuse to delete anything — this is the
// original shouldSkipTwoWayDeletion check, now subsumed into
// planTwoWayDeletion. (#80)
func TestPlanTwoWayDeletion_EmptyDestWithPriorRefuses(t *testing.T) {
	source := newEventMap("a", "b", "c")
	dest := newEventMap()
	prior := newPriorMap("a", "b", "c")

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected empty-dest guard to fire")
	}
	if toDelete != nil {
		t.Errorf("expected nil delete list when guard fires, got %d", len(toDelete))
	}
}

// TestPlanTwoWayDeletion_EmptyDestNoPriorAllowsEmpty verifies the
// legitimate first-sync scenario for the dest side. Empty dest with
// zero prior records is not a safety violation — there's nothing to
// protect. The function should return cleanly with an empty list. (#80)
func TestPlanTwoWayDeletion_EmptyDestNoPriorAllowsEmpty(t *testing.T) {
	source := newEventMap("a", "b")
	dest := newEventMap()
	prior := newPriorMap()

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("first-sync (no prior records) should not trigger guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected empty delete list, got %d", len(toDelete))
	}
}

// -----------------------------------------------------------------------------
// Rule 2: empty-source guard (NEW — the William fix)
// -----------------------------------------------------------------------------

// TestPlanTwoWayDeletion_EmptySourceWithPriorRefuses is the direct
// regression test for the bug that lost William's 748 events. When
// the source query returns 0 events but we have prior sync records,
// the deletion pass MUST refuse — even if the destination has events.
// Without this guard, every prior UID would be classified as
// "deleted from source" and propagated to the destination. (#80)
func TestPlanTwoWayDeletion_EmptySourceWithPriorRefuses(t *testing.T) {
	source := newEventMap() // empty — simulates iCloud query failure
	dest := newEventMap("a", "b", "c", "d", "e")
	prior := newPriorMap("a", "b", "c", "d", "e")

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected empty-source guard to fire — this is the William #80 regression")
	}
	if toDelete != nil {
		t.Errorf("expected nil delete list when guard fires, got %d", len(toDelete))
	}
}

// TestPlanTwoWayDeletion_EmptySourceNoPriorAllowsEmpty verifies that
// the empty-source guard does not fire when there are no prior
// records — that is the legitimate "fresh source, no history" case
// (e.g., a brand-new source that hasn't had its first successful
// sync yet). Mirror of EmptyDestNoPriorAllowsEmpty. (#80)
func TestPlanTwoWayDeletion_EmptySourceNoPriorAllowsEmpty(t *testing.T) {
	source := newEventMap()
	dest := newEventMap("a", "b")
	prior := newPriorMap()

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("empty-source with no prior should not trigger guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected empty delete list, got %d", len(toDelete))
	}
}

// -----------------------------------------------------------------------------
// Rule 3: mass-deletion ratio guard (NEW)
// -----------------------------------------------------------------------------

// TestPlanTwoWayDeletion_PartialSourceTriggersRatioGuard is the
// other half of the William regression. When the source returns
// SOME events but is missing the bulk of them, neither the empty-
// dest nor the empty-source guard fires — but the ratio guard
// catches it. With 100 prior records and only 10 still on source,
// 90 would be deleted (90% of prior > 50% threshold) → refuse. (#80)
func TestPlanTwoWayDeletion_PartialSourceTriggersRatioGuard(t *testing.T) {
	// 100 prior records, source returns only 10 of them, dest has
	// all 100. Without the ratio guard the function would propose
	// 90 deletes (90% of prior).
	priorUIDs := make([]string, 100)
	for i := range priorUIDs {
		priorUIDs[i] = string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
	}
	prior := newPriorMap(priorUIDs...)
	dest := newEventMap(priorUIDs...)
	source := newEventMap(priorUIDs[:10]...) // only first 10 still on source

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected ratio guard to fire on 90% mass-deletion proposal")
	}
	if toDelete != nil {
		t.Errorf("expected nil delete list when ratio guard fires, got %d", len(toDelete))
	}
}

// TestPlanTwoWayDeletion_NormalDeletionAllowed verifies the happy
// path: a single legitimate deletion (well under the ratio
// threshold) flows through. 10 prior records, 9 still on source,
// 1 missing → 1 delete (10% of prior, well below 50%). (#80)
func TestPlanTwoWayDeletion_NormalDeletionAllowed(t *testing.T) {
	source := newEventMap("a", "b", "c", "d", "e", "f", "g", "h", "i")
	dest := newEventMap("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	prior := newPriorMap("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("legitimate single deletion should not trigger any guard: %q", warning)
	}
	if len(toDelete) != 1 || toDelete[0] != "j" {
		t.Errorf("expected exactly one deletion of UID j, got %v", toDelete)
	}
}

// TestPlanTwoWayDeletion_RatioDisabledWhenZero verifies that passing
// maxDeleteRatio=0 disables the ratio check entirely (matching
// planOrphanDeletion's disable semantics). When the threshold is
// disabled, even a 100% deletion proposal flows through — useful
// for tests and for callers that want only the empty-side guards.
// (#80)
func TestPlanTwoWayDeletion_RatioDisabledWhenZero(t *testing.T) {
	source := newEventMap("only-this-one")
	dest := newEventMap("a", "b", "c", "d", "e", "only-this-one")
	prior := newPriorMap("a", "b", "c", "d", "e", "only-this-one")

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0)

	if warning != "" {
		t.Errorf("ratio=0 disables the ratio guard, got warning: %q", warning)
	}
	if len(toDelete) != 5 {
		t.Errorf("expected 5 deletions when ratio is disabled, got %d", len(toDelete))
	}
}

// TestPlanTwoWayDeletion_RatioExactlyAtThresholdAllowed verifies the
// boundary: a deletion ratio exactly equal to the threshold is
// allowed (the check is strict greater-than). 4 of 8 prior records
// missing from source = 50% exactly = allowed. (#80)
func TestPlanTwoWayDeletion_RatioExactlyAtThresholdAllowed(t *testing.T) {
	source := newEventMap("a", "b", "c", "d")
	dest := newEventMap("a", "b", "c", "d", "e", "f", "g", "h")
	prior := newPriorMap("a", "b", "c", "d", "e", "f", "g", "h")

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("ratio exactly at threshold should be allowed, got warning: %q", warning)
	}
	if len(toDelete) != 4 {
		t.Errorf("expected 4 deletions, got %d", len(toDelete))
	}
}

// TestPlanTwoWayDeletion_OnlyDeletesIntersectionOfPriorAndDest
// verifies the ownership semantics: an event must be in
// previouslySyncedMap (we synced it) AND on dest (still there) AND
// not on source (gone) to qualify for deletion. Events that exist
// only on dest without ever being synced are NOT deleted — those
// belong to the user, not to us. (#80)
func TestPlanTwoWayDeletion_OnlyDeletesIntersectionOfPriorAndDest(t *testing.T) {
	source := newEventMap("a")
	dest := newEventMap("a", "b", "user-only-1", "user-only-2")
	prior := newPriorMap("a", "b") // user-only-1 and user-only-2 were never synced by us

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("normal deletion should not trigger guard: %q", warning)
	}
	if len(toDelete) != 1 || toDelete[0] != "b" {
		t.Errorf("expected exactly one deletion of UID b (user-only events should be untouched), got %v", toDelete)
	}
}

// TestPlanTwoWayDeletion_DoesNotDeleteEventsStillOnSource verifies
// the basic correctness check: events that exist on BOTH source and
// dest are not candidates for deletion regardless of any other
// state. (#80)
func TestPlanTwoWayDeletion_DoesNotDeleteEventsStillOnSource(t *testing.T) {
	source := newEventMap("a", "b", "c")
	dest := newEventMap("a", "b", "c")
	prior := newPriorMap("a", "b", "c")

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("no deletions case should not trigger guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected zero deletions when source has all prior events, got %d", len(toDelete))
	}
}

// -----------------------------------------------------------------------------
// Combined / canary tests
// -----------------------------------------------------------------------------

// TestPlanTwoWayDeletion_GuardOrderEmptyDestBeforeEmptySource
// documents that the empty-dest guard fires BEFORE the empty-source
// guard when both conditions hold. The user-visible warning message
// matters because it points the operator at the right side to debug.
// (#80)
func TestPlanTwoWayDeletion_GuardOrderEmptyDestBeforeEmptySource(t *testing.T) {
	source := newEventMap()
	dest := newEventMap()
	prior := newPriorMap("a", "b", "c")

	_, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected a guard to fire when both sides empty")
	}
	// The dest-side guard message should win because Rule 1 is
	// checked first. This is documented behavior, not a coincidence.
	if !contains(warning, "destination returned 0 events") {
		t.Errorf("expected dest-empty warning first, got: %q", warning)
	}
}

// TestPlanTwoWayDeletion_WilliamScenarioReproduction reconstructs the
// actual production failure: 748 prior records, dest has 130 of them
// (with the rest filtered out by the date window upstream), source
// returned 0 for one calendar (the partial-failure case). The empty-
// source guard MUST fire and zero events MUST be deleted. (#80)
//
// Without the fix, this would propose 130 deletions (every UID in
// dest that is also in prior but not in source) — and that's just
// one calendar. The original bug saw all 748 evaporate over the
// full sync cycle.
func TestPlanTwoWayDeletion_WilliamScenarioReproduction(t *testing.T) {
	priorUIDs := make([]string, 748)
	for i := range priorUIDs {
		priorUIDs[i] = "evt-" + string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
	}
	prior := newPriorMap(priorUIDs...)
	// Dest has the first 130 prior UIDs (post-date-filter slice).
	dest := newEventMap(priorUIDs[:130]...)
	// Source returned 0 — the iCloud partial-failure that started it.
	source := newEventMap()

	toDelete, warning := planTwoWayDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("William scenario MUST trigger the empty-source guard — this is the regression")
	}
	if toDelete != nil {
		t.Errorf("William scenario MUST refuse to delete anything, got %d candidates", len(toDelete))
	}
}

// contains is a tiny helper to avoid pulling in strings.Contains
// just for the canary tests above.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
