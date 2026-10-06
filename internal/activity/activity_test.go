package activity

import (
	"encoding/json"
	"testing"
)

// TestUpdateCalendarJSONContract pins the wire name of the calendar
// progress counter. The Go field was renamed from Calendarssynced to
// CalendarsSynced (#264); the SPA reads "calendars_synced", so the
// JSON tag must not change with it.
func TestUpdateCalendarJSONContract(t *testing.T) {
	tr := NewTracker()
	tr.StartSync("src-1", "Source", 3)
	tr.UpdateCalendar("src-1", "Work", 2)

	active := tr.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active sync, got %d", len(active))
	}
	if active[0].CalendarsSynced != 2 {
		t.Errorf("CalendarsSynced = %d, want 2", active[0].CalendarsSynced)
	}

	raw, err := json.Marshal(active[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, ok := decoded["calendars_synced"]; !ok || got != float64(2) {
		t.Errorf("calendars_synced = %v (present=%v), want 2", got, ok)
	}
}
