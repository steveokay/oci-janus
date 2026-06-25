import * as React from "react";
import { Crown, Shield } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { TenantUserRoleSummary } from "@/lib/api/tenant-users";

// FUT-012 Phase C — compact chip strip for the per-user role aggregate.
// Renders the platform-admin and tenant-admin badges first (highest
// privilege wins), then the count chips for org admin / writer / reader
// and the repo grant count.
//
// Zero-count chips are suppressed so a long list of "0 / 0 / 0 / 0"
// noise doesn't dominate the column. A user with no grants renders an
// em-dash so the column doesn't visually collapse.
export function RoleSummaryChips({
  roles,
}: {
  roles: TenantUserRoleSummary;
}): React.ReactElement {
  const chips: React.ReactNode[] = [];

  if (roles.platform_admin) {
    chips.push(
      <Badge key="platform" tone="accent" title="Platform admin — admin on every org via the platform marker">
        <Crown className="size-3" /> Platform admin
      </Badge>,
    );
  }
  if (roles.tenant_admin) {
    chips.push(
      <Badge key="tenant" tone="accent" title="Tenant admin — manages users in this tenant">
        <Shield className="size-3" /> Tenant admin
      </Badge>,
    );
  }
  if (roles.org_admin_count > 0) {
    chips.push(
      <Badge key="org-admin" tone="warning" title="Org-admin grants">
        Org admin × {roles.org_admin_count}
      </Badge>,
    );
  }
  if (roles.org_writer_count > 0) {
    chips.push(
      <Badge key="org-writer" tone="neutral" title="Org-writer grants">
        Writer × {roles.org_writer_count}
      </Badge>,
    );
  }
  if (roles.org_reader_count > 0) {
    chips.push(
      <Badge key="org-reader" tone="neutral" title="Org-reader grants">
        Reader × {roles.org_reader_count}
      </Badge>,
    );
  }
  if (roles.repo_grant_count > 0) {
    chips.push(
      <Badge key="repo" tone="neutral" title="Per-repo role grants">
        Repo × {roles.repo_grant_count}
      </Badge>,
    );
  }

  if (chips.length === 0) {
    return (
      <span className="text-xs text-[var(--color-fg-subtle)]" aria-label="No role grants">
        —
      </span>
    );
  }
  return <div className="flex flex-wrap items-center gap-1.5">{chips}</div>;
}
