import * as React from "react";
import {
  ShieldCheck,
  ShieldOff,
  ShieldQuestion,
  FileSignature,
  CheckCircle2,
  CircleX,
  PenLine,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import { SignManifestDialog } from "./sign-manifest-dialog";
import {
  useSignature,
  SIGNING_DISABLED,
  type SignatureRecord,
} from "@/lib/api/signature";
import { useServiceAccounts } from "@/lib/api/service-accounts";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// UUID_RE matches a canonical UUID. FUT-009: a signature signed via a service
// account records the SA's shadow user_id (a UUID) as its signer_id. We use
// this to decide whether to resolve the raw signer_id to an SA display name
// (UUID → managed identity) or render it verbatim (free-form historical row).
const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// SignerDisplay resolves a raw signer_id for display. When it is a UUID that
// matches a known service account's shadow_user_id, we return that SA's name;
// otherwise the signer_id is a free-form string and is returned unchanged.
//
// `saByShadowId` is a lookup built once by the panel from useServiceAccounts()
// so every SignatureCard shares a single map instead of re-scanning the list.
function resolveSignerLabel(
  signerId: string,
  saByShadowId: Map<string, string>,
): { label: string; isServiceAccount: boolean } {
  if (UUID_RE.test(signerId)) {
    const name = saByShadowId.get(signerId);
    if (name) {
      return { label: name, isServiceAccount: true };
    }
  }
  // Free-form signer_id, or a UUID we can't resolve (e.g. a deleted SA) — show
  // it verbatim so the row is never blank.
  return { label: signerId, isServiceAccount: false };
}

interface SigningPanelProps {
  org: string;
  repo: string;
  tag: string;
}

// Beacon — SigningPanel (FE-API-003).
//
// Three rendered states:
//  - **Disabled**  — signer service isn't wired on the BFF. We say so
//                    explicitly so the operator knows it's a config thing,
//                    not a tag posture.
//  - **Unsigned**  — no Cosign / Notary signatures recorded for this digest.
//  - **Signed**    — one or more signatures; render each signer + key.
export function SigningPanel({
  org,
  repo,
  tag,
}: SigningPanelProps): React.ReactElement {
  // verifyOn flips when the operator clicks "Verify now". We pass it to
  // useSignature so the query key changes — that triggers a fresh fetch
  // with ?verify=true and stores the verified state in a separate cache
  // entry (the cheap default path stays shared across tabs).
  const [verifyOn, setVerifyOn] = React.useState(false);
  const [signOpen, setSignOpen] = React.useState(false);
  const { data, isLoading, isError, refetch, isFetching } = useSignature(
    org,
    repo,
    tag,
    { verify: verifyOn },
  );

  // FUT-009 — build a shadow_user_id → SA name lookup so signatures signed by
  // a service account render the managed identity's name instead of the raw
  // UUID. include_disabled so a signature by a since-disabled SA still resolves
  // to its name (the row is historical; we only render, never re-sign).
  const { data: serviceAccounts } = useServiceAccounts({ includeDisabled: true });
  const saByShadowId = React.useMemo(() => {
    const m = new Map<string, string>();
    for (const sa of serviceAccounts ?? []) {
      m.set(sa.shadow_user_id, sa.name);
    }
    return m;
  }, [serviceAccounts]);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load signing status"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-4 w-44" />
        </CardHeader>
        <CardContent className="space-y-2">
          <Skeleton className="h-3 w-full" />
          <Skeleton className="h-3 w-3/4" />
        </CardContent>
      </Card>
    );
  }

  if (data === SIGNING_DISABLED) {
    return <DisabledCard />;
  }

  if (!data) {
    return (
      <EmptyState
        icon={<ShieldQuestion className="size-5" />}
        title="No signing data"
        description="The signer service responded but returned nothing for this tag."
      />
    );
  }

  if (!data.signed) {
    return (
      <div className="space-y-4">
        <UnsignedCard digest={data.manifest_digest} />
        <ActionRibbon
          signed={false}
          verifyOn={verifyOn}
          verifying={isFetching && verifyOn}
          onVerify={() => setVerifyOn(true)}
          onSign={() => setSignOpen(true)}
        />
        <SignManifestDialog
          open={signOpen}
          onOpenChange={setSignOpen}
          org={org}
          repo={repo}
          tag={tag}
        />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <SignedCard
        digest={data.manifest_digest}
        signatures={data.signatures}
        verifyOn={verifyOn}
        saByShadowId={saByShadowId}
      />
      <ActionRibbon
        signed
        verifyOn={verifyOn}
        verifying={isFetching && verifyOn}
        onVerify={() => setVerifyOn(true)}
        onSign={() => setSignOpen(true)}
      />
      <SignManifestDialog
        open={signOpen}
        onOpenChange={setSignOpen}
        org={org}
        repo={repo}
        tag={tag}
      />
    </div>
  );
}

