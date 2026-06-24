import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  FileSignature,
  PenLine,
  ShieldCheck,
  ShieldOff,
  ShieldQuestion,
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  useSignaturesByDigest,
  useSignByDigest,
  SIGNING_DISABLED,
  type SignatureRecord,
} from "@/lib/api/proxy-cache";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

// FUT-018 — Signing tab on the proxy-cache detail page.
//
// Three rendered states, matching the per-tag SigningPanel vocabulary:
//   • Disabled  — signer service unwired (404 → SIGNING_DISABLED).
//   • Unsigned  — backend returned `signed:false, signatures:[]`. The
//                 cached manifest hasn't been signed by this tenant.
//   • Signed    — at least one signature; list with signer/key/digest.
//
// Why not reuse SigningPanel directly? It's keyed on (org, repo, tag)
// and pulls FE-API-025 verify-on-demand inline. Cached manifests don't
// have a tag-scoped verify route — the BFF only exposes the GET/POST
// pair for digests. We render the same SignedCard/UnsignedCard look
// minus the verify-now affordance (a follow-up could add a
// `?verify=true` digest variant if operators ask for it).

interface SigningTabProps {
  digest: string;
}

export function SigningTab({ digest }: SigningTabProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useSignaturesByDigest(digest);
  const [signOpen, setSignOpen] = React.useState(false);

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

  // SIGNING_DISABLED is the sentinel returned when the BFF route 404s
  // because SIGNER_GRPC_ADDR is unset. Same sentinel useSignature uses
  // for the per-tag surface — keeps both code paths visually identical.
  if (data === SIGNING_DISABLED) {
    return <DisabledCard />;
  }

  if (!data) {
    return (
      <EmptyState
        icon={<ShieldQuestion className="size-5" />}
        title="No signing data"
        description="The signer service responded but returned nothing for this digest."
      />
    );
  }

  if (!data.signed) {
    return (
      <>
        <UnsignedCard
          digest={data.manifest_digest}
          onSign={() => setSignOpen(true)}
        />
        <SignByDigestDialog
          open={signOpen}
          onOpenChange={setSignOpen}
          digest={digest}
        />
      </>
    );
  }

  return (
    <>
      <SignedCard
        digest={data.manifest_digest}
        signatures={data.signatures}
        onSignAgain={() => setSignOpen(true)}
      />
      <SignByDigestDialog
        open={signOpen}
        onOpenChange={setSignOpen}
        digest={digest}
      />
    </>
  );
}

// ─── State cards ────────────────────────────────────────────────────

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
          restart to enable signing for cached manifests.
        </p>
      </CardContent>
    </Card>
  );
}

function UnsignedCard({
  digest,
  onSign,
}: {
  digest: string;
  onSign: () => void;
}): React.ReactElement {
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
          No Cosign or Notary v2 signatures have been recorded for this cached
          manifest. Sign with your workspace's default key to record an
          attestation, or use{" "}
          <code className="font-mono text-[var(--color-fg)]">cosign sign</code>{" "}
          out-of-band.
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
        <Button onClick={onSign} data-testid="sign-default-button">
          <PenLine className="size-4" />
          Sign with default key
        </Button>
      </CardContent>
    </Card>
  );
}

