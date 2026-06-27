// REDESIGN-001 Phase 4.3 §2 — first-run onboarding wizard at /getting-started.
//
// What this is:
//   A six-step Welcome → Org → Repo → Push cheat-sheet → API key → Done
//   wizard rendered inside the standard authenticated AppShell. It walks a
//   fresh operator from "I just logged in" to "I have an org + repo + key
//   and know how to docker push my first image".
//
// Who lands here:
//   - Phase 4.3 §3 (NOT this PR) will redirect callers from `/` to
//     `/getting-started` when `me.onboarding_complete !== true`. Until that
//     ships, the route is reachable by direct navigation only — useful for
//     hand-driven demos and for ourselves while developing.
//   - Operators who want to replay the wizard (Phase 4.3 §3 will add a
//     Settings link — not yet wired).
//
// Who calls useCompleteOnboarding:
//   The wizard itself, on either the final "Open repositories" CTA (Step 5)
//   or the universal "Skip for now" button (Steps 0..4). Both paths POST
//   `/api/v1/users/me/onboarding/complete` and patch the cached MeResponse
//   so the dashboard redirect logic in Phase 4.3 §3 sees the new truth
//   immediately.
//
// Why the `_authenticated.` prefix:
//   We read `useMe()` + `useWorkspace()` and call mutations against the
//   management BFF — all of which require a JWT. The wizard is not a
//   public landing page; unauthenticated visitors hit `/login` first.
//
// Implementation shape:
//   One file, one component, six step renderers + a couple of small
//   primitives (header, footer, step indicator). Steps are inline JSX
//   blocks indexed via React.useState<number>; nothing here is heavy
//   enough to warrant extraction yet (largest step ~70 LOC). If the
//   wizard grows new steps, lift each step into its own file under
//   `components/onboarding/`.
import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  KeyRound,
  Plus,
  Rocket,
  Sparkles,
  Terminal,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { CopyButton } from "@/components/ui/copy-button";
import { useMe, useCompleteOnboarding } from "@/lib/api/me";
import { useWorkspace } from "@/lib/api/workspace";
import { useClaimOrg } from "@/lib/api/admin-orgs";
import { useCreateRepository } from "@/lib/api/repositories";
import { useCreateApiKey } from "@/lib/api/api-keys";

// Route definition — sits under `_authenticated` so the AppShell + auth
// gate applies automatically (see `_authenticated.tsx`). No search params,
// no loader: the page is self-contained and pulls everything from hooks.
export const Route = createFileRoute("/_authenticated/getting-started")({
  component: GettingStartedWizard,
});

// Validation regexes mirror CLAUDE.md §7 "Input Validation" — keep these
// here (not pulled from a shared module) so the wizard's UX errors line up
// with what the BFF actually accepts. If the server rules ever loosen,
// loosen here too so we don't reject something the backend would accept.
const ORG_NAME_RE = /^[a-z0-9-]{2,64}$/;
const REPO_NAME_RE = /^[a-z0-9]+([._-][a-z0-9]+)*$/;

// TOTAL_STEPS is referenced by the step indicator and the footer copy.
// Bumping this if a new step is inserted is the only change required —
// the renderer dispatches on `step` so adding cases is mechanical.
const TOTAL_STEPS = 6;

