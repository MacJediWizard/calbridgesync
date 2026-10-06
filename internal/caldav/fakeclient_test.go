package caldav

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// fakeCalendarClient is an in-memory stand-in for *Client used by the
// sync-flow characterization tests (#177). It stores events by path,
// bumps a per-object ETag on every PUT, records every PUT and DELETE
// call, and lets a test inject an error for a specific path.
//
// Path semantics mirror (*Client).PutEvent: when the event's Path is
// empty or does not live under the target calendar, the object is
// written to <calendarPath>/<UID>.ics.
type fakeCalendarClient struct {
	mu sync.Mutex

	calendars    []Calendar
	calendarPath string // value returned by GetCalendarPath
	discoverErr  error  // returned by FindCalendars/FindCalendarsGoogle

	events  map[string]Event // path -> event
	etagSeq int
	puts    []string         // resolved paths of successful PUTs, in order
	deletes []string         // paths of successful DELETEs, in order
	errOn   map[string]error // path (event or calendar) -> injected error

	ifMatches []string          // If-Match value of every PUT attempt, in order ("" = none)
	editOnPut map[string]string // path -> summary applied (once) just before a PUT to it

	// Read-side failures reported by GetEventsWithReport (#206).
	readErrOn map[string]error  // stored event path -> fetch error (object is listed but unreadable)
	malformed map[string]string // listed path -> raw UID ("" if none) of a malformed object
}

func newFakeCalendarClient(calendarPath string) *fakeCalendarClient {
	return &fakeCalendarClient{
		calendars:    []Calendar{{Path: calendarPath, Name: "Fake"}},
		calendarPath: calendarPath,
		events:       make(map[string]Event),
		errOn:        make(map[string]error),
		editOnPut:    make(map[string]string),
		readErrOn:    make(map[string]error),
		malformed:    make(map[string]string),
	}
}

var _ calendarClient = (*fakeCalendarClient)(nil)

func (f *fakeCalendarClient) nextETag() string {
	f.etagSeq++
	return fmt.Sprintf("\"etag-%d\"", f.etagSeq)
}

// seed stores an event directly (no PUT log entry) under
// <calendarPath>/<uid>.ics and returns the stored copy.
func (f *fakeCalendarClient) seed(calendarPath, uid, summary, start string) Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := Event{
		Path:      strings.TrimSuffix(calendarPath, "/") + "/" + uid + ".ics",
		ETag:      f.nextETag(),
		Data:      testICS(uid, summary, start),
		UID:       uid,
		Summary:   summary,
		StartTime: start,
	}
	f.events[e.Path] = e
	return e
}

// seedAt stores an event at an explicit path, for servers whose object
// names are not <UID>.ics.
func (f *fakeCalendarClient) seedAt(path, uid, summary, start string) Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := Event{
		Path:      path,
		ETag:      f.nextETag(),
		Data:      testICS(uid, summary, start),
		UID:       uid,
		Summary:   summary,
		StartTime: start,
	}
	f.events[e.Path] = e
	return e
}

// edit simulates an out-of-band change to an existing object (for
// example a user editing it in a calendar app): the summary changes
// and the ETag is bumped.
func (f *fakeCalendarClient) edit(path, newSummary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.events[path]
	if !ok {
		panic("fakeCalendarClient.edit: no event at " + path)
	}
	e.Summary = newSummary
	e.Data = testICS(e.UID, newSummary, e.StartTime)
	e.ETag = f.nextETag()
	f.events[path] = e
}

// remove simulates an out-of-band delete (no DELETE log entry).
func (f *fakeCalendarClient) remove(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.events, path)
}

func (f *fakeCalendarClient) failOn(path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errOn[path] = err
}

// failReadOn makes a stored event unreadable: listings report it as
// an unreadable object instead of returning it.
func (f *fakeCalendarClient) failReadOn(path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readErrOn[path] = err
}

// addMalformed lists a malformed object at path. rawUID is what a raw
// "UID:" line would yield ("" when the object has none).
func (f *fakeCalendarClient) addMalformed(path, rawUID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.malformed[path] = rawUID
}

func (f *fakeCalendarClient) resetLog() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = nil
	f.deletes = nil
	f.ifMatches = nil
}

// ifMatchLog returns the If-Match value of every PUT attempt since the
// last resetLog, in call order.
func (f *fakeCalendarClient) ifMatchLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ifMatches...)
}

// editBeforePut makes the next PUT to path first apply an out-of-band
// edit, as if a user changed the object after the sync read it.
func (f *fakeCalendarClient) editBeforePut(path, newSummary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.editOnPut[path] = newSummary
}

func (f *fakeCalendarClient) putLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.puts...)
	sort.Strings(out)
	return out
}

func (f *fakeCalendarClient) deleteLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.deletes...)
	sort.Strings(out)
	return out
}

