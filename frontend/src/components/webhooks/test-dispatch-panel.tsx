import * as React from "react";
import { toast } from "sonner";
import { Play, CircleCheck, CircleX } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  useTestWebhook,
  type TestDispatchResult,
} from "@/lib/api/webhooks";
import { cn } from "@/lib/utils";

interface TestDispatchPanelProps {
  endpointId: string;
}

// Beacon — TestDispatchPanel. Synchronous "fire a test event and tell me
// what the endpoint did" affordance. Keeps the last result visible until
// the operator dispatches again.
export function TestDispatchPanel({
  endpointId,
}: TestDispatchPanelProps): React.ReactElement {
  const test = useTestWebhook();
  const [lastResult, setLastResult] = React.useState<TestDispatchResult | null>(
    null,
  );

  async function dispatch(): Promise<void> {
    try {
      const result = await test.mutateAsync(endpointId);
      setLastResult(result);
      if (result.error) {
        toast.error("Endpoint responded with an error.");
      } else if (result.status_code >= 200 && result.status_code < 300) {
        toast.success(`Endpoint returned ${result.status_code}.`);
      } else {
        toast.error(`Endpoint returned ${result.status_code}.`);
      }
    } catch {
      toast.error("Couldn't dispatch test event.");
    }
  }

  const ok =
    lastResult &&
    !lastResult.error &&
    lastResult.status_code >= 200 &&
    lastResult.status_code < 400;
  const accentBar = lastResult ? (ok ? "success" : "danger") : "neutral";

  return (
    <Card accentBar={accentBar}>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Test dispatch
          </CardDescription>
          <Button
            size="sm"
            onClick={() => void dispatch()}
            loading={test.isPending}
            disabled={test.isPending}
          >
            <Play className="size-3" />
            Fire test event
          </Button>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {!lastResult ? (
          <p className="text-sm text-[var(--color-fg-muted)]">
            Sends a synthetic <code className="font-mono">webhook.test</code>{" "}
            payload and reports the response. Not recorded in the delivery log.
          </p>
        ) : (
          <div className="space-y-3">
            <div className="flex items-center gap-3">
              {ok ? (
                <CircleCheck className="size-5 text-[var(--color-success)]" />
              ) : (
                <CircleX className="size-5 text-[var(--color-danger)]" />
              )}
              <div>
                <div className="font-display text-2xl font-medium leading-none tracking-tight">
                  {lastResult.status_code || "ERR"}
                </div>
                <div className="mt-1 text-xs text-[var(--color-fg-muted)]">
                  Round-trip{" "}
                  <span className="font-mono text-[var(--color-fg)]">
                    {lastResult.duration_ms} ms
                  </span>
                </div>
              </div>
            </div>
            {lastResult.error ? (
              <div
                className={cn(
                  "rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5",
                  "p-3 font-mono text-xs text-[var(--color-fg)]",
                )}
              >
                {lastResult.error}
              </div>
            ) : null}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
