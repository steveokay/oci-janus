import * as React from "react";
import {
  ShieldCheck,
  ShieldOff,
  ShieldQuestion,
  FileSignature,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import {
  useSignature,
  SIGNING_DISABLED,
  type SignatureRecord,
} from "@/lib/api/signature";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

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
  const { data, isLoading, isError, refetch } = useSignature(org, repo, tag);

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
    return <UnsignedCard digest={data.manifest_digest} />;
  }

  return <SignedCard digest={data.manifest_digest} signatures={data.signatures} />;
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
}: {
  digest: string;
  signatures: SignatureRecord[];
}): React.ReactElement {
  return (
    <div className="space-y-4">
      <Card accentBar="success">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Signing
            </CardDescription>
            <Badge tone="success">
              <ShieldCheck className="size-3" /> Signed
            </Badge>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-[var(--color-fg-muted)]">
            {signatures.length === 1
              ? "This manifest carries one signature."
              : `This manifest carries ${signatures.length} signatures.`}{" "}
            Verify locally with{" "}
            <code className="font-mono text-[var(--color-fg)]">
              cosign verify
            </code>{" "}
            against the listed keys before trusting at scale.
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
          <SignatureCard key={`${s.signature_digest}-${i}`} signature={s} />
        ))}
      </div>
    </div>
  );
}

function SignatureCard({
  signature,
}: {
  signature: SignatureRecord;
}): React.ReactElement {
  return (
    <Card>
      <CardContent className="space-y-3 py-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <FileSignature className="size-3.5 text-[var(--color-accent)]" />
            <span className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
              Signer
            </span>
            <code className="font-mono text-xs text-[var(--color-fg)]">
              {signature.signer_id}
            </code>
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
