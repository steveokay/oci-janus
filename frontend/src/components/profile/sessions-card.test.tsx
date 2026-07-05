import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionsCard } from "./sessions-card";

// Mutable session list the mocked useSessions reads from — tests reassign it
// before render to exercise the paginated path.
let sessionData = [] as Array<{
  sid: string;
  device_label: string;
  user_agent: string;
  ip: string;
  created_at: string;
  last_active_at: string;
  current: boolean;
}>;

// Beacon — SessionsCard tests.
//
// The card reads useSessions and drives revoke / revoke-others mutations. We
// mock the hooks module so no network is hit, mock sonner for toast asserts,
// and mock @tanstack/react-router's useNavigate so we can assert the
// self-logout redirect fires only for the current session.

const revokeMutate = vi.fn();
const revokeOthersMutate = vi.fn();

// Loaded session fixture: one current device + one other device.
const sessions = [
  {
    sid: "sess-current",
    device_label: "Chrome on macOS",
    user_agent: "Mozilla/5.0 (Macintosh)",
    ip: "10.0.0.1",
    created_at: "2026-07-01T10:00:00Z",
    last_active_at: "2026-07-05T09:00:00Z",
    current: true,
  },
  {
    sid: "sess-other",
    device_label: "Firefox on Linux",
    user_agent: "Mozilla/5.0 (X11)",
    ip: "10.0.0.2",
    created_at: "2026-06-20T08:00:00Z",
    last_active_at: "2026-07-04T12:00:00Z",
    current: false,
  },
];

vi.mock("@/lib/api/sessions", () => ({
  useSessions: () => ({
    data: sessionData,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  }),
  useRevokeSession: () => ({ mutateAsync: revokeMutate }),
  useRevokeOtherSessions: () => ({ mutateAsync: revokeOthersMutate }),
}));

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: {
    success: (...a: unknown[]) => toastSuccess(...a),
    error: (...a: unknown[]) => toastError(...a),
  },
}));

const navigateMock = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}));

beforeEach(() => {
  vi.clearAllMocks();
  // Default every test to the two-session fixture; the pagination test
  // overrides this with a longer list before rendering.
  sessionData = sessions;
});

describe("SessionsCard", () => {
  it("lists sessions and flags the current device", () => {
    render(<SessionsCard />);

    // Both device labels are visible.
    expect(screen.getByText("Chrome on macOS")).toBeInTheDocument();
    expect(screen.getByText("Firefox on Linux")).toBeInTheDocument();
    // The current session is badged.
    expect(screen.getByText("This device")).toBeInTheDocument();
  });

  it("revokes a non-current session by sid and does NOT log out", async () => {
    revokeMutate.mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<SessionsCard />);

    // The non-current row's action button reads "Revoke".
    await user.click(screen.getByRole("button", { name: "Revoke" }));

    // A confirm dialog appears; scope the confirm click to the dialog so the
    // row button (also "Revoke") doesn't create selector ambiguity.
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Revoke" }));

    await waitFor(() => {
      expect(revokeMutate).toHaveBeenCalledWith("sess-other");
    });
    // Non-current revoke is not a self-logout — no navigation.
    expect(navigateMock).not.toHaveBeenCalled();
    expect(toastSuccess).toHaveBeenCalledWith("Session revoked.");
  });

  it("paginates when there are more sessions than one page", async () => {
    // 7 sessions → 2 pages at a page size of 5. Row 0 is the current device.
    sessionData = Array.from({ length: 7 }, (_, i) => ({
      sid: `sess-${i}`,
      device_label: `Device ${i}`,
      user_agent: `UA ${i}`,
      ip: `10.0.0.${i}`,
      created_at: "2026-06-20T08:00:00Z",
      last_active_at: "2026-07-04T12:00:00Z",
      current: i === 0,
    }));
    const user = userEvent.setup();
    render(<SessionsCard />);

    // Page 1 shows the first five rows; the 6th is not rendered yet.
    expect(screen.getByText("Device 0")).toBeInTheDocument();
    expect(screen.getByText("Device 4")).toBeInTheDocument();
    expect(screen.queryByText("Device 5")).not.toBeInTheDocument();
    expect(screen.getByText("Showing 1–5 of 7")).toBeInTheDocument();
    // Previous is disabled on the first page.
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();

    // Advance to page 2 — the remaining two rows appear, page-1 rows drop out.
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Device 5")).toBeInTheDocument();
    expect(screen.getByText("Device 6")).toBeInTheDocument();
    expect(screen.queryByText("Device 0")).not.toBeInTheDocument();
    expect(screen.getByText("Showing 6–7 of 7")).toBeInTheDocument();
    // Next is now disabled on the last page.
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  it("shows no pager when sessions fit on one page", () => {
    // The default two-session fixture is under the page size.
    render(<SessionsCard />);
    expect(
      screen.queryByRole("button", { name: "Next" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/Showing/)).not.toBeInTheDocument();
  });
});
