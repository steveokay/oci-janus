import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { DomainsTable } from "../domains-table";
import type { DomainEntry } from "@/lib/api/domains";

// ---------------------------------------------------------------------------
// DSGN-021 — row-expand affordance for unverified custom domains.
//
// The table pulls verify / promote / delete mutations from
// `@/lib/api/domains`; we replace the module with stubs whose `mutateAsync`
// can be controlled per-test. The Sonner toaster is also stubbed because
// jsdom doesn't render the portal target and the component fires toasts on
// successful verify.
// ---------------------------------------------------------------------------

const verifyMutateAsync = vi.fn();
const promoteMutateAsync = vi.fn();
const deleteMutateAsync = vi.fn();

vi.mock("@/lib/api/domains", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/domains")>(
    "@/lib/api/domains",
  );
  return {
    ...actual,
    useVerifyDomain: () => ({
      mutateAsync: verifyMutateAsync,
      isPending: false,
    }),
    usePromoteDomain: () => ({
      mutateAsync: promoteMutateAsync,
      isPending: false,
    }),
    useDeleteDomain: () => ({
      mutateAsync: deleteMutateAsync,
      isPending: false,
    }),
  };
});

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), {
    success: vi.fn(),
    error: vi.fn(),
    message: vi.fn(),
  }),
}));

// Stub the clipboard so the CopyButton flash transitions correctly without
// jsdom complaining about a missing `navigator.clipboard.writeText`.
const writeText = vi.fn().mockResolvedValue(undefined);
Object.defineProperty(navigator, "clipboard", {
  value: { writeText },
  writable: true,
});

const unverifiedDomain: DomainEntry = {
  domain: "janus.athelos.co",
  verified: false,
  is_primary: false,
  registered_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
  verified_at: null,
  next_poll_after: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
  notified_24h: false,
  notified_48h: false,
  verification_token: "janus-verify=abc123xyz",
  txt_record_name: "_registry-verify.janus.athelos.co",
};

const verifiedDomain: DomainEntry = {
  domain: "registry.acme.com",
  verified: true,
  is_primary: true,
  registered_at: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
  verified_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
  next_poll_after: null,
  notified_24h: false,
  notified_48h: false,
};

describe("DomainsTable — DSGN-021 row expander", () => {
  beforeEach(() => {
    verifyMutateAsync.mockReset();
    promoteMutateAsync.mockReset();
    deleteMutateAsync.mockReset();
    writeText.mockClear();
  });

  test("unverified row renders the expand chevron; verified row does not", async () => {
    await act(async () => {
      render(<DomainsTable domains={[unverifiedDomain, verifiedDomain]} />);
    });

    // Unverified row exposes the toggle (named via aria-label so we don't
    // hard-couple to icon DOM shape).
    const toggle = screen.getByRole("button", { name: /show txt challenge/i });
    expect(toggle).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    // Verified row should not — exactly one toggle in the table.
    expect(
      screen.getAllByRole("button", { name: /show txt challenge/i }),
    ).toHaveLength(1);
  });

  test("clicking the chevron reveals TXT name and TXT value", async () => {
    await act(async () => {
      render(<DomainsTable domains={[unverifiedDomain]} />);
    });

    const toggle = screen.getByRole("button", { name: /show txt challenge/i });

    // Pre-expand: panel content absent.
    expect(
      screen.queryByText("_registry-verify.janus.athelos.co"),
    ).not.toBeInTheDocument();

    await act(async () => {
      fireEvent.click(toggle);
    });

    // Post-expand: TXT name + value both render as <code>.
    expect(
      screen.getByText("_registry-verify.janus.athelos.co"),
    ).toBeInTheDocument();
    expect(screen.getByText("janus-verify=abc123xyz")).toBeInTheDocument();
    // Toggle now reports the inverse aria-label so screen readers see the
    // collapse affordance.
    expect(
      screen.getByRole("button", { name: /hide txt challenge/i }),
    ).toHaveAttribute("aria-expanded", "true");
  });

  test("copy button flashes 'Copied' then reverts after the timeout", async () => {
    await act(async () => {
      render(<DomainsTable domains={[unverifiedDomain]} />);
    });

    const toggle = screen.getByRole("button", { name: /show txt challenge/i });
    await act(async () => {
      fireEvent.click(toggle);
    });

    // Two copy buttons in the expanded panel (TXT name + TXT value). Both
    // start in the "Copy" label state; the icon-only variant uses an
    // aria-label that flips to "Copied" while the check icon is showing.
    const copyButtons = screen.getAllByRole("button", { name: /^copy$/i });
    expect(copyButtons.length).toBeGreaterThanOrEqual(2);

    await act(async () => {
      fireEvent.click(copyButtons[1]);
    });

    // writeText is awaited inside the handler. waitFor uses real timers so
    // the flash state surfaces before the 1.6s revert.
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith("janus-verify=abc123xyz");
    });
    await waitFor(() => {
      expect(
        screen.getAllByRole("button", { name: /copied/i }).length,
      ).toBeGreaterThan(0);
    });

    // Wait past the 1.6s revert window and confirm we're back to "Copy".
    // The sleep is wrapped in act() so the setState that flips Copied→Copy
    // (driven by the CopyButton's setTimeout) is flushed before our assertion.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 1_800));
    });
    expect(
      screen.queryAllByRole("button", { name: /copied/i }).length,
    ).toBe(0);
  }, 10_000);

  test("Check DNS now calls verify and stays open on still-pending", async () => {
    verifyMutateAsync.mockResolvedValueOnce({
      ...unverifiedDomain,
      verified: false,
    });
    await act(async () => {
      render(<DomainsTable domains={[unverifiedDomain]} />);
    });

    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /show txt challenge/i }),
      );
    });

    const checkBtn = screen.getByRole("button", { name: /check dns now/i });
    await act(async () => {
      fireEvent.click(checkBtn);
    });

    await waitFor(() => {
      expect(verifyMutateAsync).toHaveBeenCalledWith("janus.athelos.co");
    });
    // Panel must NOT auto-collapse — operator needs to keep re-reading the
    // value if the check failed.
    expect(
      screen.getByText("_registry-verify.janus.athelos.co"),
    ).toBeInTheDocument();
    // Inline "still pending" result text surfaces. The phrase lives inside
    // a single <span> with the timestamp as a sibling text fragment; we
    // match against direct text content rather than descendant aggregation
    // so the matcher returns a unique node (not every ancestor of <body>).
    await waitFor(() => {
      expect(
        screen.getByText(
          (content) => content.toLowerCase().includes("still pending"),
          { selector: "span" },
        ),
      ).toBeInTheDocument();
    });
  });
});
