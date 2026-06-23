// Beacon — shared response shapes.
// These mirror what services/management actually returns; keep this file
// canonical for hooks + components. If the BFF schema changes, swap here.

export interface StatsResponse {
  total_repos: number;
  storage_used_bytes: number;
  storage_quota_bytes: number;
  daily_pulls: number;
  vulnerability_count: number;
  system_health_pct: number;
  // FE-API-016 — per-severity counts now returned by the backend.
  critical_count: number;
  high_count: number;
  medium_count: number;
  low_count: number;
  negligible_count: number;
}

export interface Repository {
  repo_id: string;
  org_id: string;
  // `org` is the JOINed org name (FE-API-010 done on backend).
  org: string;
  name: string;
  is_public: boolean;
  storage_used_bytes: number;
  storage_quota_bytes: number;
  created_at: string;
  // FE-API-006 — operator-supplied markdown description.
  description: string;
  // Tag immutability (futures.md Tier 1 #2). When true, services/core
  // rejects any push that would move an existing tag's digest. Default
  // false; flipped via the Immutable-tags switch on the repo Settings
  // tab. Note: per-tag pins (Tag.immutable) work independently of this
  // flag.
  immutable_tags?: boolean;
  // Signed-image admission (futures.md Tier 1 #3). When true,
  // services/core blocks every GetManifest that has no recorded
  // signature with 403 DENIED. Default false; flipped via the
  // Signed-image-required switch on the repo Settings tab. Pulls
  // of signed manifests succeed normally; unsigned pulls fail
  // closed so the operator must sign (cosign) or turn the policy
  // off explicitly.
  require_signature?: boolean;
}

// Signed-image admission Phase 2 (futures.md Tier 1 #3). One entry
// in the per-repo trusted-key allowlist. Surfaced by the Settings
// tab's RepoTrustedKeysSection card. When require_signature=true
// AND the list is non-empty, services/core narrows the admission
// gate to signatures produced by an approved key_id only. Empty
// list falls back to "ANY signature passes" so the operator can
// flip the flag first and pin keys incrementally.
export interface TrustedKey {
  id: string;
  key_id: string;
  display_name?: string;
  added_by?: string;
  added_at: string;
}

// Audit-log streaming to SIEM (futures.md Tier 1 #4). Per-tenant
// config surfaced by the workspace Settings tab's
// `AuditExportSection`. Secrets are write-only via the PUT body —
// the GET response carries `hmac_secret_set` / `bearer_token_set`
// booleans so the FE renders "(saved)" placeholders without
// round-tripping the secret material. Observability counters
// (last_success_at, last_error, dlx_depth) let the operator see
// whether the stream is healthy.
export type AuditExportFormat = "syslog_rfc5424" | "cef" | "webhook";

export interface AuditExportConfig {
  id: string;
  enabled: boolean;
  format: AuditExportFormat;
  target_url: string;
  hmac_secret_set: boolean;
  bearer_token_set: boolean;
  event_filters_json?: string;
  last_success_at?: string;
  last_attempt_at?: string;
  last_error?: string;
  dlx_depth: number;
  updated_at: string;
}

// `null` value means "no config yet" — the GET handler returns this
// shape so the FE renders the empty form rather than treating
// missing-config as an error toast.
export interface AuditExportConfigResponse {
  config: AuditExportConfig | null;
}

export interface AuditExportTestResponse {
  delivered: boolean;
  error?: string;
  rendered_event?: string;
}

export interface RepositoriesListResponse {
  repositories: Repository[];
  total: number;
  next_page_token?: string;
}

export interface CreateRepositoryBody {
  org: string;
  name: string;
  is_public: boolean;
  storage_quota?: number;
  description?: string;
}

export interface Tag {
  name: string;
  manifest_digest: string;
  // FE-API-001 — `size_bytes` is now populated by the backend.
  // Pre-FE-API-001 rows return 0 until re-pushed or backfilled.
  size_bytes: number;
  updated_at: string;
  created_at: string;
  // REM-013 gap 1 — RFC3339 timestamp of when the retention executor
  // soft-deleted this manifest. Absent on the wire (and undefined in
  // TS) when the manifest is not in the retention grace window — the
  // common case. The dashboard renders a "🗑 deletes in N days" pill
  // on the Tags table when set; FE derives the ETA from this stamp +
  // the platform's configured grace window (default 7d).
  retention_pending_delete_at?: string;
  // FE-API-050 — parent manifest's quarantined flag. True means the
  // pull-time gate in registry-core is rejecting pulls of this
  // manifest with 451 Unavailable For Legal Reasons. The Tags table
  // renders a 🔒 pill on quarantined rows; the lift action lives on
  // the tag detail page.
  quarantined?: boolean;
  // S-MAINT-1 Batch 5 (P6 + F4) — derived artifact-type discriminator
  // ("image" | "helm" | "signature" | "sbom" | "other"). Drives the
  // per-tag pill + the filter chip row on the repo detail page. Empty
  // when the manifest had no parseable config block (rare, pre-Batch-5
  // legacy row).
  artifact_type?: ArtifactType;
  // Tag immutability pin (futures.md Tier 1 #2). When true, services/core
  // rejects any push that would move this tag to a new digest, regardless
  // of the parent repo's `immutable_tags` flag. The Tags table renders a
  // 📌 pill on pinned rows; the tag detail page has a Pin/Unpin button
  // gated on repo admin role.
  immutable?: boolean;
}

// S-MAINT-1 Batch 5 — union of stable discriminator values mirroring
// services/metadata/internal/repository.deriveArtifactType. Adding a
// new category needs an entry here + the matching BFF allowlist +
// the deriveArtifactType switch on both core + metadata.
export type ArtifactType =
  | "image"
  | "helm"
  | "signature"
  | "sbom"
  | "other"
  | "";

export interface TagsListResponse {
  tags: Tag[];
}

export interface ScanResult {
  scan_id: string;
  status: "pending" | "running" | "complete" | "failed";
  scanner_name: string;
  scanner_version: string;
  severity_counts: Partial<Record<"CRITICAL" | "HIGH" | "MEDIUM" | "LOW", number>>;
  findings_json?: string;
  started_at: string;
  completed_at?: string;
}

export interface BuildRecord {
  build_id: string;
  status: "success" | "failure";
  commit_hash?: string;
  triggered_by: string;
  duration: string;
  occurred_at: string;
}

export interface BuildsListResponse {
  builds: BuildRecord[];
  total: number;
}

