import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";

// FUT-017 — UpstreamPoliciesCard contract tests.
//
// Strategy: stub the four policy hooks the card consumes so we can drive
// it through its three visibility states (hidden / partial / live) plus
// the debounced auto-save + the auto_sign-without-key disable rule. The
// underlying mutation hooks expose just `mutateAsync` + `isPending` so
// the tests can assert on PUT body content without spinning up a QueryClient.

const scanMutateAsync = vi.fn().mockResolvedValue({});
const signMutateAsync = vi.fn().mockResolvedValue({});

let scanListResult: {
  data: unknown;
  isLoading: boolean;
};
let signListResult: {
  data: unknown;
  isLoading: boolean;
};

vi.mock("@/lib/api/proxy-cache", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/proxy-cache")>(
    "@/lib/api/proxy-cache",
  );
  return {
    ...actual,
    useProxyCacheScanPolicies: () => scanListResult,
    useProxyCacheSignPolicies: () => signListResult,
    useUpdateProxyCacheScanPolicy: () => ({
      mutateAsync: scanMutateAsync,
      isPending: false,
    }),
    useUpdateProxyCacheSignPolicy: () => ({
      mutateAsync: signMutateAsync,
      isPending: false,
    }),
  };
});

// Sonner emits toasts on save success/failure. jsdom doesn't render the
// portal target; stub the surface to keep the test focused.
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

import { UpstreamPoliciesCard } from "../upstream-policies-card";

describe("UpstreamPoliciesCard — visibility gating", () => {
  beforeEach(() => {
    scanMutateAsync.mockClear();
    signMutateAsync.mockClear();
  });

  test("renders nothing when both list hooks return null", () => {
    scanListResult = { data: null, isLoading: false };
    signListResult = { data: null, isLoading: false };

    const { container } = render(
      <UpstreamPoliciesCard upstreamNames={["dockerhub"]} />,
    );
    // null return ⇒ no DOM. Asserting on the rendered output (not the
    // component identity) keeps the test useful even if the card grows a
    // wrapping fragment later.
    expect(container.firstChild).toBeNull();
  });

  test("renders the card with scan-unavailable hint when only scan list is null", () => {
    scanListResult = { data: null, isLoading: false };
    signListResult = { data: [], isLoading: false };

    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    // Card chrome renders.
    expect(screen.getByText(/Cache policies/i)).toBeInTheDocument();
    // The row shows the scanner-not-wired hint.
    expect(
      screen.getByText(/scanner not wired/i),
    ).toBeInTheDocument();
    // Auto-scan switch is disabled.
    const autoScanSwitch = screen.getByLabelText(/Auto-scan for dockerhub/i);
    expect(autoScanSwitch).toBeDisabled();
  });

  test("renders the card with sign-unavailable hint when only sign list is null", () => {
    scanListResult = { data: [], isLoading: false };
    signListResult = { data: null, isLoading: false };

    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    expect(screen.getByText(/signer not wired/i)).toBeInTheDocument();
    const autoSignSwitch = screen.getByLabelText(/Auto-sign for dockerhub/i);
    expect(autoSignSwitch).toBeDisabled();
  });
});

describe("UpstreamPoliciesCard — row rendering", () => {
  beforeEach(() => {
    scanMutateAsync.mockClear();
    signMutateAsync.mockClear();
  });

  test("renders one row per unique upstream from the manifest list", () => {
    scanListResult = { data: [], isLoading: false };
    signListResult = { data: [], isLoading: false };

    render(
      <UpstreamPoliciesCard upstreamNames={["dockerhub", "gcr", "dockerhub"]} />,
    );

    // Three names in, two unique rows out. Asserting on the testid that
    // the card stamps onto each row catches both dedupe + ordering bugs.
    expect(screen.getByTestId("upstream-row-dockerhub")).toBeInTheDocument();
    expect(screen.getByTestId("upstream-row-gcr")).toBeInTheDocument();
    expect(
      screen.queryAllByTestId(/^upstream-row-/),
    ).toHaveLength(2);
  });

  test("unions cache-row upstreams with server-side policy rows", () => {
    scanListResult = {
      data: [
        {
          upstream_name: "quay",
          auto_scan: true,
          severity_threshold: "high",
        },
      ],
      isLoading: false,
    };
    signListResult = { data: [], isLoading: false };

    // Operator-visible cache rows surface dockerhub; the server-side
    // policy table also has a "quay" entry from a prior session. Both
    // should render so a policy isn't orphaned just because nothing has
    // been pulled through quay yet in this session.
    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    expect(screen.getByTestId("upstream-row-dockerhub")).toBeInTheDocument();
    expect(screen.getByTestId("upstream-row-quay")).toBeInTheDocument();
  });
});

