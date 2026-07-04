import * as React from "react";
import { KeyRound } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

// SSOReadOnlyCard — shared "SSO is deployment-config, not editable here"
// explainer. Extracted from the near-duplicate cards that lived inline in
// Settings › Workspace and Settings › Platform (they had already drifted:
// text-lg vs text-xl heading). Per RM-003/004 SSO is configured in the
// deployment's environment / Helm values in both modes today; the mode-
// specific tail sentence explains where (if anywhere) an editable surface
// lives, and is passed in via `note`.
interface SSOReadOnlyCardProps {
  // Mode-specific guidance rendered after the shared lead sentence.
  // Workspace tab points at Settings › Platform; Platform tab explains the
  // editor is a future phase.
  note: React.ReactNode;
  // When set, the card is wrapped in a <section id> with scroll-margin so an
  // in-page nav can anchor to it (the Platform tab links to #sso). The
  // Workspace tab renders the bare card and omits this.
  sectionId?: string;
}

export function SSOReadOnlyCard({
  note,
  sectionId,
}: SSOReadOnlyCardProps): React.ReactElement {
  const card = (
    <Card>
      <CardHeader>
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Sign-in
        </CardDescription>
        <div className="flex items-center justify-between gap-2">
          {/* Unified heading scale (text-xl) — resolves the prior text-lg vs
              text-xl drift between the two call sites. */}
          <h2 className="flex items-center gap-2 font-display text-xl font-medium">
            <KeyRound className="size-4 text-[var(--color-fg-muted)]" />
            Single sign-on
          </h2>
          <Badge tone="neutral" className="text-[10px]">
            Read-only
          </Badge>
        </div>
      </CardHeader>
      <CardContent>
        <p className="text-sm text-[var(--color-fg-muted)]">
          {/* Shared lead — identical intent across both modes. */}
          SSO providers (Google, GitHub, Microsoft, generic OIDC, SAML 2.0) are
          configured in the deployment&apos;s environment / Helm values, not
          from the dashboard. {note}
        </p>
      </CardContent>
    </Card>
  );

  // Anchor wrapper only when a sectionId is supplied.
  if (sectionId) {
    return (
      <section id={sectionId} className="scroll-mt-24">
        {card}
      </section>
    );
  }
  return card;
}