// ActionRibbon — Verify-now flips the parent's verifyOn state so the
// signature query refetches with ?verify=true. Sign-with opens
// SignManifestDialog.
interface ActionRibbonProps {
  signed: boolean;
  verifyOn: boolean;
  verifying: boolean;
  onVerify: () => void;
  onSign: () => void;
}

function ActionRibbon({
  signed,
  verifyOn,
  verifying,
  onVerify,
  onSign,
}: ActionRibbonProps): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Actions
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={onVerify}
          disabled={!signed || verifying || verifyOn}
          loading={verifying}
        >
          {!verifying && (verifyOn ? (
            <CheckCircle2 className="size-4 text-[var(--color-success)]" />
          ) : (
            <CheckCircle2 className="size-4" />
          ))}
          {verifying ? "Verifying" : verifyOn ? "Verified" : signed ? "Verify now" : "Verify when signed"}
        </Button>
        <Button variant="outline" size="sm" onClick={onSign}>
          <PenLine className="size-4" />
          {signed ? "Add signature" : "Sign manifest"}
        </Button>
        <span className="text-xs text-[var(--color-fg-subtle)]">
          {signed
            ? "Verify runs signer.VerifyManifest against every recorded signature in parallel (capped at 16)."
            : "Sign with a configured signer to record a Cosign / Notary signature for this digest."}
        </span>
      </CardContent>
    </Card>
  );
}

function DisabledCard(): React.ReactElement {
  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Signing
          </CardDescription>
          <Badge tone="neutral">
            <ShieldQuestion className="size-3" /> Disabled
          </Badge>
        </div>
      </CardHeader>
      <CardContent>
        <p className="text-sm text-[var(--color-fg-muted)]">
          The management API isn't wired to a signer service. Set{" "}
          <code className="font-mono">SIGNER_GRPC_ADDR</code> on the BFF and
          restart to enable signature verification.
        </p>
      </CardContent>
    </Card>
  );
}

