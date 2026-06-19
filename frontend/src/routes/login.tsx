import * as React from "react";
import { createFileRoute, useNavigate, redirect } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { motion } from "framer-motion";
import { ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { login } from "@/lib/api/auth";
import { authStore } from "@/lib/auth/store";

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

export const Route = createFileRoute("/login")({
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
  const [submitting, setSubmitting] = React.useState(false);
  const [rootError, setRootError] = React.useState<string | null>(null);

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { username: "", password: "" },
  });

  async function onSubmit(values: FormValues): Promise<void> {
    setRootError(null);
    setSubmitting(true);
    try {
      await login(values.username, values.password);
      void navigate({ to: "/", replace: true });
    } catch {
      // Single error path for every failure mode — see FE-SEC-005.
      setRootError(LOGIN_ERROR);
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

        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-5 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-elevated)]"
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
            <Input
              id="password"
              type="password"
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

        <p className="mt-6 text-center text-xs text-[var(--color-fg-subtle)]">
          Trouble signing in? Ask your platform administrator.
        </p>
      </motion.div>
    </div>
  );
}
