package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestNew_PragmasApplyToEveryConnection guards against PRAGMAs being set on
// only one pooled connection. SQLite PRAGMAs are per-connection, so they must
// be applied by the driver whenever it opens a new connection (issue #188).
func TestNew_PragmasApplyToEveryConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	const n = 5
	conns := make([]*sql.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	// Hold all connections at once so the pool must open distinct ones.
	for i := 0; i < n; i++ {
		c, err := database.Conn().Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}

	want := map[string]string{
		"foreign_keys":  "1",
		"busy_timeout":  "30000",
		"journal_mode":  "wal",
		"secure_delete": "1",
		"synchronous":   "1", // NORMAL
	}
	for i, c := range conns {
		for pragma, expected := range want {
			var got string
			if err := c.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatalf("conn %d: PRAGMA %s: %v", i, pragma, err)
			}
			if got != expected {
				t.Errorf("conn %d: PRAGMA %s = %q, want %q", i, pragma, got, expected)
			}
		}
	}
}

// TestNew_SidecarFilePermissions checks that the WAL and shared-memory files,
// which hold the same data as the main database file, are restricted to 0600.
func TestNew_SidecarFilePermissions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if mode := info.Mode().Perm(); mode != 0600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), mode)
		}
	}
}
