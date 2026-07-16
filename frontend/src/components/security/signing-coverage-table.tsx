import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import type { AllowlistHealth, RepoCoverage } from "@/lib/api/signing-coverage";
import { SigningCoverageBar } from "./signing-coverage-bar";

// Beacon — SigningCoverageTable. Workspace rollup, one row per repo. Read-only:
// the rightmost cell drills into the existing per-repo Settings tab (trusted-key
// editor + require_signature toggle) rather than duplicating those controls.

// HEALTH_META maps each allowlist-health posture to a badge label + token
// classes. enforced_with_allowlist is the strong posture (success);
// enforced_any_signature is enforcing-but-empty-allowlist (warning); advisory
// is tracked-but-not-enforced (muted).
const HEALTH_META: Record<AllowlistHealth, { label: string; className: string }> = {
  enforced_with_allowlist: {
    label: "Enforced + allowlist",
    className: "text-[var(--color-success)] bg-[var(--color-success)]/10",
  },
  enforced_any_signature: {
    label: "Enforced · any sig",
    className: "text-[var(--color-warning)] bg-[var(--color-warning)]/10",
  },
  advisory: {
    label: "Advisory",
    className: "text-[var(--color-fg-muted)] bg-[var(--color-surface-sunken)]",
  },
};

function AllowlistHealthBadge({
  health,
  keyCount,
}: {
  health: AllowlistHealth;
  keyCount: number;
}): React.ReactElement {
  const meta = HEALTH_META[health];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium",
        meta.className,
      )}
    >
      {meta.label}
      {/* Only the enforcing postures carry a trusted-key count — advisory has
          no allowlist concept, so we omit the "· N keys" suffix there. */}
      {health !== "advisory" && (
        <span className="text-[var(--color-fg-subtle)]">· {keyCount} keys</span>
      )}
    </span>
  );
}

interface SigningCoverageTableProps {
  repos: RepoCoverage[];
}

export function SigningCoverageTable({ repos }: SigningCoverageTableProps): React.ReactElement {
  const [filter, setFilter] = React.useState("");
  const [requiredOnly, setRequiredOnly] = React.useState(false);

  // Derive the visible rows from the raw repos + the two client-side controls.
  // Memoised so typing in the filter doesn't re-filter on unrelated re-renders.
  const rows = React.useMemo(() => {
    const q = filter.trim().toLowerCase();
    return repos.filter((r) => {
      if (requiredOnly && !r.require_signature) return false;
      if (!q) return true;
      return `${r.org}/${r.repo}`.toLowerCase().includes(q);
    });
  }, [repos, filter, requiredOnly]);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <input
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter repositories…"
          className="h-9 w-64 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-sm"
          aria-label="Filter repositories"
        />
        <label className="flex items-center gap-2 text-sm text-[var(--color-fg-muted)]">
          <input
            type="checkbox"
            checked={requiredOnly}
            onChange={(e) => setRequiredOnly(e.target.checked)}
          />
          Requires signature only
        </label>
      </div>

      {/* Table already provides its own overflow-x-auto scroll container, so we
          only add the rounded-border shell (matching VulnerabilitiesTable). */}
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Repository</TableHead>
              <TableHead>Policy</TableHead>
              <TableHead>Signed coverage</TableHead>
              <TableHead>Trusted keys</TableHead>
              <TableHead>Recent signers</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={`${r.org}/${r.repo}`}>
                <TableCell className="font-medium">
                  {r.org}/{r.repo}
                </TableCell>
                <TableCell>
                  {r.require_signature ? (
                    <span className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 text-xs">
                      require_signature
                    </span>
                  ) : (
                    <span className="text-[var(--color-fg-subtle)]">—</span>
                  )}
                </TableCell>
                <TableCell>
                  <SigningCoverageBar
                    pct={r.signed_pct}
                    signed={r.signed_tags}
                    total={r.tags_in_window}
                  />
                </TableCell>
                <TableCell>
                  <AllowlistHealthBadge health={r.allowlist_health} keyCount={r.trusted_key_count} />
                  {r.stale_trusted_keys > 0 && (
                    <span className="ml-1 text-xs text-[var(--color-fg-subtle)]">
                      ({r.stale_trusted_keys} stale)
                    </span>
                  )}
                </TableCell>
                <TableCell className="text-xs text-[var(--color-fg-muted)]">
                  {r.recent_signers.length === 0
                    ? "—"
                    : r.recent_signers
                        .slice(0, 3)
                        .map((s) => s.signer_id || s.key_id)
                        .join(", ")}
                </TableCell>
                <TableCell className="text-right">
                  {/* Drill into the per-repo Settings tab (trusted-key editor +
                      require_signature toggle). The repo-detail route keeps its
                      active tab in a ?tab= search param, so we deep-link
                      straight to the Settings tab rather than duplicating the
                      controls here. */}
                  <Link
                    to="/repositories/$org/$repo"
                    params={{ org: r.org, repo: r.repo }}
                    search={{ tab: "settings" }}
                    className="text-xs font-medium text-[var(--color-accent)] hover:underline"
                  >
                    Settings →
                  </Link>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
