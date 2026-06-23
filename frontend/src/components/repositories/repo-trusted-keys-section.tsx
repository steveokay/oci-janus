import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { KeyRound, Plus, Trash2, ShieldCheck } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
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
import {
  useTrustedKeys,
  useAddTrustedKey,
  useRemoveTrustedKey,
} from "@/lib/api/trusted-keys";
import { useRepository } from "@/lib/api/repositories";

// RepoTrustedKeysSection — Settings-tab card on the repo detail page.
//
// Companion to RepoSignaturePolicySection. Manages the per-repo
// allowlist that narrows Phase 1's "ANY signature passes" gate down
// to a specific set of approved `key_id`s. Two contracts the operator
// needs to see at a glance:
//
//   1. Empty list = ANY signature passes (Phase 1 fallback). This is
//      called out in the empty-state copy so an operator doesn't
//      assume "no keys = nothing trusted = lockdown."
//   2. Non-empty list = only signatures from a listed key_id pass.
//      The card surfaces a warning pill when require_signature is on
//      but the list is empty, so the operator knows they're in
//      Phase 1 fallback mode and may want to pin keys.
//
// The card stays useful even when require_signature is off — operators
// often want to seed the allowlist before flipping the policy on.
// We render it in a "disabled but informative" tone in that case so
// the dependency between the two cards is visible.
//
// SECURITY: BFF gates the Add + Remove routes on repo admin/owner.
// The FE optimistically renders the buttons; 403 surfaces as a toast.
// List is reader-allowed (the key_ids are not secrets — they're
// already visible on the tag-detail signing panel for signed tags).

interface RepoTrustedKeysSectionProps {
  org: string;
  repo: string;
}

// key_id validation mirrors the BFF's keyIDPattern in
// services/management/internal/handler/trusted_keys.go: 8-256 chars
// of hex / base64-ish / colon-separated. Loose enough to accept
// Vault Transit + Cosign keyless + future formats.
const KEY_ID_REGEX = /^[a-zA-Z0-9_:./-]{8,256}$/;

const addSchema = z.object({
  key_id: z
    .string()
    .trim()
    .min(8, "Key id is too short — 8 characters minimum.")
    .max(256, "Key id is too long — 256 characters maximum.")
    .regex(KEY_ID_REGEX, "Allowed characters: letters, digits, _ : . / -"),
  display_name: z
    .string()
    .trim()
    .max(128, "Keep the display name under 128 characters.")
    .optional()
    .or(z.literal("")),
});
type AddValues = z.infer<typeof addSchema>;

export function RepoTrustedKeysSection({
  org,
  repo,
}: RepoTrustedKeysSectionProps): React.ReactElement {
  const { data: repoData } = useRepository(org, repo);
  const requireSignature = repoData?.require_signature ?? false;
  const { data: keys, isLoading, isError, refetch } = useTrustedKeys(org, repo);
  const remove = useRemoveTrustedKey();
  const [addOpen, setAddOpen] = React.useState(false);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load trusted keys"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  const hasKeys = (keys?.length ?? 0) > 0;
  const inPhase1Fallback = requireSignature && !hasKeys;

  async function onRemove(key: { id: string; key_id: string; display_name?: string }): Promise<void> {
    const label = key.display_name ? `${key.display_name} (${shortKey(key.key_id)})` : shortKey(key.key_id);
    if (!window.confirm(`Remove "${label}" from the trusted-key allowlist?`)) {
      return;
    }
    try {
      await remove.mutateAsync({ org, repo, key_id: key.key_id });
      toast.success(
        hasKeys && keys && keys.length === 1
          ? "Trusted key removed. Allowlist is now empty — any signature passes."
          : "Trusted key removed.",
      );
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : code === 404
            ? "That key was already removed."
            : "Couldn't remove trusted key. Check the BFF logs.",
      );
    }
  }

  return (
    <>
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-start justify-between gap-3">
            <div className="space-y-1">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Trusted signing keys
              </CardDescription>
              <p className="text-xs text-[var(--color-fg-muted)]">
                When the allowlist is non-empty, signed-image admission
                narrows to signatures produced by an approved{" "}
                <code className="font-mono text-[10px]">key_id</code>.
                Empty list falls back to{" "}
                <strong>any signature passes</strong> — Phase 1 behavior —
                so you can flip the policy on first and pin keys
                incrementally.
              </p>
            </div>
            {hasKeys ? (
              <Badge tone="accent">
                <ShieldCheck className="size-3" /> {keys?.length} approved
              </Badge>
            ) : inPhase1Fallback ? (
              <Badge tone="warning">Phase 1 fallback</Badge>
            ) : (
              <Badge tone="neutral">Empty</Badge>
            )}
          </div>
        </CardHeader>
        <CardContent className="pt-0 space-y-3">
          {isLoading ? (
            <Skeleton className="h-16 w-full" />
          ) : hasKeys ? (
            <ul className="divide-y divide-[var(--color-border)] rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)]">
              {keys!.map((k) => (
                <li key={k.id} className="flex items-center gap-3 px-3 py-2">
                  <KeyRound className="size-4 shrink-0 text-[var(--color-fg-muted)]" />
                  <div className="min-w-0 flex-1">
                    {k.display_name ? (
                      <div className="truncate text-sm font-medium text-[var(--color-fg)]">
                        {k.display_name}
                      </div>
                    ) : null}
                    <div
                      className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]"
                      title={k.key_id}
                    >
                      {k.key_id}
                    </div>
                    <div className="text-[10px] text-[var(--color-fg-subtle)]">
                      added {formatRelative(k.added_at)}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={remove.isPending}
                    onClick={() => void onRemove(k)}
                    aria-label={`Remove ${k.display_name ?? k.key_id}`}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="rounded-md border border-dashed border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-3 text-xs text-[var(--color-fg-muted)]">
              No trusted keys yet. Any signature passes admission while
              this list is empty.
            </p>
          )}

          {inPhase1Fallback ? (
            <p className="flex items-start gap-2 rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning-subtle)]/30 px-3 py-2 text-xs text-[var(--color-fg)]">
              <span className="mt-0.5 text-[var(--color-warning)]">!</span>
              <span>
                <strong>Signed-image admission is on</strong> but no keys
                are approved yet. The gate currently accepts ANY signed
                manifest. Add an approved key to lock down to your trusted
                signers.
              </span>
            </p>
          ) : null}

          <div className="flex justify-end">
            <Button variant="outline" size="sm" onClick={() => setAddOpen(true)}>
              <Plus className="size-3.5" /> Approve a key
            </Button>
          </div>
        </CardContent>
      </Card>

      <AddTrustedKeyDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        org={org}
        repo={repo}
      />
    </>
  );
}

interface AddTrustedKeyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
}

