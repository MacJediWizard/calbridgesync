#!/usr/bin/env bash
#
# backup.sh — safe pre-deploy snapshot of the calbridgesync SQLite database.
#
# Copies the main .db file together with its .db-wal and .db-shm siblings
# to a timestamped backup directory. SQLite in WAL mode writes the most
# recent transactions to the -wal file; a copy of only the main .db
# would silently drop those writes, so ALL three files must be backed
# up as a unit.
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
#   2  copy failed (permissions, disk full)
#   3  integrity check failed (if sqlite3 is available)
#
# Integrity check: if sqlite3 is installed on the host running this
# script, the freshly-copied backup is verified with
# "PRAGMA integrity_check" before the script exits. A failing check
# indicates the source DB is corrupt — DO NOT DEPLOY.
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

echo "Backing up ${DB_PATH} → ${DEST_DIR}/"

# Main DB file
if ! cp "${DB_PATH}" "${DEST_DIR}/${DB_BASENAME}"; then
    echo "ERROR: failed to copy main DB file" >&2
    exit 2
fi

# Write-ahead log: only exists if the DB has uncommitted transactions.
# In normal operation with WAL mode enabled it's almost always present.
if [[ -f "${DB_PATH}-wal" ]]; then
    if ! cp "${DB_PATH}-wal" "${DEST_DIR}/${DB_BASENAME}-wal"; then
        echo "ERROR: failed to copy WAL file — backup is incomplete" >&2
        exit 2
    fi
    echo "  ✓ ${DB_BASENAME}-wal"
fi

# Shared memory file: session-local, can be safely omitted but we copy
# it for completeness so restores are bit-identical.
if [[ -f "${DB_PATH}-shm" ]]; then
    if ! cp "${DB_PATH}-shm" "${DEST_DIR}/${DB_BASENAME}-shm"; then
        echo "WARNING: failed to copy shared memory file — restore may recreate it" >&2
        # Don't exit — .db-shm is reconstructable
    else
        echo "  ✓ ${DB_BASENAME}-shm"
    fi
fi

echo "  ✓ ${DB_BASENAME}"

# Integrity check if sqlite3 is available. A failing check means the
# source DB (and therefore this backup) is corrupt. The backup is
# preserved on disk so the operator can inspect it, but the exit
# code signals the failure so any CI/CD pipeline halts.
if command -v sqlite3 >/dev/null 2>&1; then
    echo -n "Verifying backup integrity... "
    if INTEGRITY="$(sqlite3 "${DEST_DIR}/${DB_BASENAME}" "PRAGMA integrity_check;" 2>&1)"; then
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
else
    echo "NOTE: sqlite3 not found on host — skipping integrity verification."
    echo "      Install sqlite3 for stronger backup guarantees."
fi

echo ""
echo "Backup complete: ${DEST_DIR}"
echo ""
echo "To restore:"
echo "  1. Stop the calbridgesync container/process"
echo "  2. cp ${DEST_DIR}/${DB_BASENAME}* \$(dirname ${DB_PATH})/"
echo "  3. Restart the service"
