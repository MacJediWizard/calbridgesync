package caldav

import (
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Issue #82 regression suite for planTwoWaySourceDeletion.
//
// Background: PR #80 added empty-source + ratio guards to the dest-
// deletion direction (planTwoWayDeletion) but explicitly deferred the
// source-deletion direction "to a follow-up." That follow-up
// deferred too long: with the dest calendar mid-recovery from the
// previous mass-delete bug, the unguarded source-deletion pass saw
// 360+ prior UIDs as "missing from dest" and deleted them from
// iCloud at ~360 events/cycle. This file is the regression suite
// for the symmetric guard.

// -----------------------------------------------------------------------------
// Empty-source guard
// -----------------------------------------------------------------------------

// TestPlanTwoWaySourceDeletion_EmptySourceWithPriorRefuses verifies
// that an empty source query short-circuits the source-deletion pass
// (same rationale as the dest-side empty-source guard in #80). (#82)
func TestPlanTwoWaySourceDeletion_EmptySourceWithPriorRefuses(t *testing.T) {
	source := newEventMap() // empty
	dest := newEventMap("a", "b", "c")
	prior := newPriorMap("a", "b", "c")

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected empty-source guard to fire")
	}
	if toDelete != nil {
		t.Errorf("expected nil delete list when guard fires, got %d", len(toDelete))
	}
}

// TestPlanTwoWaySourceDeletion_EmptySourceNoPriorAllowsEmpty verifies
// that empty source with NO prior records is the legitimate first-
// sync case and must not trigger the guard. (#82)
func TestPlanTwoWaySourceDeletion_EmptySourceNoPriorAllowsEmpty(t *testing.T) {
	source := newEventMap()
	dest := newEventMap("a", "b")
	prior := newPriorMap()

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("first-sync (no prior records) should not trigger guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected empty delete list, got %d", len(toDelete))
	}
}

// -----------------------------------------------------------------------------
// Empty-dest guard
// -----------------------------------------------------------------------------

// TestPlanTwoWaySourceDeletion_EmptyDestWithPriorRefuses verifies the
// guard against destination query failures. If destination returns
// 0 events and we have prior records, the unguarded code would
// delete every prior UID from source. (#82)
func TestPlanTwoWaySourceDeletion_EmptyDestWithPriorRefuses(t *testing.T) {
	source := newEventMap("a", "b", "c", "d")
	dest := newEventMap()
	prior := newPriorMap("a", "b", "c", "d")

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected empty-dest guard to fire")
	}
	if toDelete != nil {
		t.Errorf("expected nil delete list when guard fires, got %d", len(toDelete))
	}
}

// TestPlanTwoWaySourceDeletion_EmptyDestNoPriorAllowsEmpty verifies
// the legitimate fresh-dest case. (#82)
func TestPlanTwoWaySourceDeletion_EmptyDestNoPriorAllowsEmpty(t *testing.T) {
	source := newEventMap("a", "b")
	dest := newEventMap()
	prior := newPriorMap()

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("empty-dest with no prior should not trigger guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected empty delete list, got %d", len(toDelete))
	}
}

// -----------------------------------------------------------------------------
// Mass-deletion ratio guard
// -----------------------------------------------------------------------------

// TestPlanTwoWaySourceDeletion_RecoveryScenarioTriggersRatioGuard is
// the direct reproduction of the William #82 cascade. The dest
// calendar is mid-recovery: the previous mass-delete event removed
// most of the dest events, this cycle's forward sync has only
// re-created a fraction of them, and the source-delete pass would
// otherwise propagate the still-missing UIDs as deletes from source.
//
// 100 prior records, source has all 100, dest has only 10 (the rest
// are pending recovery) → would propose 90 deletes (90% of prior)
// → ratio guard fires. (#82)
func TestPlanTwoWaySourceDeletion_RecoveryScenarioTriggersRatioGuard(t *testing.T) {
	priorUIDs := make([]string, 100)
	for i := range priorUIDs {
		priorUIDs[i] = string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
	}
	prior := newPriorMap(priorUIDs...)
	source := newEventMap(priorUIDs...)    // source still has everything
	dest := newEventMap(priorUIDs[:10]...) // dest only has the first 10 (mid-recovery)

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning == "" {
		t.Fatal("expected ratio guard to fire on 90% mass-deletion proposal — this is the William #82 regression")
	}
	if toDelete != nil {
		t.Errorf("expected nil delete list when ratio guard fires, got %d", len(toDelete))
	}
}

// TestPlanTwoWaySourceDeletion_NormalDeletionAllowed verifies the
// happy path: a single legitimate user-initiated deletion (well
// under the ratio threshold) flows through. (#82)
func TestPlanTwoWaySourceDeletion_NormalDeletionAllowed(t *testing.T) {
	source := newEventMap("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	dest := newEventMap("a", "b", "c", "d", "e", "f", "g", "h", "i") // user deleted j on dest
	prior := newPriorMap("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("legitimate single deletion should not trigger any guard: %q", warning)
	}
	if len(toDelete) != 1 || toDelete[0] != "j" {
		t.Errorf("expected exactly one deletion of UID j, got %v", toDelete)
	}
}

// TestPlanTwoWaySourceDeletion_RatioDisabledWhenZero verifies the
// disable semantics (matches planOrphanDeletion + planTwoWayDeletion). (#82)
func TestPlanTwoWaySourceDeletion_RatioDisabledWhenZero(t *testing.T) {
	source := newEventMap("a", "b", "c", "d", "e")
	dest := newEventMap()
	// Need to bypass empty-dest guard with zero prior records
	prior := newPriorMap()

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0)

	if warning != "" {
		t.Errorf("ratio=0 with no prior should not trigger any guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected 0 deletions, got %d", len(toDelete))
	}
}

