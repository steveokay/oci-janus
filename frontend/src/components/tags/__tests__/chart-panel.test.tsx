import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChartPanel } from "../chart-panel";
import * as api from "@/lib/api/chart";

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ChartPanel org="acme" repo="web" tag="1.0.0" active />
    </QueryClientProvider>,
  );
}

describe("ChartPanel", () => {
  beforeEach(() => {
    vi.spyOn(api, "useDownloadChart").mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof api.useDownloadChart>);
  });

  it("renders metadata + values", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: {
          name: "web",
          version: "1.0.0",
          app_version: "2.0.0",
          description: "the web chart",
          dependencies: [{ name: "pg", version: "12.x", repository: "oci://r" }],
        },
        values: "replicaCount: 1\n",
        values_truncated: false,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText("web")).toBeInTheDocument();
    expect(screen.getByText(/the web chart/)).toBeInTheDocument();
    expect(screen.getByText(/replicaCount/)).toBeInTheDocument();
    expect(screen.getByText("pg")).toBeInTheDocument();
  });

  it("shows a truncation banner when values_truncated", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: { name: "web", version: "1.0.0" },
        values: "a: 1\n",
        values_truncated: true,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText(/truncated/i)).toBeInTheDocument();
  });

  it("renders an empty state when chart detail is not enabled (null)", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: null,
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText(/not (available|enabled)/i)).toBeInTheDocument();
    // Guard against a regression that moves the download button above the
    // early return: it must be absent in the not-enabled (null) state.
    expect(screen.queryByRole("button", { name: /download/i })).toBeNull();
  });

  it("renders a skeleton while loading", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    const { container } = renderPanel();
    // The metadata heading must be absent and skeleton placeholders present.
    expect(screen.queryByText("web")).toBeNull();
    expect(container.querySelector(".skeleton-shimmer")).not.toBeNull();
    // The download button must not render while chart detail is loading.
    expect(screen.queryByRole("button", { name: /download/i })).toBeNull();
  });

  it("renders an error state with a retry affordance", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /retry/i }),
    ).toBeInTheDocument();
    // The download button must not render in the error state.
    expect(screen.queryByRole("button", { name: /download/i })).toBeNull();
  });

  it("renders maintainers", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: {
          name: "web",
          version: "1.0.0",
          maintainers: [{ name: "Ada", email: "a@x.io" }],
        },
        values: "",
        values_truncated: false,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText("Ada")).toBeInTheDocument();
  });

  it("never renders a javascript: home URL as a link", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: {
          name: "web",
          version: "1.0.0",
          home: "javascript:alert(1)",
        },
        values: "",
        values_truncated: false,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    // The unsafe home URL renders as plain text, not an anchor.
    expect(screen.queryByRole("link", { name: /home/i })).toBeNull();
    expect(screen.getByText("Home")).toBeInTheDocument();
  });

  it("renders a download button that triggers the download mutation", () => {
    const mutate = vi.fn();
    vi.spyOn(api, "useDownloadChart").mockReturnValue({
      mutate,
      isPending: false,
    } as unknown as ReturnType<typeof api.useDownloadChart>);
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: { name: "web", version: "1.0.0" },
        values: "a: 1\n",
        values_truncated: false,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    const btn = screen.getByRole("button", { name: /download/i });
    btn.click();
    expect(mutate).toHaveBeenCalledWith(
      { org: "acme", repo: "web", tag: "1.0.0" },
      expect.anything(),
    );
  });
});
