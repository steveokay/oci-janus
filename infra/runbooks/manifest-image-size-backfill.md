# Runbook — `manifests.image_size_bytes` backfill

> **Audience:** registry-metadata operators with `psql` access to the
> production database.
> **When to run:** once, on the maintenance window after the FE-API-001 release
> goes live. Skip if the registry has been running ≤2026-06-19 (no pre-FE-API-001
> rows exist).
> **Expected runtime:** ~1 minute per 100 000 manifest rows on a single-region
> Postgres; scales linearly with row count and per-row JSON size.

## Why this isn't in a migration

Migration `00004_manifest_image_size.sql` adds the `image_size_bytes` column
with `DEFAULT 0` only. The original migration also ran the backfill inline in
a PL/pgSQL `DO` block, but Goose runs each migration in a single transaction;
on a tenant with hundreds of thousands of `manifests` rows that DO loop would
hold one connection + one long-running transaction for the full backfill,
block autovacuum on the table, and stall `registry-metadata` startup since
`goose up` runs before the server starts accepting traffic. See PENTEST-028 in
`security.md` for the security trace.

The column default is 0. New rows are populated by `parseImageSize` inside
`PutManifest` (Go), so the rows that need backfilling are exactly those that
existed before the FE-API-001 release.

## What the script does

For each row where `image_size_bytes = 0`:

- **Image manifest** (`raw_json` has a `layers` array): sum
  `config.size + SUM(layers[].size)`.
- **Image index** (`raw_json` has a `manifests` array): sum
  `SUM(manifests[].size)`. This is the cumulative size of the per-platform
  manifest documents the index points to — not the per-platform image size,
  which would require resolving each child.
- **Anything else** (legacy schemaVersion 1, malformed JSON, etc.): leave at
  0. A future `PutManifest` will overwrite the row.

The script processes in batches of 1 000 rows, committing per batch, so a
crash mid-run leaves the already-completed rows persisted and the script can
be re-run safely (it only touches `WHERE image_size_bytes = 0`). Autovacuum
is unblocked between batches.

## Pre-flight

1. **Confirm the column exists:**

   ```sql
   SELECT column_name FROM information_schema.columns
   WHERE table_name = 'manifests' AND column_name = 'image_size_bytes';
   ```

   If empty, run `goose up` first.

2. **Confirm the row count to back-fill:**

   ```sql
   SELECT COUNT(*) FROM manifests WHERE image_size_bytes = 0;
   ```

   Multiply by ~1 ms to estimate runtime. If the count is small (< 10 000),
   you may run the script in one go without splitting into batches.

3. **Note the current pg_stat_activity / replication lag** so you can compare
   after the run.

## Run

> ⚠️ **Do NOT wrap this in a transaction.** The procedure uses explicit
> `COMMIT` between batches so vacuum / replication can keep up. Running it
> via `BEGIN; … ; COMMIT;` collapses every batch back into one transaction
> and undoes the point of the script.

Save as `backfill.sql`:

