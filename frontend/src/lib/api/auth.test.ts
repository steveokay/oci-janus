import { describe, it, expect, vi, beforeEach } from "vitest";

// Beacon — auth.ts login-branch tests.
//
// login() now returns a discriminated LoginResult. We mock the bare axios
// instance (apiClientRaw) and the auth store to assert each branch: a plain
// token stores it and reports kind "token"; an mfa_required body surfaces the
// challenge without storing anything; an mfa_setup_required body surfaces the
// setup token.

const post = vi.fn();
vi.mock("./client", () => ({
  apiClientRaw: { post: (...args: unknown[]) => post(...args) },
}));

const setToken = vi.fn();
vi.mock("@/lib/auth/store", () => ({
  authStore: { setToken: (t: string) => setToken(t) },
}));

// Import after the mocks are registered.
import { login, loginMfa } from "./auth";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("login()", () => {
  it("stores the token and returns kind 'token' on plain success", async () => {
    post.mockResolvedValueOnce({ data: { token: "jwt-123" } });
    const result = await login("alice", "pw", "tenant-1");
    expect(result).toEqual({ kind: "token" });
    expect(setToken).toHaveBeenCalledWith("jwt-123");
  });

  it("surfaces the challenge (no token stored) when mfa_required", async () => {
    post.mockResolvedValueOnce({
      data: { mfa_required: true, challenge_token: "chal-abc" },
    });
    const result = await login("alice", "pw", "tenant-1");
    expect(result).toEqual({ kind: "mfa", challengeToken: "chal-abc" });
    expect(setToken).not.toHaveBeenCalled();
  });

  it("surfaces the setup token when mfa_setup_required", async () => {
    post.mockResolvedValueOnce({
      data: { mfa_setup_required: true, setup_token: "setup-xyz" },
    });
    const result = await login("alice", "pw", "tenant-1");
    expect(result).toEqual({ kind: "mfa_setup", setupToken: "setup-xyz" });
    expect(setToken).not.toHaveBeenCalled();
  });
});

describe("loginMfa()", () => {
  it("stores the token returned by /login/mfa", async () => {
    post.mockResolvedValueOnce({ data: { token: "jwt-final" } });
    await loginMfa("chal-abc", "123456");
    expect(post).toHaveBeenCalledWith("/login/mfa", {
      challenge_token: "chal-abc",
      code: "123456",
    });
    expect(setToken).toHaveBeenCalledWith("jwt-final");
  });
});
