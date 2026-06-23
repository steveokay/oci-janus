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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useTrustedKeys,
  useAddTrustedKey,
  useRemoveTrustedKey,
  useRecentSigners,
  type RecentSigner,
} from "@/lib/api/trusted-keys";
import { useRepository } from "@/lib/api/repositories";
import { cn } from "@/lib/utils";

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

// AddTrustedKeyMode — picker vs manual entry. Two pill buttons up top
// flip between them. Auto-selects "manual" when the recent-signers list
// comes back empty so the operator doesn't land on a dead-end tab.
type AddTrustedKeyMode = "recent" | "manual";

function AddTrustedKeyDialog({
  open,
  onOpenChange,
  org,
  repo,
}: AddTrustedKeyDialogProps): React.ReactElement {
  const add = useAddTrustedKey();
  // Recent-signers feeds the "Pick from recent signers" mode. Hook is
  // gated on `open` so we don't fan out the per-tag ListSignatures call
  // when the dialog is closed — saves ~20 gRPC hops per page mount.
  const {
    data: recentSigners,
    isLoading: recentSignersLoading,
  } = useRecentSigners(org, repo, open);
  const form = useForm<AddValues>({
    resolver: zodResolver(addSchema),
    defaultValues: { key_id: "", display_name: "" },
  });

  const [mode, setMode] = React.useState<AddTrustedKeyMode>("recent");
  // Tracks which recent-signer entry is currently selected. Stored as
  // key_id rather than index so the Select stays stable across refetches
  // that might reorder the underlying list.
  const [pickedKeyID, setPickedKeyID] = React.useState<string>("");

  // Reset the form + mode + selection on open transitions so re-opening
  // after a submit doesn't replay the previous values + errors.
  React.useEffect(() => {
    if (open) {
      form.reset({ key_id: "", display_name: "" });
      setMode("recent");
      setPickedKeyID("");
    }
  }, [open, form]);

  // When the recent-signers query resolves to empty, downgrade the
  // default mode to "manual" so the dialog opens straight on a usable
  // tab. Operators with an unsigned repo would otherwise see the empty-
  // state and have to click the second pill before they can type.
  React.useEffect(() => {
    if (!open) return;
    if (!recentSignersLoading && (recentSigners?.length ?? 0) === 0) {
      setMode("manual");
    }
  }, [open, recentSigners, recentSignersLoading]);

  // When the operator picks a recent signer, sync its key_id into the
  // form's hidden value + auto-fill the display_name input with the
  // signer_id. Operator can still edit display_name afterward — the
  // form's `setValue` keeps the input controlled.
  function handlePickRecent(keyID: string): void {
    setPickedKeyID(keyID);
    const entry = recentSigners?.find((s) => s.key_id === keyID);
    if (entry) {
      form.setValue("key_id", entry.key_id, { shouldValidate: true });
      // Only auto-fill the display_name when the operator hasn't typed
      // anything yet — don't overwrite their work if they're flipping
      // back and forth between picker rows.
      if (!form.getValues("display_name") && entry.signer_id) {
        form.setValue("display_name", entry.signer_id);
      }
    }
  }

  // When the operator flips between Recent / Manual we need to reset
  // the hidden key_id field so a stale picker selection doesn't get
  // submitted from manual mode (or vice versa).
  function handleModeChange(next: AddTrustedKeyMode): void {
    setMode(next);
    if (next === "manual") {
      // Don't blow away whatever the operator may have started typing
      // already; just clear the picker's selection state.
      setPickedKeyID("");
    } else {
      // Switching back to picker → clear any half-typed manual value so
      // the form doesn't submit a stale string when the operator then
      // picks an entry that doesn't validate.
      form.setValue("key_id", "");
      form.setValue("display_name", "");
    }
  }

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

  const hasRecent = (recentSigners?.length ?? 0) > 0;

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

        {/* Mode pill bar — picker vs manual. Two Button variants styled
            as pills so we don't have to introduce a new RadioGroup
            primitive. role=tablist keeps the toggle semantically
            keyboard-navigable for screen readers. */}
        <div
          role="tablist"
          aria-label="Approval mode"
          className="inline-flex items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-1"
        >
          <ModePill
            label="Recent signer"
            active={mode === "recent"}
            onSelect={() => handleModeChange("recent")}
          />
          <ModePill
            label="Manual entry"
            active={mode === "manual"}
            onSelect={() => handleModeChange("manual")}
          />
        </div>

        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
          {mode === "recent" ? (
            <RecentSignerPicker
              loading={recentSignersLoading}
              signers={recentSigners ?? []}
              picked={pickedKeyID}
              onPick={handlePickRecent}
            />
          ) : (
            <ManualEntryFields form={form} />
          )}

          {/* The display_name input is shared across both modes — the
              picker auto-fills it from signer_id but the operator can
              still edit. Lives outside the mode-specific blocks so the
              picker→manual switch doesn't lose a typed-in name. */}
          {mode === "recent" && hasRecent ? (
            <div className="space-y-1.5">
              <Label htmlFor="display_name_recent">Display name (optional)</Label>
              <Input
                id="display_name_recent"
                autoComplete="off"
                placeholder="e.g. ci-prod-2026"
                {...form.register("display_name")}
              />
              <p className="text-[11px] text-[var(--color-fg-muted)]">
                Auto-filled from the signer&apos;s identity above — feel
                free to override with something more operator-friendly.
              </p>
              {form.formState.errors.display_name ? (
                <p className="text-[11px] text-[var(--color-danger)]">
                  {form.formState.errors.display_name.message}
                </p>
              ) : null}
            </div>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                add.isPending ||
                // Block submit on picker mode without a selection to
                // avoid the awkward "approve" → 400 round-trip on an
                // empty key_id.
                (mode === "recent" && !pickedKeyID)
              }
            >
              {add.isPending ? "Approving…" : "Approve key"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ModePill — one of the two segmented-control buttons that flips the
// dialog between picker + manual. Visual: looks like a subtle inline
// tab; active state uses the accent surface so the choice is obvious.
// Keeps the inline layout flat instead of pulling in a full
// RadioGroup primitive for two options.
interface ModePillProps {
  label: string;
  active: boolean;
  onSelect: () => void;
}

function ModePill({ label, active, onSelect }: ModePillProps): React.ReactElement {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onSelect}
      className={cn(
        "rounded px-2.5 py-1 text-xs font-medium transition-colors",
        active
          ? "bg-[var(--color-accent-subtle)] text-[var(--color-accent-fg)]"
          : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)]",
      )}
    >
      {label}
    </button>
  );
}

