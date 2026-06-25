import { render, screen } from "@testing-library/react";
import { describe, test, expect } from "vitest";
import { RoleSummaryChips } from "../role-summary-chips";
import type { TenantUserRoleSummary } from "@/lib/api/tenant-users";

// FUT-012 Phase C — RoleSummaryChips rendering contract.
//
// Two things this component owes the operator:
//   1. Zero-count chips are suppressed so the column doesn't drown in
//      "0/0/0/0" noise.
//   2. Platform-admin and tenant-admin badges are always rendered
//      first because they convey the highest-privilege state — a
//      casual scan of the column should immediately surface those
//      principals.

const empty: TenantUserRoleSummary = {
  org_admin_count: 0,
  org_writer_count: 0,
  org_reader_count: 0,
  repo_grant_count: 0,
  tenant_admin: false,
  platform_admin: false,
};

describe("RoleSummaryChips", () => {
  test("renders em-dash when the user has zero grants", () => {
    render(<RoleSummaryChips roles={empty} />);
    expect(screen.getByLabelText("No role grants")).toBeInTheDocument();
  });

  test("renders the platform-admin badge when set", () => {
    render(<RoleSummaryChips roles={{ ...empty, platform_admin: true }} />);
    expect(screen.getByText("Platform admin")).toBeInTheDocument();
  });

  test("renders the tenant-admin badge when set", () => {
    render(<RoleSummaryChips roles={{ ...empty, tenant_admin: true }} />);
    expect(screen.getByText("Tenant admin")).toBeInTheDocument();
  });

  test("renders count chips with their values", () => {
    render(
      <RoleSummaryChips
        roles={{ ...empty, org_admin_count: 3, org_writer_count: 5, org_reader_count: 7, repo_grant_count: 11 }}
      />,
    );
    expect(screen.getByText("Org admin × 3")).toBeInTheDocument();
    expect(screen.getByText("Writer × 5")).toBeInTheDocument();
    expect(screen.getByText("Reader × 7")).toBeInTheDocument();
    expect(screen.getByText("Repo × 11")).toBeInTheDocument();
  });

  test("suppresses zero-count chips even when others are present", () => {
    render(
      <RoleSummaryChips
        roles={{ ...empty, org_admin_count: 1, org_writer_count: 0, org_reader_count: 0, repo_grant_count: 0 }}
      />,
    );
    // org-admin chip should render; the other count chips must NOT.
    // (We don't search for "× 0" — the suppression is the whole point.)
    expect(screen.getByText("Org admin × 1")).toBeInTheDocument();
    expect(screen.queryByText(/Writer ×/)).toBeNull();
    expect(screen.queryByText(/Reader ×/)).toBeNull();
    expect(screen.queryByText(/Repo ×/)).toBeNull();
  });
});
