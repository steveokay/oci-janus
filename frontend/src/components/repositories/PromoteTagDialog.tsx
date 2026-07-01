import * as React from "react";
import { useForm } from "react-hook-form";
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
import { usePromoteTag } from "@/lib/api/promotions";

// PromoteTagDialog — FUT-020.
//
// Posts to POST /api/v1/repositories/{srcOrg}/{srcRepo}/tags/{srcTag}/promote
// with `{dst_org, dst_repo, dst_tag, note?}`. The backend requires writer
// role on BOTH source and destination — we don't pre-validate on the FE,
// we just surface the 403 as a clear toast so the operator understands
// which side they lack access to.
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
});

type FormValues = z.infer<typeof schema>;

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
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      dst_org: srcOrg,
      dst_repo: srcRepo,
      dst_tag: srcTag,
      note: "",
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
      });
      toast.success(
        `Promoted ${srcOrg}/${srcRepo}:${srcTag} → ${prom.dst_org}/${prom.dst_repo}:${prom.dst_tag}.`,
      );
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
            ? "Source tag or destination repository not found."
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
          {/* Destination org */}
          <div>
            <Label htmlFor="promote-dst-org" className="mb-2 inline-block">
              Destination org
            </Label>
            <Input
              id="promote-dst-org"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={Boolean(errors.dst_org) || undefined}
              {...register("dst_org")}
            />
            {errors.dst_org ? (
              <p className="mt-2 text-xs text-[var(--color-danger)]">
                {errors.dst_org.message}
              </p>
            ) : null}
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