function SignedCard({
  digest,
  signatures,
  onSignAgain,
}: {
  digest: string;
  signatures: SignatureRecord[];
  onSignAgain: () => void;
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
            Each row records the signer + key id; verify locally with{" "}
            <code className="font-mono">cosign verify</code>.
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
          <div className="flex justify-end">
            <Button
              variant="outline"
              size="sm"
              onClick={onSignAgain}
              data-testid="sign-again-button"
            >
              <PenLine className="size-4" />
              Sign again
            </Button>
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
    <Card accentBar="neutral">
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

// ─── Sign dialog ────────────────────────────────────────────────────
//
// The dialog is intentionally lighter-weight than ConfirmDestructiveDialog
// (medium = type-the-name) — signing is not destructive, just an
// append-only attestation. We render a single optional signer_id input
// (defaults to empty → workspace default key) with a low-friction
// confirm. Mirrors the FUT-018 task brief's "just a low confirm is
// fine" guidance.

interface SignByDigestDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  digest: string;
}

// Same SIGNER_ID regex as SignManifestDialog — ASCII printable, 1-256
// chars. Empty is allowed (defaults to workspace key on the BFF).
const SIGNER_ID_REGEX = /^[\x20-\x7e]{0,256}$/;

function SignByDigestDialog({
  open,
  onOpenChange,
  digest,
}: SignByDigestDialogProps): React.ReactElement {
  const [signerID, setSignerID] = React.useState("");
  const sign = useSignByDigest();

  // Reset the input whenever the dialog opens so a leftover value from
  // a previously cancelled flow doesn't accidentally get submitted.
  React.useEffect(() => {
    if (open) setSignerID("");
  }, [open]);

  const valid = SIGNER_ID_REGEX.test(signerID);

  const onSubmit = async () => {
    if (!valid) return;
    try {
      const trimmed = signerID.trim();
      const resp = await sign.mutateAsync({
        digest,
        signer_id: trimmed.length > 0 ? trimmed : undefined,
      });
      toast.success(
        `Signed ${digest.slice(0, 19)}… as ${resp.signer_id || "workspace key"}.`,
      );
      onOpenChange(false);
    } catch (e) {
      toast.error(signErrorMessage(e));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <PenLine className="size-4 text-[var(--color-accent)]" />
            Sign cached manifest
          </DialogTitle>
          <DialogDescription>
            Records a Cosign / Notary v2 signature against this digest. Key
            material stays in the signer service's Vault backend — this
            dialog never sees a private key. Leave the signer ID empty to
            use your workspace's default.
          </DialogDescription>
        </DialogHeader>

        <div className="mb-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
          <div className="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
            Manifest digest
          </div>
          <code className="mt-0.5 block truncate font-mono text-xs text-[var(--color-fg)]">
            {digest}
          </code>
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            void onSubmit();
          }}
          className="space-y-4"
          noValidate
        >
          <div>
            <Label htmlFor="sign-by-digest-signer-id" className="mb-2 inline-block">
              Signer ID (optional)
            </Label>
            <Input
              id="sign-by-digest-signer-id"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              value={signerID}
              onChange={(e) => setSignerID(e.target.value)}
              placeholder="workspace default"
              aria-invalid={!valid || undefined}
              data-testid="sign-dialog-signer-id"
            />
            <p className="mt-2 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
              Leave empty to use the workspace default key. Override with a
              fully-qualified KMS ARN or the dev key id (e.g.{" "}
              <code className="font-mono text-[var(--color-fg-muted)]">
                registry-signer
              </code>
              ) when needed.
            </p>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={sign.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              loading={sign.isPending}
              disabled={sign.isPending || !valid}
              data-testid="sign-dialog-confirm"
            >
              {sign.isPending ? "Signing" : "Sign"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// signErrorMessage — maps the BFF's structured 4xx envelopes onto
// operator-friendly toast copy. 403 + 404 are the common ones; other
// statuses fall back to whatever `error` the BFF returned.
function signErrorMessage(err: unknown): string {
  if (err instanceof AxiosError) {
    const status = err.response?.status;
    if (status === 403) return "Writer role required to sign cached manifests.";
    if (status === 404)
      return "Signer service is not wired on this BFF (SIGNER_GRPC_ADDR).";
    if (status === 400) {
      const detail = (err.response?.data as { error?: string } | undefined)?.error;
      return detail || "Signer rejected the request — check the signer ID.";
    }
    const detail = (err.response?.data as { error?: string } | undefined)?.error;
    if (detail) return detail;
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return "Couldn't sign manifest.";
}

export { SignByDigestDialog };
