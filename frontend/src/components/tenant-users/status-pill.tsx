import * as React from "react";
import { CheckCircle2, Mail, MinusCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { TenantUserStatus } from "@/lib/api/tenant-users";

// FUT-012 Phase C — the status pill on each tenant-users row.
//
// active   → success-tone with a check icon
// invited  → accent-tone with a mail icon (matches the "invite copied"
//            success affordance pattern in the invite dialog)
// disabled → danger-tone with a minus-circle icon
//
// Falls back to a neutral pill for any future / unknown status string
// so a new backend value doesn't make the column visually disappear.
export function StatusPill({ status }: { status: TenantUserStatus }): React.ReactElement {
  switch (status) {
    case "active":
      return (
        <Badge tone="success">
          <CheckCircle2 className="size-3" /> Active
        </Badge>
      );
    case "invited":
      return (
        <Badge tone="accent">
          <Mail className="size-3" /> Invited
        </Badge>
      );
    case "disabled":
      return (
        <Badge tone="danger">
          <MinusCircle className="size-3" /> Disabled
        </Badge>
      );
    default:
      return <Badge tone="neutral">{String(status)}</Badge>;
  }
}
