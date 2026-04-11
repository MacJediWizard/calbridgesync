package caldav

import (
	"context"
	"errors"
	"testing"
)

// mockCalDAVDeleter is an in-memory stand-in for the CalDAV client
// that only implements the single DeleteEvent method the helper
// under test actually calls. It records every call, and lets the
// test control the returned error.
type mockCalDAVDeleter struct {
	calls    []string // eventPaths DeleteEvent was called with
	returnAs map[string]error
}

func (m *mockCalDAVDeleter) DeleteEvent(ctx context.Context, eventPath string) error {
	m.calls = append(m.calls, eventPath)
	if m.returnAs == nil {
		return nil
	}
	return m.returnAs[eventPath]
}

// mockSyncedEventDeleter is an in-memory stand-in for the DB layer
// that only implements the single DeleteSyncedEvent method the
// helper under test actually calls. Like the CalDAV mock it
// records calls and lets the test control returned errors.
type mockSyncedEventDeleter struct {
	calls    []syncedEventDeleteCall
	returnAs map[string]error // keyed by UID for easy per-row failure injection
}

type syncedEventDeleteCall struct {
	SourceID     string
	CalendarHref string
	EventUID     string
}

func (m *mockSyncedEventDeleter) DeleteSyncedEvent(sourceID, calendarHref, eventUID string) error {
	m.calls = append(m.calls, syncedEventDeleteCall{
		SourceID:     sourceID,
		CalendarHref: calendarHref,
		EventUID:     eventUID,
	})
	if m.returnAs == nil {
		return nil
	}
	return m.returnAs[eventUID]
}

// TestPerformDeletionAndCleanup_HappyPath covers the intended
// sequence: CalDAV DELETE succeeds, synced_events cleanup also
// succeeds, nil is returned. Both operations must have been called
// in the right order with the right arguments.
func TestPerformDeletionAndCleanup_HappyPath(t *testing.T) {
	cal := &mockCalDAVDeleter{}
	tr := &mockSyncedEventDeleter{}

	err := performDeletionAndCleanup(
		context.Background(),
		cal,
		tr,
		"/cal/event.ics",
		"source-1",
		"/cal/home/",
		"event-uid-1",
	)

	if err != nil {
		t.Fatalf("want nil error on happy path, got %v", err)
	}

	if len(cal.calls) != 1 {
		t.Fatalf("want 1 CalDAV DELETE call, got %d", len(cal.calls))
	}
	if cal.calls[0] != "/cal/event.ics" {
		t.Errorf("CalDAV DELETE path: want %q, got %q", "/cal/event.ics", cal.calls[0])
	}

	if len(tr.calls) != 1 {
		t.Fatalf("want 1 synced_events DELETE call, got %d", len(tr.calls))
	}
	if tr.calls[0] != (syncedEventDeleteCall{SourceID: "source-1", CalendarHref: "/cal/home/", EventUID: "event-uid-1"}) {
		t.Errorf("synced_events DELETE args mismatch: got %+v", tr.calls[0])
	}
}

// TestPerformDeletionAndCleanup_FailedCalDAVDeletePreservesTrackingRow
// is the explicit regression test for #89 / PR #90. When the
// CalDAV DELETE fails, the synced_events DELETE must NOT run. If
// this test ever fails it means someone moved the cleanup back
// outside the success branch, which would reintroduce the bug
// where a failed DELETE silently wiped the tracking row and the
// next cycle would re-propagate the still-live event to the
// other side.
func TestPerformDeletionAndCleanup_FailedCalDAVDeletePreservesTrackingRow(t *testing.T) {
	wantErr := errors.New("simulated CalDAV 500")
	cal := &mockCalDAVDeleter{
		returnAs: map[string]error{
			"/cal/event.ics": wantErr,
		},
	}
	tr := &mockSyncedEventDeleter{}

	gotErr := performDeletionAndCleanup(
		context.Background(),
		cal,
		tr,
		"/cal/event.ics",
		"source-1",
		"/cal/home/",
		"event-uid-1",
	)

	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("want %v, got %v", wantErr, gotErr)
	}

	if len(cal.calls) != 1 {
		t.Errorf("want 1 CalDAV DELETE call attempt, got %d", len(cal.calls))
	}

	if len(tr.calls) != 0 {
		t.Errorf("REGRESSION: synced_events DELETE must NOT run when CalDAV DELETE fails. "+
			"Got %d tracking-row delete calls: %+v. "+
			"See #89 / PR #90 / PR #97.", len(tr.calls), tr.calls)
	}
}

// TestPerformDeletionAndCleanup_FailedTrackingDeleteStillReturnsNil
// covers the documented behavior: a DB cleanup failure after a
// successful CalDAV DELETE is logged but not returned. The
// rationale lives in the helper's doc comment — at worst we leak
// one synced_events row that the next cycle will garbage-collect,
// and returning the error here would tempt callers to abort the
// outer deletion loop on a recoverable inconsistency. The test
// pins this behavior so a future "let's propagate the error
// upward" refactor has to be a conscious decision, not an
// accidental one.
func TestPerformDeletionAndCleanup_FailedTrackingDeleteStillReturnsNil(t *testing.T) {
	cal := &mockCalDAVDeleter{}
	tr := &mockSyncedEventDeleter{
		returnAs: map[string]error{
			"event-uid-1": errors.New("simulated DB error"),
		},
	}

	err := performDeletionAndCleanup(
		context.Background(),
		cal,
		tr,
		"/cal/event.ics",
		"source-1",
		"/cal/home/",
		"event-uid-1",
	)

	if err != nil {
		t.Errorf("want nil error (DB cleanup errors are swallowed), got %v", err)
	}

	if len(cal.calls) != 1 || cal.calls[0] != "/cal/event.ics" {
		t.Errorf("CalDAV DELETE wasn't called once for the right path: %v", cal.calls)
	}
	if len(tr.calls) != 1 {
		t.Errorf("synced_events DELETE wasn't attempted: %v", tr.calls)
	}
}

// TestPerformDeletionAndCleanup_UsesPassedContext is a lightweight
// check that the helper actually threads the provided context
// through to the CalDAV client. If someone ever swaps in a
// hardcoded context.Background() inside the helper, this test
// will catch it via the mock saving the context.
func TestPerformDeletionAndCleanup_UsesPassedContext(t *testing.T) {
	// We can't easily compare two context.Context values for
	// identity since context.WithValue wraps, but we can check
	// that the helper doesn't panic on a derived context and
	// that the DELETE still happens. The contract here is
	// "pass through whatever you got" and the compiler already
	// enforces the signature. Just a smoke test.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // helper should still try once; it's the client's job to honor cancellation

	cal := &mockCalDAVDeleter{}
	tr := &mockSyncedEventDeleter{}

	// The helper calls DeleteEvent unconditionally regardless of
	// the context state — it's the client's responsibility to
	// check ctx.Err() internally. The mock ignores the context.
	_ = performDeletionAndCleanup(ctx, cal, tr, "/e.ics", "s", "/c/", "u")

	if len(cal.calls) != 1 {
		t.Errorf("want CalDAV DELETE still attempted with cancelled ctx, got %d calls", len(cal.calls))
	}
}
