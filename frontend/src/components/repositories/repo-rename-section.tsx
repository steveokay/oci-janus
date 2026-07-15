import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
import { Pencil } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useRenameRepository } from "@/lib/api/repositories";

// RepoRenameSection — General-section card on the repo Settings tab (repo
// rename feature, PR C). Renames the repo within its current org via the
// dedicated POST /repositories/{org}/{repo}/rename route, which also migrates
// the repo-scoped RBAC grants in registry-auth.
//
// Rename changes the repo's URL, so this is guarded by a confirm dialog and,
// on success, navigates to the new location. The BFF returns a partial-success
// `rbac_warning` when the rename committed but the scope rewrite failed — that
// is surfaced as a warning toast so the operator knows to re-grant manually.

interface RepoRenameSectionProps {
  org: string;
  repo: string;
}

// The repo-name allowlist mirrors the BFF validator (validateRepoName) and the
// metadata handler. Validating client-side gives an inline error before the
// round-trip rather than a 400 after it.
const REPO_NAME_RE = /^[a-z0-9]+([._-][a-z0-9]+)*$/;
const MAX_REPO_NAME = 128;

export function RepoRenameSection({
  org,
  repo,
}: RepoRenameSectionProps): React.ReactElement {
  const navigate = useNavigate();
  const rename = useRenameRepository();

  const [draft, setDraft] = React.useState(repo);
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  // Reset the draft to the live name whenever the repo prop changes (e.g. after
  // a successful rename navigates to the new name).
  React.useEffect(() => {
    setDraft(repo);
  }, [repo]);

  const trimmed = draft.trim();
  const changed = trimmed !== repo;
  const tooLong = trimmed.length > MAX_REPO_NAME;
  const invalid = trimmed.length > 0 && !REPO_NAME_RE.test(trimmed);
  // The Rename button opens the confirm dialog; it is enabled only for a
  // non-empty, changed, well-formed name.
  const canSubmit = changed && !tooLong && !invalid && trimmed.length > 0;

  async function doRename(): Promise<void> {
    try {
      const res = await rename.mutateAsync({ org, repo, new_name: trimmed });
      setConfirmOpen(false);
      if (res.rbac_warning) {
        // Partial success — the rename is durable but grants need attention.
        toast.warning(res.rbac_warning);
      } else {
        toast.success(
          `Renamed to ${org}/${trimmed}` +
            (res.roles_rewritten > 0
              ? ` · ${res.roles_rewritten} access grant${res.roles_rewritten === 1 ? "" : "s"} migrated`
              : ""),
        );
      }
      // Follow the repo to its new URL.
      void navigate({
        to: "/repositories/$org/$repo",
        params: { org, repo: trimmed },
      });
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      const message =
        code === 403
          ? "Repository admin role required."
          : code === 409
            ? "A repository with that name already exists in this organization."
            : "Couldn't rename the repository. Check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start gap-2">
          <Pencil className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Rename
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Change this repository&apos;s name within{" "}
              <code className="font-mono">{org}</code>. Existing tags and
              manifests are preserved; pull URLs change. Access grants migrate
              automatically.
            </p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0 space-y-3">
        <div className="flex items-center gap-2">
          <span className="shrink-0 font-mono text-sm text-[var(--color-fg-subtle)]">
            {org}/
          </span>
          <Input
            aria-label="New repository name"
            value={draft}
            disabled={rename.isPending}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            autoCapitalize="none"
            autoCorrect="off"
          />
        </div>
        {invalid && (
          <p className="text-[11px] text-[var(--color-danger)]">
            Lowercase letters, digits, and single <code>.</code> <code>_</code>{" "}
            <code>-</code> separators only.
          </p>
        )}
        {tooLong && (
          <p className="text-[11px] text-[var(--color-danger)]">
            Name exceeds {MAX_REPO_NAME} characters.
          </p>
        )}
        <div className="flex justify-end">
          <Button
            size="sm"
            disabled={!canSubmit || rename.isPending}
            onClick={() => setConfirmOpen(true)}
          >
            Rename…
          </Button>
        </div>
      </CardContent>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rename repository</DialogTitle>
            <DialogDescription asChild>
              <div className="space-y-2">
                <p>
                  Rename <code className="font-mono">{org}/{repo}</code> to{" "}
                  <code className="font-mono">{org}/{trimmed}</code>?
                </p>
                <p>
                  Anyone pulling by the old name will get a 404 until they
                  update their references. This does not affect stored images.
                </p>
              </div>
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => setConfirmOpen(false)}
              disabled={rename.isPending}
            >
              Cancel
            </Button>
            <Button onClick={() => void doRename()} disabled={rename.isPending}>
              {rename.isPending ? "Renaming…" : "Rename repository"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
