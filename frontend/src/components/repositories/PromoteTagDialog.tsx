import * as React from "react";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { toast } from "sonner";
import { Ship } from "lucide-react";
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
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { usePromoteTag } from "@/lib/api/promotions";
import { useMe } from "@/lib/api/me";
import { useRepositories } from "@/lib/api/repositories";

// PromoteTagDialog — FUT-020 + REM-030.
//
// Posts to POST /api/v1/repositories/{srcOrg}/{srcRepo}/tags/{srcTag}/promote
// with `{dst_org, dst_repo, dst_tag, note?, create_if_missing?}`. The backend
// requires writer role on BOTH source and destination — we don't pre-validate
// on the FE, we just surface the 403 as a clear toast so the operator
// understands which side they lack access to.
//
// REM-030 changes vs the original FUT-020 dialog:
//   1. Destination org is a dropdown populated from useMe().memberships —
//      the caller's writer-tier org scopes. Falls back to a free-text input
//      when the caller is a global admin (no per-org grants) or when the
//      me query is still loading, so the dialog remains usable in both
//      cases.
//   2. New "Create destination repository if it doesn't exist" switch
//      forwards create_if_missing to the BFF. Default off — matches the
//      original 404-on-missing behaviour so operators opt in.
//
// Server-side validation matches the CLAUDE.md §7 regex vocabulary:
//   org  : ^[a-z0-9-]{2,64}$
//   repo : ^[a-z0-9]+([._-][a-z0-9]+)*$
//   tag  : ^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$
// We mirror those regexes here so the operator sees the failure INLINE
// (as a form error) rather than after a round-trip to the BFF.

const ORG_REGEX = /^[a-z0-9-]{2,64}$/;
const REPO_REGEX = /^[a-z0-9]+([._-][a-z0-9]+)*$/;
const TAG_REGEX = /^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$/;

const schema = z.object({
  dst_org: z
    .string()
    .min(1, "Destination org is required.")
    .regex(ORG_REGEX, "Lowercase alphanumeric + hyphen, 2-64 chars."),
  dst_repo: z
    .string()
    .min(1, "Destination repository is required.")
    .regex(REPO_REGEX, "Lowercase alphanumeric segments joined by . _ -."),
  dst_tag: z
    .string()
    .min(1, "Destination tag is required.")
    .regex(TAG_REGEX, "Alphanumeric + . _ -, max 128 chars."),
  note: z
    .string()
    .max(256, "Keep the note under 256 characters.")
    .optional(),
  create_if_missing: z.boolean().default(false),
  re_sign_on_promote: z.boolean().default(false),
});

type FormValues = z.infer<typeof schema>;

// Roles that grant write access on an org scope. Anything below `writer`
// (reader) cannot be a promotion destination, so we filter those out. The
// order matches the RBAC hierarchy elsewhere in the FE. Used as a fallback
// source when the repositories list is empty (fresh tenant with only the
// source org populated).
const WRITER_ROLES = new Set(["owner", "admin", "writer"]);

interface PromoteTagDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // Source coordinates — pre-filled from the tag detail page's URL. Not
  // editable in the dialog; the dialog defines the DESTINATION.
  srcOrg: string;
  srcRepo: string;
  srcTag: string;
}

