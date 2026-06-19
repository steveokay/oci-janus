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
import { useDeleteRepository } from "@/lib/api/repositories";
import type { Repository } from "@/lib/api/types";

interface DeleteRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repo: Repository;
}

// Beacon — destructive confirmation. Type-to-confirm guards against the
// "I clicked the wrong row" failure mode. We pin the comparison to the
// fully-qualified `org/repo` slug so muscle-memory of typing just the
// repo name doesn't accidentally satisfy it.
export function DeleteRepositoryDialog({
  open,
  onOpenChange,
  repo,
}: DeleteRepositoryDialogProps): React.ReactElement {
  const slug = `${repo.org}/${repo.name}`;
  const [confirmText, setConfirmText] = React.useState("");
  const navigate = useNavigate();
  const del = useDeleteRepository();

  const matches = confirmText.trim() === slug;
  const submitting = del.isPending;

  // Reset the textbox whenever the dialog closes so a re-open starts fresh.
  React.useEffect(() => {
    if (!open) setConfirmText("");
  }, [open]);

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
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Destructive action
            </span>
          </div>
          <DialogTitle>Delete repository</DialogTitle>
          <DialogDescription>
            This permanently removes <code className="font-mono">{slug}</code>{" "}
            along with all its tags and manifests. Blob storage cleanup is
            handled by garbage collection. This cannot be undone.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="confirm">
            Type <span className="font-mono normal-case text-[var(--color-fg)]">{slug}</span> to confirm
          </Label>
          <Input
            id="confirm"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={slug}
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
            {submitting ? "Deleting" : "Delete repository"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
