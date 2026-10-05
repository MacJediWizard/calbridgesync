package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestMigrate_Idempotent checks that migrate() can run again on an existing
// database (it runs on every startup) and that the oidc_subject column and
// its partial unique index exist afterwards.
func TestMigrate_Idempotent(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "migrate.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	if err := db.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.migrate(); err != nil {
		t.Fatalf("third migrate: %v", err)
	}

	// Several users without a subject must coexist (partial index).
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if _, err := db.GetOrCreateUser(email, "x"); err != nil {
			t.Fatalf("GetOrCreateUser(%s): %v", email, err)
		}
	}

	if _, err := db.conn.Exec(`UPDATE users SET oidc_subject = 'dup' WHERE email = 'a@example.com'`); err != nil {
		t.Fatalf("set subject: %v", err)
	}
	if _, err := db.conn.Exec(`UPDATE users SET oidc_subject = 'dup' WHERE email = 'b@example.com'`); err == nil {
		t.Fatal("expected unique index to reject a duplicate oidc_subject")
	}
}

// TestMigrate_UpgradeBindsLegacyUser opens a database whose users table
// predates oidc_subject, as production's does, and checks the existing user
// keeps its ID and is bound on first login.
func TestMigrate_UpgradeBindsLegacyUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		email TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO users (id, email, name) VALUES ('legacy-id', 'old@example.com', 'Old')`); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	raw.Close()

	db, err := New(path)
	if err != nil {
		t.Fatalf("New on legacy db: %v", err)
	}
	defer db.Close()

	user, err := db.GetOrBindUserBySubject("sub-old", "old@example.com", "Old", true)
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if user.ID != "legacy-id" || user.OIDCSubject != "sub-old" {
		t.Errorf("got id %q subject %q, want legacy-id bound to sub-old", user.ID, user.OIDCSubject)
	}
}

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