// paths returns the sorted paths of all stored events.
func (f *fakeCalendarClient) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.events))
	for p := range f.events {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (f *fakeCalendarClient) get(path string) (Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.events[path]
	return e, ok
}

func (f *fakeCalendarClient) FindCalendars(_ context.Context) ([]Calendar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	return append([]Calendar(nil), f.calendars...), nil
}

func (f *fakeCalendarClient) FindCalendarsGoogle(ctx context.Context) ([]Calendar, error) {
	return f.FindCalendars(ctx)
}

func (f *fakeCalendarClient) GetCalendarPath() string {
	return f.calendarPath
}

func (f *fakeCalendarClient) GetEvents(ctx context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, error) {
	events, _, err := f.GetEventsWithReport(ctx, calendarPath, collector)
	return events, err
}

// GetEventsWithReport mirrors (*Client).GetEventsWithReport: objects
// injected with failReadOn are reported as transient (or malformed if
// the error looks malformed), objects added with addMalformed are
// reported as malformed with their raw UID.
func (f *fakeCalendarClient) GetEventsWithReport(_ context.Context, calendarPath string, collector *MalformedEventCollector) ([]Event, FetchReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errOn[calendarPath]; err != nil {
		return nil, FetchReport{}, err
	}
	prefix := strings.TrimSuffix(calendarPath, "/") + "/"
	var report FetchReport
	out := make([]Event, 0)
	for p, e := range f.events {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		if err := f.readErrOn[p]; err != nil {
			kind := UnreadableTransient
			if IsMalformedError(err) {
				kind = UnreadableMalformed
			}
			report.Unreadable = append(report.Unreadable, UnreadableObject{Path: p, Kind: kind, Err: err.Error()})
			continue
		}
		out = append(out, e)
	}
	for p, uid := range f.malformed {
		if strings.HasPrefix(p, prefix) {
			if collector != nil {
				collector.Add(p, "malformed (fake)")
			}
			report.Unreadable = append(report.Unreadable, UnreadableObject{Path: p, Kind: UnreadableMalformed, UID: uid, Err: "malformed (fake)"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	sort.Slice(report.Unreadable, func(i, j int) bool { return report.Unreadable[i].Path < report.Unreadable[j].Path })
	return out, report, nil
}

func (f *fakeCalendarClient) GetEvent(_ context.Context, eventPath string) (*Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errOn[eventPath]; err != nil {
		return nil, err
	}
	e, ok := f.events[eventPath]
	if !ok {
		return nil, fmt.Errorf("404 Not Found: %s", eventPath)
	}
	return &e, nil
}

func (f *fakeCalendarClient) PutEvent(ctx context.Context, calendarPath string, event *Event) error {
	return f.PutEventIfMatch(ctx, calendarPath, event, "")
}

// PutEventIfMatch enforces If-Match like a CalDAV server: when ifMatch
// is set and differs from the stored object's ETag, nothing is written
// and a 412 wrapped in ErrPreconditionFailed is returned. editOnPut
// lets a test simulate a concurrent edit landing between the sync's
// read and its PUT.
func (f *fakeCalendarClient) PutEventIfMatch(_ context.Context, calendarPath string, event *Event, ifMatch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ifMatches = append(f.ifMatches, ifMatch)
	if event.Data == "" {
		return fmt.Errorf("%w: empty iCalendar data (UID: %s)", ErrEventSkipped, event.UID)
	}
	path := event.Path
	if path == "" || !strings.HasPrefix(path, calendarPath) {
		if event.UID == "" {
			return fmt.Errorf("%w: no UID", ErrEventSkipped)
		}
		path = strings.TrimSuffix(calendarPath, "/") + "/" + event.UID + ".ics"
	}
	if err := f.errOn[path]; err != nil {
		return err
	}
	if summary, ok := f.editOnPut[path]; ok {
		delete(f.editOnPut, path)
		if e, exists := f.events[path]; exists {
			e.Summary = summary
			e.Data = testICS(e.UID, summary, e.StartTime)
			e.ETag = f.nextETag()
			f.events[path] = e
		}
	}
	if ifMatch != "" {
		if e, exists := f.events[path]; !exists || e.ETag != ifMatch {
			return fmt.Errorf("%w: failed to put event: 412 Precondition Failed", ErrPreconditionFailed)
		}
	}
	stored := *event
	stored.Path = path
	stored.ETag = f.nextETag()
	f.events[path] = stored
	f.puts = append(f.puts, path)
	return nil
}

func (f *fakeCalendarClient) DeleteEvent(_ context.Context, eventPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errOn[eventPath]; err != nil {
		return err
	}
	delete(f.events, eventPath)
	f.deletes = append(f.deletes, eventPath)
	return nil
}

// testICS builds a minimal valid VCALENDAR with one VEVENT.
func testICS(uid, summary, start string) string {
	return "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//calbridgesync//fake//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:" + start + "\r\n" +
		"SUMMARY:" + summary + "\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}
