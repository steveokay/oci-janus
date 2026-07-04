import * as React from "react";
import {
  X,
  Pencil,
  Check,
  Plus,
  Trash2,
  KeyRound,
  Activity,
  AlertTriangle,
  ExternalLink,
} from "lucide-react";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  useServiceAccount,
  useSAKeys,
  useUpdateServiceAccount,
  useDisableServiceAccount,
  useDeleteServiceAccount,
  useIssueSAKey,
  useRevokeSAKey,
  useScopeShrinkPreflight,
  type SAApiKey,
} from "@/lib/api/service-accounts";
import { useActivity } from "@/lib/api/activity";
import { formatRelativeDate, formatAbsoluteDate } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { ExpiryBadge } from "@/components/ui/expiry-badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { SecretRevealDialog } from "@/components/webhooks/secret-reveal-dialog";
import { ScopeShrinkConfirmDialog } from "./ScopeShrinkConfirmDialog";
import { cn } from "@/lib/utils";

// ServiceAccountDetail — FE-API-048 T26.
//
// Slide-in drawer (fixed-right aside) that renders the full detail view for a
// single service account. Mounted by the service-accounts route when `?id=<id>`
// is present in the URL; closed by clearing the search param.
//
// Sections:
//   1. Identity card — inline-editable name + description + scope chip editor
//   2. API keys — list with Issue + Revoke, secret reveal on issue
//   3. Activity preview — last 5 events with "View all" link
//   4. Danger zone — disable/enable + delete

// ── Scope chip constants ──────────────────────────────────────────────────────

// Known scope values for autocomplete — must mirror CreateServiceAccountDialog.
const KNOWN_SCOPES = ["pull", "push", "scan", "admin"] as const;

// ── Props ─────────────────────────────────────────────────────────────────────

interface ServiceAccountDetailProps {
  // The service account ID to display — comes from the ?id URL search param.
  saID: string;
  // Called when the drawer should close (parent clears the ?id search param).
  onClose: () => void;
}

// ── Drawer shell ──────────────────────────────────────────────────────────────

