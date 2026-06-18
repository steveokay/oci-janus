# Disaster Recovery Runbook

> **Applies to:** Production Kubernetes deployments of the registry platform.
> **Read this once before you need it.** A real DR is the wrong time to be reading docs cold.

---

## 1. RTO / RPO targets

Recovery-time objective (RTO) is how long after declaration you have the platform
serving again. Recovery-point objective (RPO) is the maximum data loss measured
in wall-clock time before the incident.

| Data class       | Owner service(s)                                        | RPO target  | RTO target | Backup mechanism                                                   |
| ---------------- | ------------------------------------------------------- | ----------- | ---------- | ------------------------------------------------------------------ |
| Auth / users     | `registry-auth`                                          | 5 min*      | 30 min     | `pg_dump` daily + optional WAL archive (see §4)                    |
| Repo / tag / manifest metadata | `registry-metadata`                       | 1 hour*     | 1 hour     | `pg_dump` daily + optional WAL archive (see §4)                    |
| Tenant directory | `registry-tenant`                                       | 24 hours    | 1 hour     | `pg_dump` daily                                                    |
| Audit log        | `registry-audit`                                        | 1 hour*     | 4 hours    | `pg_dump` daily + WAL archive (compliance retention 7y)            |
| Signer keys      | Vault Transit                                            | 24 hours    | 1 hour     | `vault operator raft snapshot` daily; manual unseal-key escrow     |
| Webhook / proxy / signer DBs | per-service Postgres                        | 24 hours    | 2 hours    | `pg_dump` daily                                                    |
| Blobs (images)   | `registry-storage` → S3 / MinIO / GCS / Azure           | provider SLA | provider SLA | Bucket versioning + cross-region replication (provider feature) |
| RabbitMQ topology | RabbitMQ                                                | 24 hours    | 30 min     | Daily definitions export                                            |
| RabbitMQ messages | RabbitMQ                                                | **n/a**     | n/a        | NOT backed up — consumers re-derive state from Postgres (§7)        |

*RPO can be tightened from 24h to ~5min by enabling WAL archiving on the
Postgres operator (see §4) — the daily `pg_dump` CronJobs are the baseline
that ships with the chart.

---

## 2. Architecture of the backup pipeline

```
                          ┌──────────────────────────────┐
                          │ infra/helm/registry/charts/   │
                          │       backup/                  │
                          │                                │
   CronJob per DB ─────►  │  cronjob-postgres.yaml         │  ─►  pg_dump   ─►  s3://<bucket>/<env>/postgres/<db>/
   CronJob (vault) ────►  │  cronjob-vault.yaml            │  ─►  curl GET  ─►  s3://<bucket>/<env>/vault/
   CronJob (rabbitmq) ──► │  cronjob-rabbitmq.yaml         │  ─►  curl GET  ─►  s3://<bucket>/<env>/rabbitmq/
                          └──────────────────────────────┘
                                          │
                                          ▼
                          ┌──────────────────────────────┐
                          │  Backup target bucket          │
                          │  (separate cloud account!)     │
                          │   * versioning ON              │
                          │   * lifecycle policy           │
                          │   * cross-region replication   │
                          └──────────────────────────────┘
                                          │
   Operator runs restore                  │
   from infra/scripts/        ◄───────────┘
       restore-*.sh
```

All CronJobs use a single backup-tools image (`infra/docker/backup-tools/`)
containing `pg_dump`, `aws-cli`, `curl`, and `jq`. Scripts live in a
ConfigMap so a fix can ship as a chart upgrade without rebuilding images
during an incident.

---

## 3. One-time setup checklist

These are the operator pre-requisites you must complete BEFORE
`backup.enabled: true` does anything useful. None of these are
auto-provisioned — they cross account / org boundaries on purpose.

### 3.1 Backup target bucket

In your **secondary cloud account** (NOT the same account as the data buckets):

```bash
# AWS example — adapt for GCS / Azure / MinIO
aws s3api create-bucket \
    --bucket registry-backups-prod \
    --region us-east-1

# Versioning ON — required so a `aws s3 rm` from a compromised platform
# account can't actually delete the data.
aws s3api put-bucket-versioning \
    --bucket registry-backups-prod \
    --versioning-configuration Status=Enabled,MFADelete=Enabled

# Default at-rest encryption (the backup scripts also set per-object SSE,
# this is belt-and-suspenders).
aws s3api put-bucket-encryption \
    --bucket registry-backups-prod \
    --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'

# Lifecycle: expire noncurrent versions after 90 days, expire current
# objects per retention class (90d / 365d / 7y — see §1 RPO table).
# Apply as a single JSON via put-bucket-lifecycle-configuration; see
# infra/scripts/bucket-lifecycle-template.json (not yet generated — TODO).

# Cross-region replication to a second region.
aws s3api put-bucket-replication \
    --bucket registry-backups-prod \
    --replication-configuration file://crr.json
```

