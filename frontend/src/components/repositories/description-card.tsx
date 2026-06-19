import * as React from "react";
import { FileText } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";

interface DescriptionCardProps {
  description: string | undefined;
}

// Beacon — repository description (FE-API-006).
//
// Renders the operator-supplied markdown description as paragraphs.
// We intentionally do NOT run a markdown parser yet — see FE-SEC-011
// in status.md ("user-supplied content must use React's default text
// rendering, no dangerouslySetInnerHTML"). Each blank-line-separated
// block becomes a <p>. When we add a proper sanitised renderer this is
// the one place to swap it in.
export function DescriptionCard({
  description,
}: DescriptionCardProps): React.ReactElement | null {
  // No description? Render nothing — the detail page already has plenty
  // of structure. Editing comes in S7 with the broader profile rework.
  if (!description || !description.trim()) return null;

  const paragraphs = description
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .filter(Boolean);

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center gap-2">
          <FileText className="size-3.5 text-[var(--color-fg-subtle)]" aria-hidden />
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Description
          </CardDescription>
        </div>
      </CardHeader>
      <CardContent>
        <div className="space-y-3 text-sm leading-relaxed text-[var(--color-fg)]">
          {paragraphs.map((p, i) => (
            <p key={i}>{p}</p>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}
