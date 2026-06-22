import { render, screen } from "@testing-library/react";
import { describe, test, expect } from "vitest";
import { PreviewBanner } from "../PreviewBanner";

// PreviewBanner is a pure presentational component — no router or auth context
// required. Tests focus on the accessibility contract (role="status" +
// aria-live="polite") and that the sprint label + future ID are rendered.

describe("PreviewBanner", () => {
  test("exposes role=status with aria-live=polite", () => {
    render(<PreviewBanner sprint="Sprint 11" futureID="FUT-001" />);

    // The element must have role="status" so assistive technology treats it as
    // a live region. aria-live="polite" means announcements happen at the next
    // opportunity, not interrupting current focus.
    const banner = screen.getByRole("status");
    expect(banner).toHaveAttribute("aria-live", "polite");
  });

  test("renders the sprint label in the banner", () => {
    render(<PreviewBanner sprint="Sprint 11" futureID="FUT-001" />);
    const banner = screen.getByRole("status");
    expect(banner).toHaveTextContent("Sprint 11");
  });

  test("renders the future ID in the banner", () => {
    render(<PreviewBanner sprint="Sprint 11" futureID="FUT-001" />);
    const banner = screen.getByRole("status");
    expect(banner).toHaveTextContent("FUT-001");
  });

  test("renders sprint and futureID together in a single status region", () => {
    render(<PreviewBanner sprint="Sprint 12" futureID="FUT-099" />);
    const banner = screen.getByRole("status");
    expect(banner).toHaveAttribute("aria-live", "polite");
    expect(banner).toHaveTextContent("Sprint 12");
    expect(banner).toHaveTextContent("FUT-099");
  });
});
