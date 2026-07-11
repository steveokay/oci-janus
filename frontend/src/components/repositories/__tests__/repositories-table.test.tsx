import { render, screen } from "@testing-library/react";
import { describe, test, expect, vi } from "vitest";
import { RepositoriesTable } from "@/components/repositories/repositories-table";
import type { Repository } from "@/lib/api/types";

// The table's Row uses TanStack Router's <Link> + useNavigate for row
// navigation. Neither needs a real router for these tests — stub Link to a
// plain anchor and useNavigate to a no-op so the component renders standalone.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
  useNavigate: () => vi.fn(),
}));

// repo builds a Repository fixture with all required fields, letting each test
// override just the artifact_types under test.
function repo(partial: Partial<Repository>): Repository {
  return {
    repo_id: "r1",
    org_id: "o1",
    org: "dev",
    name: "api",
    is_public: false,
    storage_used_bytes: 1,
    storage_quota_bytes: 100,
    created_at: "2026-07-10T00:00:00Z",
    description: "",
    ...partial,
  } as Repository;
}

describe("RepositoriesTable type column", () => {
  test("renders one badge per artifact type; mixed repo shows both", () => {
    render(
      <RepositoriesTable
        repositories={[repo({ artifact_types: ["image", "helm"] })]}
      />,
    );
    expect(screen.getByText("Image")).toBeInTheDocument();
    expect(screen.getByText("Helm chart")).toBeInTheDocument();
  });

  test("repo with no artifact types renders no badge", () => {
    render(
      <RepositoriesTable repositories={[repo({ artifact_types: [] })]} />,
    );
    expect(screen.queryByText("Image")).toBeNull();
    expect(screen.queryByText("Helm chart")).toBeNull();
  });
});
