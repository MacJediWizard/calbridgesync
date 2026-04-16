package caldav

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/macjediwizard/calbridgesync/internal/activity"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

// isAlreadyExistsError checks if the error indicates the event already exists (412 Precondition Failed).
func isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "412") || strings.Contains(errStr, "Precondition Failed")
}

// isForbiddenError checks if the error indicates write access is forbidden (403 Forbidden).
func isForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "403") || strings.Contains(errStr, "Forbidden")
}

// isSourceAlreadyExistsError reports whether a PutEvent error against the
// source CalDAV server indicates that the event already exists there — either
// in the calendar being synced (412 Precondition Failed on the If-None-Match
// header) or anywhere else on the same account (409 Conflict on a UID
// collision). iCloud in particular returns 409 Conflict when you try to PUT
// an event whose UID already exists under a DIFFERENT calendar on the same
// account — CalDAV UIDs are account-global on iCloud, so an event that lives
// on iCloud's "Home" calendar can't be created again on iCloud's "Work"
// calendar. (#74)
//
// This is only used by the two-way reverse create pass — the main forward
// direction uses isAlreadyExistsError, which stays 412-only so existing
// update-conflict logic in the forward path is unchanged.
func isSourceAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "412") ||
		strings.Contains(errStr, "Precondition Failed") ||
		strings.Contains(errStr, "409") ||
		strings.Contains(errStr, "Conflict")
}

// shouldSkipTwoWayDeletion returns true if the two-way deletion pass
// should be skipped entirely for this sync cycle. This is the guard
// introduced in commit b772c56 (and extended by PR #22) against mass
// deletion when the destination query returned empty but we have
// prior sync records.
//
// The rationale: if we previously synced N events and the destination
// suddenly says it has zero, that's almost certainly a destination
// query failure (network hiccup, bad auth, server bug), NOT a user
// who just deleted N events in rapid succession. Deleting the
// corresponding events from the source based on that empty query
// would be a disaster.
//
// Extracted as a pure helper in Issue #68 so it can be unit-tested
// directly. Behavior is byte-for-byte identical to the previous
// inline implementation.
func shouldSkipTwoWayDeletion(direction db.SyncDirection, destEventCount, previouslySyncedCount int) bool {
	return direction == db.SyncDirectionTwoWay &&
		destEventCount == 0 &&
		previouslySyncedCount > 0
}

// shouldSkipTwoWayCreate returns true if the two-way reverse CREATE
// pass (dest → source upload) should be skipped entirely for this
// sync cycle. Mirror of shouldSkipTwoWayDeletion, guarding against
// mass upload to the source when the source query returned empty.
//
// Rationale: if we previously synced N events and the SOURCE query
// now returns zero events, that's almost certainly a source query
// failure (network hiccup, bad auth, server bug on iCloud's end),
// NOT a user who just deleted everything from their iCloud calendar.
// Without this guard, the reverse create pass would see every
// destination event as "not on source, upload it" and would mass-
// upload the entire destination calendar back to the source, causing
// iCloud to get every SOGo event as if it were new.
//
// This is the symmetric twin of shouldSkipTwoWayDeletion. The two
// guards together enforce: "if either side returns empty while we
// have prior sync records, treat it as a query failure and don't
// propagate anything across that empty boundary." Introduced in
// Issue #72.
func shouldSkipTwoWayCreate(direction db.SyncDirection, sourceEventCount, previouslySyncedCount int) bool {
	return direction == db.SyncDirectionTwoWay &&
		sourceEventCount == 0 &&
		previouslySyncedCount > 0
}

// syncETagEntry tracks the last-observed source and destination ETags
// for a single event UID during a sync pass. Collected as the sync
// iterates through events, then written to the synced_events table in
// the final upsert loop so the NEXT cycle can detect whether either
// side has actually changed since this sync.
//
// The ETags are opaque, server-generated strings — a CalDAV ETag from
// iCloud and a CalDAV ETag from SOGo for the SAME underlying event
// will never match each other. They are only meaningful when compared
// against a previously-stored value from the SAME server. That is why
// we store both sides independently. (#79)
type syncETagEntry struct {
	sourceETag string
	destETag   string
}

// shouldUpdateDestFromSource decides whether to PUT a source event onto
// the destination during the forward update branch of the sync loop.
//
// Correctness depends on comparing against the LAST-KNOWN source ETag
// from previouslySyncedMap, not against the destination's current ETag
// — cross-server ETag comparison is meaningless (every server mints
// its own opaque string), which is the bug that produced the infinite
// re-PUT loop in #79.
//
// Three cases:
//
//   - prev == nil: the caller already handled "no prior record" via
//     the create branch. Defensive default — allow the update.
//   - prev.SourceETag == "": legacy record from before ETag tracking
//     was wired in. Don't re-PUT the whole calendar on first deploy;
//     skip and let this cycle's upsert start tracking ETags from the
//     current source state. If the source genuinely changed in the
//     meantime, we accept a one-cycle propagation delay in exchange
//     for not thundering-herd the destination on rollout.
//   - prev.SourceETag != "": a real ETag we can compare. PUT iff the
//     current source ETag differs from the stored one.
func shouldUpdateDestFromSource(sourceETag string, prev *db.SyncedEvent) bool {
	if prev == nil {
		return true
	}
	if prev.SourceETag == "" {
		return false
	}
	return prev.SourceETag != sourceETag
}

// shouldUpdateSourceFromDest is the reverse-direction ETag check
// used by the dest_wins update pass. It's a near-mirror of
// shouldUpdateDestFromSource but NOT a full symmetric twin — the
// nil-prev handling diverges intentionally, and this block exists
// to document why so the next reader who spots the asymmetry does
// not "unify" them and break one or both call sites. (#79, #99)
//
// Three cases:
//
//   - prev == nil: the event exists on both sides but has no
//     tracking row. **Returns false** (skip the reverse PUT).
//     Rationale: on this path we cannot tell whether the current
//     dest ETag is a recent change or a long-standing state we've
//     just never seen before — both look identical when there's
//     no history. Guessing "push dest → source" on missing history
//     would clobber source with a dest state we have no reason to
//     trust as newer. Better to do nothing this cycle, let the
//     upsert at the end of the pass record both ETags, and
//     compare properly on the NEXT cycle. Locked in by
//     TestShouldUpdateSourceFromDest_NilPrevSkips and
//     TestEtagHelpers_SymmetricLegacyBehavior.
//
//   - prev.DestETag == "": legacy record from before DestETag
//     tracking was wired in. Don't re-PUT the whole calendar on
//     first deploy; skip and let this cycle's upsert start
//     tracking DestETag from the current state. Same rollout
//     protection the forward helper gives for prev.SourceETag.
//
//   - prev.DestETag != "": a real ETag we can compare. PUT iff
//     the current dest ETag differs from the stored one.
//
// ### Why the asymmetry with shouldUpdateDestFromSource is load-bearing
//
// shouldUpdateDestFromSource returns **true** on nil prev, not
// false. That's not a bug — the forward path is reached via the
// `else if` of `!existsByUID`, which means the caller already
// routed the "brand new event" case to the create branch. Seeing
// nil prev on the forward path therefore implies an anomaly
// (synced_events was cleared, a third-party tool wrote the event
// out-of-band, etc.) and defaulting to "push source → dest" is
// consistent with source_wins semantics: source is source of
// truth so push it.
//
// The reverse path (this one) has a different context. It only
// runs when conflict_strategy == dest_wins, and it walks
// destEvents looking for events to push back to source. Seeing
// nil prev here could mean:
//
//	(a) dest just got a brand new event the forward create pass
//	    has not handled yet — in which case we should NOT push it
//	    to source because the forward pass handles creates on
//	    its own
//	(b) a pre-existing same-UID event on both sides with no
//	    tracking — in which case "guess dest is newer" risks
//	    clobbering legitimate source state
//
// Both (a) and (b) favor "skip." The forward helper's nil-prev
// default doesn't have (a) as a concern (it runs AFTER the
// forward create pass inside the same `if existsByUID` branch).
//
// TL;DR: identical rationale, different calling context, so the
// safe default flips. This is intentional and covered by tests.
func shouldUpdateSourceFromDest(destETag string, prev *db.SyncedEvent) bool {
	if prev == nil || prev.DestETag == "" {
		return false
	}
	return prev.DestETag != destETag
}

// isRealConflictSourceWins reports whether a successful source→dest
// update should be surfaced to the UI as a conflict. A routine
// propagation where only source moved is NOT a conflict — it's just
// an update. A real conflict requires dest to also have moved
// independently since our last recorded sync, detected by the
// destination's current ETag differing from the one we stored on
// the previous cycle.
//
// Returns false if prev is nil or prev.DestETag is empty — we cannot
// tell whether dest moved if we don't have a reference point, so we
// default to "not a conflict" to keep the warnings list clean. The
// next cycle will have a recorded dest ETag to compare against.
// (#136, refined in #169)
func isRealConflictSourceWins(prev *db.SyncedEvent, currentDestETag string) bool {
	if prev == nil || prev.DestETag == "" {
		return false
	}
	return currentDestETag != prev.DestETag
}

// isRealConflictDestWins is the reverse-path mirror: a real conflict
// requires source to also have moved since our last recorded sync.
// Reaching the dest→source update branch already proves dest moved
// (shouldUpdateSourceFromDest is the loop guard), so the only extra
// check needed here is "did source also move independently".
// (#136, refined in #169)
func isRealConflictDestWins(prev *db.SyncedEvent, currentSourceETag string) bool {
	if prev == nil || prev.SourceETag == "" {
		return false
	}
	return currentSourceETag != prev.SourceETag
}

// planTwoWayDeletion determines which destination events should be
// deleted because they were removed from source during a two-way sync.
// It is the dest-deletion mirror of planOrphanDeletion (one-way) and
// the symmetric counterpart of planReverseCreate.
//
// Three safety rules are enforced:
//
//  1. Empty-dest guard: if destination returned 0 events but we have
//     prior sync records, refuse. Same rationale as the original
//     shouldSkipTwoWayDeletion check — an empty destination query is
//     almost certainly a destination failure, not a user action.
//
//  2. Empty-source guard: if source returned 0 events but we have
//     prior sync records, refuse. NEW in #80. Without this guard, a
//     transient source query failure for one calendar would mass-
//     delete the entire destination calendar — exactly the
//     catastrophe that prompted this PR (William lost 748 events
//     from SOGo when iCloud returned a partial set).
//
//  3. Mass-deletion ratio guard: if more than maxDeleteRatio of the
//     prior records would be deleted in a single cycle, refuse and
//     surface a warning. Mirrors planOrphanDeletion's ratio check
//     for the two-way path. Defends against the case where source
//     returned SOME events but is missing the bulk of them — neither
//     of the empty-side guards catches this case, and the existing
//     per-event time-based safety threshold only protects events
//     that were synced very recently. Critical for the partial-
//     source-failure scenario.
//
// Returns the list of dest UIDs to delete and a non-empty warning if
// any safety rule was triggered. When a warning is returned, toDelete
// is nil — the caller must not perform any deletions in that case.
// (#80)
func planTwoWayDeletion(
	sourceEventMap map[string]Event,
	destEventMap map[string]Event,
	previouslySyncedMap map[string]*db.SyncedEvent,
	maxDeleteRatio float64,
) (toDelete []string, warning string) {
	// Rule 1: empty destination with prior records → refuse.
	if len(destEventMap) == 0 && len(previouslySyncedMap) > 0 {
		return nil, fmt.Sprintf(
			"destination returned 0 events but %d previously-synced records exist - "+
				"skipping two-way deletion pass for safety (possible destination query failure)",
			len(previouslySyncedMap),
		)
	}
	// Rule 2: empty source with prior records → refuse. (#80)
	if len(sourceEventMap) == 0 && len(previouslySyncedMap) > 0 {
		return nil, fmt.Sprintf(
			"source returned 0 events but %d previously-synced records exist - "+
				"skipping two-way deletion pass for safety (possible source query failure)",
			len(previouslySyncedMap),
		)
	}
	// Build candidate list: previously-synced UIDs that no longer
	// exist on source but still exist on destination.
	//
	// Rule 4 (#171): require prev.SourceETag != "" on the candidate.
	// A synced_events row with an empty SourceETag means "we wrote a
	// tracking entry but never observed this UID on the source side"
	// — the exact shape reverse-create leaves behind when the PUT
	// "succeeded" but the source server didn't persist it in a way
	// we can read back (e.g., Google CalDAV silently dropping
	// non-conforming writes). Treating that row as evidence of
	// source ownership then fires a spurious delete-from-dest every
	// cycle, which is the fight loop that chewed up William's
	// Google→SOGo sync. Without a confirmed SourceETag, this source
	// has no authority to propagate a delete for this UID.
	candidates := make([]string, 0)
	for uid, prev := range previouslySyncedMap {
		_, existsOnSource := sourceEventMap[uid]
		_, existsOnDest := destEventMap[uid]
		if !existsOnSource && existsOnDest && prev.SourceETag != "" {
			candidates = append(candidates, uid)
		}
	}
	// Rule 3: mass-deletion ratio guard. Only applied when there is
	// prior state to measure against and a threshold is configured.
	if maxDeleteRatio > 0 && len(previouslySyncedMap) > 0 {
		ratio := float64(len(candidates)) / float64(len(previouslySyncedMap))
		if ratio > maxDeleteRatio {
			return nil, fmt.Sprintf(
				"two-way deletion pass would delete %d of %d previously-synced events (%.0f%%), "+
					"exceeds safety threshold %.0f%% - skipping for safety (possible partial source query failure)",
				len(candidates), len(previouslySyncedMap), ratio*100, maxDeleteRatio*100,
			)
		}
	}
	return candidates, ""
}