function GettingStartedWizard(): React.ReactElement {
  const navigate = useNavigate();

  // Wizard-wide state. We persist nothing in URL/localStorage — the
  // wizard is short, and if the user reloads mid-flow they restart at
  // step 0 which is fine (the backend mutations already ran are idempotent
  // for org claim, and repo/key creations get reported in the toast).
  const [step, setStep] = React.useState<number>(0);
  const [orgName, setOrgName] = React.useState<string>("");
  const [repoName, setRepoName] = React.useState<string>("");
  const [createdKeySecret, setCreatedKeySecret] = React.useState<string | null>(
    null,
  );

  const complete = useCompleteOnboarding();

  // Workspace name powers the page header eyebrow. We fall back to a
  // generic phrase when the workspace endpoint isn't wired (BFF returns
  // null in that case — see useWorkspace).
  const { data: workspace } = useWorkspace();
  const workspaceName = workspace?.name ?? "Janus";

  // finishOnboardingTo is the shared exit path used by Skip + Done. We
  // await the mutation so the cache patch lands before the navigation —
  // the dashboard redirect logic in Phase 4.3 §3 will read
  // `onboarding_complete` from the cached MeResponse, so a stale read
  // would otherwise bounce the user straight back into the wizard. If
  // the call fails we surface a toast and still navigate; the worst case
  // is the operator sees the wizard once more next session.
  const skipToHome = React.useCallback(async () => {
    try {
      await complete.mutateAsync();
    } catch {
      toast.error(
        "We couldn't mark onboarding complete. You'll see this wizard again next time you visit /.",
      );
    }
    void navigate({ to: "/" });
  }, [complete, navigate]);

  const finishToRepositories = React.useCallback(async () => {
    try {
      await complete.mutateAsync();
    } catch {
      toast.error(
        "We couldn't mark onboarding complete. You'll see this wizard again next time.",
      );
    }
    void navigate({ to: "/repositories" });
  }, [complete, navigate]);

  // Step rendering. We dispatch via a switch instead of an array of
  // components so each step can hold its own form state hooks without
  // tripping React's rules-of-hooks (hooks-per-component, not
  // per-rendered-step).
  function renderStep(): React.ReactElement {
    switch (step) {
      case 0:
        return (
          <WelcomeStep
            workspaceName={workspaceName}
            onContinue={() => setStep(1)}
          />
        );
      case 1:
        return (
          <CreateOrgStep
            initialName={orgName}
            onCreated={(name) => {
              setOrgName(name);
              setStep(2);
            }}
          />
        );
      case 2:
        return (
          <CreateRepoStep
            org={orgName}
            initialName={repoName}
            onCreated={(name) => {
              setRepoName(name);
              setStep(3);
            }}
            onBackToOrg={() => setStep(1)}
          />
        );
      case 3:
        return (
          <PushCheatSheetStep
            org={orgName}
            repo={repoName}
            onContinue={() => setStep(4)}
          />
        );
      case 4:
        return (
          <CreateApiKeyStep
            createdSecret={createdKeySecret}
            onCreated={(secret) => setCreatedKeySecret(secret)}
            onContinue={() => setStep(5)}
          />
        );
      case 5:
        return (
          <DoneStep
            org={orgName}
            repo={repoName}
            onOpenRepositories={() => void finishToRepositories()}
          />
        );
      default:
        // Defensive — should never be reachable given the setters above,
        // but if a future step setter goes off-by-one we surface a usable
        // recovery affordance instead of a blank pane.
        return (
          <div className="text-sm text-[var(--color-fg-muted)]">
            Lost the wizard state. <button onClick={() => setStep(0)}>Start over</button>.
          </div>
        );
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      {/* Page header — eyebrow + title + workspace name. Matches the
          visual tone of other authenticated pages (eyebrow uppercase
          tracking-wide, h1 in font-display). */}
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Onboarding
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Welcome to {workspaceName}
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          A six-step walkthrough to push your first image. Skip any time —
          you can replay the wizard later from Settings.
        </p>
      </header>

      {/* Step indicator — a thin progress bar with a "Step N of M"
          legend. Simpler than a chevroned breadcrumb at 6 steps; reads
          well on narrow screens. The Progress component lives in
          components/ui and already handles tone thresholds for us. */}
      <StepIndicator current={step} total={TOTAL_STEPS} />

      {/* The active step body. Wrapped in a bordered card so the
          wizard reads as a single bounded object rather than blending
          into the rest of the AppShell content. */}
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-card)]">
        {renderStep()}
      </section>

      {/* Footer row — Back / Skip / (the per-step primary Continue lives
          inside renderStep so it can bind to the form submit). We render
          Back + Skip here because they're identical across every step. */}
      <footer className="flex items-center justify-between gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setStep((s) => Math.max(0, s - 1))}
          disabled={step === 0}
        >
          <ArrowLeft className="size-3.5" />
          Back
        </Button>
        {step < TOTAL_STEPS - 1 ? (
          <Button
            variant="link"
            size="sm"
            onClick={() => void skipToHome()}
            disabled={complete.isPending}
          >
            Skip for now
          </Button>
        ) : (
          <span /> /* placeholder to keep the flex layout stable */
        )}
      </footer>
    </div>
  );
}

