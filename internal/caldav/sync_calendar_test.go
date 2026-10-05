package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// TestSyncCalendar_IgnoresSyncCollection pins that syncCalendar never
// takes the old WebDAV-Sync (RFC 6578) shortcut, even when the source
// advertises sync-collection. That branch made unconditional PUTs and
// deletes outside the planners, ratio guards and ETag gating, and then
// returned before fullSync ran.
func TestSyncCalendar_IgnoresSyncCollection(t *testing.T) {
	const calPath = "/cal/"
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\nBEGIN:VEVENT\r\nUID:sc-1\r\nDTSTAMP:20300101T000000Z\r\nDTSTART:20300101T100000Z\r\nSUMMARY:x\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	var mu sync.Mutex
	var syncCollectionReports int
	srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodOptions:
			w.Header().Set("DAV", "1, 2, 3, calendar-access, sync-collection")
			w.WriteHeader(http.StatusOK)
		case r.Method == "REPORT" && strings.Contains(string(body), "sync-collection"):
			mu.Lock()
			syncCollectionReports++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>`+calPath+`sc-1.ics</D:href>
    <D:propstat><D:prop><D:getetag>"e1"</D:getetag><C:calendar-data>`+ics+`</C:calendar-data></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
  <D:sync-token>tok-1</D:sync-token>
</D:multistatus>`)
		case r.Method == "REPORT":
			// calendar-query from fullSync: an empty calendar.
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:"></D:multistatus>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srcSrv.Close()

	dstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dstSrv.Close()

	database, err := db.New(filepath.Join(t.TempDir(), "sc.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	user, err := database.GetOrCreateUser("sc@example.com", "SC")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	source := &db.Source{
		UserID:           user.ID,
		Name:             "sc",
		SourceType:       db.SourceTypeCalDAV,
		SourceURL:        srcSrv.URL + calPath,
		DestURL:          dstSrv.URL + "/dest/",
		SyncInterval:     3600,
		SyncDirection:    db.SyncDirectionOneWay,
		ConflictStrategy: db.ConflictSourceWins,
		Enabled:          true,
	}
	if err := database.CreateSource(source); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	srcClient, err := NewClient(srcSrv.URL, "u", "p")
	if err != nil {
		t.Fatalf("NewClient src: %v", err)
	}
	dstClient, err := NewClient(dstSrv.URL+"/dest/", "u", "p")
	if err != nil {
		t.Fatalf("NewClient dst: %v", err)
	}

	se := NewSyncEngine(database, nil)
	_ = se.syncCalendar(context.Background(), source, srcClient, dstClient, Calendar{Path: calPath, Name: "Work"}, 1, false)

	mu.Lock()
	defer mu.Unlock()
	if syncCollectionReports != 0 {
		t.Fatalf("syncCalendar sent %d REPORT sync-collection request(s); want 0 (WebDAV-Sync branch must not run)", syncCollectionReports)
	}
}
