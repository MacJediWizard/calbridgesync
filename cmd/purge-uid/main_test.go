package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
)

// TestFindUIDInEvents_ParsedUIDWins covers the normal case where the
// iCalendar parser populated Event.UID correctly.
func TestFindUIDInEvents_ParsedUIDWins(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "other-uid-1", Data: ""},
		{Path: "/cal/b.ics", UID: "TARGET", Data: "BEGIN:VEVENT\nUID:TARGET\nEND:VEVENT"},
		{Path: "/cal/c.ics", UID: "other-uid-2", Data: ""},
	}

	got := findUIDInEvents(events, "TARGET")
	if !reflect.DeepEqual(got, []string{"/cal/b.ics"}) {
		t.Errorf("expected [/cal/b.ics], got %q", got)
	}
}

// TestFindUIDInEvents_NotFound covers the common no-match case.
func TestFindUIDInEvents_NotFound(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "uid-1"},
		{Path: "/cal/b.ics", UID: "uid-2"},
	}

	got := findUIDInEvents(events, "NOT-PRESENT")
	if len(got) != 0 {
		t.Errorf("expected no matches, got %q", got)
	}
}

// TestFindUIDInEvents_EmptyList covers the edge case where the
// calendar has zero events. Must not panic and must return nothing.
func TestFindUIDInEvents_EmptyList(t *testing.T) {
	if got := findUIDInEvents(nil, "anything"); len(got) != 0 {
		t.Errorf("expected no matches for nil slice, got %q", got)
	}
	if got := findUIDInEvents([]caldav.Event{}, "anything"); len(got) != 0 {
		t.Errorf("expected no matches for empty slice, got %q", got)
	}
}

// TestFindUIDInEvents_RawFallback covers the zombie-recovery case:
// the parsed UID field is empty (parser dropped it) but the raw
// Event.Data still carries the UID property. Pattern is exactly how
// the WOS zombie manifested during recovery.
func TestFindUIDInEvents_RawFallback(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "", Data: "BEGIN:VEVENT\r\nUID:040000-TARGET\r\nSUMMARY:Untitled\r\nEND:VEVENT"},
	}

	got := findUIDInEvents(events, "040000-TARGET")
	if !reflect.DeepEqual(got, []string{"/cal/a.ics"}) {
		t.Errorf("expected raw-fallback match at /cal/a.ics, got %q", got)
	}
}

// TestFindUIDInEvents_ParsedAndRawBothReturned verifies that when a
// parsed UID matches in one object and a raw-data UID matches in
// another, both are returned (parsed matches first). Every copy of a
// corrupted UID must be purged, not just the "authoritative" one.
func TestFindUIDInEvents_ParsedAndRawBothReturned(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/raw.ics", UID: "", Data: "BEGIN:VEVENT\r\nUID:TARGET\r\nEND:VEVENT"},
		{Path: "/cal/parsed.ics", UID: "TARGET", Data: ""},
	}

	got := findUIDInEvents(events, "TARGET")
	want := []string{"/cal/parsed.ics", "/cal/raw.ics"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestFindUIDInEvents_SubstringSafety verifies that "TARGET" appearing
// inside a different property (e.g. SUMMARY) is not a match.
func TestFindUIDInEvents_SubstringSafety(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/a.ics", UID: "different-uid", Data: "BEGIN:VEVENT\r\nUID:different-uid\r\nSUMMARY:Meet with TARGET team\r\nEND:VEVENT"},
	}

	if got := findUIDInEvents(events, "TARGET"); len(got) != 0 {
		t.Errorf("expected no match (TARGET appears only in SUMMARY), got %q", got)
	}
}

// TestFindUIDInEvents_PrefixIsNotMatch: purging "abc" must never
// select an event whose UID is "abcd", via either the parsed UID or
// the raw-data fallback.
func TestFindUIDInEvents_PrefixIsNotMatch(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/parsed.ics", UID: "abcd", Data: "BEGIN:VEVENT\r\nUID:abcd\r\nEND:VEVENT"},
		{Path: "/cal/raw.ics", UID: "", Data: "BEGIN:VEVENT\r\nUID:abcd\r\nEND:VEVENT"},
		{Path: "/cal/rawlf.ics", UID: "", Data: "BEGIN:VEVENT\nUID:abcd\nEND:VEVENT"},
	}

	if got := findUIDInEvents(events, "abc"); len(got) != 0 {
		t.Errorf("expected no match for prefix UID abc, got %q", got)
	}
}

// TestFindUIDInEvents_RawFoldedLine: a long UID folded across lines
// (RFC 5545 section 3.1) is still matched after unfolding.
func TestFindUIDInEvents_RawFoldedLine(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/folded.ics", UID: "", Data: "BEGIN:VEVENT\r\nUID:0400000082-very-\r\n long-TARGET\r\nEND:VEVENT"},
	}

	got := findUIDInEvents(events, "0400000082-very-long-TARGET")
	if !reflect.DeepEqual(got, []string{"/cal/folded.ics"}) {
		t.Errorf("expected folded raw match, got %q", got)
	}
}