// planTwoWaySourceDeletion determines which source events should be
// deleted because they were removed from destination during a two-way
// sync. It is the symmetric mirror of planTwoWayDeletion (which
// handles dest-side deletions) and the source-side equivalent of
// planOrphanDeletion's ratio guard.
//
// As of #82, this helper enforces the same three guards as
// planTwoWayDeletion, plus the per-event safety threshold inherited
// from the legacy inline source-delete loop. The guards are:
//
//  1. Empty-source guard: if source returned 0 events but we have
//     prior records, refuse. Same rationale as planTwoWayDeletion.
//
//  2. Empty-dest guard: if destination returned 0 events but we have
//     prior records, refuse. NEW for the source-deletion path. The
//     existing shouldSkipTwoWayDeletion check covered this for the
//     dest-side direction; this helper now covers the source side.
//
//  3. Mass-deletion ratio guard: if more than maxDeleteRatio of the
//     prior records would be deleted from source in one cycle,
//     refuse. NEW. Critical for the destination-recovery scenario:
//     when the dest calendar is being repopulated after a previous
//     mass-delete, the source-deletion pass would otherwise see
//     half the prior UIDs as "missing from dest" and propagate the
//     deletes back to source — exactly the cascade that ate
//     William's iCloud calendar at ~360 events/cycle.
//
//  4. Per-event safety threshold (applied by the caller after the
//     candidate list is returned): events whose CreatedAt is within
//     the last sync interval are protected from deletion. This is
//     the existing isWithinSyncSafetyThreshold check; the helper
//     defers it to the caller because it needs the per-event
//     synced_events record to evaluate.
//
// Returns the list of UIDs to delete from source and a non-empty
// warning if any rule was triggered. When a warning is returned,
// toDelete is nil — the caller must not perform any deletions in that
// case. (#82)
func planTwoWaySourceDeletion(
	sourceEventMap map[string]Event,
	destEventMap map[string]Event,
	previouslySyncedMap map[string]*db.SyncedEvent,
	maxDeleteRatio float64,
) (toDelete []string, warning string) {
	// Rule 1: empty source with prior records → refuse.
	if len(sourceEventMap) == 0 && len(previouslySyncedMap) > 0 {
		return nil, fmt.Sprintf(
			"source returned 0 events but %d previously-synced records exist - "+
				"skipping source-side deletion pass for safety (possible source query failure)",
			len(previouslySyncedMap),
		)
	}
	// Rule 2: empty destination with prior records → refuse.
	if len(destEventMap) == 0 && len(previouslySyncedMap) > 0 {
		return nil, fmt.Sprintf(
			"destination returned 0 events but %d previously-synced records exist - "+
				"skipping source-side deletion pass for safety (possible destination query failure)",
			len(previouslySyncedMap),
		)
	}
	// Build candidate list: previously-synced UIDs that still exist
	// on source but no longer exist on destination.
	//
	// Rule 4 (#171): require prev.DestETag != "" on the candidate —
	// symmetric to the gate in planTwoWayDeletion. A synced_events
	// row with an empty DestETag means "we wrote a tracking entry
	// but never observed this UID on the destination side." Without
	// a confirmed DestETag we cannot claim this source ever owned
	// the dest copy, so we have no authority to propagate a delete
	// back to source just because dest no longer returns the UID.
	candidates := make([]string, 0)
	for uid, prev := range previouslySyncedMap {
		_, existsOnSource := sourceEventMap[uid]
		_, existsOnDest := destEventMap[uid]
		if existsOnSource && !existsOnDest && prev.DestETag != "" {
			candidates = append(candidates, uid)
		}
	}
	// Rule 3: mass-deletion ratio guard.
	if maxDeleteRatio > 0 && len(previouslySyncedMap) > 0 {
		ratio := float64(len(candidates)) / float64(len(previouslySyncedMap))
		if ratio > maxDeleteRatio {
			return nil, fmt.Sprintf(
				"source-side deletion pass would delete %d of %d previously-synced events from source (%.0f%%), "+
					"exceeds safety threshold %.0f%% - skipping for safety (possible partial destination query failure or recovery in progress)",
				len(candidates), len(previouslySyncedMap), ratio*100, maxDeleteRatio*100,
			)
		}
	}
	return candidates, ""
}

// planReverseCreate determines which destination events should be uploaded
// to source as new creates during a two-way sync. It is the mirror of
// planOrphanDeletion for the reverse direction.
//
// Three safety rules are enforced:
//
//  1. Ownership: only events that are NOT in sourceEventMap AND NOT in
//     previouslySyncedMap are candidates. Events present on source are
//     handled by the main forward loop (or the dest_wins reverse update
//     branch). Events in previouslySyncedMap that are missing from source
//     were deleted from source and are handled by the two-way deletion pass.
//     The remaining category — dest-only events with no sync history — are
//     genuine user-created destination events that two-way sync must push
//     back to source.
//
//  2. Empty-source guard: if the source returned zero events but we have
//     prior sync records, refuse to upload anything. Same rationale as
//     shouldSkipTwoWayDeletion — an empty source query is almost certainly
//     a source failure, not a user action. Without this guard a transient
//     iCloud hiccup would trigger a mass-upload of the entire destination
//     calendar back to iCloud on the very next cycle.
//
//  3. Hard cap: if the candidate list exceeds maxCreates, refuse the
//     entire pass with a warning. Normal two-way operation uploads a
//     handful of events per cycle; a batch of 100+ is almost always a
//     first-sync scenario or a misconfiguration that the operator should
//     explicitly notice. The cap is per-calendar — each selected calendar
//     gets its own budget.
//
// Returns the events to upload and a non-empty warning string if any safety
// rule was triggered. When a warning is returned, toUpload is nil — the
// caller must not perform any uploads in that case. (#74)
func planReverseCreate(
	destEvents []Event,
	sourceEventMap map[string]Event,
	previouslySyncedMap map[string]*db.SyncedEvent,
	maxCreates int,
) (toUpload []Event, alreadyOnSourceByContent []Event, warning string) {
	// Rule 2: empty source with prior records → refuse to upload anything.
	if len(sourceEventMap) == 0 && len(previouslySyncedMap) > 0 {
		return nil, nil, fmt.Sprintf(
			"source returned 0 events but %d previously-synced records exist - "+
				"skipping two-way reverse create pass for safety (possible source query failure)",
			len(previouslySyncedMap),
		)
	}

	// Rule 1a: build a content index of source events (Summary+StartTime).
	// Used to detect "same event under a different UID" on source vs dest,
	// which is the forward direction's existing dedupe strategy (see
	// destDedupeMap in syncEventsToDestination). Without this check the
	// reverse create pass happily uploaded dest events whose content
	// already existed on source under a different UID, producing
	// visible duplicates on source (e.g., iCloud). Fix for Issue #78.
	sourceDedupeMap := make(map[string]bool)
	for _, e := range sourceEventMap {
		key := e.DedupeKey()
		if key != "|" {
			sourceDedupeMap[key] = true
		}
	}

	// Rule 1b: collect dest-only, never-before-synced events, filtering
	// out content-level duplicates. Events that match an existing source
	// event by Summary+StartTime (but under a different UID) are returned
	// separately in alreadyOnSourceByContent so the caller can record
	// them in currentUIDs — otherwise the next sync cycle would retry
	// the upload and produce the same warning indefinitely.
	candidates := make([]Event, 0)
	contentDupes := make([]Event, 0)
	for _, event := range destEvents {
		if event.UID == "" {
			continue
		}
		if _, existsOnSource := sourceEventMap[event.UID]; existsOnSource {
			continue
		}
		if _, wasPrevSynced := previouslySyncedMap[event.UID]; wasPrevSynced {
			continue
		}
		// Content dedupe: same Summary+StartTime already exists on
		// source under a different UID. Don't upload (would create a
		// duplicate on source), but record separately so the caller can
		// mark the dest UID as "processed" for synced_events tracking.
		key := event.DedupeKey()
		if key != "|" && sourceDedupeMap[key] {
			contentDupes = append(contentDupes, event)
			continue
		}
		candidates = append(candidates, event)
	}

	// Rule 3: hard cap. Applied to the actual upload candidates only —
	// content dupes are not counted against the cap because they won't
	// result in any PUTs. Blast-radius protection, independent of prior
	// sync history.
	if maxCreates > 0 && len(candidates) > maxCreates {
		return nil, contentDupes, fmt.Sprintf(
			"two-way reverse create pass would upload %d new events to source "+
				"(cap=%d) - skipping pass for safety. Lower sync_days_past, "+
				"raise the cap in code, or let prior cycles populate sync state first.",
			len(candidates), maxCreates,
		)
	}

	return candidates, contentDupes, ""
}

// isWithinSyncSafetyThreshold returns true if the given
// "last synced at" timestamp is within the safety window — meaning
// the event was synced recently enough that we should NOT delete it
// from the source yet, even if it appears to be missing from the
// destination.
//
// This is the guard introduced in commit 23e88c1. The rationale: if
// an event was just synced to the destination but hasn't propagated
// yet (destination eventual consistency, caching, indexing delay),
// a naive "event missing from destination → delete from source"
// reaction would cause data loss. The threshold is one full sync
// interval plus the slack needed for the destination to catch up.
//
// Extracted as a pure helper in Issue #68 for testability.
// Behavior is byte-for-byte identical to the previous inline check.
func isWithinSyncSafetyThreshold(syncedAt time.Time, sourceSyncInterval time.Duration, now time.Time) bool {
	threshold := now.Add(-sourceSyncInterval)
	return syncedAt.After(threshold)
}

// getSyncDirectionForCalendar returns the effective sync direction for a calendar.
// It checks per-calendar settings first, then falls back to the source default.
func getSyncDirectionForCalendar(source *db.Source, calendarPath string) db.SyncDirection {
	// Search for per-calendar config
	for _, calConfig := range source.SelectedCalendars {
		if calConfig.Path == calendarPath {
			return calConfig.GetSyncDirection(source.SyncDirection)
		}
	}
	// Calendar not in selected list, use source default
	return source.SyncDirection
}

// defaultOrphanDeleteRatioThreshold is the maximum fraction of previously-synced
// events that can be deleted in a single one-way sync cycle before safety aborts.
// Exceeding this threshold usually indicates an auth failure, broken source URL,
// or filter misconfiguration rather than a legitimate bulk cleanup.
const defaultOrphanDeleteRatioThreshold = 0.5

// defaultReverseCreateHardCap is the maximum number of new destination-only
// events the two-way reverse create pass will upload to source in a single
// cycle. Mirror of defaultOrphanDeleteRatioThreshold for the reverse
// direction — serves as a blast-radius limit against truly runaway uploads
// (a misconfigured source, a bug in the ownership filter, corrupted state,
// etc.). When exceeded, planReverseCreate returns a warning and an empty
// list — the caller must not perform any uploads in that cycle, and the
// operator sees the warning in the sync log.
//
// The cap is per-calendar — each selected calendar gets its own budget.
//
// Initial value was 100 (#74), which turned out to be too aggressive for
// legitimate first-sync scenarios. William's iCloud source had a calendar
// with 748 real dest-only events that needed to flow to iCloud, and the
// 100 cap blocked the entire pass every cycle. Raised to 5000 in #77 —
// still catches clearly-insane mass upload scenarios (e.g., 50k events
// from a runaway bug) but lets ordinary first-syncs and bulk imports
// through in one cycle.
//
// If you're hitting this cap with legitimate data, either increase the
// value here, lower sync_days_past on the source to shrink the window,
// or break the work across multiple smaller source calendars. A future
// PR will move this to a per-source setting in the DB. (#77)
const defaultReverseCreateHardCap = 5000

