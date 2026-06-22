import * as React from "react";

// PreviewBanner — amber informational banner rendered at the top of every
// FUT-00x preview surface. Tells operators that the content is illustrative
// and ships in a future sprint.
//
// Accessibility:
//   - role="status" + aria-live="polite" so assistive technology announces
//     the banner without interrupting the user's current focus.
//   - No interactive controls — pure informational region.
interface PreviewBannerProps {
  /** Human-readable sprint label, e.g. "Sprint 11". */
  sprint: string;
  /** Backlog future item identifier, e.g. "FUT-001". */
  futureID: string;
}

export function PreviewBanner({
  sprint,
  futureID,
}: PreviewBannerProps): React.ReactElement {
  return (
    <div
      role="status"
      aria-live="polite"
      className="rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm dark:border-amber-700 dark:bg-amber-950/40"
    >
      <strong className="font-medium">Preview.</strong> This surface ships in{" "}
      <strong>{sprint}</strong> (<code className="font-mono">{futureID}</code>).
      The data below is illustrative. Have feedback? Drop it in{" "}
      <code className="font-mono">futures.md</code>.
    </div>
  );
}
