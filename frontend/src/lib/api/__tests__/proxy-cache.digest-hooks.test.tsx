import * as React from "react";
import { renderHook, waitFor, act } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AxiosError } from "axios";

// FUT-018 — digest-keyed scan + signature hook contracts.
//
// These hooks back the Scans + Signing tabs on the proxy-cache detail
// page and the Severity + Signed columns on the list table. Pinning
// the 200/403/404/500 contract here keeps a future refactor from
// silently flipping a 404 into a thrown error and crashing the list
// table for every row.
//
// The mutation contracts (POST trigger-scan, POST sign-by-digest)
// verify that invalidation happens on success — without it, the read
// query would keep serving the stale "no scan / unsigned" state and
// the operator would see a stuck pending pill.

const getMock = vi.fn();
const postMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: (...args: unknown[]) => getMock(...args),
    post: (...args: unknown[]) => postMock(...args),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

function wrapper(): {
  Wrapper: React.FC<{ children: React.ReactNode }>;
  client: QueryClient;
} {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  function Wrap({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  }
  return { Wrapper: Wrap, client };
}

function axiosErrorFromStatus(status: number): AxiosError {
  const err = new AxiosError(`Request failed with status code ${status}`);
  err.response = {
    status,
    statusText: "",
    headers: {},
    config: {} as never,
    data: {},
  };
  return err;
}

const DIGEST = "sha256:" + "a".repeat(64);

describe("useScanByDigest — read contract", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  test("resolves to the parsed scan body on 200", async () => {
    const { useScanByDigest } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        scan_id: "scan-1",
        status: "complete",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: { CRITICAL: 2, HIGH: 3 },
        started_at: "2026-06-20T00:00:00Z",
        completed_at: "2026-06-20T00:01:00Z",
      },
    });

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useScanByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.scan_id).toBe("scan-1");
    expect(result.current.data?.severity_counts.CRITICAL).toBe(2);
  });

  test("resolves to null on 404 (no scan recorded)", async () => {
    const { useScanByDigest } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useScanByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("resolves to null on 403 (read-only tenant member)", async () => {
    const { useScanByDigest } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useScanByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });

  test("propagates 500 as a thrown error", async () => {
    const { useScanByDigest } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useScanByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.data).toBeUndefined();
  });

  test("disabled when digest is undefined", async () => {
    const { useScanByDigest } = await import("../proxy-cache");
    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useScanByDigest(undefined), {
      wrapper: Wrapper,
    });
    // queryFn never runs, so no GET fires.
    expect(getMock).not.toHaveBeenCalled();
    expect(result.current.fetchStatus).toBe("idle");
  });
});

describe("useTriggerScanByDigest — mutation contract", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  test("posts to /scan-by-digest/{digest} and writes optimistic stub", async () => {
    const { useScanByDigest, useTriggerScanByDigest } = await import(
      "../proxy-cache"
    );
    // GET returns 404 first → null. Mutation success writes an optimistic
    // pending stub into the same query key.
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));
    postMock.mockResolvedValueOnce({
      data: {
        scan_id: "queued-1",
        manifest_digest: DIGEST,
        status: "queued",
      },
    });

    const { Wrapper, client } = wrapper();
    const readHook = renderHook(() => useScanByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    const mutHook = renderHook(() => useTriggerScanByDigest(), {
      wrapper: Wrapper,
    });

    await waitFor(() => expect(readHook.result.current.isSuccess).toBe(true));
    expect(readHook.result.current.data).toBeNull();

    // Confirm the POST went to the right URL.
    await act(async () => {
      await mutHook.result.current.mutateAsync(DIGEST);
    });
    expect(postMock).toHaveBeenCalledWith(
      `/scan-by-digest/${encodeURIComponent(DIGEST)}`,
      {},
    );

    // Optimistic stub now visible in the cache.
    const cached = client.getQueryData([
      "proxy-cache",
      "scanByDigest",
      DIGEST,
    ]) as { status: string; scan_id: string };
    expect(cached.status).toBe("pending");
    expect(cached.scan_id).toBe("queued-1");
  });

  test("preserves prior scanner_name/version on rescan", async () => {
    const { useScanByDigest, useTriggerScanByDigest } = await import(
      "../proxy-cache"
    );
    getMock.mockResolvedValueOnce({
      data: {
        scan_id: "prior-1",
        status: "complete",
        scanner_name: "trivy",
        scanner_version: "0.50",
        severity_counts: { LOW: 1 },
        started_at: "2026-06-20T00:00:00Z",
        completed_at: "2026-06-20T00:01:00Z",
      },
    });
    postMock.mockResolvedValueOnce({
      data: { scan_id: "queued-2", manifest_digest: DIGEST, status: "queued" },
    });

    const { Wrapper, client } = wrapper();
    const readHook = renderHook(() => useScanByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    const mutHook = renderHook(() => useTriggerScanByDigest(), {
      wrapper: Wrapper,
    });

    await waitFor(() => expect(readHook.result.current.isSuccess).toBe(true));

    await act(async () => {
      await mutHook.result.current.mutateAsync(DIGEST);
    });

    const cached = client.getQueryData([
      "proxy-cache",
      "scanByDigest",
      DIGEST,
    ]) as {
      status: string;
      scanner_name: string;
      scanner_version: string;
    };
    expect(cached.status).toBe("pending");
    // Preserved from the prior result so the Rescan UI doesn't blank
    // the "scanned by trivy@0.50" footer.
    expect(cached.scanner_name).toBe("trivy");
    expect(cached.scanner_version).toBe("0.50");
  });
});