// TestFindUIDInEvents_AllMatchesReturned: two objects carrying the
// same UID (the corrupted-duplicate case) yield two paths.
func TestFindUIDInEvents_AllMatchesReturned(t *testing.T) {
	events := []caldav.Event{
		{Path: "/cal/one.ics", UID: "DUP", Data: "BEGIN:VEVENT\r\nUID:DUP\r\nEND:VEVENT"},
		{Path: "/cal/other.ics", UID: "other"},
		{Path: "/cal/two.ics", UID: "DUP", Data: "BEGIN:VEVENT\r\nUID:DUP\r\nEND:VEVENT"},
	}

	got := findUIDInEvents(events, "DUP")
	want := []string{"/cal/one.ics", "/cal/two.ics"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// fakeClient is an in-memory eventClient keyed by calendar path.
type fakeClient struct {
	events    map[string][]caldav.Event
	getErr    error
	deleteErr map[string]error
	deleted   []string
	gotPaths  []string
}

func (f *fakeClient) GetEvents(_ context.Context, calendarPath string, _ *caldav.MalformedEventCollector) ([]caldav.Event, error) {
	f.gotPaths = append(f.gotPaths, calendarPath)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.events[calendarPath], nil
}

func (f *fakeClient) DeleteEvent(_ context.Context, eventPath string) error {
	if err := f.deleteErr[eventPath]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, eventPath)
	return nil
}

// TestPurgeCalendar_DeletesEveryMatchAndScrubs: with --confirm, every
// matching object on each side is deleted, each side is searched at
// its own calendar path, and the tracking row is scrubbed once.
func TestPurgeCalendar_DeletesEveryMatchAndScrubs(t *testing.T) {
	dest := &fakeClient{events: map[string][]caldav.Event{
		"/dest/cal/": {{Path: "/d/1.ics", UID: "X"}, {Path: "/d/2.ics", UID: "X"}},
	}}
	src := &fakeClient{events: map[string][]caldav.Event{
		"/src/cal/": {{Path: "/s/1.ics", UID: "X"}},
	}}
	scrubbed := 0

	found, deleted, errs := purgeCalendar(context.Background(), []sideTarget{
		{label: "dest", client: dest, calendarPath: "/dest/cal/"},
		{label: "source", client: src, calendarPath: "/src/cal/"},
	}, "X", true, func() error { scrubbed++; return nil })

	if found != 3 || deleted != 3 || errs != 0 {
		t.Errorf("found/deleted/errs = %d/%d/%d, want 3/3/0", found, deleted, errs)
	}
	if !reflect.DeepEqual(dest.deleted, []string{"/d/1.ics", "/d/2.ics"}) {
		t.Errorf("dest deleted %q", dest.deleted)
	}
	if !reflect.DeepEqual(dest.gotPaths, []string{"/dest/cal/"}) || !reflect.DeepEqual(src.gotPaths, []string{"/src/cal/"}) {
		t.Errorf("searched wrong paths: dest=%q source=%q", dest.gotPaths, src.gotPaths)
	}
	if scrubbed != 1 {
		t.Errorf("expected scrub once, got %d", scrubbed)
	}
}

// TestPurgeCalendar_SkipsScrubWhenDeleteFails: if any DELETE fails the
// remote object survives, so the synced_events row must survive too
// (PR #90 anti-pattern).
func TestPurgeCalendar_SkipsScrubWhenDeleteFails(t *testing.T) {
	dest := &fakeClient{
		events:    map[string][]caldav.Event{"/dest/cal/": {{Path: "/d/1.ics", UID: "X"}, {Path: "/d/2.ics", UID: "X"}}},
		deleteErr: map[string]error{"/d/2.ics": errors.New("boom")},
	}
	src := &fakeClient{events: map[string][]caldav.Event{"/src/cal/": {{Path: "/s/1.ics", UID: "X"}}}}
	scrubbed := 0

	found, deleted, errs := purgeCalendar(context.Background(), []sideTarget{
		{label: "dest", client: dest, calendarPath: "/dest/cal/"},
		{label: "source", client: src, calendarPath: "/src/cal/"},
	}, "X", true, func() error { scrubbed++; return nil })

	if found != 3 || deleted != 2 || errs != 1 {
		t.Errorf("found/deleted/errs = %d/%d/%d, want 3/2/1", found, deleted, errs)
	}
	if scrubbed != 0 {
		t.Errorf("scrub ran %d time(s) after a failed delete; must be skipped", scrubbed)
	}
}

// TestPurgeCalendar_SkipsScrubWhenSearchFails: a search failure means
// we do not know whether the object still exists, so no scrub.
func TestPurgeCalendar_SkipsScrubWhenSearchFails(t *testing.T) {
	dest := &fakeClient{getErr: errors.New("unreachable")}
	scrubbed := 0

	_, _, errs := purgeCalendar(context.Background(), []sideTarget{
		{label: "dest", client: dest, calendarPath: "/dest/cal/"},
	}, "X", true, func() error { scrubbed++; return nil })

	if errs != 1 || scrubbed != 0 {
		t.Errorf("errs=%d scrubbed=%d, want errs=1 scrubbed=0", errs, scrubbed)
	}
}

// TestPurgeCalendar_ScrubFailureCounted: a failed scrub is an error.
func TestPurgeCalendar_ScrubFailureCounted(t *testing.T) {
	dest := &fakeClient{}

	_, _, errs := purgeCalendar(context.Background(), []sideTarget{
		{label: "dest", client: dest, calendarPath: "/dest/cal/"},
	}, "X", true, func() error { return errors.New("db locked") })

	if errs != 1 {
		t.Errorf("errs=%d, want 1", errs)
	}
}

// TestPurgeCalendar_DryRunTouchesNothing: without --confirm nothing is
// deleted and the scrub never runs.
func TestPurgeCalendar_DryRunTouchesNothing(t *testing.T) {
	dest := &fakeClient{events: map[string][]caldav.Event{"/dest/cal/": {{Path: "/d/1.ics", UID: "X"}}}}
	scrubbed := 0

	found, deleted, errs := purgeCalendar(context.Background(), []sideTarget{
		{label: "dest", client: dest, calendarPath: "/dest/cal/"},
	}, "X", false, func() error { scrubbed++; return nil })

	if found != 1 || deleted != 0 || errs != 0 || len(dest.deleted) != 0 || scrubbed != 0 {
		t.Errorf("dry-run mutated state: found=%d deleted=%d errs=%d dest.deleted=%q scrubbed=%d",
			found, deleted, errs, dest.deleted, scrubbed)
	}
}

// fakeFinder implements calendarFinder for destination discovery tests.
type fakeFinder struct {
	cals, googleCals []caldav.Calendar
	err              error
	urlPath          string
	usedGoogle       bool
}

func (f *fakeFinder) FindCalendars(context.Context) ([]caldav.Calendar, error) {
	return f.cals, f.err
}

func (f *fakeFinder) FindCalendarsGoogle(context.Context) ([]caldav.Calendar, error) {
	f.usedGoogle = true
	return f.googleCals, f.err
}

func (f *fakeFinder) GetCalendarPath() string { return f.urlPath }

// TestDiscoverDestCalendarPath mirrors the sync engine's destination
// discovery: first discovered calendar, URL path on error or empty
// result, and Google discovery for Google URLs.
func TestDiscoverDestCalendarPath(t *testing.T) {
	ctx := context.Background()

	f := &fakeFinder{cals: []caldav.Calendar{{Path: "/dav/cal-a/"}, {Path: "/dav/cal-b/"}}, urlPath: "/dav/"}
	if got := discoverDestCalendarPath(ctx, f, "https://dav.example.com/dav/"); got != "/dav/cal-a/" {
		t.Errorf("discovered: got %q, want /dav/cal-a/", got)
	}

	f = &fakeFinder{err: errors.New("nope"), urlPath: "/dav/fallback/"}
	if got := discoverDestCalendarPath(ctx, f, "https://dav.example.com/dav/fallback/"); got != "/dav/fallback/" {
		t.Errorf("error fallback: got %q, want /dav/fallback/", got)
	}

	f = &fakeFinder{urlPath: "/dav/empty/"}
	if got := discoverDestCalendarPath(ctx, f, "https://dav.example.com/dav/empty/"); got != "/dav/empty/" {
		t.Errorf("empty fallback: got %q, want /dav/empty/", got)
	}

	f = &fakeFinder{googleCals: []caldav.Calendar{{Path: "/caldav/v2/me/events/"}}, urlPath: "/caldav/v2/"}
	if got := discoverDestCalendarPath(ctx, f, "https://apidata.googleusercontent.com/caldav/v2/"); got != "/caldav/v2/me/events/" || !f.usedGoogle {
		t.Errorf("google: got %q usedGoogle=%v", got, f.usedGoogle)
	}
}

// TestModeLabel covers the tiny mode-label helper for consistency
// between the startup banner and the summary footer.
func TestModeLabel(t *testing.T) {
	if got := modeLabel(true); got != "CONFIRM (will delete)" {
		t.Errorf("confirm=true: unexpected label %q", got)
	}
	if got := modeLabel(false); got != "DRY-RUN (read-only)" {
		t.Errorf("confirm=false: unexpected label %q", got)
	}
}
