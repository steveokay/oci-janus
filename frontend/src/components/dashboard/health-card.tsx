import * as React from "react";
import { Activity } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { AnimatedNumber } from "@/components/ui/animated-number";

interface HealthCardProps {
  pct?: number;
  loading?: boolean;
}

type HealthState = { label: string; tone: "success" | "warning" | "danger" };

function classify(pct: number): HealthState {
  if (pct >= 99) return { label: "Healthy", tone: "success" };
  if (pct >= 95) return { label: "Degraded", tone: "warning" };
  return { label: "Critical", tone: "danger" };
}

// Beacon — System health. Status badge sits next to the percentage so the
// number always reads with its semantic meaning. The pulse on the dot is
// the only "live" indicator we ship without backing it with real polling —
// fair, because the stat IS being polled (30s in useStats).
export function HealthCard({
  pct,
  loading,
}: HealthCardProps): React.ReactElement {
  const state =
    typeof pct === "number" ? classify(pct) : { label: "—", tone: "success" as const };
  const accentBar = state.tone;
  return (
    <Card accentBar={accentBar}>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            System health
          </CardDescription>
          <Activity className="size-4 text-[var(--color-fg-subtle)]" />
        </div>
      </CardHeader>
      <CardContent className="space-y-3 pt-0 pb-5">
        {loading || pct === undefined ? (
          <Skeleton className="h-10 w-32" />
        ) : (
          <div className="font-display text-4xl font-medium leading-none tracking-tight">
            <AnimatedNumber
              value={pct}
              format={(n) => `${n.toFixed(1)}%`}
            />
          </div>
        )}
        {/* UIR-2: pulse only for the attention-worthy tones. The prior
            `|| !loading` made a healthy (success) badge pulse once loaded,
            diluting the "needs attention" cue. Degraded/critical still pulse. */}
        <Badge tone={state.tone} dot pulse={state.tone !== "success"}>
          {state.label}
        </Badge>
      </CardContent>
    </Card>
  );
}
