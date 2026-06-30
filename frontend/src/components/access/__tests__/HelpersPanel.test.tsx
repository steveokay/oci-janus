import * as React from "react";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HelpersPanel } from "../HelpersPanel";

// renderWithClient — wraps the panel in a fresh QueryClient so each test
// starts with an empty cache. retry: false keeps fetch failures from
// looping during the test.
function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("HelpersPanel", () => {
  it("renders the heading + does NOT render the amber preview banner", () => {
    renderWithClient(<HelpersPanel />);
    expect(
      screen.getByRole("heading", { name: /credential helpers/i }),
    ).toBeInTheDocument();
    // The preview banner is gone now that the surface is live.
    expect(
      screen.queryByText(/Sprint 11.*FUT-002/i),
    ).not.toBeInTheDocument();
  });

  it("renders a loading state while data fetches", () => {
    renderWithClient(<HelpersPanel />);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });
});