```sql
-- backfill.sql — paginated backfill for manifests.image_size_bytes.
-- Uses a high-water-mark cursor (last_id) so a row whose raw_json fails to
-- parse is permanently skipped after one attempt; without the cursor a
-- batch of all-malformed rows would loop forever (we'd keep re-selecting
-- the same rows because they stay at image_size_bytes = 0).
--
-- Re-runnable across psql invocations: on a fresh run the cursor restarts
-- at the zero UUID, but rows already at non-zero are skipped by the
-- `image_size_bytes = 0` predicate, so the only re-work is one no-op pass
-- over previously-malformed rows.

CREATE OR REPLACE PROCEDURE registry_backfill_manifest_image_size(
    batch_size INT DEFAULT 1000
) LANGUAGE plpgsql AS $$
DECLARE
  r RECORD;
  doc jsonb;
  total BIGINT;
  rows_in_batch INT;
  processed INT := 0;
  -- last_id is the high-water mark: each batch starts strictly after the
  -- largest id we've already considered. Initialise to the all-zero UUID
  -- so the first batch starts from the beginning of the index.
  last_id UUID := '00000000-0000-0000-0000-000000000000'::UUID;
  max_id_in_batch UUID;
BEGIN
  LOOP
    rows_in_batch := 0;
    max_id_in_batch := last_id;

    -- ORDER BY id walks the primary key index sequentially. The cursor
    -- advance ensures malformed rows are visited exactly once.
    FOR r IN
      SELECT id, raw_json
      FROM manifests
      WHERE image_size_bytes = 0 AND id > last_id
      ORDER BY id
      LIMIT batch_size
    LOOP
      -- The cast can raise on malformed UTF-8 or bad JSON; wrap each row
      -- in its own exception block so one bad raw_json never derails the
      -- whole batch.
      BEGIN
        doc := convert_from(r.raw_json, 'UTF8')::jsonb;
        total := 0;
        IF doc ? 'layers' THEN
          total := total + COALESCE((doc->'config'->>'size')::bigint, 0);
          total := total + COALESCE((
            SELECT SUM((layer->>'size')::bigint)
            FROM jsonb_array_elements(doc->'layers') AS layer
          ), 0);
        ELSIF doc ? 'manifests' THEN
          total := total + COALESCE((
            SELECT SUM((m->>'size')::bigint)
            FROM jsonb_array_elements(doc->'manifests') AS m
          ), 0);
        END IF;

        -- A successful parse with total = 0 (every field genuinely zero)
        -- leaves the row alone: bumping it to non-zero would let it
        -- masquerade as backfilled, and 0 will be corrected by the next
        -- PutManifest. A negative result would mean a corrupted row, so
        -- the IF total > 0 guard is also a safety belt.
        IF total > 0 THEN
          UPDATE manifests SET image_size_bytes = total WHERE id = r.id;
        END IF;
      EXCEPTION WHEN OTHERS THEN
        NULL;  -- Leave the row at 0; PutManifest will overwrite on next push.
      END;

      rows_in_batch := rows_in_batch + 1;
      max_id_in_batch := r.id;
    END LOOP;

    EXIT WHEN rows_in_batch = 0;

    last_id := max_id_in_batch;
    processed := processed + rows_in_batch;
    RAISE NOTICE 'backfilled % rows so far (last_id=%)', processed, last_id;
    COMMIT;
  END LOOP;
END;
$$;

CALL registry_backfill_manifest_image_size(1000);

-- Drop the procedure when done so it doesn't linger in pg_proc.
DROP PROCEDURE registry_backfill_manifest_image_size(INT);
```

Then:

```bash
psql "$DB_DSN" -v ON_ERROR_STOP=1 -f backfill.sql
```

`ON_ERROR_STOP=1` is intentional — the script's own per-row `EXCEPTION` clause
already catches malformed JSON; anything bubbling past it (e.g. a connection
drop) should abort psql so the operator notices.

## Verify

```sql
-- Should return 0 (or close to it — a small residue of rows with
-- unparseable raw_json is expected).
SELECT COUNT(*) FROM manifests WHERE image_size_bytes = 0;

-- Spot-check a random tag's reported size against `docker manifest inspect`.
SELECT t.name AS tag, m.image_size_bytes, m.size_bytes AS manifest_doc_size
FROM tags t
JOIN manifests m ON m.repo_id = t.repo_id AND m.digest = t.manifest_digest
ORDER BY random()
LIMIT 5;
```

## Rollback

The column has a default of `0` and PutManifest is the canonical writer going
forward; there is no rollback needed for a successful backfill. If the script
needs to be re-run later (e.g. after a schema migration), it is idempotent —
it only touches rows that are still at 0.

If you need to reset the entire column to 0 (you almost certainly don't):

```sql
-- Heavy: rewrites every manifests row. Do not run on production without
-- coordinating with the team.
UPDATE manifests SET image_size_bytes = 0;
```