### 3.2 IAM principal for the backup CronJobs

```bash
# Create an IAM user (or IRSA role on EKS, Workload Identity on GKE)
# scoped to s3:PutObject on the backup bucket ONLY. Critical: do NOT grant
# s3:DeleteObject — backup jobs should never be able to delete prior backups.

cat > backup-write-policy.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["s3:PutObject", "s3:PutObjectAcl"],
    "Resource": "arn:aws:s3:::registry-backups-prod/*"
  }]
}
EOF

# Store the access key in your secrets manager → registry-backup-target-credentials.
# The K8s secret must have keys: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY.
```

### 3.3 Per-DB credentials secrets

For each row in `backup.databases` (auth, metadata, tenant, proxy, webhook,
audit, signer), create a K8s secret with these keys:

```bash
kubectl create secret generic registry-backup-creds-metadata -n registry \
    --from-literal=PGHOST=postgres.registry.svc.cluster.local \
    --from-literal=PGPORT=5432 \
    --from-literal=PGUSER=registry_metadata_app \
    --from-literal=PGPASSWORD="$(vault kv get -field=DB_PASSWORD secret/registry/metadata)"
```

The user used here MUST have `pg_dump` rights on the DB. The
`registry_${service}_app` role from the standard provisioning is sufficient
on its own DB (Postgres lets a DB owner dump their own DB; the role does
NOT need superuser).

### 3.4 Vault snapshot policy + token

```hcl
# vault policy write registry-backup-snapshot -
path "sys/storage/raft/snapshot" {
    capabilities = ["read"]
}
```

```bash
vault policy write registry-backup-snapshot - <<'EOF'
path "sys/storage/raft/snapshot" {
    capabilities = ["read"]
}
EOF

# Long-lived periodic token so it doesn't expire mid-CronJob.
SNAP_TOKEN=$(vault token create -policy=registry-backup-snapshot -period=720h -orphan -field=token)

kubectl create secret generic registry-backup-vault-token -n registry \
    --from-literal=VAULT_TOKEN="$SNAP_TOKEN"
```

### 3.5 RabbitMQ admin credentials

```bash
# Use a backup-only user with monitoring tag (read-only) rather than the
# administrator user.
rabbitmqctl add_user registry_backup "$(openssl rand -base64 24)"
rabbitmqctl set_user_tags registry_backup monitoring
rabbitmqctl set_permissions -p / registry_backup "" "" ".*"

kubectl create secret generic registry-backup-rabbitmq-creds -n registry \
    --from-literal=RABBITMQ_USER=registry_backup \
    --from-literal=RABBITMQ_PASSWORD=...
```

### 3.6 Enable in values.prod.yaml

```yaml
backup:
  enabled: true
  target:
    bucket: registry-backups-prod
    region: us-east-1
```

Apply: `helm upgrade registry ./infra/helm/registry -f values.prod.yaml -n registry`

### 3.7 Verify

```bash
# Manually trigger one CronJob to prove the pipeline works.
kubectl create job --from=cronjob/registry-backup-pg-auth registry-backup-pg-auth-manual-1 -n registry
kubectl logs job/registry-backup-pg-auth-manual-1 -n registry -f

# Check S3 for the object.
aws s3 ls s3://registry-backups-prod/production/postgres/auth/ --recursive
```

---

## 4. Optional: PITR via WAL archiving

The shipped CronJobs give 24-hour RPO for Postgres. If the auth / metadata /
audit RPO target of 5 min – 1 hour matters to you, enable WAL archiving on
the Postgres server itself. This is **a Postgres-side configuration change,
not something this chart can set** — the registry doesn't run its own
Postgres.

For the **CloudNativePG operator** (recommended):

```yaml
spec:
  backup:
    barmanObjectStore:
      destinationPath: s3://registry-backups-prod/wal/
      serverName: registry-pg
      wal:
        compression: gzip
        maxParallel: 8
    retentionPolicy: "90d"
```

