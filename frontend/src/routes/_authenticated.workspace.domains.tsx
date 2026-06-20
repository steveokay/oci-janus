import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Globe } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { ComingSoon } from "@/components/common/coming-soon";

// FE-API-027 — custom domain CRUD route.
//
// CLAUDE.md §9 promises custom-domain support and services/tenant ships the
// verification worker (REM-004) + proto plumbing. The HTTP routes that
// would back this surface aren't on the management BFF yet. The route
// renders the full plan so operators know it's coming and reviewers can
// map UI → backend work.
export const Route = createFileRoute("/_authenticated/workspace/domains")({
  component: DomainsPage,
});

function DomainsPage(): React.ReactElement {
  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Workspace
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Custom domains
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Point your own hostname at this control plane. Domains verify via
          a DNS TXT challenge and become the workspace's pull / push
          endpoint once promoted to primary.
        </p>
      </header>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Today's primary host
            </CardDescription>
            <Badge tone="accent">
              <Globe className="size-3" /> Platform
            </Badge>
          </div>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-[var(--color-fg-muted)]">
            Right now your workspace is reachable at the platform-derived
            host (
            <code className="font-mono text-[var(--color-fg)]">
              &lt;slug&gt;.registry.localhost
            </code>{" "}
            in dev). Custom domains will surface here once FE-API-027 ships
            and you've registered + verified one.
          </p>
        </CardContent>
      </Card>

      <ComingSoon
        apiId="FE-API-027"
        title="Custom domain management"
        description="Register a domain, prove ownership via DNS TXT, then promote it to primary so docker pulls land on your hostname instead of the platform default."
        highlights={[
          "POST /api/v1/workspace/me/domains — register + receive TXT challenge",
          "POST .../{domain}/verify — force a verification poll on demand",
          "PATCH .../{domain} — promote to primary; existing primary auto-demotes (FE-API-007 unique index)",
          "DELETE .../{domain} — remove a registered domain",
          "GET .../domains — list registered + verification status",
        ]}
      />
    </div>
  );
}
