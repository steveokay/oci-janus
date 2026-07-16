import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import type { SigningCoverageSummary as Summary } from "@/lib/api/signing-coverage";
import { SigningCoverageSummary } from "../signing-coverage-summary";

// SigningCoverageSummary is a pure props component (no router), so a plain
// render suffices. These tests lock in the feature's headline "soft spot"
// signal: the "Enforced w/ empty allowlist" card must carry the warning tone
// only when there is at least one such repo.
function base(overrides: Partial<Summary> = {}): Summary {
  return {
    repo_count: 10,
    repos_require_signature: 6,
    repos_enforced_empty_allowlist: 0,
    workspace_signed_tag_pct: 0.95,
    ...overrides,
  };
}

describe("SigningCoverageSummary", () => {
  it("marks the empty-allowlist card with the warning tone when > 0", () => {
    render(
      <SigningCoverageSummary
        summary={base({ repos_enforced_empty_allowlist: 2 })}
      />,
    );
    const value = screen.getByText("2");
    expect(value).toBeInTheDocument();
    // The soft-spot card renders in warning tone — assert the token class is
    // present on the value node.
    expect(value.className).toContain("text-[var(--color-warning)]");
  });

  it("uses the default tone when no repo has an empty allowlist", () => {
    render(
      <SigningCoverageSummary
        summary={base({ repos_enforced_empty_allowlist: 0 })}
      />,
    );
    // The empty-allowlist card renders "0" and must NOT carry the warning tone.
    const value = screen.getByText("0");
    expect(value).toBeInTheDocument();
    expect(value.className).not.toContain("text-[var(--color-warning)]");
  });

  it("renders the workspace coverage as a rounded percentage", () => {
    render(<SigningCoverageSummary summary={base({ workspace_signed_tag_pct: 0.95 })} />);
    expect(screen.getByText("95%")).toBeInTheDocument();
  });
});