For **AWS RDS / Aurora**: enable automated backups + set the
`backup_retention_period` parameter; PITR is built-in.

For **self-managed Postgres**: configure `archive_mode = on`, set
`archive_command = 'aws s3 cp %p s3://.../wal/%f --sse AES256'`, and run
`pg_basebackup` weekly.

Document the chosen approach in this file under §10 (deployment-specific
notes) so on-call knows what's actually running.

---

## 5. Why the backup bucket lives in a separate account

The single highest cause of catastrophic data loss is **a compromised
production cloud account deleting both the production data AND the
backups**. (Code Spaces 2014, Travis CI 2021, multiple ransomware incidents.)

Mitigations baked into this design:

| Mitigation                          | Where                                          |
| ----------------------------------- | ---------------------------------------------- |
| Separate cloud account for backups  | §3.1 — operational, not enforceable in code    |
| IAM principal cannot delete         | §3.2 — `s3:PutObject` only, NO `s3:DeleteObject` |
| Bucket versioning                   | §3.1 — `aws s3 rm` becomes a soft-delete       |
| MFA-delete on the bucket            | §3.1 — even root can't `s3 rm` without MFA     |
| Lifecycle rules expire noncurrent   | §3.1 — controls long-term storage cost         |
| Cross-region replication            | §3.1 — survives single-region cloud outage     |

If you only do ONE of these, do the separate-account part. The rest are
defence in depth.

---

## 6. Restore procedures

### 6.1 Order of operations (full-platform DR)

When restoring from a clean slate (new K8s cluster, new account, etc.):

1. **Vault first.** Without signer keys, nothing else can verify
   signatures — services may refuse to start. Restore Vault, prove
   `vault read transit/keys/registry-signer` works.
2. **Postgres (per service).** Restore in any order; services are
   independent. Start with `tenant` because the gateway resolves custom
   domains via tenant DB lookups; tenant down means even healthy traffic
   is rejected.
3. **MinIO / S3 blobs.** If the data bucket was lost, restore from the
   bucket's versioning history or cross-region replica. Blobs are
   content-addressed (sha256) so consistency with the metadata DB is
   automatic — but if a blob is missing, every manifest referencing it
   returns 404.
4. **RabbitMQ definitions.** Restore the topology so consumers find their
   queues. Messages are not restored.
5. **Bring services up.** Set deployment replicas back to non-zero;
   monitor `kubectl get pods -n registry -w` until all are Ready.
6. **Smoke test.** Push + pull a known small image; check
   `/api/v1/admin/tenants` returns the expected tenant list.

### 6.2 Postgres — single DB

```bash
# From an operator workstation with kubectl + aws cli configured.
# Easier path: run inside a one-shot pod that already has the right RBAC.

export BACKUP_BUCKET=registry-backups-prod
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_REGION=us-east-1

# Drop + recreate the target DB FIRST (the restore script refuses to write
# over a non-empty DB unless FORCE=1):
psql "$SUPERUSER_DSN" -c "DROP DATABASE IF EXISTS registry_metadata;"
psql "$SUPERUSER_DSN" -c "CREATE DATABASE registry_metadata OWNER registry_metadata_app;"

infra/scripts/restore-postgres.sh \
    metadata \
    latest \
    "postgres://registry_metadata_app:PASS@postgres:5432/registry_metadata?sslmode=require"
```

### 6.3 Postgres — specific point in time

```bash
# List available backups for a DB.
aws s3 ls s3://registry-backups-prod/production/postgres/metadata/ --recursive

infra/scripts/restore-postgres.sh \
    metadata \
    production/postgres/metadata/2026/06/17/dump-20260617T021512Z.pgcustom \
    "postgres://...@postgres:5432/registry_metadata?sslmode=require"
```

### 6.4 Vault

```bash
# Vault must be UP and UNSEALED before restore. If you've lost the unseal
# keys, you cannot restore — the snapshot is encrypted with them.
infra/scripts/restore-vault.sh \
    latest \
    http://vault.registry.svc:8200 \
    "$ORIGINAL_ROOT_TOKEN"   # The root token from BEFORE the snapshot was taken
```

### 6.5 RabbitMQ topology

```bash
infra/scripts/restore-rabbitmq.sh \
    latest \
    http://rabbitmq.registry.svc:15672 \
    admin "$RABBITMQ_ADMIN_PASS"
```

### 6.6 Blobs