// PromoteTagDialog opens from the "Promote" button on the tag header. The
// destination form defaults to the same org/repo as the source with the
// source tag name — the common "cut a release tag" flow — so the operator
// only has to change the destination tag when promoting inside the same
// repository.
export function PromoteTagDialog({
  open,
  onOpenChange,
  srcOrg,
  srcRepo,
  srcTag,
}: PromoteTagDialogProps): React.ReactElement {
  const promote = usePromoteTag(srcOrg, srcRepo, srcTag);
  const me = useMe();
  // Repositories the caller has visibility on. This is the widest RBAC-
  // aware set the FE already has — every repo listed here has passed the
  // BFF's reader-role gate on the caller. Deriving orgs from this covers
  // (a) global admins (they see every org), (b) users with repo-scoped
  // grants that don't include a matching org-level membership row, and
  // (c) users with proper org memberships. All three previously fell
  // into the text-input fallback because my earlier filter only walked
  // org-scoped writer-tier memberships.
  const repos = useRepositories({ perPage: 100 });

  const orgOptions = React.useMemo<string[]>(() => {
    const set = new Set<string>();
    // Preferred source: distinct orgs across every repository the caller
    // can already see. This is the same list the sidebar / workspace
    // switcher renders for "orgs I can browse", so it matches operator
    // expectations.
    for (const page of repos.data?.pages ?? []) {
      for (const r of page.repositories) {
        if (r.org) set.add(r.org);
      }
    }
    // Fallback source: writer-tier org memberships from useMe. Useful for
    // a brand-new tenant where the caller has been granted access to an
    // empty org (no repos yet) — such an org would be missing from
    // useRepositories but should still be a valid promotion destination.
    for (const m of me.data?.memberships ?? []) {
      if (m.scope_type === "org" && WRITER_ROLES.has(m.role)) {
        set.add(m.scope_value);
      }
    }
    // Always include the source org so the "promote within the same
    // repo" flow works even before either query resolves.
    set.add(srcOrg);
    return Array.from(set).sort();
  }, [repos.data, me.data, srcOrg]);

  // Fall back to text input only while the repos query hasn't resolved
  // AND me is still loading. A resolved query that legitimately produces
  // only one option (single-org tenant) still renders the dropdown — the
  // operator sees one enabled item + the srcOrg check. Global admins are
  // no longer forced into text input; the repositories query already
  // gives them the full org list.
  const useTextInput =
    !me.data && !repos.data && orgOptions.length <= 1;

  const {
    register,
    handleSubmit,
    reset,
    control,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      dst_org: srcOrg,
      dst_repo: srcRepo,
      dst_tag: srcTag,
      note: "",
      create_if_missing: false,
      re_sign_on_promote: false,
    },
  });

  // Reset the form each time the dialog opens so re-opening after a
  // previous edit doesn't preserve stale values.
  React.useEffect(() => {
    if (!open) {
      reset({
        dst_org: srcOrg,
        dst_repo: srcRepo,
        dst_tag: srcTag,
        note: "",
        create_if_missing: false,
      });
    }
  }, [open, reset, srcOrg, srcRepo, srcTag]);

  async function onSubmit(values: FormValues): Promise<void> {
    try {
      const prom = await promote.mutateAsync({
        dst_org: values.dst_org.trim(),
        dst_repo: values.dst_repo.trim(),
        dst_tag: values.dst_tag.trim(),
        note: values.note?.trim() || undefined,
        // Only forward when true — keeps the wire payload minimal on the
        // common (default off) path.
        create_if_missing: values.create_if_missing || undefined,
        re_sign_on_promote: values.re_sign_on_promote || undefined,
      });
      // Base success line for the (always durable) promotion, then append the
      // re-sign outcome so the operator knows whether the destination manifest
      // is signed. The promotion succeeding while signing failed is a real
      // state (re_signed=false + sign_error) — we still use the success channel
      // because the promotion itself worked, but the copy flags the shortfall
      // so the operator retries signing from the tag's Sign action.
      let msg = `Promoted ${srcOrg}/${srcRepo}:${srcTag} → ${prom.dst_org}/${prom.dst_repo}:${prom.dst_tag}.`;
      if (prom.sign_error) {
        msg += " Signing did not complete — re-sign the destination tag manually.";
      } else if (prom.re_signed) {
        msg += " Destination manifest signed with the workspace key.";
      }
      toast.success(msg);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      // Mirror the BFF's error taxonomy: 403 = missing writer role on
      // either side, 404 = source/destination missing, 409 = destination
      // tag immutable, 400 = shape rejected. Anything else is a transport
      // blip.
      const msg =
        status === 403
          ? "Writer role required on both source and destination repositories."
          : status === 404
            ? "Source tag or destination repository not found. Toggle \"Create destination repository\" if you want the BFF to create it."
            : status === 409
              ? "Destination tag is immutable — pick a different tag name or lift the pin."
              : status === 400
                ? "Promotion request was rejected — check the destination fields."
                : "Couldn't promote. Try again, or check the BFF logs.";
      toast.error(msg);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[520px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Ship className="size-4 text-[var(--color-accent)]" />
            Promote tag
          </DialogTitle>
          <DialogDescription>
            Copy{" "}
            <code className="font-mono">
              {srcOrg}/{srcRepo}:{srcTag}
            </code>{" "}
            onto a destination tag. Both tags reference the same manifest
            — no blobs are copied — so storage stays deduplicated. Requires
            writer role on both source and destination repositories.
          </DialogDescription>
        </DialogHeader>

        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-6"
          noValidate
        >
          {/* Destination org — dropdown when we have writer-tier memberships,
              free-text otherwise (global admins, unresolved me query). */}
          <div>
            <Label htmlFor="promote-dst-org" className="mb-2 inline-block">
              Destination org
            </Label>
            {useTextInput ? (
              <Input
                id="promote-dst-org"
                autoFocus
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                aria-invalid={Boolean(errors.dst_org) || undefined}
                {...register("dst_org")}
              />
            ) : (
              <Controller
                control={control}
                name="dst_org"
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger
                      id="promote-dst-org"
                      aria-invalid={Boolean(errors.dst_org) || undefined}
                      className="w-full font-mono"
                    >
                      <SelectValue placeholder="Pick an org" />
                    </SelectTrigger>
                    <SelectContent>
                      {orgOptions.map((org) => (
                        <SelectItem key={org} value={org} className="font-mono">
                          {org}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            )}
            {errors.dst_org ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.dst_org.message}
              </p>
            ) : useTextInput ? null : (
              <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                Orgs visible to you. The BFF re-checks writer role on the
                destination repo at submit time.
              </p>
            )}
          </div>

          {/* Destination repo */}
          <div>
            <Label htmlFor="promote-dst-repo" className="mb-2 inline-block">
              Destination repository
            </Label>
            <Input
              id="promote-dst-repo"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={Boolean(errors.dst_repo) || undefined}
              {...register("dst_repo")}
            />
            {errors.dst_repo ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.dst_repo.message}
              </p>
            ) : null}
          </div>

          {/* Destination tag */}
          <div>
            <Label htmlFor="promote-dst-tag" className="mb-2 inline-block">
              Destination tag
            </Label>
            <Input
              id="promote-dst-tag"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={Boolean(errors.dst_tag) || undefined}
              {...register("dst_tag")}
            />
            {errors.dst_tag ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.dst_tag.message}
              </p>
            ) : (
              <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                Defaults to the source tag name — override for the common
                &quot;promote v1.0 to prod&quot; flow.
              </p>
            )}
          </div>

          {/* Create-if-missing toggle. Layout mirrors the settings-page
              toggle rows: label + description on the left, switch on the
              right. Wrapped in a bordered surface so it visually reads as
              a distinct control rather than another form field. */}
          <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <Label
                  htmlFor="promote-create-if-missing"
                  className="cursor-pointer text-sm"
                >
                  Create destination repository if it doesn&apos;t exist
                </Label>
                <p className="mt-1 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                  Off (default) — a typo returns 404 so an accidental
                  destination is caught up-front. On — the BFF creates
                  the destination repo with permissive defaults inside
                  the same transaction. The destination ORG must exist
                  either way.
                </p>
              </div>
              <Controller
                control={control}
                name="create_if_missing"
                render={({ field }) => (
                  <Switch
                    id="promote-create-if-missing"
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                )}
              />
            </div>
          </div>

          {/* Re-sign toggle (FUT-020 follow-up). Same row layout as the
              create-if-missing switch above. Off by default so promotion
              stays a pure metadata copy unless the operator wants a fresh
              signature bound to the destination — the dev→prod hand-off. */}
          <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <Label
                  htmlFor="promote-re-sign"
                  className="cursor-pointer text-sm"
                >
                  Re-sign the destination manifest after promoting
                </Label>
                <p className="mt-1 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                  Off (default) — promotion copies metadata only; the
                  destination inherits whatever signatures already cover the
                  digest. On — the workspace key signs the destination
                  manifest after the promotion. Requires a configured signer;
                  the promotion still succeeds if signing fails and you can
                  retry from the tag&apos;s Sign action.
                </p>
              </div>
              <Controller
                control={control}
                name="re_sign_on_promote"
                render={({ field }) => (
                  <Switch
                    id="promote-re-sign"
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                )}
              />
            </div>
          </div>

          {/* Optional note */}
          <div>
            <Label htmlFor="promote-note" className="mb-2 inline-block">
              Note{" "}
              <span className="text-[var(--color-fg-subtle)]">(optional)</span>
            </Label>
            <Input
              id="promote-note"
              autoComplete="off"
              placeholder="e.g. green-lit release for prod"
              aria-invalid={Boolean(errors.note) || undefined}
              {...register("note")}
            />
            {errors.note ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.note.message}
              </p>
            ) : (
              <p className="mt-3 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                Surfaces on the promotion history + audit event. Max 256 chars.
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
            <Button
              type="submit"
              loading={isSubmitting}
              disabled={isSubmitting}
            >
              {isSubmitting ? "Promoting" : "Promote"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
