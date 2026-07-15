import * as React from "react";
import { FileText } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useRepository, useUpdateRepository } from "@/lib/api/repositories";
import { cn } from "@/lib/utils";

// RepoDescriptionSection — General-section card on the repo Settings tab
// (futures.md Tier 2 #2). Edits the repo's long-form README, which renders on
// the repo overview via DescriptionCard.
//
// Wires the existing UpdateRepository RPC (description field) through the
// PATCH /repositories/{org}/{repo} route + useUpdateRepository hook — no new
// backend. The hook sends `description` only when provided, and the BFF now
// treats an absent key as "leave alone" (a three-state pointer), so an
// operator saving here won't be clobbered by an unrelated security-flag PATCH.

interface RepoDescriptionSectionProps {
  org: string;
  repo: string;
}

// Soft cap on the README length. The BFF also bounds the request body; this
// keeps the UX honest with a live counter and a disabled Save past the limit
// rather than surfacing a server-side rejection after the round-trip.
const MAX_DESCRIPTION = 4096;

export function RepoDescriptionSection({
  org,
  repo,
}: RepoDescriptionSectionProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const update = useUpdateRepository();
  const current = data?.description ?? "";

  // Local draft so typing doesn't PATCH on every keystroke — committed on Save.
  const [draft, setDraft] = React.useState(current);

  // Sync the draft when the server value changes (another tab / session edited
  // it). The value-compare dependency means an unchanged background refetch
  // never resets an in-progress edit.
  React.useEffect(() => {
    setDraft(current);
  }, [current]);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load repository settings"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  const tooLong = draft.length > MAX_DESCRIPTION;
  const isDirty = draft !== current;

  async function save(): Promise<void> {
    if (tooLong) return;
    try {
      await update.mutateAsync({ org, repo, description: draft });
      toast.success("Description saved.");
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't save the description. Check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start gap-2">
          <FileText className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Description
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Long-form README for this repository. Rendered on the repo
              overview. Markdown is stored verbatim.
            </p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0 space-y-3">
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (
          <>
            <Textarea
              aria-label="Repository description"
              value={draft}
              disabled={update.isPending}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Describe what this repository holds, how to pull it, and who owns it…"
              rows={5}
            />
            <div className="flex items-center justify-between gap-3">
              <span
                className={cn(
                  "text-[11px] tabular-nums",
                  tooLong
                    ? "text-[var(--color-danger)]"
                    : "text-[var(--color-fg-subtle)]",
                )}
              >
                {draft.length.toLocaleString()} / {MAX_DESCRIPTION.toLocaleString()}
              </span>
              <Button
                size="sm"
                disabled={!isDirty || tooLong || update.isPending}
                onClick={() => void save()}
              >
                {update.isPending ? "Saving…" : "Save"}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
