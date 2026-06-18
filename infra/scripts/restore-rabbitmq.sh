#!/usr/bin/env bash
#
# restore-rabbitmq.sh — re-import RabbitMQ definitions (exchanges, queues,
# bindings, policies, users) from an S3-stored definitions JSON.
#
# IMPORTANT: this restores the broker TOPOLOGY only — not in-flight messages.
# Messages on the broker at the time of loss are lost; the platform's
# durability story (CLAUDE.md §6) is that consumers re-derive state from
# Postgres, so the topology is the only thing that needs to come back.
#
# Usage:
#   restore-rabbitmq.sh <definitions-key|latest> <mgmt-url> <user> <password>
#
# Examples:
#   restore-rabbitmq.sh latest http://rabbitmq:15672 admin XXX
#
# Env vars (required):
#   BACKUP_BUCKET, AWS_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
#   AWS_ENDPOINT_URL (for MinIO)
#
# Behaviour:
#   POST /api/definitions with the JSON body. Existing topology is MERGED —
#   queues that exist already are left alone, new ones are created.
#   To get a clean state, tear down the broker first.

set -Eeuo pipefail

usage() {
    echo "usage: $0 <definitions-key|latest> <mgmt-url> <user> <password>" >&2
    exit 64
}

[[ $# -eq 4 ]] || usage
def_arg="$1"
mgmt_url="$2"
user="$3"
password="$4"

: "${BACKUP_BUCKET:?BACKUP_BUCKET required}"
BACKUP_ENV="${BACKUP_ENV:-production}"
AWS_REGION="${AWS_REGION:-us-east-1}"
export AWS_REGION
export AWS_DEFAULT_REGION="${AWS_REGION}"

if [[ "${def_arg}" == "latest" ]]; then
    pointer_key="${BACKUP_ENV}/rabbitmq/latest.txt"
    echo "[restore-rabbitmq] resolving latest via s3://${BACKUP_BUCKET}/${pointer_key}"
    def_key=$(aws s3 cp "s3://${BACKUP_BUCKET}/${pointer_key}" - | tr -d '[:space:]')
    [[ -n "${def_key}" ]] || { echo "[restore-rabbitmq] FATAL: latest.txt empty"; exit 2; }
else
    def_key="${def_arg}"
fi

echo "[restore-rabbitmq] definitions: s3://${BACKUP_BUCKET}/${def_key}"
echo "[restore-rabbitmq] target:      ${mgmt_url}"

tmpfile="$(mktemp /tmp/rabbitmq-restore-XXXXXX.json)"
trap 'rm -f "${tmpfile}"' EXIT

aws s3 cp "s3://${BACKUP_BUCKET}/${def_key}" "${tmpfile}" --only-show-errors

# Validate the shape before posting — same check the backup script does.
jq -e '.queues and .exchanges' "${tmpfile}" >/dev/null \
    || { echo "[restore-rabbitmq] FATAL: file missing queues/exchanges"; exit 2; }

queue_count=$(jq '.queues | length' "${tmpfile}")
exchange_count=$(jq '.exchanges | length' "${tmpfile}")
echo "[restore-rabbitmq] will import ${queue_count} queues, ${exchange_count} exchanges"

http_code=$(curl --silent --show-error \
    --output /tmp/rabbitmq-restore-resp.txt \
    --write-out '%{http_code}' \
    --user "${user}:${password}" \
    --request POST \
    --header "Content-Type: application/json" \
    --data-binary "@${tmpfile}" \
    "${mgmt_url%/}/api/definitions")

if [[ "${http_code}" != "200" && "${http_code}" != "204" ]]; then
    echo "[restore-rabbitmq] FATAL: management API returned HTTP ${http_code}" >&2
    cat /tmp/rabbitmq-restore-resp.txt >&2 || true
    exit 5
fi

echo "[restore-rabbitmq] DONE — topology restored from ${def_key}"
echo "[restore-rabbitmq] reminder: in-flight messages were NOT restored."
