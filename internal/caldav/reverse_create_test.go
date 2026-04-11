package caldav

import (
	"errors"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Issue #74 regression suite.
//
// Exercises two new pieces of code:
//
//  1. planReverseCreate — filters destination events down to the set
//     that should be uploaded to source during the two-way reverse
//     create pass, enforcing ownership + empty-source + hard-cap
//     safety rules.
//
//  2. isSourceAlreadyExistsError — recognizes both 412 Precondition
//     Failed (same calendar, If-None-Match collision) and 409
//     Conflict (cross-calendar UID collision, common on iCloud) as
//     "event already exists on source" for the reverse create path.

// -----------------------------------------------------------------------------
// planReverseCreate tests
// -----------------------------------------------------------------------------

// TestPlanReverseCreate_AllCandidatesReturnedInNormalCase verifies
// the happy path: dest has some new events that aren't on source and
// aren't in prev-synced, source has some events, cap is high enough.
// All new dest events should come back as upload candidates. (#74)
func TestPlanReverseCreate_AllCandidatesReturnedInNormalCase(t *testing.T) {
	destEvents := []Event{
		{UID: "new-1"},
		{UID: "new-2"},
		{UID: "already-on-source"},
	}
	sourceEventMap := map[string]Event{
		"already-on-source": {UID: "already-on-source"},
		"something-else":    {UID: "something-else"},
	}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if warning != "" {
		t.Errorf("expected no warning in normal case, got %q", warning)
	}
	if len(toUpload) != 2 {
		t.Fatalf("expected 2 new events to upload, got %d", len(toUpload))
	}
	gotUIDs := map[string]bool{}
	for _, e := range toUpload {
		gotUIDs[e.UID] = true
	}
	if !gotUIDs["new-1"] || !gotUIDs["new-2"] {
		t.Errorf("expected new-1 and new-2 in upload list, got %v", gotUIDs)
	}
	if gotUIDs["already-on-source"] {
		t.Error("already-on-source should NOT be in upload list")
	}
}

// TestPlanReverseCreate_SkipsEventsAlreadyOnSource verifies the
// ownership filter excludes events that exist on source. (#74)
func TestPlanReverseCreate_SkipsEventsAlreadyOnSource(t *testing.T) {
	destEvents := []Event{
		{UID: "shared"},
	}
	sourceEventMap := map[string]Event{
		"shared": {UID: "shared"},
	}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if warning != "" {
		t.Errorf("expected no warning, got %q", warning)
	}
	if len(toUpload) != 0 {
		t.Errorf("expected 0 candidates (event on both sides), got %d", len(toUpload))
	}
}

// TestPlanReverseCreate_SkipsEventsInPreviouslySynced verifies the
// ownership filter excludes events that were previously synced but
// are now missing from source (they were deleted from source — the
// two-way deletion pass handles those, not the create pass). (#74)
func TestPlanReverseCreate_SkipsEventsInPreviouslySynced(t *testing.T) {
	destEvents := []Event{
		{UID: "was-synced-then-deleted-from-source"},
	}
	sourceEventMap := map[string]Event{
		"other": {UID: "other"},
	}
	previouslySyncedMap := map[string]*db.SyncedEvent{
		"was-synced-then-deleted-from-source": {EventUID: "was-synced-then-deleted-from-source"},
	}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if warning != "" {
		t.Errorf("expected no warning, got %q", warning)
	}
	if len(toUpload) != 0 {
		t.Errorf("expected 0 candidates (prev-synced events are not create candidates), got %d", len(toUpload))
	}
}

// TestPlanReverseCreate_SkipsEventsWithEmptyUID verifies events
// without a UID are excluded — they cannot be uploaded to a
// CalDAV server because the path on source would be synthesized
// from the UID. (#74)
func TestPlanReverseCreate_SkipsEventsWithEmptyUID(t *testing.T) {
	destEvents := []Event{
		{UID: ""}, // broken event
		{UID: "valid"},
	}
	sourceEventMap := map[string]Event{"other": {UID: "other"}}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, _ := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if len(toUpload) != 1 || toUpload[0].UID != "valid" {
		t.Errorf("expected only the valid-UID event, got %+v", toUpload)
	}
}

// TestPlanReverseCreate_EmptySourceWithPriorRecordsRefusesAll is
// the critical safety case: if the source query returned empty but
// we have prior sync records, refuse to upload ANYTHING. This
// prevents a transient source failure from triggering a mass upload
// of the entire destination calendar back to source. Mirror of
// planOrphanDeletion's empty-source guard. (#74)
func TestPlanReverseCreate_EmptySourceWithPriorRecordsRefusesAll(t *testing.T) {
	destEvents := []Event{
		{UID: "e1"}, {UID: "e2"}, {UID: "e3"},
	}
	sourceEventMap := map[string]Event{} // simulates a failed source query
	previouslySyncedMap := map[string]*db.SyncedEvent{
		"e1": {EventUID: "e1"},
		"e2": {EventUID: "e2"},
	}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if warning == "" {
		t.Fatal("expected a safety warning when source is empty + prior records exist")
	}
	if toUpload != nil {
		t.Errorf("expected nil upload list when safety triggers, got %d events", len(toUpload))
	}
}

// TestPlanReverseCreate_EmptySourceNoPriorAllowsNormalUpload
// verifies the legitimate first-sync scenario: source is empty but
// we have no prior sync records either, so dest-only events should
// still flow up normally. This is the "brand new calendar, let it
// populate" case. (#74)
func TestPlanReverseCreate_EmptySourceNoPriorAllowsNormalUpload(t *testing.T) {
	destEvents := []Event{
		{UID: "first-1"},
		{UID: "first-2"},
	}
	sourceEventMap := map[string]Event{}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if warning != "" {
		t.Errorf("first-sync case should not trigger safety: got warning %q", warning)
	}
	if len(toUpload) != 2 {
		t.Errorf("expected both first-sync events to be candidates, got %d", len(toUpload))
	}
}

// TestPlanReverseCreate_HardCapExceededRefusesAll verifies the
// third safety rule: if the candidate list would exceed the hard
// cap, refuse the entire upload pass with a warning. Protects
// against runaway first-syncs that would flood iCloud with hundreds
// of PUTs and trigger rate limiting. (#74)
func TestPlanReverseCreate_HardCapExceededRefusesAll(t *testing.T) {
	destEvents := make([]Event, 150)
	for i := range destEvents {
		destEvents[i] = Event{UID: string(rune('a'+i%26)) + string(rune('0'+i/26))}
	}
	sourceEventMap := map[string]Event{"other": {UID: "other"}}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 100)

	if warning == "" {
		t.Fatal("expected a safety warning when candidate count exceeds cap")
	}
	if toUpload != nil {
		t.Errorf("expected nil upload list when cap exceeded, got %d events", len(toUpload))
	}
}