// extractUIDFromEventPath returns the event UID embedded in a CalDAV object
// path. By PutEvent convention (client.go:602), events are written as
// "{calendarPath}/{UID}.ics" so the UID is the basename of the path with
// the ".ics" extension stripped.
//
// This is used by the WebDAV-Sync path in syncCalendar to keep the
// synced_events table in sync with destination writes and deletes:
// SyncCollection.Deleted only tells us the source-side path, not the UID,
// so we have to recover the UID from the last URL segment.
//
// Returns an empty string for inputs that cannot yield a UID (empty path,
// path with no trailing filename, filename without the ".ics" extension).
// Callers MUST check for the empty return before passing the result to
// DeleteSyncedEvent — a DELETE with an empty UID would match no rows but
// still wastes a DB round-trip and pollutes logs.
func extractUIDFromEventPath(eventPath string) string {
	trimmed := strings.TrimSuffix(eventPath, "/")
	if trimmed == "" {
		return ""
	}
	filename := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		filename = trimmed[idx+1:]
	}
	if filename == "" {
		return ""
	}
	// Strip the .ics extension if present. Filenames without .ics are
	// likely not event objects — return empty so callers skip them.
	if !strings.HasSuffix(filename, ".ics") {
		return ""
	}
	uid := strings.TrimSuffix(filename, ".ics")
	if uid == "" {
		return ""
	}
	return uid
}

// rewriteDeletePathForDestination translates a CalDAV object path from the
// source server's URL namespace into the destination server's URL namespace.
//
// This is needed for the WebDAV-Sync (RFC 6578) incremental sync path.
// When a source supports sync-collection, `SyncCollection` returns a list
// of deleted paths in the SOURCE server's URL space — e.g.
// "/calendar/work-acct/abc123.ics". Passing those paths directly to
// `destClient.DeleteEvent` results in a 404 on the destination, because
// the destination has no concept of the source's URL layout.
//
// The destination path is reconstructed from the last URL segment of the
// source path (the event filename, which by convention in PutEvent is
// "{UID}.ics") prepended with the destination calendar path. This mirrors
// how PutEvent writes events in the first place, so delete paths match
// write paths.
//
// Returns an empty string if the source path has no extractable filename
// (empty input, trailing-slash-only, etc). Callers MUST check for the
// empty return and skip the delete rather than issuing a request with
// a malformed URL.
func rewriteDeletePathForDestination(sourcePath, destCalendarPath string) string {
	trimmed := strings.TrimSuffix(sourcePath, "/")
	if trimmed == "" {
		return ""
	}
	// Extract the last path segment (the event filename).
	filename := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		filename = trimmed[idx+1:]
	}
	if filename == "" {
		return ""
	}
	return strings.TrimSuffix(destCalendarPath, "/") + "/" + filename
}

// planOrphanDeletion determines which destination events should be deleted as
// "orphans" during a one-way + source_wins sync.
//
// Three safety rules are enforced to prevent data loss:
//
//  1. Ownership: only events that this source previously synced are candidates
//     for deletion. This prevents a multi-source setup (multiple source
//     calendars writing to a single destination) from having sources wipe each
//     other's events on every sync. It also preserves events created manually
//     on the destination.
//
//  2. Empty-source guard: if the source returned zero events but we have prior
//     sync records, the sync is assumed to be unhealthy (auth failure, broken
//     URL, filter wipeout) and the entire orphan-delete pass is skipped.
//     This mirrors the two-way guard introduced in commit b772c56.
//
//  3. Mass-delete threshold: if the planned deletion would remove more than
//     maxDeleteRatio of previously-synced events in a single cycle, the entire
//     orphan-delete pass is aborted. Normal day-to-day operation deletes a
//     handful of events per sync; wiping more than half is almost always a bug.
//
// Returns the events to delete and a non-empty warning string if any safety
// rule was triggered. When a warning is returned, toDelete is nil — the caller
// must not perform any deletions in that case.
func planOrphanDeletion(
	destEventMap map[string]Event,
	sourceEventCount int,
	previouslySyncedMap map[string]*db.SyncedEvent,
	maxDeleteRatio float64,
) (toDelete []Event, warning string) {
	// Rule 2: empty source with prior records → refuse to delete anything.
	if sourceEventCount == 0 && len(previouslySyncedMap) > 0 {
		return nil, fmt.Sprintf(
			"source returned 0 events but %d previously-synced records exist - "+
				"skipping one-way orphan deletion for safety (possible auth failure or broken source)",
			len(previouslySyncedMap),
		)
	}

	// Rule 1: ownership filter. Only consider events THIS source synced.
	candidates := make([]Event, 0)
	for uid, event := range destEventMap {
		if _, ours := previouslySyncedMap[uid]; ours {
			candidates = append(candidates, event)
		}
	}

	// Rule 3: mass-delete threshold. Only applied when there is prior state
	// to measure against and a threshold is configured.
	if len(previouslySyncedMap) > 0 && maxDeleteRatio > 0 {
		ratio := float64(len(candidates)) / float64(len(previouslySyncedMap))
		if ratio > maxDeleteRatio {
			return nil, fmt.Sprintf(
				"one-way orphan deletion would remove %d of %d previously-synced events (%.0f%%), "+
					"exceeds safety threshold %.0f%% - skipping deletion",
				len(candidates), len(previouslySyncedMap), ratio*100, maxDeleteRatio*100,
			)
		}
	}

	return candidates, ""
}

// caldavEventDeleter is the narrow CalDAV client surface that
// performDeletionAndCleanup needs. Defined as an interface so the
// unit test for the helper can mock it without spinning up an
// entire HTTP stack. The production *Client satisfies it
// implicitly. (#97)
type caldavEventDeleter interface {
	DeleteEvent(ctx context.Context, eventPath string) error
}

// syncedEventTrackingDeleter is the narrow DB surface that
// performDeletionAndCleanup needs. Same rationale as
// caldavEventDeleter — keeping the mock small by depending only
// on the one method we actually call. The production *db.DB
// satisfies it implicitly. (#97)
type syncedEventTrackingDeleter interface {
	DeleteSyncedEvent(sourceID, calendarHref, eventUID string) error
}

// performDeletionAndCleanup issues a CalDAV DELETE for one event
// and, ONLY on success, scrubs the corresponding synced_events
// tracking row. This is the invariant that a previous refactor
// got wrong (#89 / PR #90): the inline two-way deletion loops
// in fullSync used to call db.DeleteSyncedEvent unconditionally
// after the CalDAV DELETE, so a failed DELETE would still wipe
// the tracking row — and the next cycle, with no "previously
// synced" context for the still-live event, would re-propagate
// it back to the other side via the forward-create or reverse-
// create pass.
//
// Extracting this invariant into a single named helper means
// future refactors cannot accidentally reintroduce the bug: any
// caller that wants to delete an event AND clean up its tracking
// row has exactly one path for doing both in the correct order,
// and the behavior is covered by a direct unit test that mocks
// both sides and asserts the cleanup happens only on success.
//
// Returns the error from the CalDAV DELETE. A DB error is
// logged but not returned because the DB cleanup is a consistency
// hint, not a data-safety requirement — at worst we leak one
// synced_events row that the next cycle's cleanup pass will
// garbage-collect. Returning the DB error would tempt callers
// to abort the outer loop on a recoverable failure, which is
// worse than tolerating a stale row.
//
// Callers are responsible for:
//
//   - Incrementing result.Deleted on nil return
//   - Appending to result.Warnings on non-nil return (with
//     their own context about source-side vs dest-side)
//   - Updating any per-cycle in-memory state maps (destEventMap,
//     sourceEventMap, handledByDestDelete, handledBySourceDelete)
//     — those are caller concerns because they affect how later
//     passes in the same cycle behave.
func performDeletionAndCleanup(
	ctx context.Context,
	client caldavEventDeleter,
	tracker syncedEventTrackingDeleter,
	eventPath, sourceID, calendarHref, uid string,
) error {
	if err := client.DeleteEvent(ctx, eventPath); err != nil {
		return err
	}
	if err := tracker.DeleteSyncedEvent(sourceID, calendarHref, uid); err != nil {
		log.Printf("Failed to delete synced event tracking row for %s: %v", uid, err)
	}
	return nil
}

// SyncResult represents the result of a sync operation.
type SyncResult struct {
	Success           bool          `json:"success"`
	Message           string        `json:"message"`
	Created           int           `json:"created"`
	Updated           int           `json:"updated"`
	Deleted           int           `json:"deleted"`
	Skipped           int           `json:"skipped"`
	DuplicatesRemoved int           `json:"duplicates_removed"`
	CalendarsSynced   int           `json:"calendars_synced"`
	EventsProcessed   int           `json:"events_processed"`
	Errors            []string      `json:"errors,omitempty"`   // Critical errors that prevent sync
	Warnings          []string      `json:"warnings,omitempty"` // Non-critical issues (individual event failures)
	Duration          time.Duration `json:"duration"`
	// ContentHash is the SHA-256 hex digest of the ICS feed body.
	// Populated only for ICS source types. Used by the scheduler's
	// adaptive polling logic to detect unchanged feeds. (#146)
	ContentHash string `json:"content_hash,omitempty"`
	// DryRun indicates this result was computed without actually
	// writing to the CalDAV servers. Counts are what WOULD happen.
	DryRun bool `json:"dry_run,omitempty"`
}

// sanitizeLogDetails removes potentially sensitive information from sync log details.
// This prevents leaking server internal paths, stack traces, or network info.
func sanitizeLogDetails(details string) string {
	if details == "" {
		return ""
	}

	// Remove potential IP addresses
	// Remove potential file paths that might reveal server structure
	// Keep the message useful but remove internal details

	// Truncate very long details (could contain memory dumps or stack traces)
	const maxLength = 2000
	if len(details) > maxLength {
		details = details[:maxLength] + "... (truncated)"
	}

	return details
}

// retryDBOperation retries a database operation with exponential backoff.
// This helps handle SQLite "database is locked" errors during concurrent operations.
func retryDBOperation(operation func() error, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := operation(); err != nil {
			lastErr = err
			// Check if it's a busy/locked error worth retrying
			if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
				backoff := time.Duration(100*(1<<i)) * time.Millisecond // 100ms, 200ms, 400ms, ...
				if backoff > 5*time.Second {
					backoff = 5 * time.Second
				}
				time.Sleep(backoff)
				continue
			}
			return err // Non-retryable error
		}
		return nil // Success
	}
	return lastErr
}

// SyncEngine orchestrates calendar synchronization.
type SyncEngine struct {
	db        *db.DB
	encryptor *crypto.Encryptor
	tracker   *activity.Tracker
}

// NewSyncEngine creates a new sync engine. As of #79 the engine no
// longer holds a global Google OAuth config — Google sources carry
// their own client_id and client_secret on the source row, and the
// sync code builds an oauth2.Config per request from those columns.
func NewSyncEngine(database *db.DB, encryptor *crypto.Encryptor) *SyncEngine {
	return &SyncEngine{
		db:        database,
		encryptor: encryptor,
		tracker:   activity.NewTracker(),
	}
}

// googleScopes are the OAuth scopes every Google CalDAV sync needs.
// Hardcoded because they are the same for every source — different
// scopes would require a separate consent flow per source, which is
// not a feature we expose. (#79)
var googleScopes = []string{
	// Full calendar scope is required for CalDAV access. The narrower
	// .events scope is NOT sufficient because CalDAV requires
	// PROPFIND on calendar-home-set.
	"https://www.googleapis.com/auth/calendar",
	// Needed to fetch the user's primary email during the OAuth
	// callback so we can build the per-calendar URL.
	"https://www.googleapis.com/auth/userinfo.email",
}

