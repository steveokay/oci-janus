import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { SigningCoverageBar } from "../signing-coverage-bar";

describe("SigningCoverageBar", () => {
  it("renders the signed/total label and an accessible percentage", () => {
    render(<SigningCoverageBar pct={0.95} signed={38} total={40} />);
    expect(screen.getByText("38/40")).toBeInTheDocument();
    expect(screen.getByRole("img")).toHaveAttribute(
      "aria-label",
      "95% signed (38 of 40 tags)",
    );
  });

  it("uses the danger tone below 50% coverage", () => {
    render(<SigningCoverageBar pct={0.2} signed={1} total={5} />);
    const fill = document.querySelector("span.bg-\\[var\\(--color-danger\\)\\]");
    expect(fill).not.toBeNull();
  });
});
