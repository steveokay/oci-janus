import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PRNamespacesList } from "../pr-namespaces-list";
import type { PRNamespacesResponse } from "@/lib/api/pr-registry";

// Tests for the FUT-023 PRNamespacesList. The list hook + admin gate are mocked
// so no network is involved.

let mockIsAdmin = true;
let mockData: PRNamespacesResponse | undefined;
let mockIsLoading = false;
let mockIsError = false;

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => mockIsAdmin,
}));

vi.mock("@/lib/api/pr-registry", () => ({
  usePRNamespaces: () => ({
    data: mockData,
    isLoading: mockIsLoading,
    isError: mockIsError,
    refetch: vi.fn(),
  }),
}));

function renderList() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PRNamespacesList />
    </QueryClientProvider>,
  );
}

describe("PRNamespacesList", () => {
  beforeEach(() => {
    mockIsAdmin = true;
    mockData = { namespaces: [], next_page_token: "" };
    mockIsLoading = false;
    mockIsError = false;
  });

  it("renders nothing for non-admins", () => {
    mockIsAdmin = false;
    const { container } = renderList();
    expect(container).toBeEmptyDOMElement();
  });

  it("renders the empty state when there are no active namespaces", () => {
    renderList();
    expect(screen.getByText(/no active pr namespaces/i)).toBeInTheDocument();
  });

  it("renders a row per active namespace", () => {
    mockData = {
      namespaces: [
        {
          provider: "github",
          source_repo: "acme/backend",
          pr_number: 42,
          org_name: "pr-backend-42",
          status: "active",
          created_at: "2026-07-10T12:00:00Z",
        },
      ],
      next_page_token: "",
    };
    renderList();
    expect(screen.getByText("acme/backend")).toBeInTheDocument();
    expect(screen.getByText("pr-backend-42")).toBeInTheDocument();
    expect(screen.getByText("#42")).toBeInTheDocument();
  });
});