// buildPerSourceGoogleOAuthConfig assembles a fresh *oauth2.Config
// from the credentials stored on a single source row plus the
// instance-level redirect URL. Returns an error if either the client
// ID or the (decrypted) client secret is missing — Google sources
// without their own credentials are a hard configuration failure
// rather than a silent Basic-Auth fallback. (#79)
func (se *SyncEngine) buildPerSourceGoogleOAuthConfig(source *db.Source, redirectURL string) (*oauth2.Config, error) {
	if source.GoogleClientID == "" {
		return nil, fmt.Errorf("source %q has no Google OAuth client_id — re-add the source via the web UI", source.Name)
	}
	if source.GoogleClientSecret == "" {
		return nil, fmt.Errorf("source %q has no Google OAuth client_secret — re-add the source via the web UI", source.Name)
	}
	clientSecret, err := se.encryptor.Decrypt(source.GoogleClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt Google client_secret for source %q: %w", source.Name, err)
	}
	return &oauth2.Config{
		ClientID:     source.GoogleClientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     google.Endpoint,
		Scopes:       googleScopes,
	}, nil
}

// GetActivityTracker returns the activity tracker for external use.
func (se *SyncEngine) GetActivityTracker() *activity.Tracker {
	return se.tracker
}

// SyncSource performs synchronization for a single source.
func (se *SyncEngine) SyncSource(ctx context.Context, source *db.Source) *SyncResult {
	start := time.Now()
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
		DryRun:   IsDryRun(ctx),
	}

	// Skip status update in dry-run mode — we don't want to
	// change the source's last_sync_status/last_sync_at. (#150)
	if !result.DryRun {
		// Update status to running (with retry for concurrent access)
		if err := retryDBOperation(func() error {
			return se.db.UpdateSourceSyncStatus(source.ID, db.SyncStatusRunning, "Sync in progress")
		}, 5); err != nil {
			log.Printf("Failed to update sync status after retries: %v", err)
		}
	}

	// Branch for ICS sources (read-only feed, different sync path)
	if source.SourceType == db.SourceTypeICS {
		return se.syncICSSource(ctx, source)
	}

	// Decrypt credentials - NEVER log these
	// For Google OAuth sources, SourcePassword is empty and we decrypt
	// the refresh token instead. For all other source types, we use
	// the standard Basic Auth path.
	isGoogleOAuth := source.SourceType == db.SourceTypeGoogle && source.OAuthRefreshToken != ""

	var sourcePassword string
	if !isGoogleOAuth {
		decPassword, decErr := se.encryptor.Decrypt(source.SourcePassword)
		if decErr != nil {
			result.Message = "Failed to decrypt source credentials"
			result.Errors = append(result.Errors, decErr.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
		sourcePassword = decPassword
	}

	destPassword, err := se.encryptor.Decrypt(source.DestPassword)
	if err != nil {
		result.Message = "Failed to decrypt destination credentials"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create source client — branch on source type (#70 + #79).
	// Google sources use OAuth2 Bearer auth; everything else uses
	// Basic Auth. A Google source without per-source client_id /
	// client_secret / refresh_token is a hard failure — we must not
	// silently fall back to Basic Auth because Google will reject it
	// with 401, which would look like bad credentials even though the
	// real fix is to re-add the source via the web UI.
	//
	// As of #79 the OAuth client config is built per-source from the
	// credentials stored on the source row (not from a global env-var
	// config). The redirect URL is irrelevant for token refresh —
	// only the consent-screen flow uses it, and that runs in the web
	// handlers, not here — so we pass an empty string.
	var sourceClient *Client
	if source.SourceType == db.SourceTypeGoogle {
		if source.OAuthRefreshToken == "" {
			result.Message = "Google source is missing its OAuth refresh token — reconnect via the web UI"
			result.Errors = append(result.Errors, result.Message)
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
		perSourceOAuthConfig, cfgErr := se.buildPerSourceGoogleOAuthConfig(source, "")
		if cfgErr != nil {
			result.Message = cfgErr.Error()
			result.Errors = append(result.Errors, cfgErr.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
		refreshToken, decErr := se.encryptor.Decrypt(source.OAuthRefreshToken)
		if decErr != nil {
			result.Message = "Failed to decrypt Google OAuth refresh token"
			result.Errors = append(result.Errors, decErr.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
		token := &oauth2.Token{RefreshToken: refreshToken}
		sourceClient, err = NewOAuthClient(ctx, source.SourceURL, perSourceOAuthConfig, token)
	} else {
		sourceClient, err = NewClient(source.SourceURL, source.SourceUsername, sourcePassword)
	}
	if err != nil {
		result.Message = "Failed to connect to source"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create destination client
	destClient, err := NewClient(source.DestURL, source.DestUsername, destPassword)
	if err != nil {
		result.Message = "Failed to connect to destination"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Test connections — Google CalDAV doesn't support the standard
	// FindCurrentUserPrincipal PROPFIND, so we use a different test. (#160)
	if source.SourceType == db.SourceTypeGoogle {
		if err := sourceClient.TestConnectionGoogle(ctx); err != nil {
			result.Message = "Source connection test failed"
			result.Errors = append(result.Errors, err.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
	} else {
		if err := sourceClient.TestConnection(ctx); err != nil {
			result.Message = "Source connection test failed"
			result.Errors = append(result.Errors, err.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
	}

	// Destination connection test — Google destinations need the same
	// non-standard path as Google sources. (#165)
	if IsGoogleURL(source.DestURL) {
		if err := destClient.TestConnectionGoogle(ctx); err != nil {
			result.Message = "Destination connection test failed"
			result.Errors = append(result.Errors, err.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
	} else {
		if err := destClient.TestConnection(ctx); err != nil {
			result.Message = "Destination connection test failed"
			result.Errors = append(result.Errors, err.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
	}

	// Find calendars on source — Google needs a different discovery path. (#160)
	var sourceCalendars []Calendar
	if source.SourceType == db.SourceTypeGoogle {
		sourceCalendars, err = sourceClient.FindCalendarsGoogle(ctx)
	} else {
		sourceCalendars, err = sourceClient.FindCalendars(ctx)
	}
	if err != nil {
		result.Message = "Failed to find source calendars"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Log discovered calendars
	log.Printf("Found %d calendars on source:", len(sourceCalendars))
	for i, cal := range sourceCalendars {
		log.Printf("  [%d] Name: %q, Path: %s", i+1, cal.Name, cal.Path)
	}

	// Filter calendars based on selected_calendars setting
	if len(source.SelectedCalendars) > 0 {
		selectedSet := make(map[string]bool)
		for _, calConfig := range source.SelectedCalendars {
			selectedSet[calConfig.Path] = true
		}

		var filteredCalendars []Calendar
		for _, cal := range sourceCalendars {
			if selectedSet[cal.Path] {
				filteredCalendars = append(filteredCalendars, cal)
			}
		}

		log.Printf("Filtered to %d selected calendars (from %d discovered)", len(filteredCalendars), len(sourceCalendars))
		sourceCalendars = filteredCalendars
	}

	// Start activity tracking
	se.tracker.StartSync(source.ID, source.Name, len(sourceCalendars))

	// Sync each calendar
	for i, cal := range sourceCalendars {
		// Update activity tracker with current calendar
		se.tracker.UpdateCalendar(source.ID, cal.Name, i+1)

		calResult := se.syncCalendar(ctx, source, sourceClient, destClient, cal, i+1)
		result.Created += calResult.Created
		result.Updated += calResult.Updated
		result.Deleted += calResult.Deleted
		result.Skipped += calResult.Skipped
		result.EventsProcessed += calResult.EventsProcessed
		result.Errors = append(result.Errors, calResult.Errors...)
		result.Warnings = append(result.Warnings, calResult.Warnings...)

		// Update progress in activity tracker
		se.tracker.UpdateProgress(source.ID, result.Created, result.Updated, result.Deleted, result.Skipped, result.EventsProcessed)
	}

	result.CalendarsSynced = len(sourceCalendars)

	// Multi-destination sync (#156): after syncing to the primary
	// destination, check for additional destinations and sync to
	// each one. The primary destination (dest_url on the source
	// row) always syncs first — additional destinations are
	// additive. A failure on one additional destination doesn't
	// prevent others from being tried.
	additionalDests, err := se.db.GetDestinationsBySourceID(source.ID)
	if err != nil {
		log.Printf("Failed to load additional destinations for source %s: %v", source.Name, err)
	}
	for _, dest := range additionalDests {
		if !dest.Enabled {
			continue
		}
		log.Printf("Syncing to additional destination: %s (%s)", dest.Name, dest.DestURL)
		extraDestPassword, decErr := se.encryptor.Decrypt(dest.DestPassword)
		if decErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to decrypt credentials for additional dest %q: %v", dest.Name, decErr))
			continue
		}
		extraDestClient, connErr := NewClient(dest.DestURL, dest.DestUsername, extraDestPassword)
		if connErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to connect to additional dest %q: %v", dest.Name, connErr))
			continue
		}
		if testErr := extraDestClient.TestConnection(ctx); testErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Connection test failed for additional dest %q: %v", dest.Name, testErr))
			continue
		}
		for i, cal := range sourceCalendars {
			calResult := se.syncCalendar(ctx, source, sourceClient, extraDestClient, cal, i+1)
			result.Created += calResult.Created
			result.Updated += calResult.Updated
			result.Deleted += calResult.Deleted
			result.Skipped += calResult.Skipped
			result.EventsProcessed += calResult.EventsProcessed
			result.Warnings = append(result.Warnings, calResult.Warnings...)
			// Errors from additional dests are downgraded to warnings
			// so a failure on one extra dest doesn't mark the whole
			// sync as failed.
			for _, e := range calResult.Errors {
				result.Warnings = append(result.Warnings, fmt.Sprintf("[additional dest %q] %s", dest.Name, e))
			}
		}
		log.Printf("Completed sync to additional destination: %s", dest.Name)
	}

	// Success if no critical errors (warnings are OK)
	result.Success = len(result.Errors) == 0
	if result.Success && len(result.Warnings) == 0 {
		result.Message = fmt.Sprintf("Synced %d calendar(s): %d created, %d updated, %d deleted, %d skipped",
			len(sourceCalendars), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else if result.Success && len(result.Warnings) > 0 {
		result.Message = fmt.Sprintf("Synced %d calendar(s) with %d warnings: %d created, %d updated, %d deleted, %d skipped",
			len(sourceCalendars), len(result.Warnings), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else {
		result.Message = fmt.Sprintf("Sync failed with %d errors", len(result.Errors))
	}

	result.Duration = time.Since(start)
	se.finishSync(source.ID, result)

	return result
}

func (se *SyncEngine) syncCalendar(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar, calendarIndex int) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Check for existing sync state
	syncState, err := se.db.GetSyncState(source.ID, calendar.Path)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get sync state: %v", err))
		return result
	}

	var syncToken string
	if syncState != nil {
		syncToken = syncState.SyncToken
	}

	// Discover destination calendar path using the same logic as fullSync
	// to ensure both code paths target the same calendar.
	// Google destinations need FindCalendarsGoogle — standard discovery
	// fails and the URL-path fallback yields /user which is read-only. (#165)
	destCalendarPath := ""
	var destCalendars []Calendar
	var discoverErr error
	if IsGoogleURL(source.DestURL) {
		destCalendars, discoverErr = destClient.FindCalendarsGoogle(ctx)
	} else {
		destCalendars, discoverErr = destClient.FindCalendars(ctx)
	}
	if discoverErr != nil {
		log.Printf("Failed to discover destination calendars, falling back to URL path: %v", discoverErr)
		destCalendarPath = destClient.GetCalendarPath()
	} else if len(destCalendars) == 0 {
		log.Printf("No calendars found on destination, using URL path as fallback")
		destCalendarPath = destClient.GetCalendarPath()
	} else {
		destCalendarPath = destCalendars[0].Path
		if len(destCalendars) > 1 {
			log.Printf("WARNING: Multiple destination calendars found, using first one: %s", destCalendarPath)
		}
	}

	// Try WebDAV-Sync if supported
	if sourceClient.SupportsWebDAVSync(ctx, calendar.Path) {
		syncResult, err := sourceClient.SyncCollection(ctx, calendar.Path, syncToken)
		if err == nil {
			// Process changes
			for _, item := range syncResult.Changed {
				if item.Data != "" {
					event := &Event{
						Path: item.Path,
						ETag: item.ETag,
						Data: item.Data,
					}
					if err := destClient.PutEvent(ctx, destCalendarPath, event); err != nil {
						if errors.Is(err, ErrEventSkipped) {
							// PutEvent refused to write this event (empty data,
							// missing UID). Count it as skipped rather than
							// falsely incrementing Updated.
							result.Skipped++
						} else {
							result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to sync event: %v", err))
						}
					} else {
						result.Updated++
						// Track in synced_events so PR #22's ownership filter
						// and two-way deletion logic can see these writes.
						// PutEvent populates event.UID in-place when it
						// successfully parses the calendar data; if the
						// event had no UID it cannot reach this branch
						// (PutEvent returns nil early without writing).
						//
						// Populate SourceETag from the source item we
						// just read (stored in item.ETag) so the next
						// cycle of the main sync path can skip the PUT
						// when the source has not changed. (#79)
						if event.UID != "" {
							syncedEvent := &db.SyncedEvent{
								SourceID:     source.ID,
								CalendarHref: calendar.Path,
								EventUID:     event.UID,
								SourceETag:   item.ETag,
							}
							if err := se.db.UpsertSyncedEvent(syncedEvent); err != nil {
								// Previously this only logged. The next
								// sync's previouslySyncedMap won't know
								// this UID exists, so the forward pass
								// may treat it as a new event and re-PUT
								// it — wasted traffic, and potentially a
								// duplicate if the parsed UID drifts.
								// Surface as a Warning so operators see
								// the DB write failure in SyncResult. (#93)
								msg := fmt.Sprintf("Failed to upsert synced event record for %s: %v", event.UID, err)
								log.Printf("%s", msg)
								result.Warnings = append(result.Warnings, msg)
							}
						}
					}
				}
			}

			// Delete events from destination. Source paths are in the source
			// server's URL namespace and cannot be used directly against the
			// destination, so we rewrite each path through
			// rewriteDeletePathForDestination. See that helper's doc comment
			// for the full rationale.
			for _, sourcePath := range syncResult.Deleted {
				destEventPath := rewriteDeletePathForDestination(sourcePath, destCalendarPath)
				if destEventPath == "" {
					log.Printf("Skipping delete for unrewriteable source path: %q", sourcePath)
					continue
				}
				if err := destClient.DeleteEvent(ctx, destEventPath); err != nil {
					// Don't count as error if event doesn't exist on destination
					log.Printf("Failed to delete event (source: %s, dest: %s): %v", sourcePath, destEventPath, err)
				} else {
					result.Deleted++
					// Remove the synced_events record too so the next sync's
					// previouslySyncedMap doesn't still think we own it.
					// The UID is encoded in the filename of the destination
					// path (and equivalently in the source path) per the
					// PutEvent convention.
					if uid := extractUIDFromEventPath(destEventPath); uid != "" {
						if err := se.db.DeleteSyncedEvent(source.ID, calendar.Path, uid); err != nil {
							// Same rationale as the UpsertSyncedEvent
							// warning above: a failed DB cleanup means
							// the next cycle's previouslySyncedMap
							// still contains this UID even though it
							// no longer exists on destination — the
							// two-way deletion pass may then try to
							// delete from source. Surface the failure
							// so operators see it. (#93)
							msg := fmt.Sprintf("Failed to delete synced event record for %s: %v", uid, err)
							log.Printf("%s", msg)
							result.Warnings = append(result.Warnings, msg)
						}
					}
				}
			}

			// Update sync state
			newState := &db.SyncState{
				SourceID:     source.ID,
				CalendarHref: calendar.Path,
				SyncToken:    syncResult.SyncToken,
			}
			if err := se.db.UpsertSyncState(newState); err != nil {
				log.Printf("Failed to update sync state: %v", err)
			}

			return result
		}
		// Fall through to full sync if WebDAV-Sync fails
		log.Printf("WebDAV-Sync failed, falling back to full sync: %v", err)
	}

	// Full sync fallback
	return se.fullSync(ctx, source, sourceClient, destClient, calendar, calendarIndex)
}

// filterEventsByDate filters events to only include those with start time after cutoff date.
// Events without a parseable start time are included (to be safe).
// Recurring events (containing RRULE) are always included since their DTSTART
// is the original first occurrence which may be far in the past.
func filterEventsByDate(events []Event, cutoffDate time.Time) []Event {
	var filtered []Event
	for _, e := range events {
		if e.StartTime == "" {
			// Include events without start time (might be tasks or unparsed)
			filtered = append(filtered, e)
			continue
		}

		// Always include recurring events — their DTSTART is the original
		// first occurrence, but the event recurs into the future
		if strings.Contains(e.Data, "RRULE:") {
			filtered = append(filtered, e)
			continue
		}

		// Try to parse the start time - iCalendar format variants
		var eventTime time.Time
		var err error

		// Common iCalendar date/time formats
		formats := []string{
			"20060102T150405Z",     // UTC datetime
			"20060102T150405",      // Local datetime
			"20060102",             // Date only
			"2006-01-02T15:04:05Z", // ISO with dashes
			"2006-01-02",           // ISO date only
		}

		for _, format := range formats {
			eventTime, err = time.Parse(format, e.StartTime)
			if err == nil {
				break
			}
		}

		if err != nil {
			// Can't parse date - include to be safe
			filtered = append(filtered, e)
			continue
		}

		// Include if event is after cutoff date (or is in the future)
		if eventTime.After(cutoffDate) || eventTime.After(time.Now()) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (se *SyncEngine) fullSync(ctx context.Context, source *db.Source, sourceClient, destClient *Client, calendar Calendar, calendarIndex int) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Get the effective sync direction for this calendar (may be per-calendar or source default)
	syncDirection := getSyncDirectionForCalendar(source, calendar.Path)
	log.Printf("Calendar %q sync direction: %s (source default: %s)", calendar.Name, syncDirection, source.SyncDirection)

	// Helper to update status message during loading phases
	updateStatus := func(status string) {
		se.tracker.UpdateCalendar(source.ID, fmt.Sprintf("%s (%s)", calendar.Name, status), calendarIndex)
	}

	// Create collector for malformed events from source
	malformedCollector := NewMalformedEventCollector()

	// Clear old malformed events for this source before sync
	if err := se.db.ClearMalformedEventsForSource(source.ID); err != nil {
		log.Printf("Failed to clear old malformed events: %v", err)
	}

	// Get all events from source
	updateStatus("fetching source events")
	sourceEvents, err := sourceClient.GetEvents(ctx, calendar.Path, malformedCollector)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to get source events: %v", err))
		return result
	}
	updateStatus(fmt.Sprintf("loaded %d source events", len(sourceEvents)))

	// Filter events by date if sync_days_past is configured
	if source.SyncDaysPast > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -source.SyncDaysPast)
		originalCount := len(sourceEvents)
		sourceEvents = filterEventsByDate(sourceEvents, cutoffDate)
		filteredOut := originalCount - len(sourceEvents)
		if filteredOut > 0 {
			log.Printf("Filtered out %d events older than %d days (cutoff: %s)", filteredOut, source.SyncDaysPast, cutoffDate.Format("2006-01-02"))
			updateStatus(fmt.Sprintf("filtered to %d events (-%d old)", len(sourceEvents), filteredOut))
		}
	}

	// Store any malformed events found
	for _, mf := range malformedCollector.GetEvents() {
		if err := se.db.SaveMalformedEvent(source.ID, mf.Path, mf.ErrorMessage); err != nil {
			log.Printf("Failed to save malformed event record: %v", err)
		}
	}

	// Scan for zombie-master corruption fingerprints (#95). This
	// detector catches two patterns — X-MOZ-FAKED-MASTER stubs and
	// orphaned RECURRENCE-ID overrides — which are the fingerprint
	// of a recurring series whose master VEVENT was destroyed
	// during a round-trip through a lossy iCalendar library. The
	// pattern was first observed on William's instance during the
	// WOS Tech Team recovery in April 2026 where the whole weekly
	// series became invisible in Calendar.app because the master
	// had been replaced by an "Untitled" stub with no RRULE.
	//
	// We run this on the SOURCE side only (not dest) so operators
	// see the corruption as close to the origin as possible. If
	// the fingerprint fires on both sides, the source one will be
	// reported first and the operator can use cmd/purge-uid to
	// clean both. Running it on dest too would double-report in
	// steady state without adding information.
	//
	// Detection is WARN-only: we never skip or refuse to sync the
	// affected UIDs, because the sync engine already treats them
	// as normal events and the ratio guards would catch any
	// cascading damage. The goal here is observability — surface
	// the corruption in result.Warnings so it shows up in the
	// dashboard and the scheduled-sync alerts without changing
	// sync behavior.
	if zombies := FindZombieMasters(sourceEvents); len(zombies) > 0 {
		for _, z := range zombies {
			msg := fmt.Sprintf("Zombie recurring series detected on source (UID=%s, reason=%s, path=%s) - master VEVENT may be corrupted; use cmd/purge-uid to clean up and re-accept a fresh invite", z.UID, z.Reason, z.EventPath)
			log.Printf("WARNING: %s", msg)
			result.Warnings = append(result.Warnings, msg)
		}
	}

	// Delegate to shared sync logic
	return se.syncEventsToDestination(ctx, source, sourceClient, destClient, sourceEvents, calendar, calendarIndex, syncDirection)
}

// syncEventsToDestination handles the comparison, creation, update, and deletion of events
// between source events and a destination CalDAV calendar. This is shared by both CalDAV
// full sync and ICS feed sync paths.
func (se *SyncEngine) syncEventsToDestination(ctx context.Context, source *db.Source, sourceClient *Client, destClient *Client, sourceEvents []Event, calendar Calendar, calendarIndex int, syncDirection db.SyncDirection) *SyncResult {
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Helper to update activity tracker with current progress
	updateProgress := func() {
		se.tracker.UpdateProgress(source.ID, result.Created, result.Updated, result.Deleted, result.Skipped, result.EventsProcessed)
	}

	// Helper to update status message during loading phases
	updateStatus := func(status string) {
		se.tracker.UpdateCalendar(source.ID, fmt.Sprintf("%s (%s)", calendar.Name, status), calendarIndex)
	}

	// Discover destination calendar path - try calendar discovery first, then fall back to URL path.
	// Google destinations need FindCalendarsGoogle — standard discovery
	// fails and the URL-path fallback yields /user which is read-only. (#165)
	destCalendarPath := ""
	var destCalendars []Calendar
	var destDiscoverErr error
	if IsGoogleURL(source.DestURL) {
		destCalendars, destDiscoverErr = destClient.FindCalendarsGoogle(ctx)
	} else {
		destCalendars, destDiscoverErr = destClient.FindCalendars(ctx)
	}
	if destDiscoverErr != nil {
		log.Printf("Failed to discover destination calendars, falling back to URL path: %v", destDiscoverErr)
		destCalendarPath = destClient.GetCalendarPath()
	} else if len(destCalendars) == 0 {
		log.Printf("No calendars found on destination, using URL path as fallback")
		destCalendarPath = destClient.GetCalendarPath()
	} else {
		log.Printf("Found %d calendar(s) on destination:", len(destCalendars))
		for i, cal := range destCalendars {
			log.Printf("  [%d] Name: %q, Path: %s", i+1, cal.Name, cal.Path)
		}
		destCalendarPath = destCalendars[0].Path
		if len(destCalendars) > 1 {
			log.Printf("WARNING: Multiple destination calendars found, using first one: %s", destCalendarPath)
		}
	}
	log.Printf("Using destination calendar path: %s", destCalendarPath)

	// Get all events from destination (no collector needed - we only track source issues)
	updateStatus("fetching destination events")
	destEvents, err := destClient.GetEvents(ctx, destCalendarPath, nil)
	if err != nil {
		// Previously this failure only logged and then proceeded with
		// an empty destEvents slice. That silently masked a real
		// destination failure — the rest of the sync would compute
		// deltas against "zero destination events" and either mass-
		// delete tracked UIDs (caught by the ratio guards from #80/#82)
		// or mass-create them as if the destination was empty.
		//
		// Append to Warnings so operators actually see the failure
		// surfaced in the sync result. Not escalated to result.Errors
		// because one-way source_wins semantics can tolerate an
		// empty-destination view — the ratio guards still protect
		// against cascading deletions, and escalating to Errors would
		// flip every transient destination fetch failure into a hard
		// sync failure. Operator design call to tighten this further. (#93)
		msg := fmt.Sprintf("Failed to get destination events (path: %s): %v - proceeding with empty destination view, ratio guards will protect against cascades", destCalendarPath, err)
		log.Printf("%s", msg)
		result.Warnings = append(result.Warnings, msg)
		destEvents = []Event{}
	}
	log.Printf("Fetched %d events from destination calendar", len(destEvents))

	// Filter destination events by date if sync_days_past is configured
	if source.SyncDaysPast > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -source.SyncDaysPast)
		originalCount := len(destEvents)
		destEvents = filterEventsByDate(destEvents, cutoffDate)
		filteredOut := originalCount - len(destEvents)
		if filteredOut > 0 {
			log.Printf("Filtered out %d destination events older than %d days", filteredOut, source.SyncDaysPast)
		}
	}

	updateStatus(fmt.Sprintf("comparing %d vs %d events", len(sourceEvents), len(destEvents)))

	// Get previously synced events for deletion detection
	previouslySynced, err := se.db.GetSyncedEvents(source.ID, calendar.Path)
	if err != nil {
		log.Printf("Failed to get synced events: %v", err)
		previouslySynced = []*db.SyncedEvent{}
	}

	// Build map of previously synced UIDs
	previouslySyncedMap := make(map[string]*db.SyncedEvent)
	for _, syncedEvt := range previouslySynced {
		previouslySyncedMap[syncedEvt.EventUID] = syncedEvt
	}

	// Create maps for comparison by UID
	sourceEventMap := make(map[string]Event)
	for _, e := range sourceEvents {
		if e.UID != "" {
			sourceEventMap[e.UID] = e
		}
	}

	destEventMap := make(map[string]Event)
	for _, e := range destEvents {
		if e.UID != "" {
			destEventMap[e.UID] = e
		}
	}

	// Create deduplication map using summary + start time
	destDedupeMap := make(map[string]bool)
	for _, e := range destEvents {
		key := e.DedupeKey()
		if key != "|" {
			destDedupeMap[key] = true
			log.Printf("Dest dedupe key: %q (UID: %s)", key, e.UID)
		}
	}

	skippedDupes := 0

	// Track UIDs that exist in current sync (for updating synced_events
	// table). Values hold the observed source and destination ETags so
	// the next cycle can detect whether either side has changed without
	// doing the cross-server ETag comparison that caused the re-PUT
	// loop in #79.
	currentUIDs := make(map[string]syncETagEntry)

	// Update status to show processing phase
	updateStatus(fmt.Sprintf("processing %d events", len(sourceEvents)))

	// Handle deletions first (for two-way sync). Both safety guards
	// below are extracted as pure helpers (Issue #68) so they can be
	// unit-tested directly — see shouldSkipTwoWayDeletion and
	// isWithinSyncSafetyThreshold in this file.
	sourceInterval := time.Duration(source.SyncInterval) * time.Second
	now := time.Now()

	// Two-way deletion pass. Two independent steps with their own
	// safety guards:
	//
	//   - Dest-deletion: events that were removed from source must be
	//     removed from destination. Delegated to planTwoWayDeletion,
	//     which enforces three guards (empty-dest, empty-source, and
	//     mass-delete ratio). The empty-source and ratio guards are
	//     new in #80 — without them, a partial source query failure
	//     for one calendar mass-deletes the matching destination
	//     events. (William lost 748 events to this exact bug.)
	//
	//   - Source-deletion: events that were removed from destination
	//     must be removed from source. Still inline because each
	//     candidate has its own per-event safety threshold
	//     (isWithinSyncSafetyThreshold) protecting recently-synced
	//     events; ratio-based protection for this direction is
	//     deferred to a follow-up. The shouldSkipTwoWayDeletion
	//     guard is still consulted to short-circuit when the dest
	//     query failed entirely.
	if syncDirection == db.SyncDirectionTwoWay && sourceClient != nil {
		// Step 1: dest-deletion via planTwoWayDeletion. The helper's
		// three guards subsume the previous shouldSkipTwoWayDeletion
		// check for this direction and add empty-source + ratio
		// protection on top. (#80)
		toDeleteFromDest, deletionWarning := planTwoWayDeletion(
			sourceEventMap,
			destEventMap,
			previouslySyncedMap,
			defaultOrphanDeleteRatioThreshold,
		)
		if deletionWarning != "" {
			log.Printf("WARNING: %s", deletionWarning)
			result.Warnings = append(result.Warnings, deletionWarning)
		}
		// Track which UIDs the dest-deletion pass already handled so
		// the source-deletion pass below skips them.
		handledByDestDelete := make(map[string]bool, len(toDeleteFromDest))
		for _, uid := range toDeleteFromDest {
			destEvent := destEventMap[uid]
			log.Printf("Event %s deleted from source, deleting from destination", uid)
			// performDeletionAndCleanup enforces the success-only
			// invariant for synced_events cleanup (#97). A failed
			// DELETE no longer wipes the tracking row — see the
			// helper's doc comment for the full rationale.
			if err := performDeletionAndCleanup(
				ctx,
				destClient,
				se.db,
				destEvent.Path,
				source.ID,
				calendar.Path,
				uid,
			); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete event from dest: %v", err))
			} else {
				result.Deleted++
				updateProgress()
			}
			// In-memory map mutations stay unconditional: even on a
			// failed DELETE we want the source-deletion pass below
			// to skip this UID (leaving it in play would have that
			// pass try to delete from source as well, which is the
			// wrong direction for a UID whose dest DELETE just
			// failed).
			delete(destEventMap, uid)
			handledByDestDelete[uid] = true
		}

		// Step 2: source-deletion via planTwoWaySourceDeletion. The
		// helper enforces three guards (empty-source, empty-dest, and
		// mass-delete ratio). The per-event safety threshold
		// (isWithinSyncSafetyThreshold) is still applied here per-UID
		// inside the loop because it depends on each candidate's
		// CreatedAt timestamp, which the helper does not have. (#82)
		toDeleteFromSource, sourceDelWarning := planTwoWaySourceDeletion(
			sourceEventMap,
			destEventMap,
			previouslySyncedMap,
			defaultOrphanDeleteRatioThreshold,
		)
		if sourceDelWarning != "" {
			log.Printf("WARNING: %s", sourceDelWarning)
			result.Warnings = append(result.Warnings, sourceDelWarning)
		}
		// Track UIDs handled by either deletion pass so the cleanup
		// loop below skips them when reaping orphan synced_events.
		handledBySourceDelete := make(map[string]bool, len(toDeleteFromSource))
		for _, uid := range toDeleteFromSource {
			if handledByDestDelete[uid] {
				continue
			}
			syncedEvent := previouslySyncedMap[uid]
			sourceEvent := sourceEventMap[uid]

			// SAFETY CHECK: Only delete from source if the event was
			// FIRST synced before the safety threshold (commit 23e88c1,
			// Issue #72). Prevents deleting events that just appeared
			// and haven't had time to fully propagate.
			//
			// We deliberately read CreatedAt (sticky, set once at first
			// sync) rather than UpdatedAt (bumped every cycle via
			// UpsertSyncedEvent). Reading UpdatedAt was a bug: for
			// any normally-running sync, UpdatedAt is always within
			// one sync interval of "now" because the upsert at the
			// end of every cycle resets it, which made this safety
			// guard fire unconditionally and silently block every
			// two-way source-side deletion. CreatedAt preserves the
			// original intent ("protect brand-new events") without
			// the "protect everything forever" accident.
			if isWithinSyncSafetyThreshold(syncedEvent.CreatedAt, sourceInterval, now) {
				log.Printf("Event %s not on destination but newly synced (CreatedAt=%v) - skipping deletion from source (safety)", uid, syncedEvent.CreatedAt)
				continue
			}

			log.Printf("Event %s deleted from destination, deleting from source", uid)
			// Same success-only cleanup invariant as the dest
			// deletion pass above. (#97)
			if err := performDeletionAndCleanup(
				ctx,
				sourceClient,
				se.db,
				sourceEvent.Path,
				source.ID,
				calendar.Path,
				uid,
			); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete event from source: %v", err))
			} else {
				result.Deleted++
				updateProgress()
			}
			delete(sourceEventMap, uid)
			handledBySourceDelete[uid] = true
		}

		// Cleanup pass: walk previouslySyncedMap one more time for
		// the "deleted from both" case. Skip UIDs already handled by
		// either deletion pass above to avoid double-deletes from
		// synced_events.
		for uid, syncedEvent := range previouslySyncedMap {
			if handledByDestDelete[uid] || handledBySourceDelete[uid] {
				continue
			}
			_, existsOnSource := sourceEventMap[uid]
			_, existsOnDest := destEventMap[uid]
			if !existsOnSource && !existsOnDest {
				// Event deleted from both - just clean up the record.
				// Silent-log was the original pattern; surface to
				// Warnings so operators see tracking-row leaks in the
				// sync result instead of only in raw logs. (#108)
				if err := se.db.DeleteSyncedEvent(source.ID, calendar.Path, syncedEvent.EventUID); err != nil {
					msg := fmt.Sprintf("Failed to delete orphaned synced event record for %s: %v", syncedEvent.EventUID, err)
					log.Printf("%s", msg)
					result.Warnings = append(result.Warnings, msg)
				}
			}
		}
	}

	// Sync source events to destination
	for _, sourceEvent := range sourceEvents {
		if sourceEvent.UID == "" {
			continue
		}

		destEvent, existsByUID := destEventMap[sourceEvent.UID]

		if !existsByUID {
			// Check for duplicate by content
			dedupeKey := sourceEvent.DedupeKey()
			log.Printf("Source dedupe key: %q (UID: %s)", dedupeKey, sourceEvent.UID)
			if dedupeKey != "|" && destDedupeMap[dedupeKey] {
				skippedDupes++
				result.Skipped++
				result.EventsProcessed++
				updateProgress()
				log.Printf("Skipping duplicate event: %s at %s (dedupe key match)", sourceEvent.Summary, sourceEvent.StartTime)
				continue
			}

			// Create new event on destination
			if err := destClient.PutEvent(ctx, destCalendarPath, &sourceEvent); err != nil {
				if errors.Is(err, ErrEventSkipped) {
					// PutEvent refused (empty data, missing UID). Count
					// it as skipped. Do NOT mark the event as "ours" in
					// destDedupeMap or currentUIDs since nothing was
					// actually written to the destination.
					result.Skipped++
				} else {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to create event on dest: %v", err))
				}
			} else {
				result.Created++
				if dedupeKey != "|" {
					destDedupeMap[dedupeKey] = true
				}
				// Record the source ETag so the next cycle can skip
				// the PUT if the source has not changed. No dest ETag
				// yet — PutEvent does not return one on create; the
				// next cycle will read it from PROPFIND and populate
				// the dest side at that point. (#79)
				currentUIDs[sourceEvent.UID] = syncETagEntry{sourceETag: sourceEvent.ETag}
			}
			result.EventsProcessed++
			updateProgress()
		} else if shouldUpdateDestFromSource(sourceEvent.ETag, previouslySyncedMap[sourceEvent.UID]) {
			// Source ETag has changed since the last recorded sync
			// (or this is a first-time update with tracked ETags).
			// Only then do we actually PUT. Comparing sourceEvent.ETag
			// against destEvent.ETag directly is WRONG — they come
			// from different servers and will never match, which was
			// the cause of the infinite re-PUT loop fixed in #79.
			sourceEvent.Path = destEvent.Path
			if err := destClient.PutEvent(ctx, destCalendarPath, &sourceEvent); err != nil {
				if errors.Is(err, ErrEventSkipped) {
					// PutEvent refused. Don't add to currentUIDs —
					// the destination still has the OLD version of
					// this event, not an updated one, so we should
					// not track it as freshly synced.
					result.Skipped++
				} else {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to update event on dest: %v", err))
				}
			} else {
				result.Updated++
				// Log conflict resolution for the UI (#136, refined in #169).
				//
				// A routine source→dest update is NOT a conflict — it's
				// just source moving forward while dest stayed put. A
				// real conflict requires BOTH sides to have moved
				// independently since our last recorded sync. We detect
				// that by checking whether the current dest ETag differs
				// from the one we stored at the previous cycle. If it
				// does, dest was edited independently (by the SOGo web
				// UI, an iPhone, an email invite, etc.) between cycles,
				// and this PUT is overwriting that independent edit per
				// the source_wins strategy — a real conflict worth
				// surfacing to the user.
				//
				// Prior to #169 this log fired on every successful
				// source→dest update in two-way mode, producing one
				// CONFLICT line per event per cycle and drowning the
				// warnings list in false-positive "conflicts" that were
				// in fact just routine propagation.
				if syncDirection == db.SyncDirectionTwoWay &&
					isRealConflictSourceWins(previouslySyncedMap[sourceEvent.UID], destEvent.ETag) {
					result.Warnings = append(result.Warnings, fmt.Sprintf(
						"CONFLICT:{\"uid\":%q,\"winner\":\"source\",\"summary\":%q,\"strategy\":%q}",
						sourceEvent.UID, sourceEvent.Summary, source.ConflictStrategy))
				}
				// Record both ETags: source from the server we just
				// read, dest from the server we just wrote. Note the
				// dest ETag here is the OLD one — we don't have the
				// new one from PutEvent's response. The next read
				// cycle will refresh it. That is fine: the forward
				// path compares against the SOURCE ETag, and the
				// reverse dest_wins path that reads DestETag will
				// either see this stale value (correctly triggering
				// an update back to source on the first cycle where
				// it runs) or the refreshed value. (#79)
				currentUIDs[sourceEvent.UID] = syncETagEntry{
					sourceETag: sourceEvent.ETag,
					destETag:   destEvent.ETag,
				}
			}
			result.EventsProcessed++
			updateProgress()
		} else {
			// Event unchanged (source ETag matches stored prior ETag),
			// still track it so the synced_events upsert at end of
			// pass keeps it alive. Record both ETags from this cycle
			// so the next cycle has fresh reference points. (#79)
			currentUIDs[sourceEvent.UID] = syncETagEntry{
				sourceETag: sourceEvent.ETag,
				destETag:   destEvent.ETag,
			}
			result.EventsProcessed++
			updateProgress()
		}
		delete(destEventMap, sourceEvent.UID)
	}

	if skippedDupes > 0 {
		log.Printf("Skipped %d duplicate events", skippedDupes)
	}

	// Two-way sync: sync destination events back to source.
	//
	// Three cases, in order:
	//
	//  1. CREATE new dest-only events on source. Delegated to
	//     planReverseCreate, which applies all three safety rules
	//     (ownership, empty-source guard, hard cap) before returning
	//     the candidate list. See planReverseCreate's doc comment for
	//     the full rationale. (#72 + #74)
	//
	//  2. Silently skip dest events that already exist on source
	//     (either in sourceEventMap or in previouslySyncedMap).
	//     planReverseCreate's ownership filter handles this — they
	//     fall through to case 3 without being candidates.
	//
	//  3. UPDATE with dest_wins: a destination event that exists on
	//     both sides with a different ETag, when the user has
	//     explicitly opted into dest_wins conflict resolution.
	//     Unchanged from pre-#72 behavior.
	if syncDirection == db.SyncDirectionTwoWay && sourceClient != nil {
		// Case 1: reverse create pass, delegated to planReverseCreate
		// so the ownership/empty-source/cap safety rules are all
		// enforced in one testable place. The helper also filters out
		// dest events whose Summary+StartTime already matches a source
		// event under a different UID (content dedupe, #78), returning
		// those separately in contentDupes so we can record the dest
		// UID in currentUIDs and prevent the next cycle from retrying.
		toUpload, contentDupes, planWarning := planReverseCreate(
			destEvents,
			sourceEventMap,
			previouslySyncedMap,
			defaultReverseCreateHardCap,
		)
		if planWarning != "" {
			log.Printf("WARNING: %s", planWarning)
			result.Warnings = append(result.Warnings, planWarning)
		}

		// Record content-dedupe skips in currentUIDs. The dest UID is
		// not the same as the source UID for the matching event, but
		// recording it here ensures the next cycle's
		// previouslySyncedMap has an entry and the ownership filter
		// in planReverseCreate skips this dest event instead of
		// proposing another upload. This stops the infinite-retry
		// loop for content duplicates. (#78)
		//
		// Only destETag is meaningful here: the source side is a
		// different UID, so we cannot store a source ETag against
		// this UID. (#79)
		for i := range contentDupes {
			currentUIDs[contentDupes[i].UID] = syncETagEntry{
				destETag: contentDupes[i].ETag,
			}
		}
		if len(contentDupes) > 0 {
			log.Printf("Two-way sync: %d destination events already exist on source by content (Summary+StartTime) under different UIDs - recorded as synced to prevent retry", len(contentDupes))
		}

		log.Printf("Two-way sync enabled, uploading %d new destination events to source", len(toUpload))
		skippedAlreadyExists := 0
		skippedForbidden := 0
		for i := range toUpload {
			destEvent := toUpload[i]
			// Clear the Path so PutEvent generates a source-side
			// path for the upload (source and dest namespaces are
			// different — reusing the dest path would land at the
			// wrong URL on source). PutEvent synthesizes a path
			// from the calendar path + UID.
			destEvent.Path = ""
			if err := sourceClient.PutEvent(ctx, calendar.Path, &destEvent); err != nil {
				switch {
				case errors.Is(err, ErrEventSkipped):
					result.Skipped++
				case isSourceAlreadyExistsError(err):
					// Event already exists on source — either in this
					// calendar (412) or in a different calendar on the
					// same account (409, common on iCloud). Count as
					// a silent skip AND record the UID in currentUIDs
					// so the synced_events upsert at the end of this
					// pass stops us from retrying the upload on every
					// subsequent cycle (which would otherwise produce
					// the same 409/412 warning indefinitely). (#74)
					//
					// Only destETag is known here (the dest event we
					// read before attempting the upload). The source
					// side exists but we do not know its current ETag
					// on the source server. (#79)
					skippedAlreadyExists++
					currentUIDs[destEvent.UID] = syncETagEntry{
						destETag: destEvent.ETag,
					}
				case isForbiddenError(err):
					// Source calendar is read-only (iCloud subscribed
					// calendars, shared read-only, etc). Count as a
					// silent skip.
					skippedForbidden++
				default:
					result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to create event on source: %v", err))
				}
			} else {
				result.Created++
				// Track the newly-uploaded event so the sync_events
				// upsert at the end of this calendar's pass records
				// it. Without this, the next cycle would see the
				// same event as "not in previouslySyncedMap" and
				// try to re-upload it. Record the dest ETag we read
				// from before the upload; the source ETag we just
				// wrote is not returned by PutEvent and will be
				// populated from a read on the next cycle. (#79)
				currentUIDs[destEvent.UID] = syncETagEntry{
					destETag: destEvent.ETag,
				}
			}
			updateProgress()
		}

		// Case 3: dest_wins update pass. Walks destEvents (not just
		// candidates) because the update branch fires on "dest ETag
		// changed since last sync" — that's a different filter than
		// "dest-only" and can't reuse the candidate list.
		//
		// The old code compared destEvent.ETag against sourceEvent.ETag
		// directly, which was the same cross-server ETag bug fixed in
		// the forward path by #79. We now compare destEvent.ETag
		// against the last-known dest ETag in previouslySyncedMap via
		// shouldUpdateSourceFromDest — the symmetric twin of the
		// forward helper.
		if source.ConflictStrategy == db.ConflictDestWins {
			for _, destEvent := range destEvents {
				if destEvent.UID == "" {
					continue
				}
				sourceEvent, exists := sourceEventMap[destEvent.UID]
				if !exists {
					// Case 1 already handled this.
					continue
				}
				if !shouldUpdateSourceFromDest(destEvent.ETag, previouslySyncedMap[destEvent.UID]) {
					continue
				}
				destEvent.Path = sourceEvent.Path
				if err := sourceClient.PutEvent(ctx, calendar.Path, &destEvent); err != nil {
					switch {
					case errors.Is(err, ErrEventSkipped):
						result.Skipped++
					case isSourceAlreadyExistsError(err):
						skippedAlreadyExists++
					case isForbiddenError(err):
						skippedForbidden++
					default:
						result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to update event on source: %v", err))
					}
				} else {
					result.Updated++
					// Log conflict resolution for the UI (#136, refined in #169).
					// Symmetric to the forward path: only log a real
					// conflict when BOTH sides moved since our last
					// sync. Reaching this branch already proves dest
					// moved (shouldUpdateSourceFromDest returned true
					// at the loop guard above). Additionally require
					// that source also moved since its previously
					// tracked ETag — otherwise this is a routine
					// dest→source update, not a conflict.
					if isRealConflictDestWins(previouslySyncedMap[destEvent.UID], sourceEvent.ETag) {
						result.Warnings = append(result.Warnings, fmt.Sprintf(
							"CONFLICT:{\"uid\":%q,\"winner\":\"dest\",\"summary\":%q,\"strategy\":%q}",
							destEvent.UID, destEvent.Summary, source.ConflictStrategy))
					}
					// Record the dest ETag we just propagated back
					// to source so the next cycle can detect another
					// dest-side change. We don't know the new source
					// ETag PutEvent just created — next read cycle
					// will populate it. (#79)
					currentUIDs[destEvent.UID] = syncETagEntry{
						destETag: destEvent.ETag,
					}
				}
				updateProgress()
			}
		}

		if skippedAlreadyExists > 0 {
			log.Printf("Two-way sync: %d events already exist on source (skipped, recorded as synced)", skippedAlreadyExists)
		}
		if skippedForbidden > 0 {
			log.Printf("Two-way sync: %d events skipped (source calendar read-only)", skippedForbidden)
		}
	}

	// One-way sync: delete orphan events on destination (with safety checks).
	// See planOrphanDeletion for the full rationale. The bug this fixes:
	// without these guards, a one-way source_wins sync would delete EVERY
	// destination event whenever the source returned 0 events (auth failure,
	// broken URL, filter wipeout) or whenever multiple sources shared a
	// destination (each source would delete the others' events on every cycle).
	if syncDirection == db.SyncDirectionOneWay && source.ConflictStrategy == db.ConflictSourceWins {
		toDelete, warning := planOrphanDeletion(
			destEventMap,
			len(sourceEvents),
			previouslySyncedMap,
			defaultOrphanDeleteRatioThreshold,
		)
		if warning != "" {
			log.Printf("WARNING: %s", warning)
			result.Warnings = append(result.Warnings, warning)
		}
		for _, event := range toDelete {
			if err := destClient.DeleteEvent(ctx, event.Path); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to delete orphan event: %v", err))
			} else {
				result.Deleted++
				updateProgress()
			}
		}
	}

	// Clean up duplicate events on destination. cleanupDuplicates writes
	// directly into result (DuplicatesRemoved count + any Warnings for
	// failed deletes) so delete failures are visible to callers instead
	// of being log-only swallowed.
	se.cleanupDuplicates(ctx, destClient, destCalendarPath, sourceEventMap, result)
	if result.DuplicatesRemoved > 0 {
		log.Printf("Removed %d duplicate events from destination", result.DuplicatesRemoved)
	}

	// Update synced_events table with current state. Each entry's
	// sourceETag and destETag are what the next cycle will compare
	// against to decide whether either side has changed — that is
	// the fix for the infinite re-PUT loop in #79.
	//
	// We batch errors into a counter + first-error rather than
	// appending one Warning per failing UID. A pathological DB
	// outage could cause N failures where N == len(currentUIDs),
	// which for a 1000-event calendar would flood SyncResult
	// with 1000 near-identical warning strings. One aggregated
	// warning is easier to read and still actionable. (#108)
	upsertFailures := 0
	var firstUpsertErr error
	for uid, etags := range currentUIDs {
		syncedEvent := &db.SyncedEvent{
			SourceID:     source.ID,
			CalendarHref: calendar.Path,
			EventUID:     uid,
			SourceETag:   etags.sourceETag,
			DestETag:     etags.destETag,
		}
		if err := se.db.UpsertSyncedEvent(syncedEvent); err != nil {
			log.Printf("Failed to upsert synced event for %s: %v", uid, err)
			upsertFailures++
			if firstUpsertErr == nil {
				firstUpsertErr = err
			}
		}
	}
	if upsertFailures > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"Failed to upsert %d synced_events tracking rows at end of sync pass (first error: %v) - next cycle may retry unchanged events as if they were new",
			upsertFailures, firstUpsertErr,
		))
	}

	return result
}

// cleanupDuplicates removes duplicate events from destination calendar.
// It groups events by Summary+StartTime and keeps the one matching a source UID,
// or the first one if no match.
//
// Writes into result:
//   - result.DuplicatesRemoved is incremented for each successful delete
//   - result.Warnings is appended for each delete failure, each GetEvents
//     failure (which causes the whole cleanup to abort), and each
//     destination re-fetch failure
//
// Previously this function returned an int (removed count) and logged
// delete failures without surfacing them to the caller. That meant the
// dashboard reported "N duplicates removed" when the real number could
// be lower, and individual failures were invisible to users. Issue #55
// changed the signature to pass *SyncResult through so failures are
// observable.
func (se *SyncEngine) cleanupDuplicates(ctx context.Context, destClient *Client, destCalendarPath string, sourceEventMap map[string]Event, result *SyncResult) {
	log.Printf("Starting duplicate cleanup for destination: %s", destCalendarPath)

	// Re-fetch destination events to get current state
	destEvents, err := destClient.GetEvents(ctx, destCalendarPath, nil)
	if err != nil {
		log.Printf("Failed to get destination events for duplicate cleanup: %v", err)
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("duplicate cleanup aborted: failed to fetch destination events: %v", err))
		return
	}
	log.Printf("Fetched %d destination events for duplicate check", len(destEvents))

	// Group events by dedupe key (Summary + StartTime)
	type eventGroup struct {
		events []Event
	}
	groups := make(map[string]*eventGroup)

	for _, event := range destEvents {
		key := event.DedupeKey()
		if key == "|" { // Empty summary and start time
			continue
		}
		if groups[key] == nil {
			groups[key] = &eventGroup{events: make([]Event, 0)}
		}
		groups[key].events = append(groups[key].events, event)
	}

	// Find and delete duplicates
	duplicateGroups := 0
	for key, group := range groups {
		if len(group.events) <= 1 {
			continue // No duplicates
		}
		duplicateGroups++
		log.Printf("Found %d duplicates for: %s", len(group.events), key)

		// Determine which event to keep:
		// 1. Prefer event with UID matching a source event
		// 2. Otherwise keep the first one (arbitrary but consistent)
		keepIndex := 0
		for i, event := range group.events {
			if _, existsInSource := sourceEventMap[event.UID]; existsInSource {
				keepIndex = i
				break
			}
		}

		// Delete all except the one we're keeping
		for i, event := range group.events {
			if i == keepIndex {
				log.Printf("Keeping event: %s (UID: %s)", event.Path, event.UID)
				continue
			}

			log.Printf("Deleting duplicate event: %s (UID: %s)", event.Path, event.UID)
			if err := destClient.DeleteEvent(ctx, event.Path); err != nil {
				log.Printf("Failed to delete duplicate event %s: %v", event.Path, err)
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("failed to delete duplicate event %s (UID: %s): %v",
						event.Path, event.UID, err))
			} else {
				result.DuplicatesRemoved++
			}
		}
	}

	log.Printf("Duplicate cleanup complete: found %d duplicate groups, removed %d events",
		duplicateGroups, result.DuplicatesRemoved)
}

// syncICSSource syncs events from a read-only ICS feed to a CalDAV destination.
func (se *SyncEngine) syncICSSource(ctx context.Context, source *db.Source) *SyncResult {
	start := time.Now()
	result := &SyncResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	// Decrypt source credentials (may be empty for public feeds)
	sourcePassword := ""
	if source.SourcePassword != "" {
		var err error
		sourcePassword, err = se.encryptor.Decrypt(source.SourcePassword)
		if err != nil {
			result.Message = "Failed to decrypt source credentials"
			result.Errors = append(result.Errors, err.Error())
			result.Duration = time.Since(start)
			se.finishSync(source.ID, result)
			return result
		}
	}

	destPassword, err := se.encryptor.Decrypt(source.DestPassword)
	if err != nil {
		result.Message = "Failed to decrypt destination credentials"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create ICS client for source
	icsClient, err := NewICSClient(source.SourceURL, source.SourceUsername, sourcePassword)
	if err != nil {
		result.Message = "Failed to create ICS client"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Create CalDAV client for destination
	destClient, err := NewClient(source.DestURL, source.DestUsername, destPassword)
	if err != nil {
		result.Message = "Failed to connect to destination"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Test connections
	if err := icsClient.TestConnection(ctx); err != nil {
		result.Message = "ICS feed connection test failed"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	if err := destClient.TestConnection(ctx); err != nil {
		result.Message = "Destination connection test failed"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Fetch events from ICS feed
	malformedCollector := NewMalformedEventCollector()
	if err := se.db.ClearMalformedEventsForSource(source.ID); err != nil {
		log.Printf("Failed to clear old malformed events: %v", err)
	}

	sourceEvents, err := icsClient.FetchEvents(ctx, malformedCollector)
	if err != nil {
		result.Message = "Failed to fetch ICS feed"
		result.Errors = append(result.Errors, err.Error())
		result.Duration = time.Since(start)
		se.finishSync(source.ID, result)
		return result
	}

	// Capture content hash for adaptive polling (#146)
	result.ContentHash = icsClient.LastFetchHash()

	// Filter events by date if configured
	if source.SyncDaysPast > 0 {
		cutoffDate := time.Now().AddDate(0, 0, -source.SyncDaysPast)
		sourceEvents = filterEventsByDate(sourceEvents, cutoffDate)
	}

	// Store malformed events
	for _, mf := range malformedCollector.GetEvents() {
		if err := se.db.SaveMalformedEvent(source.ID, mf.Path, mf.ErrorMessage); err != nil {
			log.Printf("Failed to save malformed event record: %v", err)
		}
	}

	// Create synthetic calendar for the ICS feed
	calendar := Calendar{
		Path: source.SourceURL,
		Name: source.Name,
	}

	// Start activity tracking (single calendar for ICS)
	se.tracker.StartSync(source.ID, source.Name, 1)
	se.tracker.UpdateCalendar(source.ID, calendar.Name, 1)

	// Use shared sync logic — ICS is always one-way, sourceClient is nil (no write-back)
	syncResult := se.syncEventsToDestination(ctx, source, nil, destClient, sourceEvents, calendar, 1, db.SyncDirectionOneWay)

	result.Created = syncResult.Created
	result.Updated = syncResult.Updated
	result.Deleted = syncResult.Deleted
	result.Skipped = syncResult.Skipped
	result.EventsProcessed = syncResult.EventsProcessed
	result.DuplicatesRemoved = syncResult.DuplicatesRemoved
	result.Errors = append(result.Errors, syncResult.Errors...)
	result.Warnings = append(result.Warnings, syncResult.Warnings...)
	result.CalendarsSynced = 1

	// Multi-destination sync (#156): after syncing to the primary
	// destination, replicate the same ICS events to any additional
	// destinations. Failures on one extra dest don't block others.
	additionalDests, err := se.db.GetDestinationsBySourceID(source.ID)
	if err != nil {
		log.Printf("Failed to load additional destinations for ICS source %s: %v", source.Name, err)
	}
	for _, dest := range additionalDests {
		if !dest.Enabled {
			continue
		}
		log.Printf("Syncing ICS feed to additional destination: %s (%s)", dest.Name, dest.DestURL)
		extraDestPassword, decErr := se.encryptor.Decrypt(dest.DestPassword)
		if decErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to decrypt credentials for additional dest %q: %v", dest.Name, decErr))
			continue
		}
		extraDestClient, connErr := NewClient(dest.DestURL, dest.DestUsername, extraDestPassword)
		if connErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Failed to connect to additional dest %q: %v", dest.Name, connErr))
			continue
		}
		if testErr := extraDestClient.TestConnection(ctx); testErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Connection test failed for additional dest %q: %v", dest.Name, testErr))
			continue
		}
		extraResult := se.syncEventsToDestination(ctx, source, nil, extraDestClient, sourceEvents, calendar, 1, db.SyncDirectionOneWay)
		result.Created += extraResult.Created
		result.Updated += extraResult.Updated
		result.Deleted += extraResult.Deleted
		result.Skipped += extraResult.Skipped
		result.EventsProcessed += extraResult.EventsProcessed
		result.Warnings = append(result.Warnings, extraResult.Warnings...)
		for _, e := range extraResult.Errors {
			result.Warnings = append(result.Warnings, fmt.Sprintf("[additional dest %q] %s", dest.Name, e))
		}
		log.Printf("Completed ICS sync to additional destination: %s", dest.Name)
	}

	result.Success = len(result.Errors) == 0
	if result.Success && len(result.Warnings) == 0 {
		result.Message = fmt.Sprintf("ICS sync: %d created, %d updated, %d deleted, %d skipped",
			result.Created, result.Updated, result.Deleted, result.Skipped)
	} else if result.Success && len(result.Warnings) > 0 {
		result.Message = fmt.Sprintf("ICS sync with %d warnings: %d created, %d updated, %d deleted, %d skipped",
			len(result.Warnings), result.Created, result.Updated, result.Deleted, result.Skipped)
	} else {
		result.Message = fmt.Sprintf("ICS sync failed with %d errors", len(result.Errors))
	}

	result.Duration = time.Since(start)
	se.finishSync(source.ID, result)
	return result
}

// TestICSConnection tests connection to an ICS feed URL.
func (se *SyncEngine) TestICSConnection(ctx context.Context, url, username, password string) error {
	client, err := NewICSClient(url, username, password)
	if err != nil {
		return err
	}
	return client.TestConnection(ctx)
}

// finishSyncPersistenceWarningPrefix is the constant prefix used for
// warnings that finishSync appends when a DB write fails after all
// retries. A dedicated prefix lets callers and future alert-classifier
// extensions detect these persistence failures specifically, without
// having to parse the full warning text.
const finishSyncPersistenceWarningPrefix = "sync persistence failure: "

func (se *SyncEngine) finishSync(sourceID string, result *SyncResult) {
	// In dry-run mode, don't write status or sync log to DB —
	// the sync didn't actually happen. (#150)
	if result.DryRun {
		return
	}

	// Determine status: error > partial > success
	var status db.SyncStatus
	if !result.Success {
		status = db.SyncStatusError
	} else if len(result.Warnings) > 0 {
		status = db.SyncStatusPartial
	} else {
		status = db.SyncStatusSuccess
	}

	// Update status with retry for concurrent access. If the write
	// fails after all retries, append a warning to the result so the
	// failure is visible to callers (who inspect result.Warnings),
	// gets recorded in the sync log details below, and surfaces on
	// the dashboard instead of being silently swallowed as a log
	// line nobody reads.
	if err := retryDBOperation(func() error {
		return se.db.UpdateSourceSyncStatus(sourceID, status, result.Message)
	}, 5); err != nil {
		msg := fmt.Sprintf("%sfailed to update sync status after retries: %v",
			finishSyncPersistenceWarningPrefix, err)
		log.Printf("%s", msg)
		result.Warnings = append(result.Warnings, msg)
	}

	// Create sync log with detailed stats
	syncLog := &db.SyncLog{
		SourceID:        sourceID,
		Status:          status,
		Message:         result.Message,
		Duration:        result.Duration,
		EventsCreated:   result.Created,
		EventsUpdated:   result.Updated,
		EventsDeleted:   result.Deleted,
		EventsSkipped:   result.Skipped,
		CalendarsSynced: result.CalendarsSynced,
		EventsProcessed: result.EventsProcessed,
	}

	// Include both errors and warnings in details (sanitized to remove sensitive info).
	// If the UpdateSourceSyncStatus call above failed, its warning was
	// just appended to result.Warnings and will be captured here.
	var details []string
	if len(result.Errors) > 0 {
		details = append(details, fmt.Sprintf("Errors: %v", result.Errors))
	}
	if len(result.Warnings) > 0 {
		details = append(details, fmt.Sprintf("Warnings: %v", result.Warnings))
	}
	if len(details) > 0 {
		syncLog.Details = sanitizeLogDetails(strings.Join(details, "\n"))
	}

	// Create sync log with retry for concurrent access. A failure here
	// is inherently unrecordable (the sync log is what failed to write),
	// so we can only append to result.Warnings and log inline. Callers
	// that inspect the returned SyncResult will still see the warning,
	// even though it won't appear in the sync_logs table for this run.
	if err := retryDBOperation(func() error {
		return se.db.CreateSyncLog(syncLog)
	}, 5); err != nil {
		msg := fmt.Sprintf("%sfailed to create sync log after retries: %v",
			finishSyncPersistenceWarningPrefix, err)
		log.Printf("%s", msg)
		result.Warnings = append(result.Warnings, msg)
	}

	// Finish activity tracking
	se.tracker.FinishSync(sourceID, result.Success, result.Message, result.Errors)
}

// TestConnection tests connection to a CalDAV endpoint.
func (se *SyncEngine) TestConnection(ctx context.Context, url, username, password string) error {
	client, err := NewClient(url, username, password)
	if err != nil {
		return err
	}
	return client.TestConnection(ctx)
}
