import { describe, test, expect } from "vitest";
import { expiryUrgency, EXPIRY_SOON_DAYS } from "../expiry-badge";

// Threshold logic for the API-key expiry urgency badge. Boundary behaviour is
// easy to get wrong, so we pin each tier + the exact cutoffs against a fixed
// `now` (injected so the test isn't clock-dependent).
describe("expiryUrgency", () => {
  // Fixed reference instant: 2026-07-04T00:00:00Z.
  const now = Date.parse("2026-07-04T00:00:00Z");
  const DAY = 24 * 60 * 60 * 1000;

  test("returns 'none' for absent expiry", () => {
    expect(expiryUrgency(null, now)).toBe("none");
    expect(expiryUrgency(undefined, now)).toBe("none");
  });

  test("returns 'none' for an unparseable timestamp", () => {
    // A garbage string must not masquerade as expired.
    expect(expiryUrgency("not-a-date", now)).toBe("none");
  });

  test("returns 'expired' when the timestamp is in the past", () => {
    expect(expiryUrgency(new Date(now - DAY).toISOString(), now)).toBe(
      "expired",
    );
  });

  test("treats exactly-now as expired (boundary is <= now)", () => {
    expect(expiryUrgency(new Date(now).toISOString(), now)).toBe("expired");
  });

  test("returns 'soon' inside the 14-day window", () => {
    // 3 days out is well within the soon window.
    expect(expiryUrgency(new Date(now + 3 * DAY).toISOString(), now)).toBe(
      "soon",
    );
    // Just under the cutoff is still soon.
    expect(
      expiryUrgency(new Date(now + EXPIRY_SOON_DAYS * DAY - 1000).toISOString(), now),
    ).toBe("soon");
  });

  test("exactly EXPIRY_SOON_DAYS out is 'ok' (half-open window)", () => {
    expect(
      expiryUrgency(new Date(now + EXPIRY_SOON_DAYS * DAY).toISOString(), now),
    ).toBe("ok");
  });

  test("returns 'ok' for a far-future expiry", () => {
    expect(expiryUrgency(new Date(now + 90 * DAY).toISOString(), now)).toBe(
      "ok",
    );
  });
});
