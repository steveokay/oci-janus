#!/usr/bin/env bash
#
# restore-postgres.sh — restore one registry Postgres DB from an S3 backup.
#
# Designed to be run from an operator workstation (or a one-shot pod using
# ghcr.io/steveokay/backup-tools) DURING a DR. Not a CronJob — restore is
# always a manual, audited action.
#
# Usage:
#   restore-postgres.sh <db-name> [<backup-key>|latest] <target-dsn>
#
# Examples:
#   restore-postgres.sh metadata latest \
#       postgres://registry_metadata_app:PASS@postgres:5432/registry_metadata?sslmode=require
#
#   restore-postgres.sh audit \
#       production/postgres/audit/2026/06/18/dump-20260618T020512Z.pgcustom \
#       postgres://...
#
# Env vars (required):
#   BACKUP_BUCKET                 — S3 bucket holding the dumps
#   AWS_ACCESS_KEY_ID / SECRET    — credentials for the backup bucket
#   AWS_REGION                    — bucket region (defaults to us-east-1)
#   AWS_ENDPOINT_URL              — set for MinIO; leave empty for AWS S3
#
# Behaviour:
#   1. Resolves "latest" to a real key via BUCKET/$ENV/postgres/$DB/latest.txt
#   2. Downloads the dump to /tmp
#   3. Verifies the file starts with the pg_dump custom-format magic bytes
#   4. Calls pg_restore --clean --if-exists --single-transaction
#   5. Runs `goose status` against the restored DB IF GOOSE_MIGRATIONS_DIR
#      is set, and warns if migration state isn't what the running service
#      expects (operator decides whether to roll forward).
#
# Notes:
#   * --single-transaction means partial restores leave the DB untouched
#     if anything fails — safer than a half-restored DB the service then
#     limps along with.
#   * The script does NOT update goose state; if the dump was taken from a
#     newer schema than the binary about to start, you must upgrade the
#     binary, not downgrade the DB.

set -Eeuo pipefail

usage() {
    echo "usage: $0 <db-name> <backup-key|latest> <target-dsn>" >&2
    exit 64
}

[[ $# -eq 3 ]] || usage
db_name="$1"
backup_arg="$2"
target_dsn="$3"

: "${BACKUP_BUCKET:?BACKUP_BUCKET required}"
BACKUP_ENV="${BACKUP_ENV:-production}"
AWS_REGION="${AWS_REGION:-us-east-1}"
export AWS_REGION
export AWS_DEFAULT_REGION="${AWS_REGION}"

echo "[restore-postgres] target db: ${db_name}"
echo "[restore-postgres] bucket:    s3://${BACKUP_BUCKET}/${BACKUP_ENV}/postgres/${db_name}/"

# Resolve "latest" by reading the pointer file.
if [[ "${backup_arg}" == "latest" ]]; then
    pointer_key="${BACKUP_ENV}/postgres/${db_name}/latest.txt"
    echo "[restore-postgres] resolving latest via s3://${BACKUP_BUCKET}/${pointer_key}"
    backup_key=$(aws s3 cp "s3://${BACKUP_BUCKET}/${pointer_key}" - | tr -d '[:space:]')
    [[ -n "${backup_key}" ]] || { echo "[restore-postgres] FATAL: latest.txt empty"; exit 2; }
else
    backup_key="${backup_arg}"
fi

echo "[restore-postgres] backup key: ${backup_key}"

tmpfile="$(mktemp /tmp/restore-${db_name}-XXXXXX.pgcustom)"
trap 'shred -u "${tmpfile}" 2>/dev/null || rm -f "${tmpfile}"' EXIT

echo "[restore-postgres] downloading..."
aws s3 cp "s3://${BACKUP_BUCKET}/${backup_key}" "${tmpfile}" --only-show-errors

bytes=$(stat -c %s "${tmpfile}" 2>/dev/null || wc -c < "${tmpfile}")
echo "[restore-postgres] downloaded ${bytes} bytes"

# Magic-byte check — same one the backup script does on the way in. Catches
# accidentally restoring a stray .txt or a half-uploaded object.
head -c 5 "${tmpfile}" | grep -q '^PGDMP' \
    || { echo "[restore-postgres] FATAL: file is not pg_dump custom format"; exit 2; }

# Refuse to run against a non-empty DB unless --force is passed via env.
# Operators routinely lose their place during a multi-DB restore and this
# guards against clobbering the wrong target.
existing_rows=$(psql "${target_dsn}" -At -c "SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace=(SELECT oid FROM pg_namespace WHERE nspname='public');" 2>/dev/null || echo "0")
if [[ "${existing_rows}" -gt 0 && "${FORCE:-0}" != "1" ]]; then
    echo "[restore-postgres] target DB already has ${existing_rows} tables in public schema." >&2
    echo "[restore-postgres] refusing to restore over a non-empty DB. Set FORCE=1 to override." >&2
    exit 3
fi

echo "[restore-postgres] running pg_restore --clean --if-exists --single-transaction"
pg_restore \
    --dbname="${target_dsn}" \
    --clean \
    --if-exists \
    --single-transaction \
    --no-owner \
    --no-privileges \
    --exit-on-error \
    "${tmpfile}"

echo "[restore-postgres] pg_restore complete."

# Optional schema-version sanity check. If the dump is from a newer schema
# than the binary about to start, the binary will reject the migration
# version mismatch and refuse to serve — far better to know now than after
# you've cut traffic over.
if [[ -n "${GOOSE_MIGRATIONS_DIR:-}" ]]; then
    echo "[restore-postgres] checking goose status against ${GOOSE_MIGRATIONS_DIR}"
    if command -v goose >/dev/null 2>&1; then
        goose -dir "${GOOSE_MIGRATIONS_DIR}" postgres "${target_dsn}" status || \
            echo "[restore-postgres] WARNING: goose status reported drift — review before starting the service"
    else
        echo "[restore-postgres] goose binary not found in PATH — skipping schema check"
    fi
fi

echo "[restore-postgres] DONE ${db_name}"