// ServiceAccountDetail renders as a fixed-right `<aside>` so the background
// table remains fully interactive. No portal required — the aside sits in the
// normal DOM flow but uses `fixed` positioning.
export function ServiceAccountDetail({
  saID,
  onClose,
}: ServiceAccountDetailProps): React.ReactElement {
  const { data: sa, isLoading, isError } = useServiceAccount(saID);

  // Close on Escape key so keyboard users can dismiss without the mouse.
  React.useEffect(() => {
    function handleKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [onClose]);

  return (
    <>
      {/* Backdrop — semi-transparent overlay. Click to close. */}
      <div
        aria-hidden
        className="fixed inset-0 z-30 bg-[var(--color-fg)]/20 backdrop-blur-[2px]"
        onClick={onClose}
      />

      {/* Drawer panel */}
      <aside
        aria-label="Service account detail"
        className={cn(
          "fixed right-0 top-0 z-40 flex h-full w-full max-w-[480px] flex-col",
          "border-l border-[var(--color-border)] bg-[var(--color-surface)]",
          "shadow-[var(--shadow-floating)]",
          "overflow-y-auto",
        )}
      >
        {/* Drawer header */}
        <div className="flex shrink-0 items-center justify-between border-b border-[var(--color-border)] px-6 py-4">
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Service account
          </p>
          <button
            type="button"
            aria-label="Close drawer"
            onClick={onClose}
            className="rounded-md p-1 text-[var(--color-fg-muted)] transition-colors hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:outline-none"
          >
            <X className="size-4" />
          </button>
        </div>

        {/* Drawer body */}
        <div className="flex-1 space-y-8 px-6 py-6">
          {isLoading ? (
            <LoadingSkeleton />
          ) : isError || !sa ? (
            <div className="rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-4 py-3 text-sm text-[var(--color-danger)]">
              Couldn't load this service account. It may have been deleted.
            </div>
          ) : (
            <>
              {/* Disabled banner */}
              {sa.disabled_at ? (
                <div className="flex items-center gap-2 rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/5 px-3 py-2 text-xs text-[var(--color-warning)]">
                  <AlertTriangle className="size-4 shrink-0" aria-hidden />
                  This account is disabled. API keys issued under it will be
                  rejected until the account is re-enabled.
                </div>
              ) : null}

              {/* 1. Identity card */}
              <IdentitySection sa={sa} />

              {/* 2. API keys */}
              <ApiKeysSection saID={sa.id} allowedScopes={sa.allowed_scopes} />

              {/* 3. Activity preview */}
              <ActivityPreviewSection
                shadowUserID={sa.shadow_user_id}
                saName={sa.name}
              />

              {/* 4. Danger zone */}
              <DangerZoneSection sa={sa} onClose={onClose} />
            </>
          )}
        </div>
      </aside>
    </>
  );
}

// ── LoadingSkeleton ───────────────────────────────────────────────────────────

function LoadingSkeleton(): React.ReactElement {
  return (
    <div className="space-y-6">
      <Skeleton className="h-7 w-48" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-32 w-full" />
      <Skeleton className="h-24 w-full" />
    </div>
  );
}

// ── SectionHeading ────────────────────────────────────────────────────────────

function SectionHeading({
  children,
}: {
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <p className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
      {children}
    </p>
  );
}

// ── ScopeChips — read-only chip row ──────────────────────────────────────────

function ScopeChip({ scope }: { scope: string }): React.ReactElement {
  return (
    <span className="inline-flex items-center rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-fg-muted)]">
      {scope}
    </span>
  );
}

// ── ScopeChipEditor — interactive chip editor for allowed_scopes ──────────────

interface ScopeChipEditorProps {
  // Current committed scopes.
  scopes: string[];
  // Ceiling scopes — if provided, the input is restricted to these values.
  // Omit (undefined) to allow freeform.
  allowed?: string[];
  onChange: (newScopes: string[]) => void;
}

function ScopeChipEditor({
  scopes,
  allowed,
  onChange,
}: ScopeChipEditorProps): React.ReactElement {
  const [input, setInput] = React.useState("");

  // Commit the current input value as one or more chips.
  function commitInput(): void {
    const raw = input.trim();
    if (!raw) return;
    const tokens = raw
      .split(",")
      .map((t) => t.trim().toLowerCase())
      .filter(Boolean)
      // When an allowed-ceiling is provided, only accept tokens that are in it.
      .filter((t) => !allowed || allowed.includes(t));
    onChange([...new Set([...scopes, ...tokens])]);
    setInput("");
  }

  function removeScope(scope: string): void {
    onChange(scopes.filter((s) => s !== scope));
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>): void {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      commitInput();
    } else if (e.key === "Backspace" && input === "" && scopes.length > 0) {
      onChange(scopes.slice(0, -1));
    }
  }

  // Suggestions: all KNOWN_SCOPES not yet added (filtered by ceiling if set).
  const suggestions = KNOWN_SCOPES.filter(
    (s) => !scopes.includes(s) && (!allowed || allowed.includes(s)),
  );

  return (
    <div className="space-y-1.5">
      <div
        role="group"
        aria-label="Allowed scope chips"
        className={cn(
          "flex min-h-[40px] flex-wrap items-center gap-1.5 rounded-md border px-3 py-2",
          "bg-[var(--color-surface)] transition-colors",
          "focus-within:border-[var(--color-accent)]",
          "border-[var(--color-border-strong)]",
        )}
        onClick={() => document.getElementById("scope-chip-input")?.focus()}
      >
        {scopes.map((scope) => (
          <span
            key={scope}
            className="inline-flex items-center gap-1 rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-fg-muted)]"
          >
            {scope}
            <button
              type="button"
              aria-label={`Remove scope ${scope}`}
              onClick={(e) => {
                e.stopPropagation();
                removeScope(scope);
              }}
              className="text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)] transition-colors"
            >
              <X className="size-3" aria-hidden />
            </button>
          </span>
        ))}
        <input
          id="scope-chip-input"
          type="text"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          onBlur={commitInput}
          placeholder={scopes.length === 0 ? "pull, push, scan…" : ""}
          className="min-w-[80px] flex-1 bg-transparent text-sm focus:outline-none placeholder:text-[var(--color-fg-subtle)]"
          list="scope-chip-suggestions"
          autoComplete="off"
        />
        <datalist id="scope-chip-suggestions">
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      </div>
      {suggestions.length > 0 ? (
        <div className="flex flex-wrap gap-1 pt-0.5">
          <span className="self-center text-[11px] text-[var(--color-fg-subtle)]">
            Add:
          </span>
          {suggestions.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() =>
                onChange(scopes.includes(s) ? scopes : [...scopes, s])
              }
              className="inline-flex items-center rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-fg-muted)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] transition-colors"
            >
              {s}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

// ── IdentitySection ───────────────────────────────────────────────────────────

interface IdentitySectionProps {
  sa: {
    id: string;
    name: string;
    description: string;
    allowed_scopes: string[];
    created_at: string;
    disabled_at?: string | null;
  };
}

function IdentitySection({ sa }: IdentitySectionProps): React.ReactElement {
  const update = useUpdateServiceAccount();
  const preflight = useScopeShrinkPreflight();

  // Inline-edit state for name.
  const [editingName, setEditingName] = React.useState(false);
  const [nameDraft, setNameDraft] = React.useState(sa.name);
  const [nameSaving, setNameSaving] = React.useState(false);

  // Inline-edit state for description.
  const [editingDesc, setEditingDesc] = React.useState(false);
  const [descDraft, setDescDraft] = React.useState(sa.description ?? "");
  const [descSaving, setDescSaving] = React.useState(false);

  // Scope editor state — we keep a local draft so edits are not committed
  // until the operator explicitly saves them.
  const [scopeDraft, setScopeDraft] = React.useState<string[] | null>(null);
  const [scopeSaving, setScopeSaving] = React.useState(false);

  // Scope-shrink confirm dialog state.
  const [shrinkDialog, setShrinkDialog] = React.useState<{
    open: boolean;
    affected: number;
    newScopes: string[];
    removed: string[];
  }>({ open: false, affected: 0, newScopes: [], removed: [] });

  // Sync drafts when sa prop changes (e.g. after a successful PATCH).
  React.useEffect(() => {
    setNameDraft(sa.name);
  }, [sa.name]);
  React.useEffect(() => {
    setDescDraft(sa.description ?? "");
  }, [sa.description]);

  // ── Name save ──────────────────────────────────────────────────────────────

  async function saveName(): Promise<void> {
    const trimmed = nameDraft.trim();
    if (!trimmed || trimmed === sa.name) {
      setEditingName(false);
      return;
    }
    setNameSaving(true);
    try {
      await update.mutateAsync({ id: sa.id, name: trimmed });
      setEditingName(false);
      toast.success("Name updated.");
    } catch {
      toast.error("Couldn't save name. Try again.");
    } finally {
      setNameSaving(false);
    }
  }

  function cancelName(): void {
    setNameDraft(sa.name);
    setEditingName(false);
  }

  // ── Description save ───────────────────────────────────────────────────────

  async function saveDesc(): Promise<void> {
    const trimmed = descDraft.trim();
    if (trimmed === (sa.description ?? "")) {
      setEditingDesc(false);
      return;
    }
    setDescSaving(true);
    try {
      await update.mutateAsync({ id: sa.id, description: trimmed });
      setEditingDesc(false);
      toast.success("Description updated.");
    } catch {
      toast.error("Couldn't save description. Try again.");
    } finally {
      setDescSaving(false);
    }
  }

  function cancelDesc(): void {
    setDescDraft(sa.description ?? "");
    setEditingDesc(false);
  }

  // ── Scope change ───────────────────────────────────────────────────────────

  // onCommitScopeChange is called when the operator clicks "Save scopes".
  // If scopes are being narrowed, trigger the preflight check first.
  async function onCommitScopeChange(newScopes: string[]): Promise<void> {
    const removed = sa.allowed_scopes.filter((s) => !newScopes.includes(s));

    if (removed.length === 0) {
      // Pure scope addition or no-op — save directly without preflight.
      setScopeSaving(true);
      try {
        await update.mutateAsync({ id: sa.id, allowed_scopes: newScopes });
        setScopeDraft(null);
        toast.success("Scopes updated.");
      } catch {
        toast.error("Couldn't update scopes. Try again.");
      } finally {
        setScopeSaving(false);
      }
      return;
    }

    // Scopes are being narrowed — run preflight to get impact count.
    try {
      const affected = await preflight.mutateAsync({
        saID: sa.id,
        allowedScopes: newScopes,
      });
      setShrinkDialog({ open: true, affected, newScopes, removed });
    } catch {
      toast.error("Couldn't check scope impact. Try again.");
    }
  }

  async function confirmShrink(): Promise<void> {
    setScopeSaving(true);
    try {
      await update.mutateAsync({
        id: sa.id,
        allowed_scopes: shrinkDialog.newScopes,
      });
      setScopeDraft(null);
      setShrinkDialog((prev) => ({ ...prev, open: false }));
      toast.success("Scopes narrowed.");
    } catch {
      toast.error("Couldn't update scopes. Try again.");
    } finally {
      setScopeSaving(false);
    }
  }

  // Active scope draft (if editing) or committed scopes.
  const displayedScopes = scopeDraft ?? sa.allowed_scopes;
  const scopesChanged =
    scopeDraft !== null &&
    JSON.stringify([...scopeDraft].sort()) !==
      JSON.stringify([...sa.allowed_scopes].sort());

  return (
    <section aria-labelledby="identity-section-heading" className="space-y-4">
      <SectionHeading>Identity</SectionHeading>

      {/* SA name — inline editable */}
      <div className="space-y-1">
        <Label className="!text-[11px]">Name</Label>
        {editingName ? (
          <div className="flex items-center gap-2">
            <Input
              autoFocus
              value={nameDraft}
              onChange={(e) => setNameDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void saveName();
                if (e.key === "Escape") cancelName();
              }}
              className="h-8 font-display text-xl font-medium"
            />
            <Button
              variant="ghost"
              size="icon"
              loading={nameSaving}
              disabled={nameSaving}
              onClick={() => void saveName()}
              aria-label="Save name"
              className="text-[var(--color-success)]"
            >
              <Check className="size-4" />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              disabled={nameSaving}
              onClick={cancelName}
              aria-label="Cancel"
              className="text-[var(--color-fg-muted)]"
            >
              <X className="size-4" />
            </Button>
          </div>
        ) : (
          <div className="group flex items-center gap-2">
            <h2 className="font-display text-xl font-medium tracking-tight text-[var(--color-fg)]">
              {sa.name}
            </h2>
            <button
              type="button"
              aria-label="Edit name"
              onClick={() => setEditingName(true)}
              className="text-[var(--color-fg-subtle)] opacity-0 transition-opacity group-hover:opacity-100 hover:text-[var(--color-fg)]"
            >
              <Pencil className="size-3.5" />
            </button>
          </div>
        )}
      </div>

      {/* Description — inline editable */}
      <div className="space-y-1">
        <Label className="!text-[11px]">Description</Label>
        {editingDesc ? (
          <div className="space-y-1.5">
            <textarea
              autoFocus
              rows={2}
              value={descDraft}
              maxLength={280}
              onChange={(e) => setDescDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") cancelDesc();
                // Allow Shift+Enter for newline inside the textarea.
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  void saveDesc();
                }
              }}
              className={cn(
                "flex w-full resize-none rounded-md border px-3 py-2 text-sm transition-colors",
                "bg-[var(--color-surface)] placeholder:text-[var(--color-fg-subtle)]",
                "focus-visible:outline-none focus-visible:border-[var(--color-accent)]",
                "border-[var(--color-border-strong)]",
              )}
            />
            <div className="flex items-center justify-between">
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                {descDraft.length}/280
              </p>
              <div className="flex gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  loading={descSaving}
                  disabled={descSaving}
                  onClick={() => void saveDesc()}
                  aria-label="Save description"
                  className="text-[var(--color-success)]"
                >
                  <Check className="size-4" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  disabled={descSaving}
                  onClick={cancelDesc}
                  aria-label="Cancel"
                  className="text-[var(--color-fg-muted)]"
                >
                  <X className="size-4" />
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div className="group flex items-start gap-2">
            <p
              className={cn(
                "flex-1 text-sm",
                sa.description
                  ? "text-[var(--color-fg-muted)]"
                  : "italic text-[var(--color-fg-subtle)]",
              )}
            >
              {sa.description || "No description"}
            </p>
            <button
              type="button"
              aria-label="Edit description"
              onClick={() => setEditingDesc(true)}
              className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)] opacity-0 transition-opacity group-hover:opacity-100 hover:text-[var(--color-fg)]"
            >
              <Pencil className="size-3.5" />
            </button>
          </div>
        )}
      </div>

      {/* Allowed scopes — chip editor with scope-shrink preflight */}
      <div className="space-y-2">
        <Label className="!text-[11px]">Allowed scopes</Label>
        <ScopeChipEditor
          scopes={displayedScopes}
          onChange={(newScopes) => setScopeDraft(newScopes)}
        />
        <p className="text-[11px] text-[var(--color-fg-subtle)]">
          Keys issued under this account can only request scopes from this list.
          Removing a scope immediately restricts existing keys.
        </p>

        {/* Show save/cancel only when the draft differs from committed state. */}
        {scopesChanged ? (
          <div className="flex items-center gap-2 pt-1">
            <Button
              size="sm"
              variant="accent"
              loading={scopeSaving || preflight.isPending}
              disabled={scopeSaving || preflight.isPending}
              onClick={() => void onCommitScopeChange(displayedScopes)}
            >
              Save scopes
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={scopeSaving || preflight.isPending}
              onClick={() => setScopeDraft(null)}
            >
              Discard
            </Button>
          </div>
        ) : null}
      </div>

      {/* Read-only metadata */}
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-[var(--color-fg-subtle)]">
        <span>
          Created{" "}
          <span
            className="text-[var(--color-fg-muted)]"
            title={formatAbsoluteDate(sa.created_at)}
          >
            {formatRelativeDate(sa.created_at)}
          </span>
        </span>
        <span className="font-mono">id: {sa.id.slice(0, 8)}…</span>
      </div>

      {/* Scope-shrink confirm dialog */}
      <ScopeShrinkConfirmDialog
        open={shrinkDialog.open}
        onOpenChange={(o) =>
          setShrinkDialog((prev) => ({ ...prev, open: o }))
        }
        saName={sa.name}
        removed={shrinkDialog.removed}
        affectedKeys={shrinkDialog.affected}
        confirming={scopeSaving}
        onConfirm={() => void confirmShrink()}
      />
    </section>
  );
}

