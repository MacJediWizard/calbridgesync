// Package backup provides automated SQLite database backup with
// retention-based cleanup. The Manager runs a daily backup via
// SQLite's VACUUM INTO command (atomic, WAL-safe) and compresses
// the result with gzip. Old backups are purged to stay within a
// configurable retention count.
//
// Designed to be driven by the scheduler's daily cleanup routine
// rather than as a standalone daemon. The Manager is stateless
// between calls — it reads the backup directory on each
// invocation to count existing backups for purge decisions.
package backup

import (
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Manager handles automated database backups.
type Manager struct {
	dbPath         string
	backupDir      string
	retentionCount int
}

// New creates a backup manager. backupDir is created if it doesn't
// exist. retentionCount is the maximum number of backup files to
// keep; oldest are deleted when exceeded.
func New(dbPath, backupDir string, retentionCount int) (*Manager, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if backupDir == "" {
		return nil, fmt.Errorf("backup directory is required")
	}
	if retentionCount < 1 {
		retentionCount = 7
	}
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create backup directory %s: %w", backupDir, err)
	}
	return &Manager{
		dbPath:         dbPath,
		backupDir:      backupDir,
		retentionCount: retentionCount,
	}, nil
}

// RunBackup creates an atomic backup of the SQLite database,
// compressed with gzip. Returns the path to the backup file.
//
// Uses VACUUM INTO which is safe to run while the database is
// open and being written to (it takes a snapshot of the current
// state including WAL contents). Requires SQLite 3.27+.
func (m *Manager) RunBackup() (string, error) {
	// Without this check, opening a missing path creates an empty
	// database and VACUUM INTO "succeeds" with an empty backup.
	if _, err := os.Stat(m.dbPath); err != nil {
		return "", fmt.Errorf("database not readable for backup: %w", err)
	}

	timestamp := time.Now().UTC().Format("20060102-150405Z")
	backupName := fmt.Sprintf("calbridgesync-%s.db.gz", timestamp)
	backupPath := filepath.Join(m.backupDir, backupName)
	tempPath := backupPath + ".tmp"
	tempDBPath := tempPath + ".db"

	if err := m.vacuumInto(tempDBPath); err != nil {
		os.Remove(tempDBPath)
		return "", err
	}
	err := gzipFile(tempDBPath, tempPath)
	os.Remove(tempDBPath)
	if err != nil {
		os.Remove(tempPath)
		return "", err
	}

	if err := os.Rename(tempPath, backupPath); err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to finalize backup: %w", err)
	}

	info, _ := os.Stat(backupPath)
	if info != nil {
		log.Printf("Backup created: %s (%.1f MB)", backupPath, float64(info.Size())/(1024*1024))
	}

	return backupPath, nil
}

// vacuumInto writes an uncompressed snapshot of the database to dest
// and restricts it to owner-only access.
func (m *Manager) vacuumInto(dest string) error {
	db, err := sql.Open("sqlite", m.dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return fmt.Errorf("failed to open database for backup: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("VACUUM INTO failed: %w", err)
	}
	if err := os.Chmod(dest, 0600); err != nil {
		return fmt.Errorf("failed to restrict snapshot permissions: %w", err)
	}
	return nil
}

// gzipFile compresses src into a new owner-only file at dest.
func gzipFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open snapshot: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("failed to create gzip file: %w", err)
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		return fmt.Errorf("gzip compression failed: %w", err)
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return fmt.Errorf("gzip compression failed: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("failed to write gzip file: %w", err)
	}
	return nil
}

// PurgeOldBackups deletes the oldest backups beyond the retention
// count. Returns the number of backups deleted.
func (m *Manager) PurgeOldBackups() (int, error) {
	entries, err := os.ReadDir(m.backupDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read backup directory: %w", err)
	}

	// Filter to only our backup files
	var backups []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "calbridgesync-") && strings.HasSuffix(e.Name(), ".db.gz") {
			backups = append(backups, e)
		}
	}

	if len(backups) <= m.retentionCount {
		return 0, nil
	}

	// Sort by name (timestamp-based names sort chronologically)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name() < backups[j].Name()
	})

	// Delete oldest beyond retention
	toDelete := backups[:len(backups)-m.retentionCount]
	deleted := 0
	for _, b := range toDelete {
		path := filepath.Join(m.backupDir, b.Name())
		if err := os.Remove(path); err != nil {
			log.Printf("Failed to delete old backup %s: %v", path, err)
		} else {
			deleted++
		}
	}

	if deleted > 0 {
		log.Printf("Purged %d old backup(s), keeping %d", deleted, m.retentionCount)
	}

	return deleted, nil
}
