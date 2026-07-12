import * as React from "react";
import { createFileRoute, useNavigate, redirect } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { motion } from "framer-motion";
import { ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "@/components/ui/password-input";
import { Label } from "@/components/ui/label";
import { login } from "@/lib/api/auth";
import { authStore } from "@/lib/auth/store";
import { lockoutMessage } from "@/lib/auth/lockout-message";
import { SSOButtons } from "@/components/auth/sso-buttons";
import { MfaChallenge } from "@/components/auth/mfa-challenge";
import { MfaEnrollDialog } from "@/components/profile/mfa-enroll-dialog";
import { useDeploymentInfo, isSingleMode } from "@/lib/api/deployment-info";

// FE-SEC-005 — vague error messages on both the inline form error AND the
// toast. Never reveal whether the username exists.
const LOGIN_ERROR = "Invalid credentials. Check your username and password.";

const schema = z.object({
  username: z
    .string()
    .min(3, "Username is too short.")
    .max(64, "Username is too long."),
  password: z.string().min(1, "Password is required."),
});

type FormValues = z.infer<typeof schema>;

// safeInternalPath — only honor a post-login redirect target when it is an
// internal absolute path: must start with "/" and must NOT start with "//"
// (protocol-relative URLs like //evil.com would make ?from= an open-redirect
// vector). Anything else falls back to home.
function safeInternalPath(from: string | undefined): string {
  if (from && from.startsWith("/") && !from.startsWith("//")) return from;
  return "/";
}

export const Route = createFileRoute("/login")({
  // Validate the ?from= search param the _authenticated guard sets when it
  // bounces an unauthenticated visitor here. Only a string survives; the
  // open-redirect gate (safeInternalPath) is applied at navigate time.
  validateSearch: (search: Record<string, unknown>): { from?: string } => {
    return typeof search.from === "string" ? { from: search.from } : {};
  },
  // If you're already signed in and hit /login, bounce home — saves a click
  // on browser back/forward.
  beforeLoad: () => {
    if (authStore.getToken()) {
      throw redirect({ to: "/" });
    }
  },
  component: LoginPage,
});

function LoginPage(): React.ReactElement {
  const navigate = useNavigate();
  // ?from= — where the _authenticated guard bounced the user from. Validated
  // shape by the route's validateSearch; safety-gated at navigate time below.
  const { from } = Route.useSearch();
  const [submitting, setSubmitting] = React.useState(false);
  const [rootError, setRootError] = React.useState<string | null>(null);
  // Two-step login state (Task 13). Exactly one of these is set once /login
  // reports an MFA branch; both null means we're still on the password form.
  const [challengeToken, setChallengeToken] = React.useState<string | null>(
    null,
  );
  const [setupToken, setSetupToken] = React.useState<string | null>(null);
  // REDESIGN-001 Phase 2.5 (RM-007) — gate hostile "ask your platform
  // administrator" copy on multi mode. In single-tenant the user IS the
  // platform administrator, so that copy is circular and unhelpful. The
  // /api/v1/deployment-info endpoint is unauthenticated by design
  // (Phase 1.4), so it's safe to call before login. While `data` is
  // undefined (cold cache) we fall back to the multi-mode copy — it's a
  // strictly safer message than a self-hoster-specific hint.
  const { data: deploymentInfo } = useDeploymentInfo();
  const singleMode = isSingleMode(deploymentInfo);

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { username: "", password: "" },
  });

  // Tenant is fixed per environment — see VITE_DEFAULT_TENANT_ID. We
  // don't surface the UUID pre-login (DSGN-014): no UX value to an
  // unauthenticated visitor, and tenant IDs are filter keys we'd rather
  // not leak. Post-login the topbar shows a short chip for the signed-in
  // tenant.
  const tenantId =
    import.meta.env.VITE_DEFAULT_TENANT_ID ?? "";

  // Post-login navigation — shared by the plain-token path and both MFA
  // branches so every success lands the user in the same place. Bounces back
  // to where the auth guard interrupted, but only to an internal absolute
  // path (see safeInternalPath) — a raw ?from= would otherwise be an open
  // redirect. `to` accepts the runtime string; the guard guarantees it's one
  // of our own paths or "/".
  const goPostLogin = React.useCallback(() => {
    void navigate({ to: safeInternalPath(from), replace: true });
  }, [navigate, from]);

  async function onSubmit(values: FormValues): Promise<void> {
    setRootError(null);
    setSubmitting(true);
    try {
      if (!tenantId) {
        setRootError(
          "Login is not configured for this environment. Set VITE_DEFAULT_TENANT_ID.",
        );
        return;
      }
      const result = await login(values.username, values.password, tenantId);
      // Branch on the login outcome. Password was correct in all three cases;
      // only "token" is immediately done.
      switch (result.kind) {
        case "token":
          goPostLogin();
          break;
        case "mfa":
          // Show the OTP step; hide the password form until it resolves.
          setChallengeToken(result.challengeToken);
          break;
        case "mfa_setup":
          // Forced enrolment — open the enroll dialog in setup-token mode.
          setSetupToken(result.setupToken);
          break;
      }
    } catch (e) {
      // A locked account gets a specific "try again in N" message (the backend
      // signals it via 423 ACCOUNT_LOCKED); every other failure stays on the
      // vague generic error so a wrong password can't enumerate accounts
      // (FE-SEC-005).
      setRootError(lockoutMessage(e) ?? LOGIN_ERROR);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-[var(--color-bg)] px-4 py-12">
      {/* Decorative — soft teal radial wash behind the dotted grid */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-dot-grid opacity-50"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute left-1/2 top-1/2 -z-0 size-[640px] -translate-x-1/2 -translate-y-1/2 rounded-full bg-[var(--color-accent-subtle)] opacity-50 blur-3xl"
      />

      <motion.div
        initial={{ opacity: 0, y: 8 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.45, ease: [0.22, 1, 0.36, 1] }}
        className="relative z-10 w-full max-w-[420px]"
      >
        <div className="mb-8 flex flex-col items-center gap-3">
          <span
            className="grid size-11 place-items-center rounded-lg bg-[var(--color-accent)] text-[var(--color-accent-fg)] shadow-[var(--shadow-elevated)]"
            aria-hidden
          >
            <span className="font-display text-xl font-semibold leading-none">
              J
            </span>
          </span>
          <div className="text-center">
            <h1 className="font-display text-2xl font-medium leading-tight">
              Sign in to Janus
            </h1>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
              Registry control plane for your organization.
            </p>
          </div>
        </div>

        {/* Second-factor step — swap the credential card for the OTP form
            while an MFA challenge is pending. Same post-login navigation as
            the plain-token path (goPostLogin). */}
        {challengeToken ? (
          <MfaChallenge challengeToken={challengeToken} onDone={goPostLogin} />
        ) : (
        <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-elevated)]">
          <SSOButtons />

          {/* Divider — the small label sits inside the line for the
              "either / or" cue. Pattern lifted from Stripe / Vercel logins. */}
          <div className="relative my-5">
            <div className="absolute inset-x-0 top-1/2 h-px bg-[var(--color-border)]" />
            <div className="relative flex justify-center">
              <span className="bg-[var(--color-surface)] px-3 text-[10px] font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
                or sign in with credentials
              </span>
            </div>
          </div>

        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-5"
          noValidate
        >
          <div className="space-y-1.5">
            <Label htmlFor="username">Username</Label>
            <Input
              id="username"
              autoComplete="username"
              autoFocus
              placeholder="you@org"
              aria-invalid={Boolean(errors.username) || undefined}
              {...register("username")}
            />
            {errors.username ? (
              <p className="text-xs text-[var(--color-danger)]">
                {errors.username.message}
              </p>
            ) : null}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="password">Password</Label>
            <PasswordInput
              id="password"
              autoComplete="current-password"
              placeholder="••••••••"
              aria-invalid={Boolean(errors.password) || undefined}
              {...register("password")}
            />
            {errors.password ? (
              <p className="text-xs text-[var(--color-danger)]">
                {errors.password.message}
              </p>
            ) : null}
          </div>

          {rootError ? (
            <div
              role="alert"
              className="rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-3 py-2 text-sm text-[var(--color-danger)]"
            >
              {rootError}
            </div>
          ) : null}

          <Button
            type="submit"
            className="w-full"
            loading={submitting}
            disabled={submitting}
          >
            {submitting ? "Signing in" : "Sign in"}
            {!submitting ? <ArrowRight className="size-4" /> : null}
          </Button>
        </form>
        </div>
        )}

        {/* Forced enrolment — the account must set up MFA before it can get a
            token. The dialog runs in setup-token mode (enroll/verify authorised
            by the setup token, no session yet). onComplete stores the access
            token minted by verify and navigates, so the user ends logged in.
            The login page stays underneath the modal. */}
        {setupToken ? (
          <MfaEnrollDialog
            open
            setupToken={setupToken}
            onComplete={(token) => {
              authStore.setToken(token);
              goPostLogin();
            }}
            onOpenChange={(next) => {
              // Only a close (cancel) reaches here while codes aren't shown;
              // drop the setup token to return to the password form.
              if (!next) setSetupToken(null);
            }}
          />
        ) : null}

        <div className="mt-6 flex flex-col items-center gap-1 text-center text-xs text-[var(--color-fg-subtle)]">
          {singleMode ? (
            <span>
              Lost access? See{" "}
              <a
                href="https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/bootstrap-first-admin.md"
                target="_blank"
                rel="noopener noreferrer"
                className="underline hover:text-[var(--color-fg)]"
              >
                the bootstrap runbook
              </a>{" "}
              to reset the first admin.
            </span>
          ) : (
            <span>Trouble signing in? Ask your platform administrator.</span>
          )}
        </div>
      </motion.div>
    </div>
  );
}
