package caldav

import (
	"testing"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// TestShouldSkipTwoWayDeletion_NormalCase verifies the happy path:
// a two-way sync with destination events and previously synced
// events does NOT skip deletion. This is the normal operating mode.
func TestShouldSkipTwoWayDeletion_NormalCase(t *testing.T) {
	if shouldSkipTwoWayDeletion(db.SyncDirectionTwoWay, 50, 50) {
		t.Error("normal case should not skip two-way deletion")
	}
}

// TestShouldSkipTwoWayDeletion_DestEmptyWithPriorSync verifies the
// critical safety case: two-way sync, destination query returned
// empty, previously synced events exist. MUST skip deletion.
// Regression test for commit b772c56 and PR #22 extension.
func TestShouldSkipTwoWayDeletion_DestEmptyWithPriorSync(t *testing.T) {
	if !shouldSkipTwoWayDeletion(db.SyncDirectionTwoWay, 0, 50) {
		t.Fatal("destination empty + prior sync records MUST skip deletion to prevent mass data loss")
	}
}

// TestShouldSkipTwoWayDeletion_OneWayNotAffected verifies that the
// guard only applies to two-way sync. A one-way sync with an empty
// destination is a legitimate "clean start" scenario, not a safety
// violation.
func TestShouldSkipTwoWayDeletion_OneWayNotAffected(t *testing.T) {
	if shouldSkipTwoWayDeletion(db.SyncDirectionOneWay, 0, 50) {
		t.Error("one-way sync should never be affected by the two-way deletion guard")
	}
}

// TestShouldSkipTwoWayDeletion_EmptyDestNoPrior verifies the
// legitimate first-sync scenario: two-way sync, empty destination,
// zero prior sync records. Nothing to protect, so don't skip — this
// allows the normal sync flow to populate the destination.
func TestShouldSkipTwoWayDeletion_EmptyDestNoPrior(t *testing.T) {
	if shouldSkipTwoWayDeletion(db.SyncDirectionTwoWay, 0, 0) {
		t.Error("legitimate first-sync (empty dest, no prior records) should not be blocked")
	}
}

// TestShouldSkipTwoWayDeletion_DestPopulatedNoPrior verifies that a
// fresh two-way sync against a destination that already has events
// (e.g., user manually added events, or migration from another tool)
// does not skip deletion — there are no prior records to protect.
func TestShouldSkipTwoWayDeletion_DestPopulatedNoPrior(t *testing.T) {
	if shouldSkipTwoWayDeletion(db.SyncDirectionTwoWay, 10, 0) {
		t.Error("populated destination with no prior records should not trigger the guard")
	}
}

// TestIsWithinSyncSafetyThreshold_RecentlySynced verifies the core
// case: an event synced within the last sync interval is "too
// recent to delete from source" — the destination may not have
// finished propagating yet.
func TestIsWithinSyncSafetyThreshold_RecentlySynced(t *testing.T) {
	now := time.Now()
	interval := 5 * time.Minute
	// Synced 2 minutes ago — well inside the 5-minute window
	syncedAt := now.Add(-2 * time.Minute)

	if !isWithinSyncSafetyThreshold(syncedAt, interval, now) {
		t.Error("event synced 2 min ago should be within 5 min safety threshold")
	}
}

// TestIsWithinSyncSafetyThreshold_OldSync verifies that an event
// synced before the safety window is eligible for deletion.
func TestIsWithinSyncSafetyThreshold_OldSync(t *testing.T) {
	now := time.Now()
	interval := 5 * time.Minute
	// Synced 10 minutes ago — outside the 5-minute window
	syncedAt := now.Add(-10 * time.Minute)

	if isWithinSyncSafetyThreshold(syncedAt, interval, now) {
		t.Error("event synced 10 min ago should be outside 5 min safety threshold")
	}
}

// TestIsWithinSyncSafetyThreshold_ExactBoundary verifies the
// boundary condition. The threshold uses strict After(), so an
// event synced exactly at the threshold is considered OUT of the
// window (eligible for deletion).
func TestIsWithinSyncSafetyThreshold_ExactBoundary(t *testing.T) {
	now := time.Now()
	interval := 5 * time.Minute
	// Exactly at the threshold: now - interval
	syncedAt := now.Add(-interval)

	if isWithinSyncSafetyThreshold(syncedAt, interval, now) {
		t.Error("event synced exactly at threshold should be OUT of window (strict After)")
	}
}

// TestIsWithinSyncSafetyThreshold_LongInterval verifies the guard
// scales with the source's configured sync interval. A source with
// a 1-hour interval gives events a longer grace period.
func TestIsWithinSyncSafetyThreshold_LongInterval(t *testing.T) {
	now := time.Now()
	interval := 1 * time.Hour

	// 30 minutes ago — within a 1-hour window
	recentSync := now.Add(-30 * time.Minute)
	if !isWithinSyncSafetyThreshold(recentSync, interval, now) {
		t.Error("30min-old sync should be within 1h threshold")
	}

	// 2 hours ago — outside a 1-hour window
	oldSync := now.Add(-2 * time.Hour)
	if isWithinSyncSafetyThreshold(oldSync, interval, now) {
		t.Error("2h-old sync should be outside 1h threshold")
	}
}

// TestIsWithinSyncSafetyThreshold_FutureSyncedAt verifies the edge
// case where syncedAt is somehow in the future (clock skew, bad
// clock on the source, corrupted timestamp). A future timestamp is
// trivially "within the threshold" and should block deletion, which
// is the safe default.
func TestIsWithinSyncSafetyThreshold_FutureSyncedAt(t *testing.T) {
	now := time.Now()
	interval := 5 * time.Minute
	// 10 seconds in the future — from clock skew
	syncedAt := now.Add(10 * time.Second)

	if !isWithinSyncSafetyThreshold(syncedAt, interval, now) {
		t.Error("future syncedAt should trivially be 'within' threshold (safe default)")
	}
}

// TestShouldSkipTwoWayCreate_NormalCase verifies the happy path:
// a two-way sync with source events present and previously synced
// events does NOT skip the reverse create pass. This is the normal
// operating mode — dest-only events should flow up to source. (#72)
func TestShouldSkipTwoWayCreate_NormalCase(t *testing.T) {
	if shouldSkipTwoWayCreate(db.SyncDirectionTwoWay, 50, 50) {
		t.Error("normal case should not skip two-way create")
	}
}

// TestShouldSkipTwoWayCreate_SourceEmptyWithPriorSync verifies the
// critical safety case: two-way sync, source query returned empty,
// previously synced events exist. MUST skip the create pass to
// prevent mass-upload of the entire destination calendar back to
// source when the source query silently failed. Mirror of
// shouldSkipTwoWayDeletion. (#72)
func TestShouldSkipTwoWayCreate_SourceEmptyWithPriorSync(t *testing.T) {
	if !shouldSkipTwoWayCreate(db.SyncDirectionTwoWay, 0, 50) {
		t.Fatal("source empty + prior sync records MUST skip create pass to prevent mass upload to source")
	}
}

// TestShouldSkipTwoWayCreate_OneWayNotAffected verifies that the
// guard only applies to two-way sync. A one-way sync has no reverse
// create pass anyway, so the guard is a no-op for one-way. (#72)
func TestShouldSkipTwoWayCreate_OneWayNotAffected(t *testing.T) {
	if shouldSkipTwoWayCreate(db.SyncDirectionOneWay, 0, 50) {
		t.Error("one-way sync should never be affected by the two-way create guard")
	}
}

// TestShouldSkipTwoWayCreate_EmptySourceNoPrior verifies the
// legitimate first-sync scenario: two-way sync, source empty, no
// prior sync records. Nothing to protect against (no "prior state"
// implying the source should have events) — don't block creates.
// This is the initial-sync case where dest has events and source
// is a fresh calendar waiting for them. (#72)
func TestShouldSkipTwoWayCreate_EmptySourceNoPrior(t *testing.T) {
	if shouldSkipTwoWayCreate(db.SyncDirectionTwoWay, 0, 0) {
		t.Error("legitimate first-sync (empty source, no prior records) should not block creates")
	}
}

// TestShouldSkipTwoWayCreate_PopulatedSourceNoPrior verifies that a
// fresh two-way sync against a source that has events and no prior
// records does not trigger the guard — the source is clearly
// working, and any dest-only events should flow up. (#72)
func TestShouldSkipTwoWayCreate_PopulatedSourceNoPrior(t *testing.T) {
	if shouldSkipTwoWayCreate(db.SyncDirectionTwoWay, 10, 0) {
		t.Error("populated source with no prior records should not trigger the guard")
	}
}

// TestShouldSkipTwoWayCreate_Symmetric verifies that
// shouldSkipTwoWayCreate and shouldSkipTwoWayDeletion are symmetric
// mirrors of each other. The delete guard protects the destination
// from mass deletion when the source returned empty; the create
// guard protects the source from mass upload when the source
// returned empty. Both trip on the same condition: "two-way mode,
// one side empty, prior sync records exist." (#72)
//
// This test is a canary — if someone changes the logic of one guard
// without the other, this test fails and flags the asymmetry.
func TestShouldSkipTwoWayCreate_Symmetric(t *testing.T) {
	// The delete guard's first param is destEventCount; the create
	// guard's first param is sourceEventCount. Both should fire on
	// "empty + prior records > 0" in two-way mode.
	cases := []struct {
		name                   string
		direction              db.SyncDirection
		emptySide              int // destEventCount for delete, sourceEventCount for create
		priorCount             int
		expectDeleteGuardFires bool
		expectCreateGuardFires bool
	}{
		{"two-way empty + prior", db.SyncDirectionTwoWay, 0, 50, true, true},
		{"two-way empty + no prior", db.SyncDirectionTwoWay, 0, 0, false, false},
		{"two-way populated + prior", db.SyncDirectionTwoWay, 10, 50, false, false},
		{"one-way empty + prior", db.SyncDirectionOneWay, 0, 50, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDelete := shouldSkipTwoWayDeletion(tc.direction, tc.emptySide, tc.priorCount)
			gotCreate := shouldSkipTwoWayCreate(tc.direction, tc.emptySide, tc.priorCount)
			if gotDelete != tc.expectDeleteGuardFires {
				t.Errorf("shouldSkipTwoWayDeletion: got %v, want %v", gotDelete, tc.expectDeleteGuardFires)
			}
			if gotCreate != tc.expectCreateGuardFires {
				t.Errorf("shouldSkipTwoWayCreate: got %v, want %v", gotCreate, tc.expectCreateGuardFires)
			}
			// The two guards must agree on whether to skip (they're
			// symmetric by design).
			if gotDelete != gotCreate {
				t.Errorf("guards disagree (delete=%v, create=%v) — they should be symmetric", gotDelete, gotCreate)
			}
		})
	}
}