// TestPlanReverseCreate_HardCapOfZeroDisabled verifies that passing
// maxCreates=0 disables the cap (matches the disable semantics on
// the orphan deletion ratio). (#74)
func TestPlanReverseCreate_HardCapOfZeroDisabled(t *testing.T) {
	destEvents := make([]Event, 500)
	for i := range destEvents {
		destEvents[i] = Event{UID: string(rune('a'+i%26)) + string(rune('0'+i/26))}
	}
	sourceEventMap := map[string]Event{"other": {UID: "other"}}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 0)

	if warning != "" {
		t.Errorf("cap=0 should disable the cap check, got warning %q", warning)
	}
	if len(toUpload) != 500 {
		t.Errorf("expected all 500 events when cap is disabled, got %d", len(toUpload))
	}
}

// TestPlanReverseCreate_CapExactlyAtCandidateCountAllowed verifies
// the boundary: if the candidate count is EXACTLY the cap, the
// upload is allowed. The cap is a strict "greater than" check, not
// "greater than or equal to." (#74)
func TestPlanReverseCreate_CapExactlyAtCandidateCountAllowed(t *testing.T) {
	destEvents := make([]Event, 5)
	for i := range destEvents {
		destEvents[i] = Event{UID: string(rune('a' + i))}
	}
	sourceEventMap := map[string]Event{"other": {UID: "other"}}
	previouslySyncedMap := map[string]*db.SyncedEvent{}

	toUpload, warning := planReverseCreate(destEvents, sourceEventMap, previouslySyncedMap, 5)

	if warning != "" {
		t.Errorf("exact-cap case should allow upload, got warning %q", warning)
	}
	if len(toUpload) != 5 {
		t.Errorf("expected 5 events, got %d", len(toUpload))
	}
}