// RecentSignerPicker — Select-based dropdown listing recent signers.
// Renders empty-state copy when the BFF returned no rows, prompting the
// operator to switch to Manual entry instead of dead-ending the flow.
interface RecentSignerPickerProps {
  loading: boolean;
  signers: RecentSigner[];
  picked: string;
  onPick: (keyID: string) => void;
}

function RecentSignerPicker({
  loading,
  signers,
  picked,
  onPick,
}: RecentSignerPickerProps): React.ReactElement {
  if (loading) {
    return <Skeleton className="h-9 w-full" />;
  }
  if (signers.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-3 text-xs text-[var(--color-fg-muted)]">
        No recent signatures in this repo — sign a tag first, or switch
        to <strong>Manual entry</strong> and paste the{" "}
        <code className="font-mono text-[10px]">key_id</code>.
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor="recent-signer-select">Recent signer</Label>
      <Select value={picked} onValueChange={onPick}>
        <SelectTrigger id="recent-signer-select" className="h-auto w-full py-1.5">
          <SelectValue placeholder="Select a recent signer…" />
        </SelectTrigger>
        <SelectContent>
          {signers.map((s) => (
            <SelectItem key={s.key_id} value={s.key_id}>
              <div className="flex w-full items-center justify-between gap-3">
                <span className="flex flex-col items-start">
                  <span className="font-mono text-[11px] text-[var(--color-fg)]">
                    {truncateKey(s.key_id)}
                  </span>
                  {s.signer_id ? (
                    <span className="text-[10px] text-[var(--color-fg-muted)]">
                      {s.signer_id}
                    </span>
                  ) : null}
                </span>
                <span className="text-[10px] text-[var(--color-fg-subtle)]">
                  {relativeFromNow(s.last_signed_at)}
                </span>
              </div>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-[11px] text-[var(--color-fg-muted)]">
        Showing the most recent {signers.length}{" "}
        {signers.length === 1 ? "signer" : "signers"} from this repo.
      </p>
    </div>
  );
}

// ManualEntryFields — the original key_id + display_name input pair,
// kept verbatim from the FE-API-003 dialog so operators with an
// off-platform key_id (e.g. a freshly-rotated Cosign keyless cert)
// can still pin it directly.
interface ManualEntryFieldsProps {
  form: ReturnType<typeof useForm<AddValues>>;
}

function ManualEntryFields({ form }: ManualEntryFieldsProps): React.ReactElement {
  return (
    <>
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
          Click <strong>Recent signer</strong> above to choose from keys
          that recently signed in this repo, or paste a{" "}
          <code className="font-mono text-[10px]">key_id</code> directly
          here.
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
    </>
  );
}

// truncateKey shrinks a long key_id to "aaaaaaaa…bbbb" so the dropdown
// row stays one line on a typical 480px-wide dialog. Tooltip on hover
// (via the underlying SelectItem) shows the full value when needed.
function truncateKey(k: string): string {
  if (k.length <= 20) return k;
  return `${k.slice(0, 8)}…${k.slice(-4)}`;
}

// relativeFromNow is a lightweight "X ago" formatter for the recent-
// signer rows. Mirrors formatRelative below but keeps a separate
// implementation so the dropdown can use a shorter form ("5m" vs
// "5 min ago") in the cramped row layout.
function relativeFromNow(iso: string): string {
  const then = new Date(iso).getTime();
  const now = Date.now();
  const diffSec = Math.floor((now - then) / 1000);
  if (diffSec < 60) return "just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  return `${Math.floor(diffSec / 86400)}d ago`;
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