function AddTrustedKeyDialog({
  open,
  onOpenChange,
  org,
  repo,
}: AddTrustedKeyDialogProps): React.ReactElement {
  const add = useAddTrustedKey();
  const form = useForm<AddValues>({
    resolver: zodResolver(addSchema),
    defaultValues: { key_id: "", display_name: "" },
  });

  // Reset the form state on open transitions so re-opening after a
  // submit doesn't replay the previous values + errors.
  React.useEffect(() => {
    if (open) form.reset({ key_id: "", display_name: "" });
  }, [open, form]);

  async function onSubmit(values: AddValues): Promise<void> {
    try {
      await add.mutateAsync({
        org,
        repo,
        key_id: values.key_id.trim(),
        display_name: values.display_name?.trim() || undefined,
      });
      toast.success("Trusted key approved.");
      onOpenChange(false);
    } catch (e) {
      const ax = e as AxiosError<{ error?: string }> | undefined;
      const code = ax?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : code === 400
            ? ax?.response?.data?.error ?? "Key id format wasn't accepted."
            : "Couldn't approve trusted key. Check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Approve a signing key</DialogTitle>
          <DialogDescription>
            Add a <code className="font-mono text-[11px]">key_id</code> to the
            allowlist. Once any key is approved, signed-image admission
            narrows to signatures produced by an approved key — empty
            allowlist falls back to &quot;any signature passes.&quot;
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="key_id">Key id</Label>
            <Input
              id="key_id"
              autoFocus
              autoComplete="off"
              placeholder="e.g. 2630bb12c4c045bf"
              {...form.register("key_id")}
            />
            <p className="text-[11px] text-[var(--color-fg-muted)]">
              Find this on any signed tag&apos;s Signing panel
              (it&apos;s the short hex string under the signature).
            </p>
            {form.formState.errors.key_id ? (
              <p className="text-[11px] text-[var(--color-danger)]">
                {form.formState.errors.key_id.message}
              </p>
            ) : null}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="display_name">Display name (optional)</Label>
            <Input
              id="display_name"
              autoComplete="off"
              placeholder="e.g. ci-prod-2026"
              {...form.register("display_name")}
            />
            <p className="text-[11px] text-[var(--color-fg-muted)]">
              Operator-facing label so the allowlist table doesn&apos;t
              render opaque hex.
            </p>
            {form.formState.errors.display_name ? (
              <p className="text-[11px] text-[var(--color-danger)]">
                {form.formState.errors.display_name.message}
              </p>
            ) : null}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={add.isPending}>
              {add.isPending ? "Approving…" : "Approve key"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// shortKey trims a key_id for the confirm-prompt fallback when an
// operator removes a key without a display_name. Keeps the dialog
// legible without copying a full SHA256 into the alert.
function shortKey(k: string): string {
  if (k.length <= 14) return k;
  return `${k.slice(0, 8)}…${k.slice(-4)}`;
}

// formatRelative is a one-shot relative formatter for the "added X
// ago" line. Avoids pulling in a full date-fns just for one card —
// we already use the lib in other surfaces, but this calc is
// trivial and the date stays a string-display so locale concerns
// don't apply.
function formatRelative(iso: string): string {
  const then = new Date(iso).getTime();
  const now = Date.now();
  const diffSec = Math.floor((now - then) / 1000);
  if (diffSec < 60) return "just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} min ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)} h ago`;
  return `${Math.floor(diffSec / 86400)} d ago`;
}
