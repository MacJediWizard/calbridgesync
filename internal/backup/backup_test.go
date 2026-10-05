package backup

import (
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// newTestDB creates a WAL-mode SQLite database with one row that has
// not been checkpointed, so a backup that ignored the WAL would miss it.
func newTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "calbridgesync.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT)",
		"INSERT INTO users VALUES ('u1', 'a@example.com')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return dbPath
}

// restore gunzips a backup to a temp file and returns the email of user u1.
func restore(t *testing.T, gzPath string) string {
	t.Helper()
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "restored.db")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create restored: %v", err)
	}
	if _, err := io.Copy(out, zr); err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close restored: %v", err)
	}

	db, err := sql.Open("sqlite", outPath)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer db.Close()
	var email string
	if err := db.QueryRow("SELECT email FROM users WHERE id = 'u1'").Scan(&email); err != nil {
		t.Fatalf("query restored: %v", err)
	}
	return email
}

func TestRunBackup(t *testing.T) {
	for _, dirName := range []string{"backups", "it's backups"} {
		t.Run(dirName, func(t *testing.T) {
			dbPath := newTestDB(t)
			m, err := New(dbPath, filepath.Join(t.TempDir(), dirName), 7)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			path, err := m.RunBackup()
			if err != nil {
				t.Fatalf("RunBackup: %v", err)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat backup: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0600 {
				t.Errorf("backup mode = %o, want 600", perm)
			}
			if got := restore(t, path); got != "a@example.com" {
				t.Errorf("restored email = %q, want a@example.com", got)
			}

			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatalf("read backup dir: %v", err)
			}
			if len(entries) != 1 {
				t.Errorf("backup dir has %d entries, want only the .gz (temp files left behind?)", len(entries))
			}
		})
	}
}

func TestRunBackupMissingDB(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "missing.db"), t.TempDir(), 7)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := m.RunBackup(); err == nil {
		t.Fatal("RunBackup on a missing database returned nil error")
	}
}

func TestPurgeOldBackups(t *testing.T) {
	dir := t.TempDir()
	m, err := New(filepath.Join(dir, "db"), dir, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("calbridgesync-2026010%d-000000Z.db.gz", i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	deleted, err := m.PurgeOldBackups()
	if err != nil {
		t.Fatalf("PurgeOldBackups: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	for i, want := range map[int]bool{1: false, 2: false, 3: true, 4: true, 5: true} {
		name := fmt.Sprintf("calbridgesync-2026010%d-000000Z.db.gz", i)
		_, err := os.Stat(filepath.Join(dir, name))
		if exists := err == nil; exists != want {
			t.Errorf("%s exists = %v, want %v", name, exists, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated.txt")); err != nil {
		t.Errorf("unrelated file was removed: %v", err)
	}
}