describe("UpstreamPoliciesCard — save behaviour", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    scanMutateAsync.mockClear();
    signMutateAsync.mockClear();
    scanListResult = { data: [], isLoading: false };
    signListResult = { data: [], isLoading: false };
  });

  test("disables Save when auto_sign is on but key_id is empty", () => {
    vi.useRealTimers();
    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    const autoSignSwitch = screen.getByLabelText(/Auto-sign for dockerhub/i);
    fireEvent.click(autoSignSwitch);

    // The Save button is gated on canSave = dirty && !signInvalid.
    // auto_sign=true + empty key_id ⇒ signInvalid ⇒ button stays disabled
    // even though the local state changed.
    const saveBtn = screen.getByTestId("save-dockerhub");
    expect(saveBtn).toBeDisabled();
  });

  test("enables Save once a key_id is provided alongside auto_sign", () => {
    vi.useRealTimers();
    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    fireEvent.click(screen.getByLabelText(/Auto-sign for dockerhub/i));
    const keyInput = screen.getByLabelText(/Signing key for dockerhub/i);
    fireEvent.change(keyInput, { target: { value: "prod-key-1" } });

    const saveBtn = screen.getByTestId("save-dockerhub");
    expect(saveBtn).toBeEnabled();
  });

  test("debounces auto-save to 2s after the last edit", async () => {
    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    // Flip auto-scan on. With no manual Save click, the 2s timer should
    // be armed and fire after the timer advance.
    act(() => {
      fireEvent.click(screen.getByLabelText(/Auto-scan for dockerhub/i));
    });

    // Pre-debounce: no mutation yet.
    expect(scanMutateAsync).not.toHaveBeenCalled();

    // Advance just under the debounce — still no fire.
    await act(async () => {
      vi.advanceTimersByTime(1_500);
    });
    expect(scanMutateAsync).not.toHaveBeenCalled();

    // Cross the debounce threshold. Flush queued microtasks too so the
    // mutation invocation chained from the timer callback resolves
    // before we assert. (waitFor times out under fake timers because
    // its internal poll can't make wall-clock progress.)
    await act(async () => {
      vi.advanceTimersByTime(700);
      await Promise.resolve();
    });

    expect(scanMutateAsync).toHaveBeenCalledTimes(1);
    expect(scanMutateAsync).toHaveBeenCalledWith({
      auto_scan: true,
      severity_threshold: "",
    });

    vi.useRealTimers();
  });

  test("manual Save fires immediately and includes both scan + sign edits", async () => {
    vi.useRealTimers();
    render(<UpstreamPoliciesCard upstreamNames={["dockerhub"]} />);

    fireEvent.click(screen.getByLabelText(/Auto-scan for dockerhub/i));
    fireEvent.click(screen.getByLabelText(/Auto-sign for dockerhub/i));
    fireEvent.change(screen.getByLabelText(/Signing key for dockerhub/i), {
      target: { value: "prod-key-1" },
    });

    fireEvent.click(screen.getByTestId("save-dockerhub"));

    await waitFor(() => expect(scanMutateAsync).toHaveBeenCalledTimes(1));
    expect(signMutateAsync).toHaveBeenCalledWith({
      auto_sign: true,
      key_id: "prod-key-1",
    });
  });
});
