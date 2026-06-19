import * as React from "react";
import { Crown, Shield, Pencil, Eye } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { Role } from "@/lib/api/members";

// Beacon — RoleBadge. Each role gets its own tone + icon so a glance at a
// column of badges immediately reveals who has what — without reading text.
// Tones are chosen so owner reads most-emphatic (warning/amber), descending
// through admin (accent/teal), writer (success/emerald), reader (neutral).

interface RoleBadgeProps {
  role: Role;
  className?: string;
}

const ICONS: Record<Role, React.ComponentType<{ className?: string }>> = {
  owner: Crown,
  admin: Shield,
  writer: Pencil,
  reader: Eye,
};

const TONES: Record<Role, React.ComponentProps<typeof Badge>["tone"]> = {
  owner: "warning",
  admin: "accent",
  writer: "success",
  reader: "neutral",
};

export function RoleBadge({
  role,
  className,
}: RoleBadgeProps): React.ReactElement {
  const Icon = ICONS[role];
  return (
    <Badge tone={TONES[role]} className={className}>
      <Icon className="size-3" aria-hidden />
      <span className="capitalize">{role}</span>
    </Badge>
  );
}
