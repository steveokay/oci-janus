import { describe, test, expect } from "vitest";
import { lockoutMessage } from "../lockout-message";

// Pins the scoped PENTEST-005 reversal on the FE side: only a 423
// ACCOUNT_LOCKED response yields a lockout message; every other failure
// returns null (the login page then shows the generic no-leak error).
describe("lockoutMessage", () => {
  function axiosErr(status: number, errors?: unknown[]) {
    return { response: { status, data: { errors } } };
  }

  test("423 ACCOUNT_LOCKED with minutes → rounded-up minutes message", () => {
    const msg = lockoutMessage(
      axiosErr(423, [{ code: "ACCOUNT_LOCKED", retry_after_seconds: 130 }]),
    );
    expect(msg).toContain("temporarily locked");
    expect(msg).toContain("~3 minutes"); // ceil(130/60) = 3
  });

  test("423 with a single minute → singular 'minute'", () => {
    const msg = lockoutMessage(
      axiosErr(423, [{ code: "ACCOUNT_LOCKED", retry_after_seconds: 60 }]),
    );
    expect(msg).toContain("~1 minute.");
    expect(msg).not.toContain("minutes");
  });

  test("423 with sub-minute → seconds message", () => {
    const msg = lockoutMessage(
      axiosErr(423, [{ code: "ACCOUNT_LOCKED", retry_after_seconds: 30 }]),
    );
    expect(msg).toContain("~30 seconds");
  });

  test("401 invalid credentials → null (stays generic, no leak)", () => {
    expect(lockoutMessage(axiosErr(401))).toBeNull();
  });

  test("423 without the ACCOUNT_LOCKED code → null", () => {
    expect(lockoutMessage(axiosErr(423, [{ code: "SOMETHING_ELSE" }]))).toBeNull();
  });

  test("non-axios / undefined error → null", () => {
    expect(lockoutMessage(new Error("network"))).toBeNull();
    expect(lockoutMessage(undefined)).toBeNull();
  });
});
