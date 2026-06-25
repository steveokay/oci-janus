import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { ShieldCheck } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useProxyCacheScanPolicies,
  useProxyCacheSignPolicies,
  useUpdateProxyCacheScanPolicy,
  useUpdateProxyCacheSignPolicy,
  type ProxyCacheScanPolicy,
  type ProxyCacheSeverity,
  type ProxyCacheSignPolicy,
} from "@/lib/api/proxy-cache";

// FUT-017 — per-upstream scan + sign policy editor.
//
// Renders one row per unique upstream observed in the cached-manifest
// list. The upstream universe is the union of:
//   • upstreams discovered from the cache rows the operator can see
//     (driver: any image the proxy actually fetched), and
//   • upstreams already carrying a policy row server-side (in case the
//     operator set a policy before any pull landed).
//
// Visibility gates:
//   • Both list hooks return `null` (scanner + signer unwired on BFF)
//     → the card hides entirely. Parent renders nothing.
//   • Only scan list is null → scan controls render disabled with a
//     "scanner not wired" hint; sign controls stay live.
//   • Only sign list is null → mirror of the above.
//
// Save model: each row owns its own debounced effect. Flipping a switch
// or picking a severity arms a 2s timer; on fire we PUT and toast the
// result. A "Save now" button per row collapses the wait when the
// operator knows they're done.
//
// Note: the form's "dirty" tracking compares the current local state
// against the last server snapshot, so a no-op edit (flip a switch
// then flip it back inside the 2s window) won't fire a PUT.

export interface UpstreamPoliciesCardProps {
  // Upstream names discovered from the cached-manifest list. We dedupe
  // + sort here; the page passes the raw list to avoid an upstreams API
  // call that doesn't exist yet.
  upstreamNames: string[];
}

export function UpstreamPoliciesCard({
  upstreamNames,
}: UpstreamPoliciesCardProps): React.ReactElement | null {
  const scanList = useProxyCacheScanPolicies();
  const signList = useProxyCacheSignPolicies();

  // Both backends unwired → render nothing. Mirrors how useCacheStats
  // gates the whole route — a feature-flag-style hide is cleaner than
  // an inline "no policies" empty state for a card the operator never
  // asked for.
  const scanUnavailable = scanList.data === null;
  const signUnavailable = signList.data === null;
  const bothLoaded = !scanList.isLoading && !signList.isLoading;
  if (bothLoaded && scanUnavailable && signUnavailable) {
    return null;
  }

  // The union of upstreams: cache-row discovery ∪ server-side policy
  // rows. This keeps the editor visible for upstreams the operator
  // already configured even if no image has been pulled through yet.
  const seen = new Set<string>(upstreamNames.filter((n) => n.length > 0));
  for (const p of scanList.data ?? []) {
    if (p.upstream_name) seen.add(p.upstream_name);
  }
  for (const p of signList.data ?? []) {
    if (p.upstream_name) seen.add(p.upstream_name);
  }
  const rows = Array.from(seen).sort();

  const scansByName = new Map<string, ProxyCacheScanPolicy>(
    (scanList.data ?? []).map((p) => [p.upstream_name, p]),
  );
  const signsByName = new Map<string, ProxyCacheSignPolicy>(
    (signList.data ?? []).map((p) => [p.upstream_name, p]),
  );

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <ShieldCheck
            aria-hidden
            className="size-4 text-[var(--color-accent)]"
          />
          <h3 className="text-base font-semibold">Cache policies</h3>
        </div>
        <CardDescription className="mt-1">
          Auto-scan and auto-sign every image the cache fetches from each
          upstream. Policies apply on the next cache write; existing rows
          can be re-scanned from the detail page.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {scanList.isLoading || signList.isLoading ? (
          <Skeleton className="h-20 w-full" data-testid="policies-skeleton" />
        ) : rows.length === 0 ? (
          // No upstreams discovered + no existing policies. The cache is
          // empty for this tenant; surface a hint rather than an empty
          // table chrome. Operators land here right after wiring proxy
          // for the first time before pulling anything.
          <p className="text-sm text-[var(--color-fg-muted)]">
            No upstreams yet. Pull an image through the proxy to register
            an upstream, then set policies here.
          </p>
        ) : (
          rows.map((name) => (
            <UpstreamRow
              key={name}
              upstreamName={name}
              scan={scansByName.get(name)}
              sign={signsByName.get(name)}
              scanUnavailable={scanUnavailable}
              signUnavailable={signUnavailable}
            />
          ))
        )}
      </CardContent>
    </Card>
  );
}

