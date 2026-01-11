package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // SQLite driver
)

var (
	ErrNotFound     = errors.New("record not found")
	ErrDuplicate    = errors.New("duplicate record")
	ErrDatabaseInit = errors.New("database initialization failed")
)

// DB represents the database connection.
type DB struct {
	conn *sql.DB
}

// New creates a new database connection and initializes the schema.
func New(dbPath string) (*DB, error) {
	// Ensure the directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("%w: failed to create directory: %w", ErrDatabaseInit, err)
	}

	// Open the database
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open database: %w", ErrDatabaseInit, err)
	}

	// Configure connection pool limits to prevent resource exhaustion
	// SQLite handles concurrency differently than other databases, but these
	// limits still help prevent file descriptor exhaustion and memory issues
	conn.SetMaxOpenConns(25)       // Maximum number of open connections
	conn.SetMaxIdleConns(5)        // Maximum idle connections in pool
	conn.SetConnMaxLifetime(0)     // Connections are reused forever
	conn.SetConnMaxIdleTime(0)     // Idle connections are kept forever

	// Configure SQLite for optimal performance and security
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA secure_delete=ON",
		"PRAGMA synchronous=NORMAL",
	}

	for _, pragma := range pragmas {
		if _, err := conn.Exec(pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("%w: failed to set pragma: %w", ErrDatabaseInit, err)
		}
	}

	db := &DB{conn: conn}

	// Run migrations
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}

	// Set file permissions (0600 for security)
	if err := os.Chmod(dbPath, 0600); err != nil {
		// Log warning but don't fail - file might not exist yet in WAL mode
		_ = err
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}

// Conn returns the underlying database connection.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// migrate creates the database schema.
func (db *DB) migrate() error {
	migrations := []string{
		// Users table
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Sources table
		`CREATE TABLE IF NOT EXISTS sources (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'custom',
			source_url TEXT NOT NULL,
			source_username TEXT NOT NULL,
			source_password TEXT NOT NULL,
			dest_url TEXT NOT NULL,
			dest_username TEXT NOT NULL,
			dest_password TEXT NOT NULL,
			sync_interval INTEGER NOT NULL DEFAULT 300,
			conflict_strategy TEXT NOT NULL DEFAULT 'source_wins',
			enabled INTEGER NOT NULL DEFAULT 1,
			last_sync_at DATETIME,
			last_sync_status TEXT NOT NULL DEFAULT 'pending',
			last_sync_message TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,

		// Index on user_id for sources
		`CREATE INDEX IF NOT EXISTS idx_sources_user_id ON sources(user_id)`,

		// Sync states table
		`CREATE TABLE IF NOT EXISTS sync_states (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			calendar_href TEXT NOT NULL,
			sync_token TEXT,
			ctag TEXT,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_id, calendar_href),
			FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
		)`,

		// Index on source_id for sync_states
		`CREATE INDEX IF NOT EXISTS idx_sync_states_source_id ON sync_states(source_id)`,

		// Sync logs table
		`CREATE TABLE IF NOT EXISTS sync_logs (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			status TEXT NOT NULL,
			message TEXT,
			details TEXT,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
		)`,

		// Index on source_id and created_at for sync_logs
		`CREATE INDEX IF NOT EXISTS idx_sync_logs_source_id ON sync_logs(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_logs_created_at ON sync_logs(created_at DESC)`,

		// Migration: Add sync_direction column to sources
		`ALTER TABLE sources ADD COLUMN sync_direction TEXT NOT NULL DEFAULT 'one_way'`,

		// Migration: Add detailed stats columns to sync_logs
		`ALTER TABLE sync_logs ADD COLUMN events_created INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sync_logs ADD COLUMN events_updated INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sync_logs ADD COLUMN events_deleted INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sync_logs ADD COLUMN events_skipped INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sync_logs ADD COLUMN calendars_synced INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sync_logs ADD COLUMN events_processed INTEGER NOT NULL DEFAULT 0`,

		// Synced events table for deletion tracking in two-way sync
		`CREATE TABLE IF NOT EXISTS synced_events (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			calendar_href TEXT NOT NULL,
			event_uid TEXT NOT NULL,
			source_etag TEXT,
			dest_etag TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_id, calendar_href, event_uid),
			FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
		)`,

		// Index on source_id and calendar_href for synced_events
		`CREATE INDEX IF NOT EXISTS idx_synced_events_source_calendar ON synced_events(source_id, calendar_href)`,

		// Malformed events table for tracking corrupted calendar events
		`CREATE TABLE IF NOT EXISTS malformed_events (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			event_path TEXT NOT NULL,
			error_message TEXT NOT NULL,
			discovered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_id, event_path),
			FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
		)`,

		// Index on source_id for malformed_events
		`CREATE INDEX IF NOT EXISTS idx_malformed_events_source_id ON malformed_events(source_id)`,

		// Migration: Add selected_calendars column to sources (JSON array of calendar paths)
		`ALTER TABLE sources ADD COLUMN selected_calendars TEXT`,
	}

	for _, migration := range migrations {
		if _, err := db.conn.Exec(migration); err != nil {
			// Ignore "duplicate column" errors for ALTER TABLE migrations
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("%w: migration failed: %w", ErrDatabaseInit, err)
			}
		}
	}

	return nil
}

// isDuplicateColumnError checks if the error is due to a duplicate column in ALTER TABLE.
func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "duplicate column") || strings.Contains(errStr, "already exists")
}

// Ping checks the database connection.
func (db *DB) Ping() error {
	return db.conn.Ping()
}
