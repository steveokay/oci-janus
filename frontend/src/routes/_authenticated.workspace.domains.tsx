import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Globe, Plus, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import { DomainsTable } from "@/components/workspace/domains-table";
import { RegisterDomainDialog } from "@/components/workspace/register-domain-dialog";
import { useDomains } from "@/lib/api/domains";
import { useWorkspace } from "@/lib/api/workspace";

// /workspace/domains — FE-API-007 / FE-API-027.
//
// Lists registered custom domains plus the platform-derived fallback,
// and lets a tenant admin register / verify / promote / delete via the
// services/tenant routes proxied through the BFF. The "Today's primary
// host" card reads from FE-API-009 (workspace.host) so promoting a
// domain in the table below updates the banner in the same render pass.
export const Route = createFileRoute("/_authenticated/workspace/domains")({
  component: DomainsPage,
});

function DomainsPage(): React.ReactElement {
  const [registerOpen, setRegisterOpen] = React.useState(false);
  const workspace = useWorkspace();
  const domains = useDomains();

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex flex-col gap-1">
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
        </div>
        <Button onClick={() => setRegisterOpen(true)}>
          <Plus className="size-4" />
          Register domain
        </Button>
      </header>

      <PrimaryHostCard
        host={workspace.data?.host}
        isCustom={workspace.data?.host_is_custom ?? false}
        loading={workspace.isLoading}
      />

      {domains.isError ? (
        <ErrorState
          title="Couldn't load domains"
          description="The tenant service didn't answer. Retry, or check the BFF logs."
          error={domains.error}
          onRetry={() => void domains.refetch()}
        />
      ) : domains.isLoading ? (
        <Card>
          <CardContent>
            <div className="space-y-2 py-3">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          </CardContent>
        </Card>
      ) : !domains.data || domains.data.length === 0 ? (
        <Card>
          <CardContent>
            <EmptyState
              icon={<Globe className="size-5" />}
              title="No custom domains yet"
              description="Register a domain to start the TXT-challenge verification flow. Until then, the platform-derived host above is your pull / push endpoint."
              action={
                <Button onClick={() => setRegisterOpen(true)}>
                  <Plus className="size-4" />
                  Register domain
                </Button>
              }
              secondaryAction={
                <a
                  href="https://github.com/steveokay/oci-janus/blob/main/docs/CUSTOM-DOMAINS.md"
                  target="_blank"
                  rel="noreferrer"
                  className="text-sm text-[var(--color-accent)] underline-offset-4 hover:underline"
                >
                  Read the docs
                </a>
              }
            />
          </CardContent>
        </Card>
      ) : (
        <DomainsTable domains={domains.data} />
      )}

      <RegisterDomainDialog
        open={registerOpen}
        onOpenChange={setRegisterOpen}
      />
    </div>
  );
}

function PrimaryHostCard({
  host,
  isCustom,
  loading,
}: {
  host: string | undefined;
  isCustom: boolean;
  loading: boolean;
}): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Today's primary host
          </CardDescription>
          <Badge tone={isCustom ? "success" : "accent"}>
            {isCustom ? (
              <>
                <ShieldCheck className="size-3" /> Custom
              </>
            ) : (
              <>
                <Globe className="size-3" /> Platform
              </>
            )}
          </Badge>
        </div>
      </CardHeader>
      <CardContent>
        {loading ? (
          <Skeleton className="h-7 w-2/3" />
        ) : host ? (
          <div className="flex flex-wrap items-center gap-2">
            <code className="font-mono text-base font-medium text-[var(--color-fg)]">
              {host}
            </code>
            <CopyButton value={host} iconOnly />
          </div>
        ) : (
          <p className="text-sm text-[var(--color-fg-muted)]">
            Workspace metadata isn't loaded yet.
          </p>
        )}
        <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">
          {isCustom
            ? "Docker pulls land on your custom hostname. Promote a different verified domain below to swap it."
            : "Until a custom domain is verified + promoted, this platform-derived host is what docker clients hit."}
        </p>
      </CardContent>
    </Card>
  );
}
