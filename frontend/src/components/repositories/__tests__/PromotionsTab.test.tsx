import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PromotionsTab } from "../PromotionsTab";
import type { Promotion } from "@/lib/api/promotions";

// Tests for the FUT-020 PromotionsTab. Only usePromotionHistory is
// consumed, so the mock surface is tiny.

let mockData: Promotion[] | undefined = [];
let mockLoading = false;
let mockError = false;

vi.mock("@/lib/api/promotions", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/promotions")>(
      "@/lib/api/promotions",
    );
  return {
    ...actual,
    usePromotionHistory: () => ({
      data: mockData,
      isLoading: mockLoading,
      isError: mockError,
      refetch: vi.fn(),
    }),
  };
});

// The formatRelativeDate helper is deterministic per input; we don't need
// to mock it — the tests assert on the source / destination cells instead.

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PromotionsTab org="acme" repo="api" />
    </QueryClientProvider>,
  );
}

const promFixture = (i: number): Promotion => ({
  id: `prom-${i}`,
  src_org: "acme",
  src_repo: "api",
  src_tag: `v1.${i}.0`,
  src_digest: "sha256:aaa",
  dst_org: "acme",
  dst_repo: "api",
  dst_tag: i === 1 ? "prod" : "staging",
  dst_digest: "sha256:aaa",
  actor_user_id: i === 1 ? "6d16d55e-4f30-4b12-9d1a-4a1c9c1c1234" : "",
  note: i === 1 ? "release" : "",
  promoted_at: "2026-07-01T00:00:00Z",
});

describe("PromotionsTab", () => {
  beforeEach(() => {
    mockData = [];
    mockLoading = false;
    mockError = false;
  });

  it("renders the empty state when there is no history", () => {
    renderTab();
    expect(screen.getByText(/no promotions yet/i)).toBeInTheDocument();
    // Empty-state copy mentions the repo path so operators know what they
    // are looking at.
    expect(screen.getByText(/acme\/api/i)).toBeInTheDocument();
  });

  it("renders the error state when the fetch errored", () => {
    mockError = true;
    renderTab();
    expect(screen.getByText(/couldn't load promotions/i)).toBeInTheDocument();
  });

  it("renders one row per promotion when data is populated", () => {
    mockData = [promFixture(1), promFixture(2)];
    renderTab();
    // Both destination tags are rendered as monospace text cells.
    expect(screen.getByText(/acme\/api:prod/)).toBeInTheDocument();
    expect(screen.getByText(/acme\/api:staging/)).toBeInTheDocument();
    // The note column shows the value on the row with a note.
    expect(screen.getByText(/^release$/)).toBeInTheDocument();
    // The row without an actor renders the automated fallback.
    expect(screen.getByText(/^automated$/i)).toBeInTheDocument();
  });

  it("renders a skeleton placeholder while loading", () => {
    mockLoading = true;
    mockData = undefined;
    renderTab();
    // A skeleton row set is present — three placeholder rows means at
    // least three skeleton spans should appear.
    const skeletons = document.querySelectorAll("[data-slot=skeleton], .h-3");
    // Sanity — at least one skeleton element rendered.
    expect(skeletons.length).toBeGreaterThan(0);
  });
});
