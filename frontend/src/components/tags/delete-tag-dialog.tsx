import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { AlertTriangle } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { useDeleteTag } from "@/lib/api/tags";

interface DeleteTagDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tag: string;
}

// Beacon — DeleteTagDialog. Same shape as DeleteRepositoryDialog: the user
// must type the exact tag name to enable the destructive button. We compare
// to just the tag name (not org/repo/tag) since the breadcrumb already
// gives the operator the context they need to know what they're deleting.
export function DeleteTagDialog({
  open,
  onOpenChange,
  org,
  repo,
  tag,
}: DeleteTagDialogProps): React.ReactElement {
  const [confirmText, setConfirmText] = React.useState("");
  const navigate = useNavigate();
  const del = useDeleteTag();

  const matches = confirmText.trim() === tag;
  const submitting = del.isPending;

  React.useEffect(() => {
    if (!open) setConfirmText("");
  }, [open]);

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
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Destructive action
            </span>
          </div>
          <DialogTitle>Delete tag</DialogTitle>
          <DialogDescription>
            This removes <code className="font-mono">{tag}</code> from{" "}
            <code className="font-mono">{org}/{repo}</code>. The underlying
            manifest stays available by digest until garbage collection.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="confirm-tag">
            Type{" "}
            <span className="font-mono normal-case text-[var(--color-fg)]">
              {tag}
            </span>{" "}
            to confirm
          </Label>
          <Input
            id="confirm-tag"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={tag}
          />
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            onClick={() => void onConfirm()}
            disabled={!matches || submitting}
            loading={submitting}
          >
            {submitting ? "Deleting" : "Delete tag"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