// ─── Single-row editor ──────────────────────────────────────────────

interface UpstreamRowProps {
  upstreamName: string;
  scan: ProxyCacheScanPolicy | undefined;
  sign: ProxyCacheSignPolicy | undefined;
  scanUnavailable: boolean;
  signUnavailable: boolean;
}

// AUTO_SAVE_MS — how long after the last local edit before we fire the
// PUT. 2s feels snappy enough that flipping a switch + walking away is
// understood as "save", but loose enough that toggling twice in a row
// (the canonical "oops" pattern) collapses into one network call.
const AUTO_SAVE_MS = 2_000;

function UpstreamRow({
  upstreamName,
  scan,
  sign,
  scanUnavailable,
  signUnavailable,
}: UpstreamRowProps): React.ReactElement {
  // Local copies of the policy state. Initialised from server data and
  // re-synced whenever the server pushes a fresh snapshot (post-PUT
  // refetch). We compare against the server snapshot to compute the
  // dirty flag; a same-as-server edit doesn't fire a PUT.
  const serverScan: ProxyCacheScanPolicy = scan ?? {
    upstream_name: upstreamName,
    auto_scan: false,
    severity_threshold: "",
  };
  const serverSign: ProxyCacheSignPolicy = sign ?? {
    upstream_name: upstreamName,
    auto_sign: false,
    key_id: "",
  };

  const [autoScan, setAutoScan] = React.useState(serverScan.auto_scan);
  const [severity, setSeverity] = React.useState<ProxyCacheSeverity>(
    serverScan.severity_threshold,
  );
  const [autoSign, setAutoSign] = React.useState(serverSign.auto_sign);
  const [keyId, setKeyId] = React.useState(serverSign.key_id ?? "");

  // Re-sync local state when the upstream server snapshot changes. The
  // dependency keys are the server field values themselves so a refetch
  // that returns identical values doesn't blow away an in-flight edit.
  React.useEffect(() => {
    setAutoScan(serverScan.auto_scan);
    setSeverity(serverScan.severity_threshold);
  }, [serverScan.auto_scan, serverScan.severity_threshold]);
  React.useEffect(() => {
    setAutoSign(serverSign.auto_sign);
    setKeyId(serverSign.key_id ?? "");
  }, [serverSign.auto_sign, serverSign.key_id]);

  const scanDirty =
    !scanUnavailable &&
    (autoScan !== serverScan.auto_scan ||
      severity !== serverScan.severity_threshold);
  const signDirty =
    !signUnavailable &&
    (autoSign !== serverSign.auto_sign ||
      (keyId || "") !== (serverSign.key_id || ""));

  // BFF rejects auto_sign=true with empty key_id as 400. Disable Save
  // until the combo is valid so the operator gets the constraint
  // surfaced in the UI rather than as a server-side error toast.
  const signInvalid = autoSign && keyId.trim().length === 0;

  const dirty = scanDirty || signDirty;
  const canSave = dirty && !signInvalid;

  const updateScan = useUpdateProxyCacheScanPolicy(upstreamName);
  const updateSign = useUpdateProxyCacheSignPolicy(upstreamName);

  // Wrap save in a stable callback so the debounce effect can fire it
  // from a setTimeout without re-creating a new timer per render.
  const saveRef = React.useRef<() => Promise<void>>(() => Promise.resolve());
  saveRef.current = async () => {
    if (!canSave) return;
    const tasks: Promise<unknown>[] = [];
    if (scanDirty) {
      tasks.push(
        updateScan.mutateAsync({
          auto_scan: autoScan,
          severity_threshold: severity,
        }),
      );
    }
    if (signDirty) {
      tasks.push(
        updateSign.mutateAsync({
          auto_sign: autoSign,
          key_id: keyId,
        }),
      );
    }
    try {
      await Promise.all(tasks);
      toast.success(`Saved policies for ${upstreamName}`);
    } catch (e) {
      toast.error(policyErrorMessage(e));
    }
  };

  // Debounced auto-save. Re-armed whenever the dirty-ness or candidate
  // values change. We don't fire while the mutation is in-flight to
  // avoid stacking PUTs; the operator can still hit "Save now" but
  // even that goes through `canSave` which is gated on dirty.
  React.useEffect(() => {
    if (!canSave) return;
    if (updateScan.isPending || updateSign.isPending) return;
    const handle = setTimeout(() => {
      void saveRef.current();
    }, AUTO_SAVE_MS);
    return () => clearTimeout(handle);
  }, [
    canSave,
    autoScan,
    severity,
    autoSign,
    keyId,
    updateScan.isPending,
    updateSign.isPending,
  ]);

  const saving = updateScan.isPending || updateSign.isPending;

  return (
    <div
      data-testid={`upstream-row-${upstreamName}`}
      className="flex flex-col gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-3 lg:flex-row lg:items-center lg:gap-6"
    >
      <div className="lg:w-40">
        <Badge tone="neutral" className="font-mono">
          {upstreamName}
        </Badge>
      </div>

      {/* Scan controls — full disabled state when scanner is unwired. */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2">
          <Switch
            id={`auto-scan-${upstreamName}`}
            checked={autoScan}
            onCheckedChange={setAutoScan}
            disabled={scanUnavailable || saving}
            aria-label={`Auto-scan for ${upstreamName}`}
          />
          <label
            htmlFor={`auto-scan-${upstreamName}`}
            className="text-sm text-[var(--color-fg)]"
          >
            Auto-scan
          </label>
        </div>
        <Select
          value={severity || "none"}
          onValueChange={(v) =>
            setSeverity(v === "none" ? "none" : (v as ProxyCacheSeverity))
          }
          disabled={scanUnavailable || !autoScan || saving}
        >
          <SelectTrigger
            aria-label={`Severity threshold for ${upstreamName}`}
            className="min-w-[8rem]"
          >
            <SelectValue placeholder="Severity" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">none</SelectItem>
            <SelectItem value="low">low</SelectItem>
            <SelectItem value="medium">medium</SelectItem>
            <SelectItem value="high">high</SelectItem>
            <SelectItem value="critical">critical</SelectItem>
          </SelectContent>
        </Select>
        {scanUnavailable ? (
          <span className="text-xs text-[var(--color-fg-subtle)]">
            scanner not wired — auto-scan unavailable
          </span>
        ) : null}
      </div>

      {/* Sign controls — same disabled posture when signer is unwired. */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2">
          <Switch
            id={`auto-sign-${upstreamName}`}
            checked={autoSign}
            onCheckedChange={setAutoSign}
            disabled={signUnavailable || saving}
            aria-label={`Auto-sign for ${upstreamName}`}
          />
          <label
            htmlFor={`auto-sign-${upstreamName}`}
            className="text-sm text-[var(--color-fg)]"
          >
            Auto-sign
          </label>
        </div>
        <Input
          // Key picker is a text input for now (v2 = dropdown of signer
          // keys). The Select primitive only handles a closed enum, so a
          // free-form id stays in an Input until the signer exposes a
          // ListKeys RPC the BFF can wrap.
          type="text"
          value={keyId}
          onChange={(e) => setKeyId(e.target.value)}
          placeholder="key_id"
          disabled={signUnavailable || !autoSign || saving}
          aria-label={`Signing key for ${upstreamName}`}
          aria-invalid={signInvalid || undefined}
          className="h-8 min-w-[14rem] flex-1"
        />
        {signUnavailable ? (
          <span className="text-xs text-[var(--color-fg-subtle)]">
            signer not wired — auto-sign unavailable
          </span>
        ) : null}
      </div>

      {/* Per-row Save button — collapses the 2s debounce when the
          operator is ready immediately. Disabled when no changes are
          pending or the sign combo is invalid (auto_sign + empty key). */}
      <div className="lg:ml-auto">
        <Button
          variant="outline"
          size="sm"
          disabled={!canSave || saving}
          onClick={() => void saveRef.current()}
          data-testid={`save-${upstreamName}`}
        >
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}

// policyErrorMessage extracts the BFF's structured error field if
// present, otherwise falls back to the AxiosError / Error message.
// The BFF returns `{ "error": "..." }` for 400 / 403 / 404.
function policyErrorMessage(err: unknown): string {
  if (err instanceof AxiosError) {
    const detail = (err.response?.data as { error?: string } | undefined)
      ?.error;
    if (detail) return detail;
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return "Unexpected error saving policy";
}