// ── Step indicator ───────────────────────────────────────────────────

interface StepIndicatorProps {
  current: number; // 0-indexed
  total: number;
}

// StepIndicator. A "Step N of M" caption + a thin progress bar. We
// compute percentage from (current + 1) so step 0 already shows 1/6
// worth of progress — the user has at least landed on the page.
function StepIndicator({ current, total }: StepIndicatorProps): React.ReactElement {
  const pct = ((current + 1) / total) * 100;
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between text-xs text-[var(--color-fg-muted)]">
        <span>
          Step {current + 1} of {total}
        </span>
        <span className="font-mono text-[var(--color-fg-subtle)]">
          {Math.round(pct)}%
        </span>
      </div>
      <Progress value={pct} tone="accent" />
    </div>
  );
}

// ── Step 0 — Welcome ─────────────────────────────────────────────────

interface WelcomeStepProps {
  workspaceName: string;
  onContinue: () => void;
}

// WelcomeStep. Sets expectations: what the platform is, what the wizard
// will walk them through. Brief on purpose — the user came here to push
// an image, not to read a brochure.
function WelcomeStep({ workspaceName, onContinue }: WelcomeStepProps): React.ReactElement {
  return (
    <div className="space-y-5">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Step 1 of {TOTAL_STEPS}
      </p>
      <div className="flex items-start gap-3">
        <div className="flex size-10 shrink-0 items-center justify-center rounded-md bg-[var(--color-surface-sunken)] text-[var(--color-accent)]">
          <Rocket className="size-5" />
        </div>
        <div>
          <h2 className="font-display text-xl font-medium">
            Let's get your registry going
          </h2>
          <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
            {workspaceName} is an OCI Distribution-Spec v1.1 Docker registry —
            push, pull, sign, scan, and audit your container images from one
            self-hosted control plane. The next few steps will set up an
            organization, your first repository, and an API key for CI.
          </p>
          <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
            None of this is irreversible — every step is editable from the
            sidebar later.
          </p>
        </div>
      </div>
      <div className="flex justify-end">
        <Button onClick={onContinue}>
          Get started
          <ArrowRight className="size-3.5" />
        </Button>
      </div>
    </div>
  );
}

// ── Step 1 — Create organization ─────────────────────────────────────

interface CreateOrgStepProps {
  initialName: string;
  onCreated: (name: string) => void;
}

