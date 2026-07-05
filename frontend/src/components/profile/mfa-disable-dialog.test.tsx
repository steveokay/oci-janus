import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MfaDisableDialog } from "./mfa-disable-dialog";

// Beacon — MfaDisableDialog tests.
//
// The dialog drives the DELETE /mfa call through useMfaDisable. We mock the
// hooks module so no network is hit, and mock sonner so we can assert which
// toast fires on each path (success vs. 401 re-auth failure).

const disableMutate = vi.fn();

vi.mock("@/lib/api/mfa", () => ({
  useMfaDisable: () => ({ mutateAsync: disableMutate }),
}));

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: { success: (...a: unknown[]) => toastSuccess(...a), error: (...a: unknown[]) => toastError(...a) },
}));

beforeEach(() => {
  vi.clearAllMocks();
});

describe("MfaDisableDialog", () => {
  it("submits with just a password and disables on success", async () => {
    disableMutate.mockResolvedValueOnce(undefined);
    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    render(<MfaDisableDialog open onOpenChange={onOpenChange} />);

    await user.type(screen.getByLabelText("Password"), "hunter2Password!");
    await user.click(
      screen.getByRole("button", { name: /disable two-factor/i }),
    );

    await waitFor(() => {
      expect(disableMutate).toHaveBeenCalledWith({
        password: "hunter2Password!",
      });
    });
    expect(toastSuccess).toHaveBeenCalledWith(
      "Two-factor authentication disabled",
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("surfaces a re-auth-failed toast on a 401 rejection", async () => {
    disableMutate.mockRejectedValueOnce({ response: { status: 401 } });
    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    render(<MfaDisableDialog open onOpenChange={onOpenChange} />);

    await user.type(screen.getByLabelText("Password"), "wrong-password");
    await user.click(
      screen.getByRole("button", { name: /disable two-factor/i }),
    );

    await waitFor(() => {
      expect(toastError).toHaveBeenCalledWith(
        "Re-authentication failed. Check your password or code.",
      );
    });
    // The dialog stays open so the user can retry with corrected input.
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("rejects an empty submission without calling the mutation", async () => {
    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    render(<MfaDisableDialog open onOpenChange={onOpenChange} />);

    await user.click(
      screen.getByRole("button", { name: /disable two-factor/i }),
    );

    await waitFor(() => {
      expect(toastError).toHaveBeenCalledWith(
        "Enter your password or a current code.",
      );
    });
    expect(disableMutate).not.toHaveBeenCalled();
  });
});
