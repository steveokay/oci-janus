import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, test, expect } from "vitest";
import { DisableUserDialog } from "../disable-user-dialog";
import type { TenantUser } from "@/lib/api/tenant-users";

// FUT-012 Phase C — type-the-username gate behaves correctly.
// The Disable button only enables once the operator types the
// EXACT username — same pattern PR #109 introduced for single-tag
// delete confirmation.

function renderWithClient(children: React.ReactNode): void {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={qc}>{children}</QueryClientProvider>);
}

const target: TenantUser = {
  user_id: "11111111-2222-3333-4444-555555555555",
  username: "alice",
  display_name: "Alice O'Neill",
  email: "alice@example.com",
  kind: "human",
  status: "active",
  created_at: new Date().toISOString(),
  roles: {
    org_admin_count: 0,
    org_writer_count: 1,
    org_reader_count: 0,
    repo_grant_count: 0,
    tenant_admin: false,
    platform_admin: false,
  },
};

describe("DisableUserDialog confirm gate", () => {
  test("wrong username keeps the disable button disabled", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <DisableUserDialog open onOpenChange={() => {}} user={target} />,
    );
    const input = screen.getByLabelText(/type/i);
    const confirm = screen.getByRole("button", { name: /^Disable user/i });
    await user.type(input, "bob");
    expect(confirm).toBeDisabled();
  });

  test("exact username enables the disable button", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <DisableUserDialog open onOpenChange={() => {}} user={target} />,
    );
    const input = screen.getByLabelText(/type/i);
    const confirm = screen.getByRole("button", { name: /^Disable user/i });
    await user.type(input, "alice");
    expect(confirm).toBeEnabled();
  });

  test("explains the blast radius before the confirm input", () => {
    renderWithClient(
      <DisableUserDialog open onOpenChange={() => {}} user={target} />,
    );
    // The bullet list must call out JWT revocation + API key disable so
    // the operator can't miss them. "API keys" appears in two contexts
    // (the bullet + the DialogDescription paragraph above), so we
    // assert at least one match rather than a unique one.
    expect(screen.getByText(/Active JWT sessions/)).toBeInTheDocument();
    expect(screen.getAllByText(/API keys/).length).toBeGreaterThan(0);
  });
});