// TestPlanTwoWaySourceDeletion_RatioExactlyAtThresholdAllowed
// verifies the boundary: exactly at the threshold is allowed (strict
// greater-than). 4 of 8 = 50% exactly = allowed. (#82)
func TestPlanTwoWaySourceDeletion_RatioExactlyAtThresholdAllowed(t *testing.T) {
	source := newEventMap("a", "b", "c", "d", "e", "f", "g", "h")
	dest := newEventMap("a", "b", "c", "d") // dest missing 4 of 8
	prior := newPriorMap("a", "b", "c", "d", "e", "f", "g", "h")

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("ratio exactly at threshold should be allowed, got warning: %q", warning)
	}
	if len(toDelete) != 4 {
		t.Errorf("expected 4 deletions, got %d", len(toDelete))
	}
}

// TestPlanTwoWaySourceDeletion_OnlyDeletesFromOurPriorMap verifies
// the ownership filter: events on source that were never in our
// previouslySyncedMap (not synced by us) are not candidates for
// deletion. They belong to the user. (#82)
func TestPlanTwoWaySourceDeletion_OnlyDeletesFromOurPriorMap(t *testing.T) {
	source := newEventMap("a", "b", "user-only-1", "user-only-2")
	dest := newEventMap("a")
	prior := newPriorMap("a", "b") // we never synced user-only-*

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("normal deletion should not trigger guard: %q", warning)
	}
	if len(toDelete) != 1 || toDelete[0] != "b" {
		t.Errorf("expected exactly one deletion of UID b (user-only events untouched), got %v", toDelete)
	}
}

// TestPlanTwoWaySourceDeletion_DoesNotDeleteEventsStillOnDest
// verifies the basic correctness check. (#82)
func TestPlanTwoWaySourceDeletion_DoesNotDeleteEventsStillOnDest(t *testing.T) {
	source := newEventMap("a", "b", "c")
	dest := newEventMap("a", "b", "c")
	prior := newPriorMap("a", "b", "c")

	toDelete, warning := planTwoWaySourceDeletion(source, dest, prior, 0.5)

	if warning != "" {
		t.Errorf("no deletions case should not trigger guard: %q", warning)
	}
	if len(toDelete) != 0 {
		t.Errorf("expected zero deletions, got %d", len(toDelete))
	}
}

// -----------------------------------------------------------------------------
// Symmetry canary
// -----------------------------------------------------------------------------

// TestPlanTwoWaySourceDeletion_SymmetricWithDestDeletion verifies
// the two helpers behave symmetrically on a swap-the-sides matrix.
// Each scenario describes the same story from each side and the
// helper for that side must agree. This is a canary against future
// refactors that drift the two helpers apart. (#82)
func TestPlanTwoWaySourceDeletion_SymmetricWithDestDeletion(t *testing.T) {
	cases := []struct {
		name             string
		side1, side2     map[string]Event
		prior            map[string]*db.SyncedEvent
		expectGuardFires bool
	}{
		{
			name:             "both sides full, no overlap missing",
			side1:            newEventMap("a", "b"),
			side2:            newEventMap("a", "b"),
			prior:            newPriorMap("a", "b"),
			expectGuardFires: false,
		},
		{
			name:             "side1 missing, prior records exist",
			side1:            newEventMap(),
			side2:            newEventMap("a", "b"),
			prior:            newPriorMap("a", "b"),
			expectGuardFires: true,
		},
		{
			name:             "side2 missing, prior records exist",
			side1:            newEventMap("a", "b"),
			side2:            newEventMap(),
			prior:            newPriorMap("a", "b"),
			expectGuardFires: true,
		},
		{
			name:             "neither missing, normal single deletion",
			side1:            newEventMap("a", "b", "c"),
			side2:            newEventMap("a", "b"),
			prior:            newPriorMap("a", "b", "c"),
			expectGuardFires: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// For source-deletion: side1 = source, side2 = dest.
			_, sourceWarn := planTwoWaySourceDeletion(tc.side1, tc.side2, tc.prior, 0.5)
			// For dest-deletion: side1 = source, side2 = dest (same arg
			// order — both helpers take source then dest).
			_, destWarn := planTwoWayDeletion(tc.side1, tc.side2, tc.prior, 0.5)

			gotSourceFires := sourceWarn != ""
			gotDestFires := destWarn != ""

			if gotSourceFires != tc.expectGuardFires {
				t.Errorf("source-deletion guard fired=%v, want %v (warn=%q)", gotSourceFires, tc.expectGuardFires, sourceWarn)
			}
			if gotDestFires != tc.expectGuardFires {
				t.Errorf("dest-deletion guard fired=%v, want %v (warn=%q)", gotDestFires, tc.expectGuardFires, destWarn)
			}
			// The helpers must agree on whether to fire — they're
			// symmetric mirrors. If they ever drift, this test
			// catches it.
			if gotSourceFires != gotDestFires {
				t.Errorf("source-deletion and dest-deletion guards disagree: source=%v, dest=%v", gotSourceFires, gotDestFires)
			}
		})
	}
}