Blob restore is **not scripted here** because the procedure is provider-specific:

* **S3 versioned bucket:** restore prior versions via the console or
  `aws s3api restore-object` per key.
* **Cross-region replica:** repoint `STORAGE_S3_BUCKET` / endpoint at the
  replica and roll the storage deployment.
* **MinIO:** restore from the secondary cluster (if you set up
  `mc admin replicate`), or from a separate `mc mirror` target.

After blob restore, run `registry-gc` in `report-only` mode and reconcile
against the `manifests` table — any manifest referencing a missing blob
will need its row deleted or the blob re-pushed.

---

## 7. Why RabbitMQ messages are not backed up

The platform's event flows are designed to be re-derivable:

| Lost message                       | Recovery path                                                |
| ---------------------------------- | ------------------------------------------------------------ |
| `scan.requested`                   | Scanner re-checks `manifests.scanned_at IS NULL` and re-queues |
| `webhook.delivery`                 | Webhook service replays unprocessed `webhook_deliveries` rows |
| `audit.event`                      | Producer wrote to Postgres truth before publishing             |
| `tenant.created` / `tenant.deleted`| Consumer (cache / domain map) re-syncs on startup              |

This is the deliberate "Postgres is truth, RabbitMQ is best-effort
notification" architecture from CLAUDE.md §6. Backing up messages would
add cost without changing recovery outcomes.

The one event class this does **not** cover is `store.queued` (proxy
pull-through retries — SEC-004). If you lose the queue while a retry is
pending, the client will simply re-pull on next request and the new
`store.queued` event will publish then. Acceptable trade.

---

## 8. Quarterly DR drill

A backup pipeline that has never been restored is not a backup pipeline.
Run this drill once per quarter, ideally during a low-traffic window.

### 8.1 Drill checklist

1. **Spin up a separate staging cluster.** Do NOT drill against production.
2. **Bootstrap from scratch:** `helm install` the chart with `backup.enabled: false`
   so no new backups corrupt the test.
3. **Restore latest:** run `infra/scripts/restore-*.sh latest ...` for every
   data class. Time each step.
4. **Smoke test:**
    * Log in as the dev admin (proves `registry-auth` came back).
    * List repos (`/api/v1/repositories`) — counts should match prod.
    * Push a small image (`docker push staging.registry.test/dev/drill:1`).
    * Pull it back (`docker pull`).
    * Run OCI conformance (`docs/TESTING.md` §3) — must still pass 75/75.
5. **Verify signer:**
    * `cosign sign` against the restored Vault transit key.
    * `cosign verify` against the just-signed image. Both must succeed.
6. **Tear down:** delete the staging cluster.

### 8.2 What to write down after the drill

| Metric                 | Target  | Actual (this drill) |
| ---------------------- | ------- | ------------------- |
| Total restore wall-clock| < RTO  |                     |
| Auth DB restore time   | 30 min  |                     |
| Metadata DB restore time| 1 hour |                     |
| Vault restore time     | 30 min  |                     |
| OCI conformance pass   | 75/75   |                     |
| Cosign verify          | pass    |                     |

If any actual exceeds target, file an issue. The fix is usually one of:
better parallelism, smaller DBs, faster instance for restore, or revised
RTO targets.

---

## 9. Known limitations

* **WAL archiving is not provisioned by this chart.** RPO < 24h requires
  WAL archive setup on the Postgres operator. See §4.
* **Blob restore is operator-driven.** Bucket versioning + CRR are the
  primary mechanisms; this runbook documents the trigger but the procedure
  depends on which storage backend you chose.
* **Unseal-key escrow is manual.** Vault is useless without its unseal
  keys. They must be stored offline (paper, safe deposit box) and tested
  during the DR drill.
* **No automated backup-integrity verification.** A future enhancement
  could restore the latest backup to a throwaway PG instance daily and
  diff row counts. Not shipped — file a tracking issue if you need it.

---

## 10. Deployment-specific notes

> **Edit this section to document your environment.** Examples below.

* **WAL archive backend:** _e.g. CloudNativePG → s3://registry-backups-prod/wal/_
* **Backup target account ID:** _e.g. 999988887777 (separate from data account 111122223333)_
* **CRR target region:** _e.g. us-east-1 primary, us-west-2 replica_
* **Vault unseal-key custodians:** _e.g. ops-lead@, sre-lead@, ciso@_
* **Last DR drill:** _YYYY-MM-DD by NAME_
