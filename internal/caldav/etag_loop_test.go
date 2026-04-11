package caldav

import (
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Issue #79 regression suite for the forward/reverse update ETag
// comparison helpers.
//
// Background: the old code compared sourceEvent.ETag against
// destEvent.ETag directly at sync.go:1168 and :1313. Those ETags come
// from different CalDAV servers (iCloud vs SOGo, etc.) and are
// opaque, server-minted strings — they will never match for the same
// underlying event. Result: every cycle PUT every event back to the
// destination, SOGo assigned new dest ETags, next cycle saw the same
// mismatch, and we got an infinite re-PUT loop running ~748 PUTs per
// 15-minute cycle on production. These tests lock in the fix.

// -----------------------------------------------------------------------------
// shouldUpdateDestFromSource — forward path helper
// -----------------------------------------------------------------------------

// TestShouldUpdateDestFromSource_NilPrevAllowsUpdate: if there is no
// prior sync record at all, the caller is about to fall into the
// "create" branch anyway. Defensive default for the update branch is
// to allow the update. (#79)
func TestShouldUpdateDestFromSource_NilPrevAllowsUpdate(t *testing.T) {
	if !shouldUpdateDestFromSource("any-etag", nil) {
		t.Error("nil prev should default to updating (defensive)")
	}
}

// TestShouldUpdateDestFromSource_EmptyStoredETagSkips: legacy records
// that existed before ETag tracking was wired in will have an empty
// SourceETag. Do NOT re-PUT the whole calendar on first deploy — skip
// and let the current cycle's upsert populate the ETag. The next
// cycle will have a real stored value to compare. (#79)
func TestShouldUpdateDestFromSource_EmptyStoredETagSkips(t *testing.T) {
	prev := &db.SyncedEvent{SourceETag: ""}
	if shouldUpdateDestFromSource("current-etag", prev) {
		t.Error("empty stored source ETag should skip the PUT (legacy record migration)")
	}
}

// TestShouldUpdateDestFromSource_MatchingETagSkips: the happy steady-
// state case. Source ETag matches the stored one, meaning the source
// has not changed since last sync. Skip the PUT. This is the path
// that stops the loop. (#79)
func TestShouldUpdateDestFromSource_MatchingETagSkips(t *testing.T) {
	prev := &db.SyncedEvent{SourceETag: "stable-etag"}
	if shouldUpdateDestFromSource("stable-etag", prev) {
		t.Error("matching source ETag should skip the PUT")
	}
}

// TestShouldUpdateDestFromSource_DiffersTriggersUpdate: the real
// update case. Source ETag has changed since last sync — PUT to
// propagate the change to dest. (#79)
func TestShouldUpdateDestFromSource_DiffersTriggersUpdate(t *testing.T) {
	prev := &db.SyncedEvent{SourceETag: "old-etag"}
	if !shouldUpdateDestFromSource("new-etag", prev) {
		t.Error("changed source ETag must trigger the PUT")
	}
}

// TestShouldUpdateDestFromSource_DoesNotCompareAgainstDestETag: the
// canary. This is the exact bug from #79 — the old code compared
// sourceEvent.ETag against destEvent.ETag. A helper that accepts a
// destETag parameter could only be used correctly against the stored
// SOURCE etag. The test locks in that the helper only reads
// prev.SourceETag. (#79)
func TestShouldUpdateDestFromSource_DoesNotCompareAgainstDestETag(t *testing.T) {
	// Stored source ETag matches current source ETag — should skip
	// regardless of what prev.DestETag happens to be.
	prev := &db.SyncedEvent{
		SourceETag: "source-v1",
		DestETag:   "dest-v99-totally-different-value",
	}
	if shouldUpdateDestFromSource("source-v1", prev) {
		t.Error("helper must compare only against SourceETag, not DestETag — #79 regression")
	}
}

// -----------------------------------------------------------------------------
// shouldUpdateSourceFromDest — reverse dest_wins path helper
// -----------------------------------------------------------------------------

// TestShouldUpdateSourceFromDest_NilPrevSkips: unlike the forward
// path, the reverse dest_wins path should NOT treat "no prior record"
// as "go ahead and update." A dest event with no synced_events row
// has not been recorded on our side yet, so we cannot know what the
// last-propagated dest ETag was. Skip and wait for the first full
// cycle to store one. (#79)
func TestShouldUpdateSourceFromDest_NilPrevSkips(t *testing.T) {
	if shouldUpdateSourceFromDest("any-etag", nil) {
		t.Error("nil prev should NOT trigger a reverse update")
	}
}

// TestShouldUpdateSourceFromDest_EmptyStoredETagSkips: same legacy-
// record migration rule as the forward path. Don't mass-PUT back to
// source on first deploy; just start tracking. (#79)
func TestShouldUpdateSourceFromDest_EmptyStoredETagSkips(t *testing.T) {
	prev := &db.SyncedEvent{DestETag: ""}
	if shouldUpdateSourceFromDest("current-etag", prev) {
		t.Error("empty stored dest ETag should skip the reverse PUT")
	}
}

// TestShouldUpdateSourceFromDest_MatchingETagSkips: steady-state
// reverse path. Dest ETag matches the stored one — nothing changed
// on dest since last sync. (#79)
func TestShouldUpdateSourceFromDest_MatchingETagSkips(t *testing.T) {
	prev := &db.SyncedEvent{DestETag: "stable-etag"}
	if shouldUpdateSourceFromDest("stable-etag", prev) {
		t.Error("matching dest ETag should skip the reverse PUT")
	}
}

// TestShouldUpdateSourceFromDest_DiffersTriggersUpdate: legitimate
// dest_wins propagation. User edited the event on dest; push it back
// to source. (#79)
func TestShouldUpdateSourceFromDest_DiffersTriggersUpdate(t *testing.T) {
	prev := &db.SyncedEvent{DestETag: "old-etag"}
	if !shouldUpdateSourceFromDest("new-etag", prev) {
		t.Error("changed dest ETag must trigger the reverse PUT")
	}
}

// TestShouldUpdateSourceFromDest_DoesNotCompareAgainstSourceETag: the
// symmetric canary for #79. The helper must only read prev.DestETag.
func TestShouldUpdateSourceFromDest_DoesNotCompareAgainstSourceETag(t *testing.T) {
	prev := &db.SyncedEvent{
		SourceETag: "source-v99-totally-different-value",
		DestETag:   "dest-v1",
	}
	if shouldUpdateSourceFromDest("dest-v1", prev) {
		t.Error("helper must compare only against DestETag, not SourceETag — #79 regression")
	}
}

// -----------------------------------------------------------------------------
// Symmetry + contract canaries
// -----------------------------------------------------------------------------

// TestEtagHelpers_SymmetricLegacyBehavior verifies both helpers apply
// the same "legacy record migration" rule: if the side we track has
// an empty stored ETag, skip. The two helpers diverge only on the
// nil-prev case (forward defaults to true, reverse defaults to
// false) — this test documents that asymmetry so future refactors
// don't accidentally unify them. (#79)
func TestEtagHelpers_SymmetricLegacyBehavior(t *testing.T) {
	// Both side's empty-etag case: skip.
	forwardLegacy := &db.SyncedEvent{SourceETag: ""}
	reverseLegacy := &db.SyncedEvent{DestETag: ""}
	if shouldUpdateDestFromSource("x", forwardLegacy) {
		t.Error("forward legacy case should skip")
	}
	if shouldUpdateSourceFromDest("x", reverseLegacy) {
		t.Error("reverse legacy case should skip")
	}

	// Intentional asymmetry: nil prev.
	if !shouldUpdateDestFromSource("x", nil) {
		t.Error("forward nil-prev defaults to true (defensive; caller is supposed to use create branch)")
	}
	if shouldUpdateSourceFromDest("x", nil) {
		t.Error("reverse nil-prev defaults to false (no history → do not guess)")
	}
}

// TestEtagHelpers_BothSidesUnchangedTable covers the combinatorial
// space with a single table. Each entry is a named scenario with
// the inputs and the expected output for each helper. (#79)
func TestEtagHelpers_BothSidesUnchangedTable(t *testing.T) {
	cases := []struct {
		name             string
		currentETag      string
		prev             *db.SyncedEvent
		expectForwardPUT bool
		expectReversePUT bool
	}{
		{
			name:             "nil prev",
			currentETag:      "any",
			prev:             nil,
			expectForwardPUT: true,  // defensive default
			expectReversePUT: false, // no history
		},
		{
			name:             "empty stored etag (legacy record)",
			currentETag:      "any",
			prev:             &db.SyncedEvent{},
			expectForwardPUT: false,
			expectReversePUT: false,
		},
		{
			name:             "matching source etag, empty dest etag",
			currentETag:      "source-1",
			prev:             &db.SyncedEvent{SourceETag: "source-1"},
			expectForwardPUT: false,
			expectReversePUT: false, // dest side is empty, reverse skips
		},
		{
			name:             "differing source etag",
			currentETag:      "source-2",
			prev:             &db.SyncedEvent{SourceETag: "source-1"},
			expectForwardPUT: true,
			expectReversePUT: false, // dest side has no data
		},
		{
			name:             "matching dest etag, empty source etag",
			currentETag:      "dest-1",
			prev:             &db.SyncedEvent{DestETag: "dest-1"},
			expectForwardPUT: false, // source side is empty, forward skips
			expectReversePUT: false,
		},
		{
			name:             "differing dest etag",
			currentETag:      "dest-2",
			prev:             &db.SyncedEvent{DestETag: "dest-1"},
			expectForwardPUT: false,
			expectReversePUT: true,
		},
		{
			name:             "both sides populated, both match (steady state)",
			currentETag:      "x",
			prev:             &db.SyncedEvent{SourceETag: "x", DestETag: "x"},
			expectForwardPUT: false,
			expectReversePUT: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotForward := shouldUpdateDestFromSource(tc.currentETag, tc.prev)
			gotReverse := shouldUpdateSourceFromDest(tc.currentETag, tc.prev)
			if gotForward != tc.expectForwardPUT {
				t.Errorf("shouldUpdateDestFromSource: got %v, want %v", gotForward, tc.expectForwardPUT)
			}
			if gotReverse != tc.expectReversePUT {
				t.Errorf("shouldUpdateSourceFromDest: got %v, want %v", gotReverse, tc.expectReversePUT)
			}
		})
	}
}
