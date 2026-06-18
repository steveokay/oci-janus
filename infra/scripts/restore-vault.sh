#!/usr/bin/env bash
#
# restore-vault.sh — restore a Vault Raft cluster from a snapshot.
#
# CAUTION: This OVERWRITES Vault state. Use only against a fresh cluster
# during DR, or a deliberately quarantined Vault you're rebuilding.
#
# Usage:
#   restore-vault.sh <snapshot-key|latest> <vault-addr> <root-token>
#
# Examples:
#   restore-vault.sh latest http://vault.registry.svc:8200 s.xxxxx
#   restore-vault.sh production/vault/2026/06/18/raft-snapshot-...snap \
#       http://vault.registry.svc:8200 s.xxxxx
#
# Env vars (required):
#   BACKUP_BUCKET, AWS_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
#   AWS_ENDPOINT_URL (for MinIO)
#
# Procedure (from Vault docs — https://developer.hashicorp.com/vault/docs/concepts/integrated-storage):
#   1. Vault must be UNSEALED before restore.
#   2. The restore is performed via /sys/storage/raft/snapshot-force which
#      bypasses the leader-must-match check — required for cross-region DR.
#   3. After restore, EVERY existing token (including the root token used
#      to call this API) is invalidated. You need an UNSEAL key + the
#      ORIGINAL root token from the snapshotted cluster to do anything else.
#   4. The auto-unseal config (KMS keys, recovery keys) is in the snapshot
#      too — pointing the restored Vault at a different KMS will brick it.

set -Eeuo pipefail

usage() {
    echo "usage: $0 <snapshot-key|latest> <vault-addr> <root-token>" >&2
    echo "  e.g. $0 latest http://vault:8200 s.xxxxx" >&2
    exit 64
}

[[ $# -eq 3 ]] || usage
snapshot_arg="$1"
vault_addr="$2"
vault_token="$3"

: "${BACKUP_BUCKET:?BACKUP_BUCKET required}"
BACKUP_ENV="${BACKUP_ENV:-production}"
AWS_REGION="${AWS_REGION:-us-east-1}"
export AWS_REGION
export AWS_DEFAULT_REGION="${AWS_REGION}"

if [[ "${snapshot_arg}" == "latest" ]]; then
    pointer_key="${BACKUP_ENV}/vault/latest.txt"
    echo "[restore-vault] resolving latest via s3://${BACKUP_BUCKET}/${pointer_key}"
    snapshot_key=$(aws s3 cp "s3://${BACKUP_BUCKET}/${pointer_key}" - | tr -d '[:space:]')
    [[ -n "${snapshot_key}" ]] || { echo "[restore-vault] FATAL: latest.txt empty"; exit 2; }
else
    snapshot_key="${snapshot_arg}"
fi

echo "[restore-vault] snapshot:  s3://${BACKUP_BUCKET}/${snapshot_key}"
echo "[restore-vault] target:    ${vault_addr}"

tmpfile="$(mktemp /tmp/vault-restore-XXXXXX.snap)"
trap 'shred -u "${tmpfile}" 2>/dev/null || rm -f "${tmpfile}"' EXIT

echo "[restore-vault] downloading..."
aws s3 cp "s3://${BACKUP_BUCKET}/${snapshot_key}" "${tmpfile}" --only-show-errors

bytes=$(stat -c %s "${tmpfile}" 2>/dev/null || wc -c < "${tmpfile}")
[[ "${bytes}" -gt 100 ]] || { echo "[restore-vault] FATAL: snapshot suspiciously small (${bytes} bytes)"; exit 2; }
echo "[restore-vault] downloaded ${bytes} bytes"

# Sanity check Vault is up + unsealed before we attempt restore.
health=$(curl -sS --fail "${vault_addr%/}/v1/sys/health" || true)
if [[ -z "${health}" ]]; then
    echo "[restore-vault] FATAL: cannot reach vault at ${vault_addr}/v1/sys/health" >&2
    exit 4
fi
if echo "${health}" | grep -q '"sealed":true'; then
    echo "[restore-vault] FATAL: vault is sealed — unseal it before restore" >&2
    exit 4
fi
echo "[restore-vault] vault is reachable and unsealed."

# Force restore — see header comment for why -force is the right choice
# in a DR (the alternative requires this Vault to already be the raft leader
# of a healthy cluster, which it isn't in a DR).
echo "[restore-vault] uploading snapshot (this WIPES the current Vault state)"
http_code=$(curl --silent --show-error \
    --output /tmp/vault-restore-resp.txt \
    --write-out '%{http_code}' \
    --header "X-Vault-Token: ${vault_token}" \
    --request POST \
    --data-binary "@${tmpfile}" \
    "${vault_addr%/}/v1/sys/storage/raft/snapshot-force")

if [[ "${http_code}" != "204" && "${http_code}" != "200" ]]; then
    echo "[restore-vault] FATAL: vault returned HTTP ${http_code}" >&2
    cat /tmp/vault-restore-resp.txt >&2 || true
    exit 5
fi

echo "[restore-vault] DONE — vault restored from ${snapshot_key}"
echo
echo "NEXT STEPS:"
echo "  1. The token you used is now INVALIDATED. Re-authenticate with the"
echo "     original root token from the snapshotted cluster."
echo "  2. Verify the signer key still exists:"
echo "       vault read transit/keys/registry-signer"
echo "  3. Re-seed any new tokens / policies that have changed since the"
echo "     snapshot was taken (refer to your change log)."