// ── ApiKeysSection ────────────────────────────────────────────────────────────

interface ApiKeysSectionProps {
  saID: string;
  // The SA's allowed_scopes ceiling — key scopes can only be a subset of these.
  allowedScopes: string[];
}

function ApiKeysSection({
  saID,
  allowedScopes,
}: ApiKeysSectionProps): React.ReactElement {
  const { data: keys, isLoading, isError, refetch } = useSAKeys(saID);
  const revoke = useRevokeSAKey(saID);

  // Issue key dialog state.
  const [issueOpen, setIssueOpen] = React.useState(false);
  // Secret reveal dialog state — holds the raw key after issue.
  const [newSecret, setNewSecret] = React.useState<string | null>(null);
  // Revoke confirmation dialog — holds the key being revoked.
  const [revokeTarget, setRevokeTarget] = React.useState<SAApiKey | null>(null);

  async function handleRevoke(key: SAApiKey): Promise<void> {
    try {
      await revoke.mutateAsync(key.id);
      setRevokeTarget(null);
      toast.success(`Key "${key.name}" revoked.`);
    } catch {
      toast.error("Couldn't revoke key. Try again.");
    }
  }

  return (
    <section aria-labelledby="api-keys-section-heading" className="space-y-3">
      <div className="flex items-center justify-between">
        <SectionHeading>API keys</SectionHeading>
        <Button size="sm" variant="outline" onClick={() => setIssueOpen(true)}>
          <Plus className="size-3.5" />
          Issue key
        </Button>
      </div>

      {isError ? (
        <div className="rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-3 py-2 text-sm text-[var(--color-danger)]">
          Couldn't load keys.{" "}
          <button
            type="button"
            className="underline"
            onClick={() => void refetch()}
          >
            Retry
          </button>
        </div>
      ) : isLoading ? (
        <div className="space-y-2">
          {[1, 2].map((i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : !keys || keys.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-[var(--color-border)] py-6 text-center">
          <KeyRound className="size-6 text-[var(--color-fg-subtle)]" aria-hidden />
          <p className="text-sm text-[var(--color-fg-muted)]">
            No active API keys
          </p>
          <Button size="sm" variant="outline" onClick={() => setIssueOpen(true)}>
            <Plus className="size-3.5" />
            Issue first key
          </Button>
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Prefix</TableHead>
                <TableHead className="hidden sm:table-cell">Scopes</TableHead>
                {/* The cell below renders `expires_at`; the header used to say
                    "Last used" which mislabelled the column. SAApiKey carries
                    no last_used_at field (T14 enrichment doesn't include it),
                    so there is no real "Last used" column to add yet. */}
                <TableHead className="hidden md:table-cell">Expires</TableHead>
                <TableHead className="text-right">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.map((key) => (
                <TableRow key={key.id}>
                  <TableCell className="py-2.5">
                    <span className="text-sm font-medium text-[var(--color-fg)]">
                      {key.name}
                    </span>
                  </TableCell>
                  <TableCell>
                    <code className="font-mono text-[11px] text-[var(--color-fg-muted)]">
                      {key.prefix}
                    </code>
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">
                    <div className="flex flex-wrap gap-1">
                      {key.scopes.length > 0 ? (
                        key.scopes.map((s) => <ScopeChip key={s} scope={s} />)
                      ) : (
                        <span className="text-xs text-[var(--color-fg-subtle)]">
                          —
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    {/* Urgency-aware expiry (shared ExpiryBadge): danger */}
                    {/* "Expired", warning countdown within 14 days, else */}
                    {/* plain relative time; muted "Never" when unset. */}
                    <ExpiryBadge expiresAt={key.expires_at} />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setRevokeTarget(key)}
                      className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                    >
                      <Trash2 className="size-3.5" />
                      Revoke
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {/* Issue key dialog */}
      <IssueKeyDialog
        open={issueOpen}
        onOpenChange={setIssueOpen}
        saID={saID}
        allowedScopes={allowedScopes}
        onIssued={(rawKey) => {
          setIssueOpen(false);
          setNewSecret(rawKey);
        }}
      />

      {/* Secret reveal — shown once immediately after key issue */}
      <SecretRevealDialog
        open={newSecret !== null}
        onOpenChange={(o) => {
          if (!o) setNewSecret(null);
        }}
        secret={newSecret}
        title="New API key secret"
        description="Copy this key now — it won't be shown again. Use it as the Bearer token or Basic-auth password when authenticating against the registry."
        onAcknowledge={() => toast.success("Key issued.")}
      />

      {/* Revoke confirmation dialog */}
      {revokeTarget ? (
        <RevokeKeyDialog
          open
          onOpenChange={(o) => {
            if (!o) setRevokeTarget(null);
          }}
          keyName={revokeTarget.name}
          revoking={revoke.isPending}
          onConfirm={() => void handleRevoke(revokeTarget)}
        />
      ) : null}
    </section>
  );
}

// ── IssueKeyDialog ────────────────────────────────────────────────────────────

interface IssueKeyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  saID: string;
  // Ceiling scopes from the parent SA — key scopes must be a subset of these.
  allowedScopes: string[];
  // Called with the raw plaintext secret returned by the backend.
  onIssued: (rawKey: string) => void;
}

function IssueKeyDialog({
  open,
  onOpenChange,
  saID,
  allowedScopes,
  onIssued,
}: IssueKeyDialogProps): React.ReactElement {
  const issue = useIssueSAKey(saID);
  const [name, setName] = React.useState("");
  const [scopes, setScopes] = React.useState<string[]>([]);
  const [submitting, setSubmitting] = React.useState(false);

  // Reset form when dialog closes.
  React.useEffect(() => {
    if (!open) {
      setName("");
      setScopes([]);
      issue.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Pre-populate scopes with the parent SA's allowed scopes when the dialog
  // opens — a key almost always wants all scopes its parent allows.
  React.useEffect(() => {
    if (open) {
      setScopes(allowedScopes);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    if (!name.trim()) return;
    setSubmitting(true);
    try {
      const created = await issue.mutateAsync({
        name: name.trim(),
        scopes,
      });
      // `key` is the plaintext secret — present only on issue.
      onIssued(created.key ?? "");
    } catch {
      const status = (issue.error as { response?: { status?: number } } | null)
        ?.response?.status;
      toast.error(
        status === 422
          ? "Invalid key configuration. Check name and scopes."
          : "Couldn't issue key. Try again.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-4 text-[var(--color-accent)]" />
            Issue API key
          </DialogTitle>
          <DialogDescription>
            Create a scoped API key owned by this service account. Scopes must
            be a subset of the account's allowed scopes.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={(e) => void handleSubmit(e)} className="space-y-4">
          {/* Name */}
          <div className="space-y-1.5">
            <Label htmlFor="issue-key-name">Name</Label>
            <Input
              id="issue-key-name"
              autoFocus
              placeholder="deploy-prod"
              autoComplete="off"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>

          {/* Scopes — restricted to the SA's allowed_scopes ceiling */}
          <div className="space-y-1.5">
            <Label>Scopes</Label>
            <ScopeChipEditor
              scopes={scopes}
              allowed={allowedScopes.length > 0 ? allowedScopes : undefined}
              onChange={setScopes}
            />
            {allowedScopes.length > 0 ? (
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                Restricted to the account's ceiling:{" "}
                {allowedScopes.join(", ") || "none"}.
              </p>
            ) : null}
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
              type="submit"
              loading={submitting}
              disabled={submitting || !name.trim()}
            >
              Issue key
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ── RevokeKeyDialog ───────────────────────────────────────────────────────────

interface RevokeKeyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  keyName: string;
  revoking: boolean;
  onConfirm: () => void;
}

function RevokeKeyDialog({
  open,
  onOpenChange,
  keyName,
  revoking,
  onConfirm,
}: RevokeKeyDialogProps): React.ReactElement {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Revoke "{keyName}"?</DialogTitle>
          <DialogDescription>
            This immediately invalidates the key. Any pipeline or script using
            it will receive 401 Unauthorized. This cannot be undone — issue a
            new key if you need access restored.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={revoking}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            loading={revoking}
            disabled={revoking}
            onClick={onConfirm}
          >
            Revoke key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ── ActivityPreviewSection ────────────────────────────────────────────────────

interface ActivityPreviewSectionProps {
  shadowUserID: string;
  saName: string;
}

function ActivityPreviewSection({
  shadowUserID,
  saName,
}: ActivityPreviewSectionProps): React.ReactElement {
  // Fetch the last 5 events for this SA's shadow user.
  const { data, isLoading } = useActivity(shadowUserID, 5);
  const rows = data?.activity ?? [];

  return (
    <section
      aria-labelledby="activity-preview-section-heading"
      className="space-y-3"
    >
      <div className="flex items-center justify-between">
        <SectionHeading>Recent activity</SectionHeading>
        {/* "View all" navigates to the activity page pre-filtered to this SA */}
        <Link
          to="/api-keys/activity"
          search={{ principal: shadowUserID } as Record<string, string>}
          className="flex items-center gap-1 text-[11px] text-[var(--color-accent)] hover:underline"
        >
          View all
          <ExternalLink className="size-3" aria-hidden />
        </Link>
      </div>

      {isLoading ? (
        <div className="space-y-1.5">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <div className="flex items-center gap-2 rounded-md border border-dashed border-[var(--color-border)] px-3 py-4 text-sm text-[var(--color-fg-subtle)]">
          <Activity className="size-4 shrink-0" aria-hidden />
          No activity recorded yet.
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Action</TableHead>
                <TableHead className="text-right">Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row, i) => (
                <TableRow key={`${row.at}-${i}`}>
                  <TableCell>
                    <span
                      className="text-xs text-[var(--color-fg-muted)]"
                      title={formatAbsoluteDate(row.at)}
                    >
                      {formatRelativeDate(row.at)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <code className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-xs text-[var(--color-fg)]">
                      {row.action || "—"}
                    </code>
                  </TableCell>
                  <TableCell className="text-right">
                    <Badge
                      tone={row.status === "success" ? "success" : "danger"}
                    >
                      {row.status}
                    </Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {/* Only show the "View all" link in context if there are events */}
      {rows.length > 0 ? (
        <p className="text-[11px] text-[var(--color-fg-subtle)]">
          Showing last {rows.length} event{rows.length !== 1 ? "s" : ""} for{" "}
          <span className="text-[var(--color-fg-muted)]">{saName}</span>.
        </p>
      ) : null}
    </section>
  );
}

// ── DangerZoneSection ─────────────────────────────────────────────────────────

interface DangerZoneSectionProps {
  sa: {
    id: string;
    name: string;
    disabled_at?: string | null;
    active_key_count: number;
  };
  onClose: () => void;
}

function DangerZoneSection({
  sa,
  onClose,
}: DangerZoneSectionProps): React.ReactElement {
  const disable = useDisableServiceAccount();
  const deleteSA = useDeleteServiceAccount();

  const [deleteDialogOpen, setDeleteDialogOpen] = React.useState(false);
  const [toggling, setToggling] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  const isDisabled = Boolean(sa.disabled_at);

  async function handleToggleDisabled(): Promise<void> {
    setToggling(true);
    try {
      await disable.mutateAsync({ id: sa.id, disabled: !isDisabled });
      toast.success(
        isDisabled
          ? `"${sa.name}" re-enabled.`
          : `"${sa.name}" disabled. Existing keys are now rejected.`,
      );
    } catch {
      toast.error(
        `Couldn't ${isDisabled ? "re-enable" : "disable"} account. Try again.`,
      );
    } finally {
      setToggling(false);
    }
  }

  async function handleDelete(): Promise<void> {
    setDeleting(true);
    try {
      await deleteSA.mutateAsync(sa.id);
      toast.success(`"${sa.name}" deleted.`);
      setDeleteDialogOpen(false);
      // Close the drawer — the SA no longer exists.
      onClose();
    } catch {
      toast.error("Couldn't delete account. Try again.");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <section
      aria-labelledby="danger-zone-section-heading"
      className="space-y-3 rounded-lg border border-[var(--color-danger)]/30 p-4"
    >
      <SectionHeading>Danger zone</SectionHeading>

      {/* Disable / Re-enable */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-[var(--color-fg)]">
            {isDisabled ? "Re-enable account" : "Disable account"}
          </p>
          <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
            {isDisabled
              ? "Re-enables this account. Existing API keys will be accepted again."
              : "Disables this account. All API keys issued under it will be rejected immediately."}
          </p>
        </div>
        <Button
          variant={isDisabled ? "outline" : "outline"}
          size="sm"
          loading={toggling}
          disabled={toggling}
          onClick={() => void handleToggleDisabled()}
          className={
            isDisabled
              ? "shrink-0 text-[var(--color-success)] border-[var(--color-success)]/40 hover:bg-[var(--color-success)]/5"
              : "shrink-0 text-[var(--color-warning)] border-[var(--color-warning)]/40 hover:bg-[var(--color-warning)]/5"
          }
        >
          {isDisabled ? "Re-enable" : "Disable"}
        </Button>
      </div>

      <div className="h-px bg-[var(--color-danger)]/20" />

      {/* Delete */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-[var(--color-danger)]">
            Delete account
          </p>
          <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
            Permanently removes this account, its shadow user, and all{" "}
            {sa.active_key_count > 0 ? sa.active_key_count : "associated"}{" "}
            API key{sa.active_key_count !== 1 ? "s" : ""}. Cannot be undone.
          </p>
        </div>
        <Button
          variant="danger"
          size="sm"
          className="shrink-0"
          onClick={() => setDeleteDialogOpen(true)}
        >
          Delete
        </Button>
      </div>

      {/* Delete confirmation dialog */}
      <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete "{sa.name}"?</DialogTitle>
            <DialogDescription>
              This cascades to{" "}
              <strong>
                {sa.active_key_count} key
                {sa.active_key_count !== 1 ? "s" : ""}
              </strong>{" "}
              and cannot be undone. All API keys issued under this account will
              stop working immediately, and all associated data will be
              permanently removed.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setDeleteDialogOpen(false)}
              disabled={deleting}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="danger"
              loading={deleting}
              disabled={deleting}
              onClick={() => void handleDelete()}
            >
              Delete account
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