// -----------------------------------------------------------------------------
// isSourceAlreadyExistsError tests
// -----------------------------------------------------------------------------

// TestIsSourceAlreadyExistsError_412PreconditionFailed verifies the
// helper recognizes the 412 response that iCloud returns on an
// If-None-Match collision within the same calendar. (#74)
func TestIsSourceAlreadyExistsError_412PreconditionFailed(t *testing.T) {
	err := errors.New("failed to put event: 412 Precondition Failed")
	if !isSourceAlreadyExistsError(err) {
		t.Error("412 Precondition Failed should be recognized as already-exists")
	}
}

// TestIsSourceAlreadyExistsError_409Conflict verifies the helper
// recognizes the 409 response that iCloud returns on a cross-calendar
// UID collision. This is the specific case that was producing 166
// false-warning events per cycle in William's sync before the fix. (#74)
func TestIsSourceAlreadyExistsError_409Conflict(t *testing.T) {
	err := errors.New("connection failed: failed to put event: 409 Conflict")
	if !isSourceAlreadyExistsError(err) {
		t.Error("409 Conflict should be recognized as already-exists")
	}
}

// TestIsSourceAlreadyExistsError_BareConflictString verifies the
// helper catches responses that use the word "Conflict" without a
// literal "409" (defensive: some servers may format the error
// differently). (#74)
func TestIsSourceAlreadyExistsError_BareConflictString(t *testing.T) {
	err := errors.New("server reported a conflict on UID x")
	// Strictly speaking this would match because strings.Contains is
	// case-sensitive and the test string doesn't contain "Conflict"
	// capitalized. Document the actual case-sensitive behavior so
	// future developers don't get bitten by the contract.
	if isSourceAlreadyExistsError(err) {
		t.Error("lowercase 'conflict' should NOT match — contract is case-sensitive")
	}
	err2 := errors.New("server reported a Conflict on UID x")
	if !isSourceAlreadyExistsError(err2) {
		t.Error("capital 'Conflict' should match")
	}
}

// TestIsSourceAlreadyExistsError_NotFoundIsNotMatched verifies the
// helper does NOT match 404 or other non-existence errors — those
// are real failures that should surface as warnings. (#74)
func TestIsSourceAlreadyExistsError_NotFoundIsNotMatched(t *testing.T) {
	tests := []string{
		"404 Not Found",
		"500 Internal Server Error",
		"502 Bad Gateway",
		"unreachable host",
		"connection timeout",
	}
	for _, msg := range tests {
		t.Run(msg, func(t *testing.T) {
			if isSourceAlreadyExistsError(errors.New(msg)) {
				t.Errorf("%q should NOT be recognized as already-exists", msg)
			}
		})
	}
}

// TestIsSourceAlreadyExistsError_NilReturnsFalse verifies the nil
// guard. (#74)
func TestIsSourceAlreadyExistsError_NilReturnsFalse(t *testing.T) {
	if isSourceAlreadyExistsError(nil) {
		t.Error("nil error must return false")
	}
}

// TestIsSourceAlreadyExistsError_StrictlyForReverseCreate documents
// the intentional split from isAlreadyExistsError. The original
// helper is 412-only because the forward-direction update path uses
// If-Match with a known ETag, where 412 is the specific "ETag
// changed between fetch and write" signal. Treating 409 as "already
// exists" there would mask legitimate errors. This test is a canary
// that fails if someone tries to unify the two helpers without
// understanding why. (#74)
func TestIsSourceAlreadyExistsError_StrictlyForReverseCreate(t *testing.T) {
	err := errors.New("failed to put event: 409 Conflict")
	if isAlreadyExistsError(err) {
		t.Error("isAlreadyExistsError must stay 412-only — do not unify with isSourceAlreadyExistsError")
	}
	if !isSourceAlreadyExistsError(err) {
		t.Error("isSourceAlreadyExistsError must recognize 409")
	}
}
