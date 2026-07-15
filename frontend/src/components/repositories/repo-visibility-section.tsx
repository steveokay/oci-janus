import * as React from "react";
import { Globe, Lock } from "lucide-react";
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

// RepoVisibilitySection — General-section card on the repo Settings tab
// (futures.md Tier 2 #2). Toggles the repo's `is_public` flag.
//
// Public repos allow anonymous pull; private require authentication. Wires the
// dedicated UpdateRepositoryVisibility RPC via the PATCH route's new `is_public`
// field — a separate RPC (not the description write) so the access-relevant
// change is audit-legible and busts services/core's GetRepository cache.

interface RepoVisibilitySectionProps {
  org: string;
  repo: string;
}

export function RepoVisibilitySection({
  org,
  repo,
}: RepoVisibilitySectionProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const update = useUpdateRepository();
  const isPublic = data?.is_public ?? false;

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
      await update.mutateAsync({ org, repo, is_public: next });
      toast.success(
        next
          ? "Repository is now public — anyone can pull without authenticating."
          : "Repository is now private — pulls require authentication.",
      );
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't update visibility. Check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-start gap-2">
            {isPublic ? (
              <Globe className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
            ) : (
              <Lock className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
            )}
            <div className="space-y-1">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Visibility
              </CardDescription>
              <p className="text-xs text-[var(--color-fg-muted)]">
                Public repositories allow anonymous pull. Private repositories
                require an authenticated session or API key. Push always
                requires write access regardless.
              </p>
            </div>
          </div>
          {isLoading ? (
            <Skeleton className="size-4 rounded-full" />
          ) : isPublic ? (
            <Badge tone="warning">
              <Globe className="size-3" /> Public
            </Badge>
          ) : (
            <Badge tone="neutral">
              <Lock className="size-3" /> Private
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
                Allow anonymous pull
              </span>
              <span className="text-xs text-[var(--color-fg-muted)]">
                {isPublic
                  ? "Anyone with the image reference can pull. Turn off to require auth."
                  : "Only authenticated principals can pull. Turn on to make it public."}
              </span>
            </span>
            <Switch
              checked={isPublic}
              disabled={update.isPending}
              onCheckedChange={(next) => void toggle(next)}
              aria-label="Allow anonymous pull"
            />
          </label>
        )}
      </CardContent>
    </Card>
  );
}