describe("useSignaturesByDigest — read contract", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  test("resolves to parsed body on 200 (signed)", async () => {
    const { useSignaturesByDigest } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: {
        manifest_digest: DIGEST,
        signed: true,
        signatures: [
          {
            signer_id: "registry-signer",
            key_id: "key-1",
            signature_digest: "sha256:abc",
            signed_at: "2026-06-20T00:00:00Z",
          },
        ],
      },
    });

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignaturesByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual(
      expect.objectContaining({ signed: true }),
    );
  });

  test("resolves to parsed body on 200 (unsigned)", async () => {
    const { useSignaturesByDigest } = await import("../proxy-cache");
    getMock.mockResolvedValueOnce({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
    });

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignaturesByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual(
      expect.objectContaining({ signed: false, signatures: [] }),
    );
  });

  test("resolves to SIGNING_DISABLED on 404 (route disabled)", async () => {
    const { useSignaturesByDigest, SIGNING_DISABLED } = await import(
      "../proxy-cache"
    );
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(404));

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignaturesByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBe(SIGNING_DISABLED);
  });

  test("propagates 403 as a thrown error", async () => {
    const { useSignaturesByDigest } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(403));

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignaturesByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    // 403 from the signature route isn't a "feature off" signal — the
    // BFF lets any tenant member read signature lists. A 403 here
    // means something deeper has gone wrong; surface as error.
    await waitFor(() => expect(result.current.isError).toBe(true));
  });

  test("propagates 500 as a thrown error", async () => {
    const { useSignaturesByDigest } = await import("../proxy-cache");
    getMock.mockRejectedValueOnce(axiosErrorFromStatus(500));

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignaturesByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
  });
});

describe("useSignByDigest — mutation contract", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  test("posts to /sign-by-digest/{digest} with signer_id when provided", async () => {
    const { useSignByDigest } = await import("../proxy-cache");
    postMock.mockResolvedValueOnce({
      data: {
        manifest_digest: DIGEST,
        signer_id: "alice",
        key_id: "key-1",
        signature_digest: "sha256:def",
        signed_at: "2026-06-20T00:00:00Z",
      },
    });

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignByDigest(), { wrapper: Wrapper });
    await act(async () => {
      await result.current.mutateAsync({ digest: DIGEST, signer_id: "alice" });
    });
    expect(postMock).toHaveBeenCalledWith(
      `/sign-by-digest/${encodeURIComponent(DIGEST)}`,
      { signer_id: "alice" },
    );
  });

  test("omits signer_id from the body when not provided", async () => {
    const { useSignByDigest } = await import("../proxy-cache");
    postMock.mockResolvedValueOnce({
      data: {
        manifest_digest: DIGEST,
        signer_id: "registry-signer",
        key_id: "key-1",
        signature_digest: "sha256:def",
        signed_at: "2026-06-20T00:00:00Z",
      },
    });

    const { Wrapper } = wrapper();
    const { result } = renderHook(() => useSignByDigest(), { wrapper: Wrapper });
    await act(async () => {
      await result.current.mutateAsync({ digest: DIGEST });
    });
    expect(postMock).toHaveBeenCalledWith(
      `/sign-by-digest/${encodeURIComponent(DIGEST)}`,
      {},
    );
  });

  test("invalidates the read query on success", async () => {
    const { useSignaturesByDigest, useSignByDigest } = await import(
      "../proxy-cache"
    );
    // Seed the read cache with an unsigned response.
    getMock.mockResolvedValueOnce({
      data: { manifest_digest: DIGEST, signed: false, signatures: [] },
    });
    postMock.mockResolvedValueOnce({
      data: {
        manifest_digest: DIGEST,
        signer_id: "registry-signer",
        key_id: "key-1",
        signature_digest: "sha256:def",
        signed_at: "2026-06-20T00:00:00Z",
      },
    });
    // After mutation, the read query should refetch — return a signed
    // response this time.
    getMock.mockResolvedValueOnce({
      data: {
        manifest_digest: DIGEST,
        signed: true,
        signatures: [
          {
            signer_id: "registry-signer",
            key_id: "key-1",
            signature_digest: "sha256:def",
            signed_at: "2026-06-20T00:00:00Z",
          },
        ],
      },
    });

    const { Wrapper } = wrapper();
    const readHook = renderHook(() => useSignaturesByDigest(DIGEST), {
      wrapper: Wrapper,
    });
    const mutHook = renderHook(() => useSignByDigest(), { wrapper: Wrapper });

    await waitFor(() => expect(readHook.result.current.isSuccess).toBe(true));
    expect(
      (readHook.result.current.data as { signed: boolean }).signed,
    ).toBe(false);

    await act(async () => {
      await mutHook.result.current.mutateAsync({ digest: DIGEST });
    });

    // Read should refetch and surface the signed state.
    await waitFor(() => {
      const d = readHook.result.current.data as { signed: boolean } | unknown;
      expect((d as { signed: boolean }).signed).toBe(true);
    });
  });
});
