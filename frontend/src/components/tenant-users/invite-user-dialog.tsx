import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Check, Copy, Mail, UserPlus } from "lucide-react";
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
import { useInviteUser } from "@/lib/api/tenant-users";
import type { InviteUserResult } from "@/lib/api/tenant-users";

interface InviteUserDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// FUT-012 Phase C — InviteUserDialog has two states:
//
//   form    — collect email + display_name + optional initial role/org.
//             Mutation fires on submit.
//   reveal  — display the one-time invite_token from the response. The
//             operator copies the link (or token) to share. Closing the
//             dialog throws the token away forever — the BFF never lets
//             us read it back.
//
// The reveal state is a deliberate moment of attention: large amber
// note that the token won't be shown again, plus a copy-button that
// confirms via toast. Same discipline as the api-key creation flow
// (which produces an identical 32-byte hex token).
export function InviteUserDialog({
  open,
  onOpenChange,
}: InviteUserDialogProps): React.ReactElement {
  const invite = useInviteUser();
  const [email, setEmail] = React.useState("");
  const [displayName, setDisplayName] = React.useState("");
  const [initialOrgRole, setInitialOrgRole] = React.useState("");
  const [initialOrgName, setInitialOrgName] = React.useState("");
  const [result, setResult] = React.useState<InviteUserResult | null>(null);

  // Reset to form state whenever the dialog closes so a re-open starts
  // clean. The token field is wiped here — that's the one place we
  // explicitly drop the only copy of the raw value.
  React.useEffect(() => {
    if (!open) {
      setEmail("");
      setDisplayName("");
      setInitialOrgRole("");
      setInitialOrgName("");
      setResult(null);
    }
  }, [open]);

  // Paired-field validation: BFF rejects half-set initial role/org.
  // Mirror the gate here so the submit button is disabled before the
  // request fires.
  const initialPaired = (initialOrgRole === "") === (initialOrgName === "");
  const canSubmit = email.trim() !== "" && displayName.trim() !== "" && initialPaired;

  async function handleSubmit(): Promise<void> {
    try {
      const out = await invite.mutateAsync({
        email: email.trim(),
        display_name: displayName.trim(),
        ...(initialOrgRole && initialOrgName
          ? { initial_org_role: initialOrgRole, initial_org_name: initialOrgName }
          : {}),
      });
      setResult(out);
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 409
          ? "A user with that email or username already exists."
          : status === 403
            ? "Tenant-admin role required."
            : status === 400
              ? "Backend rejected the invite. Check the email shape + display name."
              : "Couldn't invite. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {result === null ? (
          <InviteForm
            email={email}
            displayName={displayName}
            initialOrgRole={initialOrgRole}
            initialOrgName={initialOrgName}
            setEmail={setEmail}
            setDisplayName={setDisplayName}
            setInitialOrgRole={setInitialOrgRole}
            setInitialOrgName={setInitialOrgName}
            initialPaired={initialPaired}
            canSubmit={canSubmit}
            submitting={invite.isPending}
            onSubmit={handleSubmit}
            onCancel={() => onOpenChange(false)}
          />
        ) : (
          <InviteReveal
            result={result}
            email={email}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function InviteForm({
  email,
  displayName,
  initialOrgRole,
  initialOrgName,
  setEmail,
  setDisplayName,
  setInitialOrgRole,
  setInitialOrgName,
  initialPaired,
  canSubmit,
  submitting,
  onSubmit,
  onCancel,
}: {
  email: string;
  displayName: string;
  initialOrgRole: string;
  initialOrgName: string;
  setEmail: (v: string) => void;
  setDisplayName: (v: string) => void;
  setInitialOrgRole: (v: string) => void;
  setInitialOrgName: (v: string) => void;
  initialPaired: boolean;
  canSubmit: boolean;
  submitting: boolean;
  onSubmit: () => void;
  onCancel: () => void;
}): React.ReactElement {
  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <UserPlus className="size-4" /> Invite user
        </DialogTitle>
        <DialogDescription>
          Creates a pending invite. The recipient redeems the one-time
          link to set their password and activate the account.
        </DialogDescription>
      </DialogHeader>

      <div className="space-y-3">
        <div>
          <Label htmlFor="invite-email">Email</Label>
          <Input
            id="invite-email"
            type="email"
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="alice@example.com"
          />
        </div>
        <div>
          <Label htmlFor="invite-name">Display name</Label>
          <Input
            id="invite-name"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            placeholder="Alice O'Neill"
          />
        </div>
        <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
          <div className="mb-2 text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Optional — grant initial role
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <Label htmlFor="invite-role" className="text-xs">
                Role
              </Label>
              <Input
                id="invite-role"
                value={initialOrgRole}
                onChange={(e) => setInitialOrgRole(e.target.value)}
                placeholder="writer"
              />
            </div>
            <div>
              <Label htmlFor="invite-org" className="text-xs">
                Org
              </Label>
              <Input
                id="invite-org"
                value={initialOrgName}
                onChange={(e) => setInitialOrgName(e.target.value)}
                placeholder="dev"
              />
            </div>
          </div>
          {!initialPaired ? (
            <p className="mt-2 text-xs text-[var(--color-danger)]">
              Set both role and org, or leave both blank.
            </p>
          ) : null}
        </div>
      </div>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button
          type="button"
          onClick={onSubmit}
          loading={submitting}
          disabled={!canSubmit || submitting}
        >
          <Mail className="size-4" />
          {submitting ? "Inviting" : "Send invite"}
        </Button>
      </DialogFooter>
    </>
  );
}

function InviteReveal({
  result,
  email,
  onClose,
}: {
  result: InviteUserResult;
  email: string;
  onClose: () => void;
}): React.ReactElement {
  const [copied, setCopied] = React.useState(false);
  async function copy(value: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
      toast.success("Copied to clipboard");
    } catch {
      toast.error("Couldn't copy — copy the field manually");
    }
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <Check className="size-4 text-[var(--color-success)]" />
          Invite created
        </DialogTitle>
        <DialogDescription>
          The recipient (<span className="font-mono text-xs">{email}</span>)
          needs the token below to redeem. Copy it now — once this dialog
          closes, the token is unrecoverable.
        </DialogDescription>
      </DialogHeader>

      <div className="space-y-3">
        <div className="rounded-md border border-[var(--color-warning)]/40 bg-[var(--color-warning)]/10 p-3">
          <p className="text-xs text-[var(--color-warning)]">
            ⚠ This token is shown once. It expires{" "}
            {new Date(result.invite_expires_at).toLocaleString()}.
          </p>
        </div>
        <div>
          <Label className="text-xs uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Invite token
          </Label>
          <div className="mt-1 flex items-center gap-2">
            <code className="flex-1 truncate rounded border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2 font-mono text-xs">
              {result.invite_token}
            </code>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void copy(result.invite_token)}
            >
              {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
        </div>
      </div>

      <DialogFooter>
        <Button type="button" onClick={onClose}>
          Done
        </Button>
      </DialogFooter>
    </>
  );
}
