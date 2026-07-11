import { render, screen } from "@testing-library/react";
import { describe, test, expect, vi } from "vitest";
import { OrgCard } from "@/components/orgs/org-card";

// OrgCard wraps the whole card in TanStack Router's <Link>. It needs no real
// router here — stub Link to a plain anchor so the card renders standalone.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

describe("OrgCard type split", () => {
  test("shows image + chart counts when present", () => {
    render(
      <OrgCard
        org={{
          org_id: "o1",
          org: "dev",
          repo_count: 3,
          storage_used_bytes: 2048,
          image_repo_count: 2,
          helm_repo_count: 1,
        }}
      />,
    );
    expect(screen.getByText(/2 images/i)).toBeInTheDocument();
    expect(screen.getByText(/1 chart/i)).toBeInTheDocument();
  });

  test("omits the split when there are no charts and no images", () => {
    render(
      <OrgCard
        org={{ org_id: "o1", org: "empty", repo_count: 0, storage_used_bytes: 0 }}
      />,
    );
    expect(screen.queryByText(/images/i)).toBeNull();
  });
});
