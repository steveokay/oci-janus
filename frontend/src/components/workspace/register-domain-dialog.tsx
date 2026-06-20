import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { Globe, ShieldCheck } from "lucide-react";
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
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import {
  useRegisterDomain,
  DOMAIN_REGEX,
  type RegisterDomainResponse,
} from "@/lib/api/domains";

const schema = z.object({
  domain: z
    .string()
    .min(1, "Domain is required.")
    .max(253, "Keep it under 253 characters.")
    .regex(
      DOMAIN_REGEX,
      "Looks invalid — use a fully-qualified domain like registry.acme.com",
    ),
});

type FormValues = z.infer<typeof schema>;

interface RegisterDomainDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// RegisterDomainDialog — FE-API-027.
//
// Two-phase: enter a domain, get back a TXT challenge to paste into DNS.
// The dialog stays open after a successful register so the operator can
// copy the TXT record without losing the modal — they close it manually
// once they've added the record.
export function RegisterDomainDialog({
  open,
  onOpenChange,
}: RegisterDomainDialogProps): React.ReactElement {
  const register = useRegisterDomain();
  const [challenge, setChallenge] = React.useState<RegisterDomainResponse | null>(null);
  const {
    register: registerField,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { domain: "" },
  });

  React.useEffect(() => {
    if (!open) {
      reset();
      setChallenge(null);
    }
  }, [open, reset]);

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      const res = await register.mutateAsync(values.domain.trim().toLowerCase());
      setChallenge(res);
      toast.success("Domain registered — add the TXT record to verify.");
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const msg =
        status === 409
          ? "Domain is already registered (on this tenant or another)."
          : status === 403
            ? "Tenant admin role required to register domains."
            : status === 400
              ? "Backend rejected the domain — check the format."
              : "Couldn't register. Try again, or check the BFF logs.";
      toast.error(msg);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[560px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Globe className="size-4 text-[var(--color-accent)]" />
            Register custom domain
          </DialogTitle>
          <DialogDescription>
            Point your own hostname at this control plane. After registering,
            add the TXT record we hand back to your DNS provider; verification
            polls every few minutes until it succeeds.
          </DialogDescription>
        </DialogHeader>

        {!challenge ? (
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-6" noValidate>
            <div>
              <Label htmlFor="domain" className="mb-2 inline-block">
                Domain
              </Label>
              <Input
                id="domain"
                autoFocus
                autoComplete="off"
                spellCheck={false}
                placeholder="registry.acme.com"
                className="font-mono"
                aria-invalid={Boolean(errors.domain) || undefined}
                {...registerField("domain")}
              />
              {errors.domain ? (
                <p className="mt-2 text-xs text-[var(--color-danger)]">
                  {errors.domain.message}
                </p>
              ) : (
                <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                  Fully-qualified domain name. Must be unique across the
                  control plane; verification requires a TXT record at{" "}
                  <code className="font-mono text-[var(--color-fg-muted)]">
                    _registry-verify
                  </code>{" "}
                  on your domain.
                </p>
              )}
            </div>

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={isSubmitting}
              >
                Cancel
              </Button>
              <Button type="submit" loading={isSubmitting} disabled={isSubmitting}>
                {isSubmitting ? "Registering" : "Register"}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <div className="space-y-5">
            <div className="rounded-md border border-[var(--color-success)]/30 bg-[var(--color-success)]/5 px-3 py-2 text-xs text-[var(--color-fg)]">
              <div className="flex items-center gap-1.5 font-medium text-[var(--color-success)]">
                <ShieldCheck className="size-3.5" />
                Domain registered — verification pending
              </div>
            </div>

            <ChallengeRow label="Domain" value={challenge.domain} />
            <ChallengeRow
              label="TXT record name"
              value={challenge.txt_record_name}
            />
            <ChallengeRow
              label="TXT record value"
              value={challenge.verification_token}
              hint="Paste this exact string as the TXT record's value."
            />

            <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2 text-xs leading-relaxed text-[var(--color-fg-muted)]">
              {challenge.instructions}
            </div>

            <DialogFooter>
              <Button onClick={() => onOpenChange(false)}>Done</Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ChallengeRow({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}): React.ReactElement {
  return (
    <div>
      <Label className="mb-2 inline-block">{label}</Label>
      <div className="flex items-center gap-2 rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-3 py-2">
        <code className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--color-fg)]">
          {value}
        </code>
        <CopyButton value={value} iconOnly />
      </div>
      {hint ? (
        <p className="mt-2 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
          {hint}
        </p>
      ) : null}
    </div>
  );
}