// CreateOrgStep. Org creation in this codebase is a side effect of repo
// creation; the platform-admin claim route (`POST /admin/orgs/{org}/claim`)
// is the only way to materialize an org membership before the first repo
// exists. We use it here because it's the one BFF surface that gives the
// caller an admin role on the new org so the repo-create step that
// follows passes the per-org RBAC check.
//
// Operators who already have orgs (via memberships in `useMe()`) get a
// "Use an existing org" affordance below the form so we don't make them
// invent a second name.
function CreateOrgStep({ initialName, onCreated }: CreateOrgStepProps): React.ReactElement {
  const [name, setName] = React.useState<string>(initialName);
  const [error, setError] = React.useState<string | null>(null);
  const claim = useClaimOrg();

  // Pull existing org memberships from /users/me. Filtering on
  // scope_type === "org" picks the per-org role assignments (the
  // platform-admin marker (admin, org, "*") also passes the filter, but
  // "*" is rejected by the org-name regex so we hide it explicitly).
  const { data: me } = useMe();
  const existingOrgs = React.useMemo(() => {
    const seen = new Set<string>();
    return (me?.memberships ?? [])
      .filter((m) => m.scope_type === "org" && m.scope_value !== "*")
      .map((m) => m.scope_value)
      .filter((o) => {
        if (seen.has(o)) return false;
        seen.add(o);
        return true;
      });
  }, [me?.memberships]);

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    setError(null);
    const trimmed = name.trim();
    if (!ORG_NAME_RE.test(trimmed)) {
      setError("Use 2–64 lowercase letters, digits, or hyphens.");
      return;
    }
    try {
      await claim.mutateAsync(trimmed);
      onCreated(trimmed);
    } catch (err) {
      const status = (err as { response?: { status?: number } })?.response?.status;
      if (status === 403) {
        setError(
          "You don't have permission to claim a new org. Pick an existing org below, or ask an admin.",
        );
      } else {
        setError("Couldn't create the org. Try again, or check the BFF logs.");
      }
    }
  }

  return (
    <div className="space-y-5">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Step 2 of {TOTAL_STEPS}
      </p>
      <div>
        <h2 className="font-display text-xl font-medium">
          Create your first organization
        </h2>
        <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
          Organizations group related repositories and own RBAC (members,
          retention, signing policy). Pick something short — you'll see it
          as the first segment of every image reference, like
          <code className="ml-1 rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-xs">
            registry/<span className="text-[var(--color-accent)]">your-org</span>/api:1.0
          </code>
          .
        </p>
      </div>

      <form onSubmit={handleSubmit} className="space-y-3" noValidate>
        <div className="space-y-1.5">
          <Label htmlFor="org-name">Organization name</Label>
          <Input
            id="org-name"
            placeholder="acme"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            aria-invalid={error ? true : undefined}
          />
          {error ? (
            <p className="text-xs text-[var(--color-danger)]">{error}</p>
          ) : (
            <p className="text-xs text-[var(--color-fg-subtle)]">
              Lowercase letters, digits, and hyphens. 2–64 characters.
            </p>
          )}
        </div>
        <div className="flex justify-end">
          <Button type="submit" loading={claim.isPending} disabled={claim.isPending}>
            {claim.isPending ? "Creating" : "Create org"}
            <ArrowRight className="size-3.5" />
          </Button>
        </div>
      </form>

      {existingOrgs.length > 0 ? (
        <div className="border-t border-[var(--color-border)] pt-4">
          <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Or skip — use an org you already own
          </p>
          <div className="mt-2 flex flex-wrap gap-2">
            {existingOrgs.map((o) => (
              <button
                key={o}
                type="button"
                onClick={() => onCreated(o)}
                className="rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 py-1 text-xs text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)]"
              >
                {o}
              </button>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

// ── Step 2 — Create repository ───────────────────────────────────────

interface CreateRepoStepProps {
  org: string;
  initialName: string;
  onCreated: (name: string) => void;
  onBackToOrg: () => void;
}

// CreateRepoStep. Calls the existing `useCreateRepository` mutation with
// `is_public: false` (the safer default — operators can flip later from
// the repo Settings tab). The org name is fixed by Step 1 and shown as
// the URL prefix so it's clear what reference will be created.
function CreateRepoStep({
  org,
  initialName,
  onCreated,
  onBackToOrg,
}: CreateRepoStepProps): React.ReactElement {
  const [name, setName] = React.useState<string>(initialName);
  const [error, setError] = React.useState<string | null>(null);
  const create = useCreateRepository();

  // If the user backed out of step 1 we won't have an org. Render a
  // small recovery affordance instead of letting them submit an
  // invalid body.
  if (!org) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-[var(--color-fg-muted)]">
          We lost track of which org we're creating the repo under.
        </p>
        <Button onClick={onBackToOrg}>Pick an org</Button>
      </div>
    );
  }

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    setError(null);
    const trimmed = name.trim();
    if (!REPO_NAME_RE.test(trimmed) || trimmed.length > 128) {
      setError(
        "Use lowercase letters, digits, and dot/dash/underscore separators. Max 128 chars.",
      );
      return;
    }
    try {
      await create.mutateAsync({
        org,
        name: trimmed,
        is_public: false,
      });
      onCreated(trimmed);
    } catch (err) {
      const status = (err as { response?: { status?: number } })?.response?.status;
      if (status === 409) {
        setError("That repository already exists in this org.");
      } else if (status === 403) {
        setError("You don't have permission to create repositories here.");
      } else {
        setError("Couldn't create the repository. Try again.");
      }
    }
  }

  return (
    <div className="space-y-5">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Step 3 of {TOTAL_STEPS}
      </p>
      <div>
        <h2 className="font-display text-xl font-medium">
          Create your first repository
        </h2>
        <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
          Repositories hold image tags and manifests. Make a private repo
          now — flipping visibility and adding write members is one click
          from the repo's Settings tab later.
        </p>
      </div>

      <form onSubmit={handleSubmit} className="space-y-3" noValidate>
        <div className="space-y-1.5">
          <Label htmlFor="repo-name">Repository name</Label>
          <div className="flex items-stretch overflow-hidden rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface)]">
            <span className="flex items-center px-3 text-sm text-[var(--color-fg-muted)]">
              {org}/
            </span>
            <Input
              id="repo-name"
              placeholder="api"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              aria-invalid={error ? true : undefined}
              className="border-0 focus-visible:border-0"
            />
          </div>
          {error ? (
            <p className="text-xs text-[var(--color-danger)]">{error}</p>
          ) : (
            <p className="text-xs text-[var(--color-fg-subtle)]">
              Lowercase + digits, separators . _ -. Max 128 chars.
            </p>
          )}
        </div>
        <div className="flex justify-end">
          <Button type="submit" loading={create.isPending} disabled={create.isPending}>
            {create.isPending ? "Creating" : "Create repository"}
            <ArrowRight className="size-3.5" />
          </Button>
        </div>
      </form>
    </div>
  );
}

// ── Step 3 — Push cheat sheet ────────────────────────────────────────

interface PushCheatSheetStepProps {
  org: string;
  repo: string;
  onContinue: () => void;
}

// PushCheatSheetStep. A static code block showing the canonical
// docker-login + tag + push commands, with the live host/org/repo
// substituted in. We deliberately do NOT poll the metadata API to verify
// the user actually pushed — the wizard trusts the operator and lets them
// move on. (Verification belongs on the dashboard "first push" success
// state, not in the wizard.)
function PushCheatSheetStep({
  org,
  repo,
  onContinue,
}: PushCheatSheetStepProps): React.ReactElement {
  // window.location.hostname is the gateway hostname — correct for the
  // current deployment regardless of whether the operator wired a
  // custom domain. In SSR contexts this would explode; in our SPA we
  // safely access window because the wizard only renders on the client.
  const host = typeof window !== "undefined" ? window.location.hostname : "registry.example.com";
  const ref = `${host}/${org || "your-org"}/${repo || "your-repo"}:latest`;
  const script = [
    "# Log in (use your username/password or an API key)",
    `docker login ${host}`,
    "",
    "# Tag your local image",
    `docker tag my-image:latest ${ref}`,
    "",
    "# Push",
    `docker push ${ref}`,
  ].join("\n");

  return (
    <div className="space-y-5">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Step 4 of {TOTAL_STEPS}
      </p>
      <div>
        <div className="flex items-center gap-2">
          <Terminal className="size-4 text-[var(--color-fg-muted)]" />
          <h2 className="font-display text-xl font-medium">
            Push your first image
          </h2>
        </div>
        <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
          Run these commands from your local shell. The reference uses
          this deployment's gateway hostname, so the snippet works
          verbatim. Your username and password are the same ones you
          used to log in.
        </p>
      </div>

      <div className="relative rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-4">
        <pre className="overflow-x-auto font-mono text-xs leading-relaxed text-[var(--color-fg)]">
          {script}
        </pre>
        <div className="absolute right-2 top-2">
          <CopyButton value={script} label="Copy commands" />
        </div>
      </div>

      <p className="text-xs text-[var(--color-fg-subtle)]">
        Not pushing right now? Skip ahead — the snippet stays the same
        and you can grab it from the repo's Pull/Push card later.
      </p>

      <div className="flex justify-end">
        <Button onClick={onContinue}>
          Next
          <ArrowRight className="size-3.5" />
        </Button>
      </div>
    </div>
  );
}

// ── Step 4 — Create API key ──────────────────────────────────────────

interface CreateApiKeyStepProps {
  createdSecret: string | null;
  onCreated: (secret: string) => void;
  onContinue: () => void;
}

// CreateApiKeyStep. Inline-form variant of `CreateApiKeyDialog` so we
// don't have to coordinate dialog open/close inside the wizard. The
// plaintext secret is rendered inline once created — we trust the
// wizard's overall posture (we just walked you through it) more than
// the dialog's masked-reveal UX, which makes more sense outside of a
// guided flow.
function CreateApiKeyStep({
  createdSecret,
  onCreated,
  onContinue,
}: CreateApiKeyStepProps): React.ReactElement {
  const [name, setName] = React.useState<string>("ci-bot");
  const [error, setError] = React.useState<string | null>(null);
  const create = useCreateApiKey();

  async function handleSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    setError(null);
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Name is required.");
      return;
    }
    try {
      const created = await create.mutateAsync({ name: trimmed });
      onCreated(created.key);
    } catch (err) {
      const status = (err as { response?: { status?: number } })?.response?.status;
      setError(
        status === 403
          ? "You don't have permission to create API keys."
          : "Couldn't create the key. Try again.",
      );
    }
  }

  return (
    <div className="space-y-5">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Step 5 of {TOTAL_STEPS}
      </p>
      <div>
        <div className="flex items-center gap-2">
          <KeyRound className="size-4 text-[var(--color-accent)]" />
          <h2 className="font-display text-xl font-medium">
            Create an API key
          </h2>
        </div>
        <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
          API keys are long-lived credentials for CI pipelines, Terraform,
          and scripts. The plaintext secret is shown once below — store
          it in your secrets manager before moving on.
        </p>
      </div>

      {createdSecret === null ? (
        <form onSubmit={handleSubmit} className="space-y-3" noValidate>
          <div className="space-y-1.5">
            <Label htmlFor="key-name">Key name</Label>
            <Input
              id="key-name"
              placeholder="ci-bot"
              value={name}
              onChange={(e) => setName(e.target.value)}
              aria-invalid={error ? true : undefined}
            />
            {error ? (
              <p className="text-xs text-[var(--color-danger)]">{error}</p>
            ) : (
              <p className="text-xs text-[var(--color-fg-subtle)]">
                A short label so you can find this key in /api-keys later.
              </p>
            )}
          </div>
          <div className="flex justify-between">
            <Button type="button" variant="ghost" size="sm" onClick={onContinue}>
              Skip — I'll create one later
            </Button>
            <Button type="submit" loading={create.isPending} disabled={create.isPending}>
              <Plus className="size-3.5" />
              {create.isPending ? "Creating" : "Create key"}
            </Button>
          </div>
        </form>
      ) : (
        // Post-creation: render the plaintext secret + copy button + a
        // continue affordance. The secret is fully visible (not masked)
        // because the wizard's bordered card already implies a private
        // viewport — the masked-reveal UX from SecretRevealDialog is
        // overkill here.
        <div className="space-y-3">
          <div className="rounded-md border border-[var(--color-warning)]/40 bg-[var(--color-warning)]/5 p-3 text-xs text-[var(--color-fg)]">
            <strong className="font-medium">Save this now —</strong> the
            plaintext is shown exactly once. Closing this step or
            navigating away loses it permanently.
          </div>
          <div className="flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
            <code className="grow break-all font-mono text-xs text-[var(--color-fg)]">
              {createdSecret}
            </code>
            <CopyButton value={createdSecret} label="Copy" />
          </div>
          <div className="flex justify-end">
            <Button onClick={onContinue}>
              I've saved it
              <ArrowRight className="size-3.5" />
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

// ── Step 5 — Done ─────────────────────────────────────────────────────

interface DoneStepProps {
  org: string;
  repo: string;
  onOpenRepositories: () => void;
}

// DoneStep. The CTA fires the onboarding-complete mutation and
// navigates the user to the repositories list — the most useful place
// to land given they just created a repo.
function DoneStep({
  org,
  repo,
  onOpenRepositories,
}: DoneStepProps): React.ReactElement {
  return (
    <div className="space-y-5 text-center">
      <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Step 6 of {TOTAL_STEPS}
      </p>
      <div className="mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-success)]/15 text-[var(--color-success)]">
        <CheckCircle2 className="size-7" />
      </div>
      <div>
        <h2 className="font-display text-2xl font-medium">You're set up</h2>
        <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
          {org && repo
            ? `${org}/${repo} is live, your API key is in your secrets manager, and you know how to push. Open Repositories to see the first push land, or jump into Settings to tighten retention, signing, and access policy.`
            : "Open Repositories to see your registry, or jump into Settings to tighten retention, signing, and access policy."}
        </p>
      </div>
      <div className="flex flex-wrap justify-center gap-2">
        <Button onClick={onOpenRepositories}>
          <Sparkles className="size-3.5" />
          Open repositories
        </Button>
      </div>
    </div>
  );
}

