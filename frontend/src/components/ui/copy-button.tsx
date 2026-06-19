import * as React from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "./button";
import { cn } from "@/lib/utils";

interface CopyButtonProps {
  value: string;
  label?: string;
  className?: string;
  // When true the button only shows the icon (used inside the pull-command card).
  iconOnly?: boolean;
}

// Beacon — CopyButton. Writes to the clipboard, flashes a check for 1.6s,
// then reverts. Safe to call even when `navigator.clipboard` is unavailable
// (older Safari / non-secure context) — falls back to a tiny textarea hack.
export function CopyButton({
  value,
  label = "Copy",
  className,
  iconOnly,
}: CopyButtonProps): React.ReactElement {
  const [copied, setCopied] = React.useState(false);

  async function handleCopy(): Promise<void> {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        // Legacy fallback. Not ideal but better than failing silently.
        const ta = document.createElement("textarea");
        ta.value = value;
        ta.setAttribute("readonly", "");
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_600);
    } catch {
      // Swallow — clipboard rejection is best-effort; the user will simply
      // not see the check flash. They can re-attempt manually.
    }
  }

  return (
    <Button
      variant="ghost"
      size={iconOnly ? "icon" : "sm"}
      onClick={() => void handleCopy()}
      className={cn(
        "gap-1.5 text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
        className,
      )}
      aria-label={copied ? "Copied" : label}
    >
      {copied ? (
        <Check className="size-4 text-[var(--color-success)]" />
      ) : (
        <Copy className="size-4" />
      )}
      {iconOnly ? null : <span>{copied ? "Copied" : label}</span>}
    </Button>
  );
}
