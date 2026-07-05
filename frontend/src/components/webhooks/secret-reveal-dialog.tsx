import * as React from "react";
import { ShieldAlert, Eye, EyeOff } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { cn } from "@/lib/utils";

interface SecretRevealDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  secret: string | null;
  title: string;
  description: string;
  // Caller can optionally pre-acknowledge a follow-up nav (e.g. navigate to
  // the new webhook detail page) only after the dialog is dismissed.
  onAcknowledge?: () => void;
}

// Beacon — SecretRevealDialog. Used by webhook create + rotate-secret AND
// any future "show once" surface (API key issue is the natural reuse).
//
// UX rules:
//   * Secret is masked by default — user clicks "Reveal" to expose the
//     plaintext. Prevents shoulder-surfing during demos / screenshares.
//   * Copy works whether or not the value is currently revealed.
//   * The only way out of the dialog is the explicit acknowledgement
//     button — clicking the X corner or the overlay is ignored so a
//     stray click can't dismiss the secret unread.
export function SecretRevealDialog({
  open,
  onOpenChange,
  secret,
  title,
  description,
  onAcknowledge,
}: SecretRevealDialogProps): React.ReactElement | null {
  const [revealed, setRevealed] = React.useState(false);

  React.useEffect(() => {
    if (!open) setRevealed(false);
  }, [open]);

  function acknowledge(): void {
    onOpenChange(false);
    onAcknowledge?.();
  }

  if (!secret) return null;

  return (
    <Dialog open={open} onOpenChange={() => {/* gated — see below */}}>
      <DialogContent
        // Block the outside-click + escape dismissal so the operator must
        // explicitly acknowledge. They've been warned the secret is one-time.
        onInteractOutside={(e) => e.preventDefault()}
        onEscapeKeyDown={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-highlight)]">
            <ShieldAlert className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Shown once
            </span>
          </div>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="flex items-center gap-2 rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-3 py-2.5">
            <code
              className={cn(
                "min-w-0 flex-1 truncate font-mono text-xs",
                revealed
                  ? "text-[var(--color-fg)]"
                  : "text-[var(--color-fg-subtle)]",
              )}
            >
              {revealed ? secret : mask(secret)}
            </code>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setRevealed((r) => !r)}
              className="gap-1.5"
              aria-label={revealed ? "Hide secret" : "Reveal secret"}
            >
              {revealed ? (
                <EyeOff className="size-4" />
              ) : (
                <Eye className="size-4" />
              )}
              {revealed ? "Hide" : "Reveal"}
            </Button>
            {/* UIR-10: labelled (not icon-only) so the primary "get the
                secret out" action is unmistakable — this dialog is the only
                chance to copy a shown-once secret. */}
            <CopyButton value={secret} label="Copy secret" />
          </div>

          <div className="rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/5 px-3 py-2 text-xs text-[var(--color-fg)]">
            Store this somewhere safe — your secrets manager, an env var on
            your delivery target. It won't be shown again, and there's no
            "view secret" endpoint. If you lose it, rotate to issue a new one.
          </div>
        </div>

        <DialogFooter>
          <Button onClick={acknowledge}>I've saved it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Render a secret as "xxxx•••••••••• yyyy" — keep a tiny prefix/suffix so
// the user can verify they copied the right one without re-revealing.
function mask(secret: string): string {
  if (secret.length <= 12) return "•".repeat(secret.length);
  return `${secret.slice(0, 4)}${"•".repeat(Math.max(12, secret.length - 8))}${secret.slice(-4)}`;
}
