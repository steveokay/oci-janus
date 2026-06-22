import * as React from "react";
import { createFileRoute, redirect, useNavigate, useSearch } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ServiceAccountsTable } from "@/components/access/ServiceAccountsTable";
import { CreateServiceAccountDialog } from "@/components/access/CreateServiceAccountDialog";
import { ServiceAccountDetail } from "@/components/access/ServiceAccountDetail";
import { authStore } from "@/lib/auth/store";
import { isPlatformAdmin } from "@/lib/auth/jwt";

// /api-keys/service-accounts — workspace admin-only list + create surface.
//
// Admin guard: mirrors _authenticated.admin.scanner.tsx exactly — uses
// `authStore.getClaims()` (imperative, outside React) so we can redirect
// before the component mounts. `isPlatformAdmin` checks `roles.includes("admin")`
// in the JWT claims — the same primitive AccessSubNav uses to gate the
// "Workspace" section.
//
// The route does NOT render a detail drawer — that is T26's job.
// TODO (T26): Mount <ServiceAccountDetail id={selectedId} /> here once
// the drawer component lands. Wire it to the ?id search param below.
export const Route = createFileRoute(
  "/_authenticated/api-keys/service-accounts",
)({
  beforeLoad: () => {
    const claims = authStore.getClaims();
    if (!isPlatformAdmin(claims)) {
      // Non-admins are bounced back to the personal-keys index rather than
      // /login so they see a page rather than an unexpected redirect.
      throw redirect({ to: "/api-keys" });
    }
  },
  component: ServiceAccountsPage,
});

function ServiceAccountsPage(): React.ReactElement {
  const navigate = useNavigate();
  // Read the ?id search param set by row-click and handleCreated.
  // TanStack Router returns all current search params via useSearch; we
  // cast through unknown because the route does not declare a validateSearch
  // schema (the ?id param is set imperatively via navigate({ search: { id } })).
  const search = useSearch({ strict: false }) as Record<string, string | undefined>;
  const selectedID = search.id ?? null;
  const [createOpen, setCreateOpen] = React.useState(false);

  // Row click — set ?id=<id> in the URL so T26's drawer can read it.
  // We use `replace: false` so the browser Back button undoes the drawer open.
  function handleSelect(id: string): void {
    void navigate({
      to: "/api-keys/service-accounts",
      search: { id },
    });
  }

  // onCreate — navigate to ?id=<new-id> immediately after creation so the
  // drawer (T26) opens on the newly created account.
  function handleCreated(id: string): void {
    void navigate({
      to: "/api-keys/service-accounts",
      search: { id },
    });
  }

  return (
    <div className="space-y-6">
      {/* Page header — matches _authenticated.api-keys.index.tsx header style. */}
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Access
          </p>
          <h1 className="font-display text-3xl font-medium tracking-tight">
            Service accounts
          </h1>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            Machine identities for CI pipelines, Terraform modules, and
            automation. Each account can issue scoped API keys that are rotated
            and revoked independently of personal credentials.
          </p>
        </div>

        {/* Primary CTA — opens the Create dialog. */}
        <Button
          variant="accent"
          size="md"
          className="shrink-0 self-start sm:self-auto"
          onClick={() => setCreateOpen(true)}
        >
          <Plus aria-hidden />
          New service account
        </Button>
      </header>

      {/* Table — reads from useServiceAccounts({ includeDisabled: true }). */}
      <ServiceAccountsTable onSelect={handleSelect} onAdd={() => setCreateOpen(true)} />

      {/* ServiceAccountDetail drawer — mounts when ?id is set in the URL.
          Closing calls navigate({ search: {} }) to clear the ?id param. */}
      {selectedID ? (
        <ServiceAccountDetail
          saID={selectedID}
          onClose={() => void navigate({ to: "/api-keys/service-accounts", search: {} })}
        />
      ) : null}

      {/* Create dialog — mounted at this level so it persists across row
          selections without unmounting between navigations. */}
      <CreateServiceAccountDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={handleCreated}
      />
    </div>
  );
}
