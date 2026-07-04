import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { useDeleteRepository } from "@/lib/api/repositories";
import type { Repository } from "@/lib/api/types";

interface DeleteRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repo: Repository;
}

// Beacon — destructive confirmation, migrated onto the shared
// ConfirmDestructiveDialog primitive (DSGN-003). Confirmation strength is
// unchanged: the operator types the fully-qualified `org/repo` slug
// (severity="medium", resourceName={slug}) so muscle-memory of typing just
// the repo name can't accidentally satisfy the gate.
export function DeleteRepositoryDialog({
  open,
  onOpenChange,
  repo,
}: DeleteRepositoryDialogProps): React.ReactElement {
  const slug = `${repo.org}/${repo.name}`;
  const navigate = useNavigate();
  const del = useDeleteRepository();

  async function onConfirm(): Promise<void> {
    try {
      await del.mutateAsync({ org: repo.org, repo: repo.name });
      toast.success(`Deleted ${slug}.`);
      onOpenChange(false);
      void navigate({ to: "/repositories" });
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "You don't have permission to delete this repository."
          : "Delete failed. Try again, or check the backend logs.";
      toast.error(message);
    }
  }

  return (
    <ConfirmDestructiveDialog
      open={open}
      onOpenChange={onOpenChange}
      severity="medium"
      resourceName={slug}
      title="Delete repository"
      confirmLabel="Delete repository"
      // In-flight state drives the primitive's escape-lock + spinner.
      loading={del.isPending}
      onConfirm={onConfirm}
      description={
        <>
          This permanently removes <code className="font-mono">{slug}</code>{" "}
          along with all its tags and manifests. Blob storage cleanup is handled
          by garbage collection. This cannot be undone.
        </>
      }
    />
  );
}
