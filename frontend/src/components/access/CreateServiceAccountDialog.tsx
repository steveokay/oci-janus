import * as React from "react";
import { X } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useCreateServiceAccount } from "@/lib/api/service-accounts";

// SA name must satisfy the backend regex. Scopes chip suggestions.
// This list reflects what the backend currently supports as meaningful
// scope values; an SA may use any string, but we autocomplete these.
const SA_NAME_REGEX = /^[a-z0-9]+([._-][a-z0-9]+)*$/;
const SA_NAME_MAX = 64;

const DESC_MAX = 280;

// Known scope values for autocomplete. An SA's allowed_scopes is a gate
// on what keys issued under it can request — the SA itself doesn't gate
// actions, only provides the ceiling for its child keys.
const KNOWN_SCOPES = ["pull", "push", "scan", "admin"] as const;

function isValidName(value: string): boolean {
  return SA_NAME_REGEX.test(value) && value.length <= SA_NAME_MAX;
}

// detectErrorMessage inspects a thrown error for the HTTP status to
// surface a specific "name already taken" hint on 409 Conflict.
function detectErrorMessage(err: unknown): string {
  if (
    err &&
    typeof err === "object" &&
    "response" in err &&
    err.response &&
    typeof err.response === "object" &&
    "status" in err.response &&
    err.response.status === 409
  ) {
    return "A service account with that name already exists in this workspace. Choose a different name.";
  }
  return "Something went wrong creating the service account. Try again.";
}

interface CreateServiceAccountDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onCreated is called with the new SA's ID so the route can navigate to
  // ?id=<id> and T26's drawer can open immediately.
  onCreated: (id: string) => void;
}

