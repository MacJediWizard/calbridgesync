package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

// TestExecuteSyncICSLeavesIntervalAndAdaptiveColumnsAlone is the
// regression test for removing ICS adaptive polling (#146). The
// feature wrote last_content_hash / adaptive_interval (and bumped
// updated_at) on every ICS cycle, but nothing ever read them back, so
// the job interval never changed. After a successful ICS sync the
// columns must stay NULL and the job must keep source.SyncInterval.
func TestExecuteSyncICSLeavesIntervalAndAdaptiveColumnsAlone(t *testing.T) {
	// ICS feed stub: a valid, empty calendar.
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		fmt.Fprint(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nEND:VCALENDAR\r\n")
	}))
	defer feed.Close()

	// Destination CalDAV stub: every PROPFIND/REPORT gets a 207 that
	// names a principal and nothing else, which reads as an empty calendar.
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:"><d:response><d:href>%s</d:href><d:propstat><d:prop>
<d:current-user-principal><d:href>/principals/user/</d:href></d:current-user-principal>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.Path)
	}))
	defer dest.Close()

	// Both stubs bind to 127.0.0.1, which the SSRF guards refuse.
	d := &net.Dialer{Timeout: 5 * time.Second}
	t.Cleanup(caldav.SetDialContextForTesting(d.DialContext))
	feedAddr := feed.Listener.Addr().String()
	t.Cleanup(caldav.SetICSDialContextForTesting(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, feedAddr)
	}))

	database, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	enc, err := crypto.NewEncryptor(make([]byte, crypto.KeySize))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	destPassword, err := enc.Encrypt("destpass")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	user, err := database.GetOrCreateUser("ics@example.com", "ICS User")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	source := &db.Source{
		UserID:           user.ID,
		Name:             "ICS feed",
		SourceType:       db.SourceTypeICS,
		SourceURL:        "http://feed.test/cal.ics",
		DestURL:          dest.URL + "/calendars/user/cal/",
		DestUsername:     "destuser",
		DestPassword:     destPassword,
		SyncInterval:     300,
		SyncDaysPast:     30,
		SyncDirection:    db.SyncDirectionOneWay,
		ConflictStrategy: db.ConflictSourceWins,
		Enabled:          true,
	}
	if err := database.CreateSource(source); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	sched := New(database, caldav.NewSyncEngine(database, enc), nil)
	interval := time.Duration(source.SyncInterval) * time.Second
	if err := sched.AddJobWithDelay(source.ID, interval, time.Hour); err != nil {
		t.Fatalf("AddJobWithDelay: %v", err)
	}
	defer sched.Stop()

	// Two cycles: the second is the one where an unchanged feed would
	// have "extended" the interval.
	for i := 0; i < 2; i++ {
		sched.executeSync(source.ID)
		got, err := database.GetSourceByID(source.ID)
		if err != nil {
			t.Fatalf("GetSourceByID: %v", err)
		}
		if got.LastSyncStatus != db.SyncStatusSuccess {
			t.Fatalf("cycle %d: sync status %q (%s), want success", i+1, got.LastSyncStatus, got.LastSyncMessage)
		}
	}

	var hash sql.NullString
	var adaptive sql.NullInt64
	row := database.Conn().QueryRow(`SELECT last_content_hash, adaptive_interval FROM sources WHERE id = ?`, source.ID)
	if err := row.Scan(&hash, &adaptive); err != nil {
		t.Fatalf("scan adaptive columns: %v", err)
	}
	if hash.Valid || adaptive.Valid {
		t.Errorf("adaptive columns written: last_content_hash=%v adaptive_interval=%v, want both NULL", hash, adaptive)
	}

	sched.mu.RLock()
	job := sched.jobs[source.ID]
	sched.mu.RUnlock()
	if job == nil {
		t.Fatal("job missing after executeSync")
	}
	if job.interval != interval {
		t.Errorf("job interval = %v, want source.SyncInterval %v", job.interval, interval)
	}
}
