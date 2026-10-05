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
}

func newFakeCalendarClient(calendarPath string) *fakeCalendarClient {
	return &fakeCalendarClient{
		calendars:    []Calendar{{Path: calendarPath, Name: "Fake"}},
		calendarPath: calendarPath,
		events:       make(map[string]Event),
		errOn:        make(map[string]error),
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

func (f *fakeCalendarClient) resetLog() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = nil
	f.deletes = nil
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

func (f *fakeCalendarClient) GetEvents(_ context.Context, calendarPath string, _ *MalformedEventCollector) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errOn[calendarPath]; err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(calendarPath, "/") + "/"
	out := make([]Event, 0)
	for p, e := range f.events {
		if strings.HasPrefix(p, prefix) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
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

func (f *fakeCalendarClient) PutEvent(_ context.Context, calendarPath string, event *Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
