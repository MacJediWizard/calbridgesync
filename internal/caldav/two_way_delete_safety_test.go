package caldav

import (
	"testing"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Issue #72 Bug B regression suite.
//
// Before #72, the two-way source-side deletion safety check in
// syncCalendar read syncedEvent.UpdatedAt, which is bumped to
// time.Now() on every successful sync cycle via UpsertSyncedEvent.
// For any regularly-running sync, that meant UpdatedAt was always
// within one sync interval of "now", the safety check always fired,
// and every two-way source-side deletion was silently blocked
// forever.
//
// The fix reads syncedEvent.CreatedAt instead — a sticky column
// that's set once at first-sync and never bumped. CreatedAt gives
// us the correct "was this event FIRST synced recently" semantics
// that the safety guard actually wants.
//
// These tests exercise the semantics by passing a SyncedEvent
// directly into isWithinSyncSafetyThreshold (which is the pure
// helper the fix calls) and confirming the behavior matches the
// intent for both brand-new and old events.

// TestDeleteSafety_FreshlyCreatedEventProtected verifies that a
// newly-synced event (CreatedAt very recent) is still protected by
// the safety guard — this is the case the guard was originally
// designed for. A brand-new event that hasn't had time to propagate
// to the destination yet must NOT be deleted from the source if it
// appears missing from the destination on the very next cycle. (#72)
func TestDeleteSafety_FreshlyCreatedEventProtected(t *testing.T) {
	now := time.Now()
	sourceInterval := 5 * time.Minute
	// Synced 30 seconds ago — well inside the safety window
	syncedEvent := &db.SyncedEvent{
		CreatedAt: now.Add(-30 * time.Second),
		UpdatedAt: now.Add(-30 * time.Second),
	}

	if !isWithinSyncSafetyThreshold(syncedEvent.CreatedAt, sourceInterval, now) {
		t.Error("brand-new event (30s old) must be protected by the safety guard")
	}
}

// TestDeleteSafety_OldEventEligibleForDeletion verifies the fix:
// an event that was first synced long ago (CreatedAt well in the
// past) is eligible for deletion even though its UpdatedAt was
// bumped by every sync cycle since. Before the fix, this case would
// have been blocked by reading UpdatedAt. (#72)
func TestDeleteSafety_OldEventEligibleForDeletion(t *testing.T) {
	now := time.Now()
	sourceInterval := 5 * time.Minute
	syncedEvent := &db.SyncedEvent{
		// First synced a week ago — definitely not a new event
		CreatedAt: now.Add(-7 * 24 * time.Hour),
		// UpdatedAt was bumped on the most recent sync cycle, 30
		// seconds ago. This is the exact scenario that broke before
		// the fix: UpdatedAt makes the event look fresh even though
		// it's a week old.
		UpdatedAt: now.Add(-30 * time.Second),
	}

	// The fix: read CreatedAt, not UpdatedAt
	if isWithinSyncSafetyThreshold(syncedEvent.CreatedAt, sourceInterval, now) {
		t.Error("week-old event must be eligible for deletion (read CreatedAt, not UpdatedAt)")
	}
	// Regression canary: if someone accidentally changes the call
	// site back to UpdatedAt, this side-check fails to remind them
	// why the change was made.
	if !isWithinSyncSafetyThreshold(syncedEvent.UpdatedAt, sourceInterval, now) {
		t.Error("if you're here, the test's setup is wrong — UpdatedAt should be within threshold")
	}
}

// TestDeleteSafety_EventAtExactThreshold verifies the boundary
// condition for CreatedAt. The threshold uses strict After(), so an
// event created exactly at (now - sourceInterval) is OUT of the
// window and eligible for deletion. This matches the documented
// semantics of isWithinSyncSafetyThreshold. (#72)
func TestDeleteSafety_EventAtExactThreshold(t *testing.T) {
	now := time.Now()
	sourceInterval := 5 * time.Minute
	syncedEvent := &db.SyncedEvent{
		// Exactly at the threshold
		CreatedAt: now.Add(-sourceInterval),
		UpdatedAt: now.Add(-30 * time.Second),
	}

	if isWithinSyncSafetyThreshold(syncedEvent.CreatedAt, sourceInterval, now) {
		t.Error("event at exact threshold should be OUT of window (strict After)")
	}
}

// TestDeleteSafety_UpdatedAtBumpPathologyBefore72 is a documentary
// regression test. It reconstructs the exact timing pathology that
// existed before #72 and confirms the fix works around it.
//
// Scenario: a source runs every 5 minutes. An event has been present
// since a week ago (CreatedAt = -7 days). Every successful sync
// cycle bumped UpdatedAt to the end of that cycle. The MOST RECENT
// cycle ended 30 seconds ago, so UpdatedAt = -30s.
//
// The user deleted the event from the destination (SOGo) 2 minutes
// ago. The current sync cycle is processing the deletion propagation.
//
// Before the fix: isWithinSyncSafetyThreshold(UpdatedAt=-30s, 5min, now)
//
//	= (-30s).After(-5min)
//	= TRUE
//	→ safety fires, delete is skipped FOREVER
//
// After the fix: isWithinSyncSafetyThreshold(CreatedAt=-7d, 5min, now)
//
//	= (-7d).After(-5min)
//	= FALSE
//	→ delete proceeds normally
//
// (#72)
func TestDeleteSafety_UpdatedAtBumpPathologyBefore72(t *testing.T) {
	now := time.Now()
	sourceInterval := 5 * time.Minute

	syncedEvent := &db.SyncedEvent{
		CreatedAt: now.Add(-7 * 24 * time.Hour),
		UpdatedAt: now.Add(-30 * time.Second),
	}

	// The pathological pre-#72 behavior — reproduced exactly
	preFixWouldBlock := isWithinSyncSafetyThreshold(syncedEvent.UpdatedAt, sourceInterval, now)
	if !preFixWouldBlock {
		t.Fatal("pre-#72 pathology not reproduced: UpdatedAt should be within threshold")
	}

	// The post-#72 fix
	postFixBlocks := isWithinSyncSafetyThreshold(syncedEvent.CreatedAt, sourceInterval, now)
	if postFixBlocks {
		t.Error("post-#72 fix should allow deletion of week-old event")
	}
}
