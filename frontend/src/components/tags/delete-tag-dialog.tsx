import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { useDeleteTag } from "@/lib/api/tags";

interface DeleteTagDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tag: string;
}

// Beacon — DeleteTagDialog. Migrated onto the shared
// ConfirmDestructiveDialog primitive (DSGN-003) so the type-to-confirm
// UX, styling, and in-flight escape-lock are consistent with every other
// destructive flow. Confirmation strength is unchanged: the operator must
// type the exact tag name (severity="medium", resourceName={tag}) since
// the breadcrumb already supplies the org/repo context.
export function DeleteTagDialog({
  open,
  onOpenChange,
  org,
  repo,
  tag,
}: DeleteTagDialogProps): React.ReactElement {
  const navigate = useNavigate();
  const del = useDeleteTag();

  async function onConfirm(): Promise<void> {
    try {
      await del.mutateAsync({ org, repo, tag });
      toast.success(`Deleted ${tag}.`);
      onOpenChange(false);
      void navigate({
        to: "/repositories/$org/$repo",
        params: { org, repo },
      });
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "You don't have permission to delete tags in this repository."
          : "Delete failed. Try again, or check the backend logs.";
      toast.error(message);
    }
  }

  return (
    <ConfirmDestructiveDialog
      open={open}
      onOpenChange={onOpenChange}
      severity="medium"
      resourceName={tag}
      title="Delete tag"
      confirmLabel="Delete tag"
      // Pass the mutation's in-flight state so the primitive locks Escape /
      // outside-click and shows the spinner until the delete resolves.
      loading={del.isPending}
      onConfirm={onConfirm}
      description={
        <>
          This removes <code className="font-mono">{tag}</code> from{" "}
          <code className="font-mono">
            {org}/{repo}
          </code>
          . The underlying manifest stays available by digest until garbage
          collection.
        </>
      }
    />
  );
}
