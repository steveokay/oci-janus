import * as React from "react";
import { toast } from "sonner";
import { Pencil, Check, X, ShieldCheck, KeyRound } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { useMe, useUpdateMe, type MeResponse } from "@/lib/api/me";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

interface IdentityCardProps {
  onChangePassword: () => void;
}

// Beacon — IdentityCard.
//
// Inline-edit for `display_name` + `email`. Each row toggles into an Input
// when the user clicks the Pencil; commit on the Check button (or Enter),
// abandon on the X (or Escape). Server-side update mutation tickles the
// cache so the rendered values update without a hard refresh.
export function IdentityCard({
  onChangePassword,
}: IdentityCardProps): React.ReactElement {
  const { data, isLoading, isError } = useMe();
  const update = useUpdateMe();

  if (isError) {
    return (
      <Card accentBar="danger">
        <CardContent>
          <p className="py-2 text-sm text-[var(--color-danger)]">
            Couldn't load your profile. Sign out and back in if this persists.
          </p>
        </CardContent>
      </Card>
    );
  }

  async function commit(patch: { display_name?: string | null; email?: string | null }) {
    try {
      await update.mutateAsync(patch);
      toast.success("Profile updated.");
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      toast.error(
        status === 400
          ? "That value isn't accepted. Check the format."
          : "Couldn't save. Try again.",
      );
      throw e;
    }
  }

  return (
    <Card accentBar="accent">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Identity
          </CardDescription>
          <Button variant="outline" size="sm" onClick={onChangePassword}>
            <KeyRound className="size-3.5" />
            Change password
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading || !data ? <Skeleton className="h-40 w-full" /> : <Body data={data} commit={commit} />}
      </CardContent>
    </Card>
  );
}

interface BodyProps {
  data: MeResponse;
  commit: (patch: { display_name?: string | null; email?: string | null }) => Promise<void>;
}

function Body({ data, commit }: BodyProps): React.ReactElement {
  return (
    <div className="space-y-1">
      {/* Hero — avatar + username + tenant id chip */}
      <div className="flex items-center gap-4 pb-4">
        <span
          aria-hidden
          className="grid size-14 shrink-0 place-items-center rounded-lg bg-[var(--color-accent-subtle)] font-display text-2xl font-semibold text-[var(--color-accent)]"
        >
          {(data.display_name ?? data.username)[0]?.toUpperCase() ?? "·"}
        </span>
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="font-display text-2xl font-medium tracking-tight">
              {data.display_name ?? data.username}
            </h2>
            {data.roles.includes("admin") ? (
              <Badge tone="warning">
                <ShieldCheck className="size-3" /> admin
              </Badge>
            ) : null}
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-[var(--color-fg-muted)]">
            <span className="font-mono">{data.username}</span>
            <span>·</span>
            <span className="font-mono">tenant {data.tenant_id.slice(0, 8)}…</span>
          </div>
        </div>
      </div>

      {/* Editable rows */}
      <Divider />
      <EditableRow
        label="Display name"
        value={data.display_name ?? ""}
        placeholder="No display name set"
        onSave={(v) => commit({ display_name: v.trim() === "" ? null : v.trim() })}
      />
      <Divider />
      <EditableRow
        label="Email"
        value={data.email ?? ""}
        placeholder="No email set"
        type="email"
        validate={(v) => {
          const trimmed = v.trim();
          if (trimmed === "") return null; // empty allowed → clears the field
          if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed))
            return "Enter a valid email address.";
          return null;
        }}
        onSave={(v) => commit({ email: v.trim() === "" ? null : v.trim() })}
      />
      <Divider />

      {/* Read-only metadata */}
      <ReadOnlyRow
        label="Last sign-in"
        value={
          data.last_login_at
            ? `${formatRelativeDate(data.last_login_at)} · ${formatAbsoluteDate(data.last_login_at)}`
            : "Never recorded"
        }
      />
      <Divider />
      <ReadOnlyRow
        label="Account created"
        value={`${formatRelativeDate(data.created_at)} · ${formatAbsoluteDate(data.created_at)}`}
      />
      <Divider />
      <ReadOnlyRow
        label="Memberships"
        value={
          data.memberships.length === 0 ? (
            <span className="text-[var(--color-fg-subtle)]">
              No explicit role assignments
            </span>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {data.memberships.map((m, i) => (
                <Badge key={i} tone="accent" className="font-mono">
                  {m.role}@{m.scope_type}:{m.scope_value}
                </Badge>
              ))}
            </div>
          )
        }
      />
    </div>
  );
}

function Divider(): React.ReactElement {
  return <div className="h-px bg-[var(--color-border)]" />;
}

interface EditableRowProps {
  label: string;
  value: string;
  placeholder: string;
  type?: string;
  validate?: (v: string) => string | null;
  onSave: (v: string) => Promise<void>;
}

function EditableRow({
  label,
  value,
  placeholder,
  type,
  validate,
  onSave,
}: EditableRowProps): React.ReactElement {
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState(value);
  const [error, setError] = React.useState<string | null>(null);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    if (!editing) setDraft(value);
  }, [value, editing]);

  async function save() {
    const issue = validate?.(draft) ?? null;
    if (issue) {
      setError(issue);
      return;
    }
    setSaving(true);
    try {
      await onSave(draft);
      setEditing(false);
    } catch {
      // toast handled in commit()
    } finally {
      setSaving(false);
    }
  }

  function cancel() {
    setDraft(value);
    setError(null);
    setEditing(false);
  }

  return (
    <div className="grid grid-cols-[140px_1fr_auto] items-center gap-3 py-3">
      <Label className="!text-[11px]">{label}</Label>
      {editing ? (
        <Input
          value={draft}
          type={type}
          autoFocus
          onChange={(e) => {
            setDraft(e.target.value);
            if (error) setError(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") void save();
            if (e.key === "Escape") cancel();
          }}
          aria-invalid={Boolean(error) || undefined}
          className="h-8"
        />
      ) : (
        <div
          className={cn(
            "min-w-0 truncate text-sm",
            value
              ? "text-[var(--color-fg)]"
              : "text-[var(--color-fg-subtle)]",
          )}
        >
          {value || placeholder}
        </div>
      )}
      {editing ? (
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => void save()}
            loading={saving}
            disabled={saving}
            aria-label="Save"
            className="text-[var(--color-success)]"
          >
            <Check className="size-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={cancel}
            disabled={saving}
            aria-label="Cancel"
            className="text-[var(--color-fg-muted)]"
          >
            <X className="size-4" />
          </Button>
        </div>
      ) : (
        <Button
          variant="ghost"
          size="icon"
          onClick={() => setEditing(true)}
          aria-label={`Edit ${label.toLowerCase()}`}
          className="text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        >
          <Pencil className="size-3.5" />
        </Button>
      )}
      {error ? (
        <p className="col-start-2 -mt-2 text-xs text-[var(--color-danger)]">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function ReadOnlyRow({
  label,
  value,
}: {
  label: string;
  value: React.ReactNode;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-[140px_1fr] items-center gap-3 py-3">
      <Label className="!text-[11px]">{label}</Label>
      <div className="min-w-0 text-sm text-[var(--color-fg)]">{value}</div>
    </div>
  );
}
