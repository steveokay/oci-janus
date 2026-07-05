import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AxiosError } from "axios";
import { MfaChallenge } from "./mfa-challenge";

// Beacon — MfaChallenge tests.
//
// The challenge form drives the OTP step through loginMfa. We mock the auth
// API so no network is hit and assert: a valid TOTP calls loginMfa + onDone;
// a 401 surfaces an error toast and does NOT call onDone; and the backup-code
// toggle relaxes validation (a non-6-digit value) while still submitting.

const loginMfa = vi.fn();
vi.mock("@/lib/api/auth", () => ({
  loginMfa: (challengeToken: string, code: string) =>
    loginMfa(challengeToken, code),
}));

const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: (msg: string) => toastError(msg) },
}));

const CHALLENGE = "challenge-token-abc";

beforeEach(() => {
  vi.clearAllMocks();
  loginMfa.mockResolvedValue(undefined);
});

describe("MfaChallenge", () => {
  it("submits a valid 6-digit code then calls loginMfa + onDone", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    render(<MfaChallenge challengeToken={CHALLENGE} onDone={onDone} />);

    await user.type(screen.getByLabelText("Authentication code"), "123456");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    await waitFor(() => {
      expect(loginMfa).toHaveBeenCalledWith(CHALLENGE, "123456");
    });
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(toastError).not.toHaveBeenCalled();
  });

  it("shows an error toast and does not call onDone on a 401", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    // Reject with a real AxiosError carrying a 401 response so the component's
    // extractErrorMeta status mapping fires.
    loginMfa.mockRejectedValueOnce(
      new AxiosError("Unauthorized", "401", undefined, undefined, {
        status: 401,
        statusText: "Unauthorized",
        data: {},
        headers: {},
        // Minimal config to satisfy the AxiosResponse shape.
        config: {} as never,
      }),
    );
    render(<MfaChallenge challengeToken={CHALLENGE} onDone={onDone} />);

    await user.type(screen.getByLabelText("Authentication code"), "000000");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    await waitFor(() => {
      expect(toastError).toHaveBeenCalledWith("Invalid code. Try again.");
    });
    expect(onDone).not.toHaveBeenCalled();
  });

  it("backup-code toggle relaxes validation and still submits", async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    render(<MfaChallenge challengeToken={CHALLENGE} onDone={onDone} />);

    // Switch to backup-code mode — the field relabels and validation relaxes.
    await user.click(
      screen.getByRole("button", { name: /use a backup code instead/i }),
    );

    // A backup code is not 6 digits; under the TOTP schema this would fail
    // validation, but backup mode accepts it.
    await user.type(screen.getByLabelText("Backup code"), "AAAA-1111");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    await waitFor(() => {
      expect(loginMfa).toHaveBeenCalledWith(CHALLENGE, "AAAA-1111");
    });
    expect(onDone).toHaveBeenCalledTimes(1);
  });
});
