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
}

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

