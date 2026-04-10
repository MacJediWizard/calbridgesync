package caldav

import (
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// TestPlanOrphanDeletion_EmptySourceWithPriorRecords verifies that a source
// returning zero events combined with a populated previouslySyncedMap
// produces zero deletions and a warning. This is the primary data-loss
// scenario: an auth failure or broken source URL must not wipe the
// destination.
func TestPlanOrphanDeletion_EmptySourceWithPriorRecords(t *testing.T) {
	destEventMap := map[string]Event{
		"uid-1": {UID: "uid-1", Path: "/cal/uid-1.ics"},
		"uid-2": {UID: "uid-2", Path: "/cal/uid-2.ics"},
		"uid-3": {UID: "uid-3", Path: "/cal/uid-3.ics"},
	}
	previouslySyncedMap := map[string]*db.SyncedEvent{
		"uid-1": {EventUID: "uid-1"},
		"uid-2": {EventUID: "uid-2"},
		"uid-3": {EventUID: "uid-3"},
	}

	toDelete, warning := planOrphanDeletion(destEventMap, 0, previouslySyncedMap, 0.5)

	if len(toDelete) != 0 {
		t.Fatalf("expected zero deletions when source is empty but previouslySyncedMap is populated; got %d", len(toDelete))
	}
	if warning == "" {
		t.Fatal("expected a safety warning when source returned 0 events but previouslySyncedMap is populated")
	}
	if !strings.Contains(warning, "0 events") {
		t.Errorf("warning should mention the zero-events condition; got: %q", warning)
	}
}

// TestPlanOrphanDeletion_EmptySourceNoPriorRecords verifies that the
// empty-source guard does NOT block a legitimate first sync of a
// new, empty source. Before any sync has happened, previouslySyncedMap
// is empty and there is nothing to protect.
func TestPlanOrphanDeletion_EmptySourceNoPriorRecords(t *testing.T) {
	destEventMap := map[string]Event{}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toDelete, warning := planOrphanDeletion(destEventMap, 0, previouslySyncedMap, 0.5)

	if len(toDelete) != 0 {
		t.Fatalf("expected zero deletions from an empty destination; got %d", len(toDelete))
	}
	if warning != "" {
		t.Errorf("expected no warning on first-sync empty case; got: %q", warning)
	}
}

// TestPlanOrphanDeletion_OwnershipFilter verifies the multi-source →
// one-destination protection. destEventMap contains events from multiple
// sources, but only events in previouslySyncedMap (owned by THIS source)
// are eligible for deletion. This is the second data-loss scenario: the
// user has Work, Home, and Family source calendars all syncing to a single
// SOGo destination. Without this filter, each source wipes the others'
// events on every sync cycle.
func TestPlanOrphanDeletion_OwnershipFilter(t *testing.T) {
	destEventMap := map[string]Event{
		// Owned by this source (in previouslySyncedMap)
		"our-uid-1": {UID: "our-uid-1", Path: "/cal/our-uid-1.ics"},
		"our-uid-2": {UID: "our-uid-2", Path: "/cal/our-uid-2.ics"},
		// Owned by a different source (NOT in previouslySyncedMap)
		"other-uid-1": {UID: "other-uid-1", Path: "/cal/other-uid-1.ics"},
		"other-uid-2": {UID: "other-uid-2", Path: "/cal/other-uid-2.ics"},
		"other-uid-3": {UID: "other-uid-3", Path: "/cal/other-uid-3.ics"},
	}
	previouslySyncedMap := map[string]*db.SyncedEvent{
		"our-uid-1": {EventUID: "our-uid-1"},
		"our-uid-2": {EventUID: "our-uid-2"},
	}
	// Source is present but returned zero events — i.e., both our-uid-1 and
	// our-uid-2 were intentionally deleted from source. Pass a non-zero
	// sourceEventCount so the empty-source guard does not trigger; we want
	// to isolate the ownership-filter behavior in this test.
	// (sourceEventCount=1 simulates a source that still has unrelated events.)
	toDelete, warning := planOrphanDeletion(destEventMap, 1, previouslySyncedMap, 0.0)

	if warning != "" {
		t.Fatalf("expected no warning; got: %q", warning)
	}
	if len(toDelete) != 2 {
		t.Fatalf("expected to delete 2 events owned by this source; got %d", len(toDelete))
	}

	// Verify only our-owned UIDs are in toDelete.
	gotUIDs := make(map[string]bool)
	for _, e := range toDelete {
		gotUIDs[e.UID] = true
	}
	for uid := range previouslySyncedMap {
		if !gotUIDs[uid] {
			t.Errorf("expected toDelete to include %q (owned by this source)", uid)
		}
	}
	for uid := range destEventMap {
		if _, owned := previouslySyncedMap[uid]; !owned && gotUIDs[uid] {
			t.Errorf("toDelete must NOT include %q (owned by a different source)", uid)
		}
	}
}

// TestPlanOrphanDeletion_MassDeleteThresholdExceeded verifies the
// defense-in-depth against a scenario where, despite the ownership filter,
// a source would legitimately delete more than half of its own previously
// synced events in a single cycle. This is almost always the result of a
// partial auth failure or source-side corruption rather than an intentional
// bulk cleanup by the user.
func TestPlanOrphanDeletion_MassDeleteThresholdExceeded(t *testing.T) {
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	destEventMap := make(map[string]Event)
	// Ten previously-synced events, eight still on destination (would be deleted).
	for i := 0; i < 10; i++ {
		uid := "uid-" + string(rune('a'+i))
		previouslySyncedMap[uid] = &db.SyncedEvent{EventUID: uid}
	}
	for i := 0; i < 8; i++ {
		uid := "uid-" + string(rune('a'+i))
		destEventMap[uid] = Event{UID: uid, Path: "/cal/" + uid + ".ics"}
	}

	// Source claims it has only 2 events (the remaining uid-i and uid-j).
	// Ratio of would-be-deletes to previouslySyncedMap = 8/10 = 80%,
	// exceeds the 50% threshold.
	toDelete, warning := planOrphanDeletion(destEventMap, 2, previouslySyncedMap, 0.5)

	if len(toDelete) != 0 {
		t.Fatalf("expected zero deletions when threshold exceeded; got %d", len(toDelete))
	}
	if warning == "" {
		t.Fatal("expected a safety warning when mass-delete threshold exceeded")
	}
	if !strings.Contains(warning, "80%") {
		t.Errorf("warning should report the actual ratio; got: %q", warning)
	}
	if !strings.Contains(warning, "50%") {
		t.Errorf("warning should report the configured threshold; got: %q", warning)
	}
}

// TestPlanOrphanDeletion_NormalOperation verifies that a routine,
// small-delta sync still works correctly. A user deletes a handful of
// events from source; the same events are removed from destination.
// Nothing else is touched.
func TestPlanOrphanDeletion_NormalOperation(t *testing.T) {
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	destEventMap := make(map[string]Event)
	// 20 previously-synced events.
	for i := 0; i < 20; i++ {
		uid := "uid-" + string(rune('a'+i))
		previouslySyncedMap[uid] = &db.SyncedEvent{EventUID: uid}
	}
	// 2 of them (uid-a, uid-b) remain on destination but are absent from
	// source — i.e., user intentionally deleted them. The other 18 are
	// still present on both source and destination, so the sync loop at
	// sync.go:625 would have already removed them from destEventMap
	// before planOrphanDeletion was called.
	destEventMap["uid-a"] = Event{UID: "uid-a", Path: "/cal/uid-a.ics"}
	destEventMap["uid-b"] = Event{UID: "uid-b", Path: "/cal/uid-b.ics"}

	// Source returned 18 events, so the empty-source guard does not trigger.
	toDelete, warning := planOrphanDeletion(destEventMap, 18, previouslySyncedMap, 0.5)

	if warning != "" {
		t.Errorf("expected no warning on normal 2-of-20 delete; got: %q", warning)
	}
	if len(toDelete) != 2 {
		t.Fatalf("expected 2 events to be deleted; got %d", len(toDelete))
	}

	gotUIDs := make(map[string]bool)
	for _, e := range toDelete {
		gotUIDs[e.UID] = true
	}
	if !gotUIDs["uid-a"] || !gotUIDs["uid-b"] {
		t.Errorf("expected toDelete to contain uid-a and uid-b; got %+v", gotUIDs)
	}
}

// TestPlanOrphanDeletion_ThresholdBoundary verifies that the ratio check
// uses strict greater-than, so a ratio exactly at the threshold is allowed
// through. This prevents a single-event-off edge case where legitimate
// operations at the boundary get blocked unnecessarily.
func TestPlanOrphanDeletion_ThresholdBoundary(t *testing.T) {
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	destEventMap := make(map[string]Event)
	for i := 0; i < 10; i++ {
		uid := "uid-" + string(rune('a'+i))
		previouslySyncedMap[uid] = &db.SyncedEvent{EventUID: uid}
	}
	// Exactly 5 of 10 remain on destination (50%).
	for i := 0; i < 5; i++ {
		uid := "uid-" + string(rune('a'+i))
		destEventMap[uid] = Event{UID: uid, Path: "/cal/" + uid + ".ics"}
	}

	// 50% ratio == threshold. Should NOT trigger the safety check.
	toDelete, warning := planOrphanDeletion(destEventMap, 5, previouslySyncedMap, 0.5)

	if warning != "" {
		t.Errorf("ratio at the threshold should not trigger safety; got warning: %q", warning)
	}
	if len(toDelete) != 5 {
		t.Errorf("expected 5 events at the 50%% boundary; got %d", len(toDelete))
	}
}

// TestPlanOrphanDeletion_ManualDestEventsPreserved verifies that events
// the user created manually on the destination (events which are therefore
// NOT in previouslySyncedMap) are never touched by one-way orphan deletion.
// This is a pleasant side-effect of the ownership filter: users who add
// events directly to their destination calendar won't see them disappear
// on the next sync.
func TestPlanOrphanDeletion_ManualDestEventsPreserved(t *testing.T) {
	// Populate previouslySyncedMap with 10 synced events so the mass-delete
	// ratio guard does not trigger — we only want to isolate the
	// ownership-filter assertion in this test.
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	for i := 0; i < 10; i++ {
		uid := "synced-uid-" + string(rune('a'+i))
		previouslySyncedMap[uid] = &db.SyncedEvent{EventUID: uid}
	}

	// destEventMap contains exactly one previously-synced event that is
	// absent from source (legitimate orphan), plus two events the user
	// created manually on the destination (never tracked by this source).
	destEventMap := map[string]Event{
		"synced-uid-a": {UID: "synced-uid-a", Path: "/cal/synced-uid-a.ics"},
		"manual-uid-1": {UID: "manual-uid-1", Path: "/cal/manual-uid-1.ics"},
		"manual-uid-2": {UID: "manual-uid-2", Path: "/cal/manual-uid-2.ics"},
	}

	// Source returned 9 events (all the other synced-uid-b..j still exist
	// on source, only synced-uid-a was intentionally deleted by the user).
	// Ratio of candidates to previouslySyncedMap = 1/10 = 10%, well below
	// the 50% safety threshold.
	toDelete, warning := planOrphanDeletion(destEventMap, 9, previouslySyncedMap, 0.5)

	if warning != "" {
		t.Fatalf("expected no warning on routine 1-of-10 delete; got: %q", warning)
	}
	if len(toDelete) != 1 {
		t.Fatalf("expected exactly 1 event to be deleted (synced-uid-a); got %d", len(toDelete))
	}
	if toDelete[0].UID != "synced-uid-a" {
		t.Errorf("expected toDelete[0].UID = synced-uid-a; got %q", toDelete[0].UID)
	}

	// Double-check no manual events sneaked in.
	for _, e := range toDelete {
		if e.UID == "manual-uid-1" || e.UID == "manual-uid-2" {
			t.Errorf("manual destination event %q must not be deleted by one-way sync", e.UID)
		}
	}
}

// TestRewriteDeletePathForDestination verifies the WebDAV-Sync delete-path
// rewrite logic. When a source supports sync-collection (RFC 6578), the
// deleted-paths list returned by SyncCollection is in the SOURCE server's
// URL namespace and cannot be used directly against the destination. This
// helper extracts the event filename (UID.ics by PutEvent convention) and
// reattaches it to the destination calendar path.
//
// Before this helper existed, every WebDAV-Sync delete silently 404'd on
// the destination and stale events accumulated forever.
func TestRewriteDeletePathForDestination(t *testing.T) {
	tests := []struct {
		name             string
		sourcePath       string
		destCalendarPath string
		want             string
	}{
		{
			name:             "normal case — deep source path, clean destination",
			sourcePath:       "/calendar/work-acct/abc123.ics",
			destCalendarPath: "/SOGo/dav/user@host/Calendar/personal",
			want:             "/SOGo/dav/user@host/Calendar/personal/abc123.ics",
		},
		{
			name:             "destination has trailing slash — no double slash",
			sourcePath:       "/calendar/work-acct/abc123.ics",
			destCalendarPath: "/SOGo/dav/user@host/Calendar/personal/",
			want:             "/SOGo/dav/user@host/Calendar/personal/abc123.ics",
		},
		{
			name:             "source has trailing slash — still extracts filename",
			sourcePath:       "/calendar/work-acct/abc123.ics/",
			destCalendarPath: "/dest/cal",
			want:             "/dest/cal/abc123.ics",
		},
		{
			name:             "URL-encoded filename preserved as-is",
			sourcePath:       "/cal/event%20with%20spaces.ics",
			destCalendarPath: "/dest/cal",
			want:             "/dest/cal/event%20with%20spaces.ics",
		},
		{
			name:             "root-level source path",
			sourcePath:       "/abc.ics",
			destCalendarPath: "/dest/cal",
			want:             "/dest/cal/abc.ics",
		},
		{
			name:             "filename only, no leading slash",
			sourcePath:       "abc.ics",
			destCalendarPath: "/dest/cal",
			want:             "/dest/cal/abc.ics",
		},
		{
			name:             "empty source path returns empty (skip-signal)",
			sourcePath:       "",
			destCalendarPath: "/dest/cal",
			want:             "",
		},
		{
			name:             "source path is a single slash returns empty (skip-signal)",
			sourcePath:       "/",
			destCalendarPath: "/dest/cal",
			want:             "",
		},
		{
			name:             "UID with dots and hyphens",
			sourcePath:       "/cal/user/20260101T120000Z-event-uid-1234.ics",
			destCalendarPath: "/dest/cal",
			want:             "/dest/cal/20260101T120000Z-event-uid-1234.ics",
		},
		{
			name:             "destination is root /",
			sourcePath:       "/cal/abc.ics",
			destCalendarPath: "/",
			want:             "/abc.ics",
		},
		{
			name:             "destination is empty string",
			sourcePath:       "/cal/abc.ics",
			destCalendarPath: "",
			want:             "/abc.ics",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteDeletePathForDestination(tt.sourcePath, tt.destCalendarPath)
			if got != tt.want {
				t.Errorf("rewriteDeletePathForDestination(%q, %q) = %q, want %q",
					tt.sourcePath, tt.destCalendarPath, got, tt.want)
			}
		})
	}
}

// TestRewriteDeletePathForDestination_FilenameMatchesPutEventConvention
// documents the contract between this helper and client.go:PutEvent.
// PutEvent writes events as "{calendarPath}/{UID}.ics" (see client.go:602).
// rewriteDeletePathForDestination must produce a path in the same shape
// so that a delete issued after a put finds the same file.
func TestRewriteDeletePathForDestination_FilenameMatchesPutEventConvention(t *testing.T) {
	// Simulate what PutEvent would have written.
	destCalendarPath := "/SOGo/dav/user@host/Calendar/personal"
	uid := "event-uid-12345"
	putPath := strings.TrimSuffix(destCalendarPath, "/") + "/" + uid + ".ics"

	// Simulate what WebDAV-Sync returns from the source for the same UID.
	// Source server uses a different URL layout.
	sourcePath := "/caldav/different-layout/" + uid + ".ics"

	// The rewritten delete path must match the put path.
	deletePath := rewriteDeletePathForDestination(sourcePath, destCalendarPath)
	if deletePath != putPath {
		t.Errorf("rewrite produced %q, but PutEvent would have written %q — delete will 404",
			deletePath, putPath)
	}
}
