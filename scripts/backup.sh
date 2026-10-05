#!/usr/bin/env bash
#
# backup.sh — safe pre-deploy snapshot of the calbridgesync SQLite database.
#
# Uses SQLite's VACUUM INTO, an online snapshot that produces a
# consistent single-file copy including WAL contents while the service
# keeps running. Copying the .db/.db-wal/.db-shm files with cp is NOT
# safe on a live database: the files can change between copies and the
# result may be torn.
#
# Usage:
#   scripts/backup.sh [DB_PATH] [BACKUP_DIR]
#
# Defaults:
#   DB_PATH     = ./data/calbridgesync.db
#   BACKUP_DIR  = ./data/backups
#
# Exit codes:
#   0  success
#   1  database file not found
#   2  backup failed (sqlite3 missing, permissions, disk full)
#   3  integrity check failed
#
# Requires sqlite3 on the host running this script. If the database
# lives in a Docker volume the host user can't read, run the backup in
# a throwaway container that mounts the volume instead:
#
#   docker run --rm -v <volume>:/data alpine:3.20 sh -c \
#     'apk add -q sqlite && sqlite3 /data/calbridgesync.db ".backup /data/backups/manual.db"'
#
# Designed to be run MANUALLY before any deploy that touches the DB
# (schema migration, binary upgrade, container restart) and inside
# CI/CD pipelines as a pre-migration step.

set -euo pipefail

DB_PATH="${1:-./data/calbridgesync.db}"
BACKUP_DIR="${2:-./data/backups}"

# Normalize paths
DB_PATH="${DB_PATH%/}"
BACKUP_DIR="${BACKUP_DIR%/}"

if [[ ! -f "${DB_PATH}" ]]; then
    echo "ERROR: database not found at: ${DB_PATH}" >&2
    echo "Pass the path as the first argument, or set the default." >&2
    exit 1
fi

mkdir -p "${BACKUP_DIR}"

# Generate a backup identifier from the current timestamp + (if
# available) the git commit SHA of the calbridgesync checkout running
# this script. Operators can correlate backups with the code version
# that was about to be deployed.
TIMESTAMP="$(date -u +%Y%m%d-%H%M%SZ)"
if SHA="$(git rev-parse --short HEAD 2>/dev/null)"; then
    BACKUP_ID="${TIMESTAMP}-${SHA}"
else
    BACKUP_ID="${TIMESTAMP}"
fi

DEST_DIR="${BACKUP_DIR}/${BACKUP_ID}"
mkdir -p "${DEST_DIR}"

DB_BASENAME="$(basename "${DB_PATH}")"
DEST="${DEST_DIR}/${DB_BASENAME}"

if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "ERROR: sqlite3 not found on host — refusing to cp a live WAL database." >&2
    echo "       Install sqlite3 or use the container method in this script's header." >&2
    exit 2
fi

echo "Backing up ${DB_PATH} → ${DEST}"

# VACUUM INTO is an online, WAL-safe snapshot. The path is a SQL string
# literal, so single quotes are doubled. -init /dev/null ignores any
# ~/.sqliterc whose settings (timer, changes) would alter the output.
q="'"
if ! sqlite3 -init /dev/null "${DB_PATH}" "VACUUM INTO '${DEST//$q/$q$q}';"; then
    echo "ERROR: sqlite3 VACUUM INTO failed" >&2
    exit 2
fi
chmod 600 "${DEST}"

# A failing check means the source DB (and therefore this backup) is
# corrupt. The backup is preserved on disk so the operator can inspect
# it, but the exit code signals the failure so any CI/CD pipeline halts.
echo -n "Verifying backup integrity... "
if INTEGRITY="$(sqlite3 -init /dev/null "${DEST}" "PRAGMA integrity_check;" 2>&1)"; then
    if [[ "${INTEGRITY}" == "ok" ]]; then
        echo "ok"
    else
        echo "FAILED"
        echo "ERROR: integrity check returned: ${INTEGRITY}" >&2
        echo "       the source database at ${DB_PATH} may be corrupt" >&2
        echo "       the backup is preserved at ${DEST_DIR} for inspection" >&2
        exit 3
    fi
else
    echo "ERROR: sqlite3 invocation failed" >&2
    exit 3
fi

echo ""
echo "Backup complete: ${DEST_DIR}"
echo ""
echo "To restore:"
echo "  1. Stop the calbridgesync container/process"
echo "  2. rm -f ${DB_PATH}-wal ${DB_PATH}-shm && cp ${DEST} ${DB_PATH}"
echo "  3. Restart the service"
