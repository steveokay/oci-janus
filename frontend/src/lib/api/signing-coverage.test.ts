import { describe, it, expect, vi, beforeEach } from "vitest";

// Beacon — signing-coverage.ts fetch test. Mirrors auth.test.ts: mock the
// axios client and assert fetchSigningCoverage hits the right URL + params
// and returns the body verbatim.

const get = vi.fn();
vi.mock("./client", () => ({
  apiClient: { get: (...args: unknown[]) => get(...args) },
}));

import { fetchSigningCoverage } from "./signing-coverage";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("fetchSigningCoverage()", () => {
  it("requests /signing/coverage with the window param and returns the body", async () => {
    const body = {
      window: 50,
      signer_enabled: true,
      summary: {
        repo_count: 1,
        repos_require_signature: 1,
        repos_enforced_empty_allowlist: 0,
        workspace_signed_tag_pct: 1,
      },
      repos: [],
    };
    get.mockResolvedValueOnce({ data: body });

    const result = await fetchSigningCoverage(50);

    expect(get).toHaveBeenCalledWith("/signing/coverage", { params: { window: 50 } });
    expect(result).toEqual(body);
  });
});
