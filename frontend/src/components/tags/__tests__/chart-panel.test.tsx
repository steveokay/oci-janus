import { describe, it, expect, vi } from "vitest";
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
  });
});
