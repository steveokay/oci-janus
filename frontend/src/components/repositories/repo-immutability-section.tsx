import * as React from "react";
import { Lock, ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useRepository, useUpdateRepository } from "@/lib/api/repositories";

// RepoImmutabilitySection — Settings-tab card on the repo detail page.
//
// Toggles the `immutable_tags` flag on the parent repository (futures.md
// Tier 1 #2). When enabled, services/core rejects any push that would
// move an existing tag's manifest_digest with a 400 MANIFEST_INVALID.
// New-tag pushes still work; idempotent same-digest re-pushes still
// work (they're not really "moves"). Per-tag pins (the 📌 pill on the
// Tags table) operate independently of this flag.
//
// Why a card rather than a toggle nested into the existing scan-policy
// section: this flag has different audit semantics + permission shape
// (it's a security-relevant transition, not a workflow config), so it
// reads better as its own visual unit. The card surfaces both the
// current state AND a brief explainer of what flipping it does — the
// kind of switch where an operator wants to read before clicking.
//
// SECURITY: the BFF gates `PATCH /repositories/{org}/{repo}` with
// `immutable_tags` on repo admin/owner. The FE doesn't re-validate
// (the server is authoritative); we just optimistically render the
// switch as enabled and let the 403 path show a toast on click.

interface RepoImmutabilitySectionProps {
  org: string;
  repo: string;
}

export function RepoImmutabilitySection({
  org,
  repo,
}: RepoImmutabilitySectionProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const update = useUpdateRepository();
  const immutable = data?.immutable_tags ?? false;

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load repository settings"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  async function toggle(next: boolean): Promise<void> {
    try {
      await update.mutateAsync({ org, repo, immutable_tags: next });
      toast.success(
        next
          ? "Tags are now immutable — re-pushes will be rejected."
          : "Tags are mutable again — re-pushes will succeed.",
      );
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't update immutability. Check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Tag immutability
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When on, pushes that would re-write an existing tag are
              rejected. New-tag pushes still work; idempotent same-digest
              re-pushes still work. Recommended for production and
              promotion repositories.
            </p>
          </div>
          {isLoading ? (
            <Skeleton className="size-4 rounded-full" />
          ) : immutable ? (
            <Badge tone="success">
              <Lock className="size-3" /> Locked
            </Badge>
          ) : (
            <Badge tone="neutral">
              <ShieldAlert className="size-3" /> Mutable
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {isLoading ? (
          <Skeleton className="h-10 w-full" />
        ) : (
          <label className="flex cursor-pointer items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-medium text-[var(--color-fg)]">
                Reject re-pushes to existing tags
              </span>
              <span className="text-xs text-[var(--color-fg-muted)]">
                Affects every tag in this repository. Per-tag pins
                {" "}
                <code className="font-mono text-[10px]">(📌)</code>
                {" "}
                still work for finer-grained locks elsewhere.
              </span>
            </span>
            <Switch
              checked={immutable}
              disabled={update.isPending}
              onCheckedChange={(next) => void toggle(next)}
              aria-label="Immutable tags"
            />
          </label>
        )}
        {immutable ? (
          <p className="mt-3 flex items-start gap-2 rounded-md border border-[var(--color-accent)]/30 bg-[var(--color-accent-subtle)]/30 px-3 py-2 text-xs text-[var(--color-fg)]">
            <Lock className="mt-0.5 size-3.5 shrink-0 text-[var(--color-accent)]" />
            <span>
              Re-pushes of <code className="font-mono">{org}/{repo}:&lt;existing&gt;</code> will
              return <code className="font-mono">400 MANIFEST_INVALID</code>.
              To replace a tag, delete it first then push the new digest.
            </span>
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}