function UnsignedCard({ digest }: { digest: string }): React.ReactElement {
  return (
    <Card accentBar="warning">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Signing
          </CardDescription>
          <Badge tone="warning">
            <ShieldOff className="size-3" /> Unsigned
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-[var(--color-fg-muted)]">
          No Cosign or Notary v2 signatures have been recorded for this
          manifest. If your policy requires signed images, sign this digest
          with{" "}
          <code className="font-mono text-[var(--color-fg)]">cosign sign</code>{" "}
          before deploying.
        </p>
        <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
          <div className="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
            Manifest digest
          </div>
          <div className="mt-0.5 flex items-center gap-1.5">
            <code
              className="truncate font-mono text-xs text-[var(--color-fg)]"
              title={digest}
            >
              {digest}
            </code>
            <CopyButton value={digest} iconOnly />
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function SignedCard({
  digest,
  signatures,
  verifyOn,
  saByShadowId,
}: {
  digest: string;
  signatures: SignatureRecord[];
  verifyOn: boolean;
  saByShadowId: Map<string, string>;
}): React.ReactElement {
  // FE-API-025 — when verifyOn flips, every signature carries a `verified`
  // bool. Roll up the counts so the header reads "3 verified, 1 failed"
  // instead of forcing operators to scan every card.
  const verifiedCount = signatures.filter((s) => s.verified === true).length;
  const failedCount = signatures.filter((s) => s.verified === false).length;
  // accentBar mirrors the worst outcome so the header colour reflects risk
  // at a glance: any failure → danger; all verified → success; default →
  // success (signed without verify).
  const accentBar =
    verifyOn && failedCount > 0
      ? ("danger" as const)
      : ("success" as const);
  return (
    <div className="space-y-4">
      <Card accentBar={accentBar}>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Signing
            </CardDescription>
            <div className="flex items-center gap-2">
              {verifyOn ? (
                failedCount === 0 ? (
                  <Badge tone="success">
                    <ShieldCheck className="size-3" /> Verified ({verifiedCount}/{signatures.length})
                  </Badge>
                ) : (
                  <Badge tone="danger">
                    <CircleX className="size-3" /> Verify failed ({failedCount}/{signatures.length})
                  </Badge>
                )
              ) : (
                <Badge tone="success">
                  <ShieldCheck className="size-3" /> Signed
                </Badge>
              )}
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-[var(--color-fg-muted)]">
            {signatures.length === 1
              ? "This manifest carries one signature."
              : `This manifest carries ${signatures.length} signatures.`}{" "}
            {verifyOn
              ? failedCount === 0
                ? "All cryptographic verifications passed against the recorded keys."
                : `${failedCount} of ${signatures.length} signature(s) failed verification — inspect the cards below.`
              : "Hit Verify now to run cryptographic verify against every signer, or verify locally with cosign verify."}
          </p>
          <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
            <div className="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
              Manifest digest
            </div>
            <div className="mt-0.5 flex items-center gap-1.5">
              <code
                className="truncate font-mono text-xs text-[var(--color-fg)]"
                title={digest}
              >
                {digest}
              </code>
              <CopyButton value={digest} iconOnly />
            </div>
          </div>
        </CardContent>
      </Card>

      <div className="space-y-2">
        {signatures.map((s, i) => (
          <SignatureCard
            key={`${s.signature_digest}-${i}`}
            signature={s}
            saByShadowId={saByShadowId}
          />
        ))}
      </div>
    </div>
  );
}

function SignatureCard({
  signature,
  saByShadowId,
}: {
  signature: SignatureRecord;
  saByShadowId: Map<string, string>;
}): React.ReactElement {
  // FE-API-025 — verified is tri-state on the wire:
  //   undefined → caller didn't opt into ?verify=true
  //   true      → signer.VerifyManifest passed
  //   false     → signer.VerifyManifest failed (failure_reason should be set)
  const verifyState = signature.verified;
  // FUT-009 — when the signer_id is an SA shadow user_id (a UUID we can
  // resolve), render the SA display name; free-form strings render verbatim.
  const { label: signerLabel, isServiceAccount } = resolveSignerLabel(
    signature.signer_id,
    saByShadowId,
  );
  return (
    <Card
      accentBar={
        verifyState === false
          ? "danger"
          : verifyState === true
            ? "success"
            : "neutral"
      }
    >
      <CardContent className="space-y-3 py-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <FileSignature className="size-3.5 text-[var(--color-accent)]" />
            <span className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
              Signer
            </span>
            {/* SA-signed rows render the display name (not monospace, since
                it's a human-readable label); free-form signer_ids stay in the
                mono treatment. `title` keeps the raw shadow user_id inspectable
                for SA rows. */}
            <span
              className={cn(
                "text-xs text-[var(--color-fg)]",
                isServiceAccount ? "font-medium" : "font-mono",
              )}
              title={isServiceAccount ? signature.signer_id : undefined}
            >
              {signerLabel}
            </span>
            {isServiceAccount ? (
              <Badge tone="neutral">Service account</Badge>
            ) : null}
            {verifyState === true ? (
              <Badge tone="success">
                <ShieldCheck className="size-3" /> Verified
              </Badge>
            ) : verifyState === false ? (
              <Badge tone="danger">
                <CircleX className="size-3" /> Failed
              </Badge>
            ) : null}
          </div>
          <span
            className="text-xs text-[var(--color-fg-muted)]"
            title={formatAbsoluteDate(signature.signed_at)}
          >
            {formatRelativeDate(signature.signed_at)}
          </span>
        </div>

        <Row label="Key ID" value={signature.key_id} />
        <Row label="Signature digest" value={signature.signature_digest} />

        {verifyState === false && signature.failure_reason ? (
          <div
            className={cn(
              "mt-2 rounded-md border border-[var(--color-danger)]/30",
              "bg-[var(--color-danger)]/5 px-3 py-2",
            )}
          >
            <div className="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-danger)]">
              Failure reason
            </div>
            <div className="mt-0.5 break-words font-mono text-xs text-[var(--color-fg)]">
              {signature.failure_reason}
            </div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function Row({
  label,
  value,
}: {
  label: string;
  value: string;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-[140px_1fr_auto] items-center gap-3">
      <div className="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <code
        className="truncate font-mono text-xs text-[var(--color-fg-muted)]"
        title={value}
      >
        {value || "—"}
      </code>
      {value ? <CopyButton value={value} iconOnly /> : <span />}
    </div>
  );
}
