import { describe, test, expect, beforeEach } from "vitest";
import { prepareSSOLogin, consumeSSOReturnTo } from "../sso";

// FUT-084 — SSO login handoff helpers.
//
// prepareSSOLogin stashes where to land after the round-trip (sessionStorage
// survives the full-page redirect to the IdP and back) and returns the auth
// service's /start URL to navigate to. consumeSSOReturnTo reads + clears that
// stash on the callback return. Both apply an open-redirect guard so a
// tampered value can only ever be an internal path.

beforeEach(() => {
  sessionStorage.clear();
});

describe("prepareSSOLogin", () => {
  test("returns the /start URL with next=/login and stashes the return path", () => {
    const url = prepareSSOLogin("/auth/oauth/google/start", "/repositories");
    expect(url).toBe("/auth/oauth/google/start?next=%2Flogin");
    expect(sessionStorage.getItem("sso_return_to")).toBe("/repositories");
  });

  test("sanitises a protocol-relative return path to /", () => {
    prepareSSOLogin("/auth/oauth/github/start", "//evil.com");
    expect(sessionStorage.getItem("sso_return_to")).toBe("/");
  });

  test("an undefined return path stashes /", () => {
    prepareSSOLogin("/auth/saml/okta/start", undefined);
    expect(sessionStorage.getItem("sso_return_to")).toBe("/");
  });
});

describe("consumeSSOReturnTo", () => {
  test("returns the stashed path and clears it", () => {
    sessionStorage.setItem("sso_return_to", "/repositories/dev");
    expect(consumeSSOReturnTo()).toBe("/repositories/dev");
    expect(sessionStorage.getItem("sso_return_to")).toBeNull();
  });

  test("defaults to / when nothing is stashed", () => {
    expect(consumeSSOReturnTo()).toBe("/");
  });

  test("sanitises an unsafe stashed value to /", () => {
    sessionStorage.setItem("sso_return_to", "https://evil.com");
    expect(consumeSSOReturnTo()).toBe("/");
  });
});