// Beacon — CreateServiceAccountDialog (FE-API-048 T25).
//
// Fields:
//   name         — required; validated against SA_NAME_REGEX on every keystroke
//   description  — optional; max DESC_MAX chars; live char counter
//   scopes       — chip editor; comma or Enter to commit; backspace removes last chip
//
// On success the dialog closes and the parent navigates to ?id=<new-id> so
// T26's ServiceAccountDetail drawer can open.
export function CreateServiceAccountDialog({
  open,
  onOpenChange,
  onCreated,
}: CreateServiceAccountDialogProps): React.ReactElement {
  // Form field state.
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  // Chip editor: committed scopes list + the live input.
  const [scopes, setScopes] = React.useState<string[]>([]);
  const [scopeInput, setScopeInput] = React.useState("");
  // Track whether the name field has been touched so we don't show the
  // regex error immediately on open.
  const [nameTouched, setNameTouched] = React.useState(false);

  const createSA = useCreateServiceAccount();

  // Reset all state when the dialog opens or closes so stale values don't
  // survive a re-open sequence.
  React.useEffect(() => {
    if (!open) {
      setName("");
      setDescription("");
      setScopes([]);
      setScopeInput("");
      setNameTouched(false);
      createSA.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Derived validation state.
  const nameEmpty = name.trim().length === 0;
  const nameInvalid = nameTouched && !nameEmpty && !isValidName(name);
  const nameTooLong = name.length > SA_NAME_MAX;
  const descTooLong = description.length > DESC_MAX;
  const canSubmit =
    !nameEmpty &&
    isValidName(name) &&
    !descTooLong &&
    !createSA.isPending;

  // Commit the current scopeInput value as a chip if it is non-empty.
  // Leading/trailing whitespace is stripped; duplicates are silently ignored.
  function commitScopeInput(): void {
    const raw = scopeInput.trim();
    if (!raw) return;
    // Allow comma-separated entry in a single paste ("pull,push").
    const tokens = raw
      .split(",")
      .map((t) => t.trim().toLowerCase())
      .filter(Boolean);
    setScopes((prev) => {
      const existing = new Set(prev);
      return [...prev, ...tokens.filter((t) => !existing.has(t))];
    });
    setScopeInput("");
  }

  function removeScope(scope: string): void {
    setScopes((prev) => prev.filter((s) => s !== scope));
  }

  function handleScopeKeyDown(e: React.KeyboardEvent<HTMLInputElement>): void {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      commitScopeInput();
    } else if (e.key === "Backspace" && scopeInput === "" && scopes.length > 0) {
      // Remove the last chip on Backspace when the input is empty.
      setScopes((prev) => prev.slice(0, -1));
    }
  }

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    if (!canSubmit) return;

    try {
      const sa = await createSA.mutateAsync({
        name: name.trim(),
        description: description.trim() || undefined,
        allowed_scopes: scopes.length > 0 ? scopes : undefined,
      });
      onOpenChange(false);
      onCreated(sa.id);
    } catch {
      // Error surfaced via createSA.error below.
    }
  }

  const errorMessage = createSA.error
    ? detectErrorMessage(createSA.error)
    : null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New service account</DialogTitle>
          <DialogDescription>
            Service accounts are machine identities. Issue scoped API keys
            under them for CI pipelines and Terraform modules.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={(e) => void handleSubmit(e)} className="space-y-5">
          {/* Error banner — appears when mutation fails. */}
          {errorMessage ? (
            <div
              role="alert"
              className="rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-4 py-3 text-sm text-[var(--color-danger)]"
            >
              {errorMessage}
            </div>
          ) : null}

          {/* Name field */}
          <div className="space-y-1.5">
            <Label htmlFor="sa-name">Name *</Label>
            <Input
              id="sa-name"
              autoFocus
              autoComplete="off"
              placeholder="ci-pipeline"
              value={name}
              maxLength={SA_NAME_MAX + 10} /* let user type before we cut */
              onChange={(e) => {
                setName(e.target.value);
                if (!nameTouched && e.target.value.length > 0) {
                  setNameTouched(true);
                }
              }}
              onBlur={() => setNameTouched(true)}
              aria-invalid={nameInvalid || nameTooLong}
              aria-describedby={
                nameInvalid || nameTooLong ? "sa-name-error" : undefined
              }
              className={
                nameInvalid || nameTooLong
                  ? "border-[var(--color-danger)]"
                  : undefined
              }
            />
            {/* Inline error / hint message */}
            {nameTooLong ? (
              <p
                id="sa-name-error"
                className="text-xs text-[var(--color-danger)]"
              >
                Name must be {SA_NAME_MAX} characters or fewer (
                {name.length}/{SA_NAME_MAX}).
              </p>
            ) : nameInvalid ? (
              <p
                id="sa-name-error"
                className="text-xs text-[var(--color-danger)]"
              >
                Only lowercase letters, digits, dots, hyphens, and underscores.
                Must start and end with a letter or digit. Pattern:{" "}
                <code className="font-mono">^[a-z0-9]+([._-][a-z0-9]+)*$</code>
              </p>
            ) : (
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                Lowercase letters, digits, dots, hyphens, and underscores only.
                Max {SA_NAME_MAX} chars.
              </p>
            )}
          </div>

          {/* Description field */}
          <div className="space-y-1.5">
            <Label htmlFor="sa-description">Description</Label>
            <textarea
              id="sa-description"
              rows={2}
              placeholder="Optional — describe what this account is used for."
              value={description}
              maxLength={DESC_MAX + 20}
              onChange={(e) => setDescription(e.target.value)}
              aria-invalid={descTooLong}
              aria-describedby={descTooLong ? "sa-desc-error" : "sa-desc-counter"}
              className={[
                "flex w-full resize-none rounded-md border px-3 py-2 text-sm transition-colors",
                "bg-[var(--color-surface)] placeholder:text-[var(--color-fg-subtle)]",
                "focus-visible:outline-none focus-visible:border-[var(--color-accent)]",
                "disabled:cursor-not-allowed disabled:opacity-50",
                descTooLong
                  ? "border-[var(--color-danger)]"
                  : "border-[var(--color-border-strong)]",
              ].join(" ")}
            />
            {descTooLong ? (
              <p
                id="sa-desc-error"
                className="text-xs text-[var(--color-danger)]"
              >
                Description must be {DESC_MAX} characters or fewer (
                {description.length}/{DESC_MAX}).
              </p>
            ) : (
              <p
                id="sa-desc-counter"
                className="text-right text-[11px] text-[var(--color-fg-subtle)]"
              >
                {description.length}/{DESC_MAX}
              </p>
            )}
          </div>

          {/* Allowed scopes — chip editor */}
          <div className="space-y-1.5">
            <Label htmlFor="sa-scopes">Allowed scopes</Label>
            {/* Chip container + inline input — clicking the container focuses
                the input so the interaction feels like a normal text field. */}
            <div
              role="group"
              aria-label="Scope chips"
              className={[
                "flex min-h-[40px] flex-wrap items-center gap-1.5 rounded-md border px-3 py-2",
                "bg-[var(--color-surface)] transition-colors",
                "focus-within:border-[var(--color-accent)]",
                "border-[var(--color-border-strong)]",
              ].join(" ")}
              onClick={() =>
                document.getElementById("sa-scopes")?.focus()
              }
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
                    className="text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
                  >
                    <X className="size-3" aria-hidden />
                  </button>
                </span>
              ))}
              <input
                id="sa-scopes"
                type="text"
                value={scopeInput}
                onChange={(e) => setScopeInput(e.target.value)}
                onKeyDown={handleScopeKeyDown}
                onBlur={commitScopeInput}
                placeholder={scopes.length === 0 ? "pull, push, scan…" : ""}
                className="min-w-[80px] flex-1 bg-transparent text-sm focus:outline-none placeholder:text-[var(--color-fg-subtle)]"
                list="scope-suggestions"
                autoComplete="off"
              />
              {/* Datalist provides browser-native autocomplete suggestions. */}
              <datalist id="scope-suggestions">
                {KNOWN_SCOPES.filter((s) => !scopes.includes(s)).map((s) => (
                  <option key={s} value={s} />
                ))}
              </datalist>
            </div>
            <p className="text-[11px] text-[var(--color-fg-subtle)]">
              Press Enter or comma to add a scope. Keys issued under this account
              can only request scopes from this list.
            </p>
            {/* Quick-add buttons for known scopes not yet added */}
            {KNOWN_SCOPES.filter((s) => !scopes.includes(s)).length > 0 ? (
              <div className="flex flex-wrap gap-1 pt-0.5">
                <span className="self-center text-[11px] text-[var(--color-fg-subtle)]">
                  Add:
                </span>
                {KNOWN_SCOPES.filter((s) => !scopes.includes(s)).map((s) => (
                  <button
                    key={s}
                    type="button"
                    onClick={() =>
                      setScopes((prev) =>
                        prev.includes(s) ? prev : [...prev, s],
                      )
                    }
                    className="inline-flex items-center rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-fg-muted)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] transition-colors"
                  >
                    {s}
                  </button>
                ))}
              </div>
            ) : null}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={createSA.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="accent"
              loading={createSA.isPending}
              disabled={!canSubmit}
            >
              Create service account
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
