import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MfaEnrollDialog } from "./mfa-enroll-dialog";

// Beacon — MfaEnrollDialog tests.
//
// The dialog drives its enrol/verify flow through the useMfa* hooks. We mock
// the hooks module so no network is hit: enroll resolves a fixed secret +
// otpauth URI, verify resolves a fixed set of backup codes. That lets us
// assert the step machine (scan → verify → codes) end to end.

const enrollMutate = vi.fn();
const verifyMutate = vi.fn();

vi.mock("@/lib/api/mfa", () => ({
  useMfaEnroll: () => ({ mutateAsync: enrollMutate }),
  useMfaVerify: () => ({ mutateAsync: verifyMutate }),
}));

// Silence sonner — the component fires toasts on error paths we don't assert.
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const ENROLL = {
  secret_base32: "JBSWY3DPEHPK3PXP",
  otpauth_uri:
    "otpauth://totp/oci-janus:alice?secret=JBSWY3DPEHPK3PXP&issuer=oci-janus",
};
const BACKUP = ["AAAA-1111", "BBBB-2222", "CCCC-3333", "DDDD-4444"];

beforeEach(() => {
  vi.clearAllMocks();
  enrollMutate.mockResolvedValue(ENROLL);
  verifyMutate.mockResolvedValue({ backup_codes: BACKUP });
});

describe("MfaEnrollDialog", () => {
  it("renders the QR code and manual secret on open (scan step)", async () => {
    render(<MfaEnrollDialog open onOpenChange={() => {}} />);

    // Enrolment fires in an effect on open — wait for the secret to land.
    await waitFor(() => {
      expect(screen.getByText(ENROLL.secret_base32)).toBeInTheDocument();
    });

    // qrcode.react renders an <svg>; it carries the accessible title we set.
    expect(screen.getByTitle("TOTP enrolment QR code")).toBeInTheDocument();
    expect(enrollMutate).toHaveBeenCalledTimes(1);
  });

  it("verifies a code then shows backup codes + confirm (verify → codes)", async () => {
    const user = userEvent.setup();
    render(<MfaEnrollDialog open onOpenChange={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText(ENROLL.secret_base32)).toBeInTheDocument();
    });

    // scan → verify
    await user.click(screen.getByRole("button", { name: "Next" }));

    // Enter the 6-digit code and submit.
    await user.type(
      screen.getByLabelText("Authentication code"),
      "123456",
    );
    await user.click(
      screen.getByRole("button", { name: /verify & enable/i }),
    );

    // Verify mutation called with the typed code.
    await waitFor(() => {
      expect(verifyMutate).toHaveBeenCalledWith("123456");
    });

    // codes step — every backup code renders plus the confirm affordance.
    for (const code of BACKUP) {
      expect(screen.getByText(code)).toBeInTheDocument();
    }
    expect(
      screen.getByRole("button", { name: /i've saved these codes/i }),
    ).toBeInTheDocument();
  });
});
