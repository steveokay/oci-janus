import * as React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { AxiosError } from "axios";

// FUT-016 — detail-page rendering contract.
//
// Strategy: mock `useCachedManifest` + the TanStack Router primitives the
// route uses (Link + createFileRoute). createFileRoute captures the
// component option into a module-scoped variable so each test can render
// the page in isolation without a real <RouterProvider>. The actual
// router wiring is exercised by existing routeTree tests; this file pins
// the visual states.

// Mock TanStack Router primitives. Link → plain anchor for href asserts;
// createFileRoute returns a stub Route whose useParams returns a fixed id.
// We import the page component directly (it's exported from the route
// module precisely so unit tests can mount it without a real Router).
vi.mock("@tanstack/react-router", async () => {
  const actual = await vi.importActual<Record<string, unknown>>(
    "@tanstack/react-router",
  );
  return {
    ...actual,
    Link: ({
      children,
      to,
    }: {
      children: React.ReactNode;
      to: string;
    }) =>
      React.createElement(
        "a",
        { href: to, "data-testid": "router-link" },
        children,
      ),
    createFileRoute: () => () => ({
      useParams: () => ({ id: "11111111-1111-4111-8111-111111111111" }),
    }),
  };
});

const useCachedManifestMock = vi.fn();
vi.mock("@/lib/api/proxy-cache", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/proxy-cache")>(
    "@/lib/api/proxy-cache",
  );
  return {
    ...actual,
    useCachedManifest: (id: string | undefined) => useCachedManifestMock(id),
  };
});

// CopyButton uses navigator.clipboard.writeText; jsdom needs the stub.
// Make it configurable so @testing-library/user-event can swap its own
// stub in via userEvent.setup() — otherwise the second test that calls
// setup() fails with "Cannot redefine property: clipboard".
Object.defineProperty(navigator, "clipboard", {
  value: { writeText: vi.fn().mockResolvedValue(undefined) },
  writable: true,
  configurable: true,
});

const imageDetail = {
  id: "11111111-1111-4111-8111-111111111111",
  upstream_id: "22222222-2222-4222-8222-222222222222",
  upstream_name: "dockerhub",
  image: "library/alpine",
  reference: "3.20",
  digest: "sha256:abcd1234567890abcdef1234567890abcdef",
  media_type: "application/vnd.oci.image.manifest.v1+json",
  size_bytes: 4096,
  fetched_at: new Date(Date.now() - 60_000).toISOString(),
  last_pulled_at: new Date(Date.now() - 30_000).toISOString(),
  pull_count: 7,
  kind: "image" as const,
  body_base64: btoa(
    JSON.stringify({
      schemaVersion: 2,
      config: {
        digest: "sha256:cfg",
        size: 100,
        mediaType: "application/vnd.oci.image.config.v1+json",
      },
      layers: [
        {
          digest: "sha256:layer1",
          size: 200,
          mediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
        },
      ],
    }),
  ),
  layers: [
    {
      digest: "sha256:layer1",
      size: 200,
      media_type: "application/vnd.oci.image.layer.v1.tar+gzip",
    },
  ],
  manifests: [],
};

// Import the page directly. The mocked createFileRoute returns a stub
// Route whose useParams returns the fixed test id, so module load
// is safe (no real router needed).
// eslint-disable-next-line @typescript-eslint/no-require-imports
import { ProxyCacheDetailPage } from "../_authenticated.workspace.proxy-cache.$id";

function renderPage() {
  return render(<ProxyCacheDetailPage />);
}

describe("ProxyCacheDetailPage", () => {
  beforeEach(() => {
    useCachedManifestMock.mockReset();
  });

  test("renders skeleton + back link while loading", async () => {
    useCachedManifestMock.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });
    renderPage();
    expect(screen.getByText(/back to pull-through cache/i)).toBeTruthy();
  });

  test("renders 404 empty state when row missing", async () => {
    const err = new AxiosError("not found");
    err.response = {
      status: 404,
      statusText: "",
      headers: {},
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      config: {} as any,
      data: {},
    };
    useCachedManifestMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: err,
      refetch: vi.fn(),
    });
    renderPage();
    expect(screen.getByText(/cached manifest not found/i)).toBeTruthy();
    expect(screen.getByText(/back to pull-through cache/i)).toBeTruthy();
  });

  test("renders header + layers tab for image manifests", async () => {
    useCachedManifestMock.mockReturnValue({
      data: imageDetail,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });
    renderPage();
    expect(screen.getByText("library/alpine")).toBeTruthy();
    // "dockerhub" appears more than once (header upstream chip + the
    // BackBar route label depending on layout); assert ≥1 rather than 1.
    expect(screen.getAllByText("dockerhub").length).toBeGreaterThan(0);
    expect(screen.getByTitle("sha256:layer1")).toBeTruthy();
    expect(screen.getByText("docker pull library/alpine:3.20")).toBeTruthy();
  });

  test("Manifest tab pretty-prints the body JSON", async () => {
    useCachedManifestMock.mockReturnValue({
      data: imageDetail,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });
    renderPage();
    // Radix Tabs only activates on real pointer events (fireEvent.click
    // skips the pointerdown handler). userEvent dispatches the full
    // sequence so the Manifest content actually mounts.
    await userEvent.setup().click(screen.getByRole("tab", { name: /manifest/i }));
    const pre = await screen.findByTestId("proxy-cache-manifest-raw");
    expect(pre.textContent).toMatch(/"schemaVersion":\s*2/);
  });

  test("renders Platforms tab for image indexes", async () => {
    const indexDetail = {
      ...imageDetail,
      kind: "index" as const,
      layers: [],
      manifests: [
        {
          digest: "sha256:amd64",
          size: 500,
          media_type: "application/vnd.oci.image.manifest.v1+json",
          architecture: "amd64",
          os: "linux",
        },
        {
          digest: "sha256:arm64",
          size: 600,
          media_type: "application/vnd.oci.image.manifest.v1+json",
          architecture: "arm64",
          os: "linux",
          variant: "v8",
        },
      ],
    };
    useCachedManifestMock.mockReturnValue({
      data: indexDetail,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });
    renderPage();
    expect(screen.getByRole("tab", { name: /platforms/i })).toBeTruthy();
    expect(screen.getByText("linux/amd64")).toBeTruthy();
    expect(screen.getByText("linux/arm64")).toBeTruthy();
    expect(screen.getByText("v8")).toBeTruthy();
  });

  test("BackBar links to the list page", async () => {
    useCachedManifestMock.mockReturnValue({
      data: imageDetail,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });
    renderPage();
    const link = screen.getAllByTestId("router-link")[0];
    expect(link.getAttribute("href")).toBe("/workspace/proxy-cache");
  });
});
