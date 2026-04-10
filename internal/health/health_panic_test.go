package health

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCheck_RecoversFromPanicInCheckFunction verifies Issue #37's core
// contract for health: if a check function panics (e.g. nil pointer
// dereference on an uninitialized db handle), the Check method must
// recover, populate the report with a synthetic "Unhealthy" entry for
// that check, AND allow the other checks to complete normally.
//
// Setup: construct a Checker with db=nil. checkDatabase's first line
// calls c.db.Ping() which triggers a nil pointer panic immediately.
// If recovery is broken, either the whole test binary crashes OR
// wg.Wait() hangs forever and the test times out.
func TestCheck_RecoversFromPanicInCheckFunction(t *testing.T) {
	// Checker with nil db — database check will panic. OIDC issuer is
	// empty so the OIDC check returns a "degraded" status quickly and
	// safely. CalDAV URL is empty so that check is skipped entirely.
	c := &Checker{
		db:         nil,
		oidcIssuer: "",
		caldavURL:  "",
	}

	// Bound the test so it can't hang even if the recovery is broken.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan *Report, 1)
	go func() {
		done <- c.Check(ctx)
	}()

	select {
	case report := <-done:
		// Database check must have a synthetic unhealthy entry from
		// the recover path.
		dbCheck, ok := report.Checks["database"]
		if !ok {
			t.Fatal("expected database entry in report.Checks even on panic")
		}
		if dbCheck.Status != StatusUnhealthy {
			t.Errorf("expected database Status=%q on panic, got %q", StatusUnhealthy, dbCheck.Status)
		}
		if !strings.Contains(dbCheck.Message, "panic") {
			t.Errorf("expected database Message to mention panic, got %q", dbCheck.Message)
		}

		// The OIDC check should still have completed normally.
		oidcCheck, ok := report.Checks["oidc"]
		if !ok {
			t.Fatal("expected oidc entry in report.Checks; a panic in one check must not prevent others from completing")
		}
		// Empty issuer → degraded
		if oidcCheck.Status != StatusDegraded {
			t.Errorf("expected oidc Status=%q for empty issuer, got %q", StatusDegraded, oidcCheck.Status)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("Check() did not return within 3s; panic recovery must be broken and wg.Wait() is hanging")
	}
}
