import * as React from "react";
import { toast } from "sonner";
import { Copy, Download, ShieldAlert, Check } from "lucide-react";
import { Button } from "@/components/ui/button";

// Beacon — MfaBackupCodes.
//
// Presentational panel that renders the one-time backup codes returned by the
// enrolment verify step (or a regeneration). The backend serves these codes
// exactly once, so the panel leans hard on "save them now": a monospace grid,
// Copy + Download affordances, a loud warning, and — when the parent passes
// `onConfirm` — an "I've saved these codes" acknowledgement that unblocks the
// enrolment dialog.
interface MfaBackupCodesProps {
  codes: string[];
  // Optional acknowledgement callback. When provided we render the
  // "I've saved these codes" button; the enrolment dialog wires it to close.
  onConfirm?: () => void;
}

export function MfaBackupCodes({
  codes,
  onConfirm,
}: MfaBackupCodesProps): React.ReactElement {
  // Local "copied" flash so the Copy button confirms the clipboard write.
  const [copied, setCopied] = React.useState(false);

  // Codes are joined newline-separated for both clipboard + file so the user
  // gets one canonical, paste-friendly block.
  const asText = codes.join("\n");

  async function handleCopy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(asText);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1_600);
      toast.success("Backup codes copied to clipboard.");
    } catch {
      // Clipboard can reject in non-secure contexts — nudge the user to the
      // Download fallback rather than failing silently.
      toast.error("Couldn't copy. Use Download instead.");
    }
  }

  function handleDownload(): void {
    // Build a text Blob and trigger a synthetic anchor click. Revoke the
    // object URL afterwards so we don't leak it for the page lifetime.
    const blob = new Blob([`${asText}\n`], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "backup-codes.txt";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    toast.success("Backup codes downloaded.");
  }

  return (
    <div className="space-y-4">
      {/* Warning banner — codes are shown once and never re-served. */}
      <div className="flex items-start gap-2 rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3 text-xs text-[var(--color-warning)]">
        <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
        <p>
          Save these backup codes somewhere safe. They're shown{" "}
          <strong>only once</strong> — each lets you sign in if you lose your
          authenticator. You won't be able to see them again.
        </p>
      </div>

      {/* Monospace grid of the codes. */}
      <ul className="grid grid-cols-2 gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
        {codes.map((code) => (
          <li
            key={code}
            className="select-all text-center font-mono text-sm tracking-wide text-[var(--color-fg)]"
          >
            {code}
          </li>
        ))}
      </ul>

      {/* Copy + Download side by side. */}
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => void handleCopy()}
        >
          {copied ? (
            <Check className="size-4 text-[var(--color-success)]" />
          ) : (
            <Copy className="size-4" />
          )}
          {copied ? "Copied" : "Copy"}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleDownload}
        >
          <Download className="size-4" />
          Download
        </Button>
      </div>

      {/* Acknowledgement — only when the parent wants a confirm gate. */}
      {onConfirm ? (
        <Button type="button" className="w-full" onClick={onConfirm}>
          I've saved these codes
        </Button>
      ) : null}
    </div>
  );
}
