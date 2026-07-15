import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CompareTagsView } from "../compare-tags-view";
import type { ImageDiff } from "@/lib/api/diff";

// The view only consumes useImageDiff; mock it so the component renders
// against canned diff data without a network call.
const mockUseImageDiff = vi.fn();
vi.mock("@/lib/api/diff", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/diff")>("@/lib/api/diff");
  return { ...actual, useImageDiff: () => mockUseImageDiff() };
});

function fullDiff(): ImageDiff {
  return {
    from: { tag: "v1", digest: "sha256:aaaaaaaaaaaa1111", size_bytes: 300, is_index: false },
    to: { tag: "v2", digest: "sha256:bbbbbbbbbbbb2222", size_bytes: 550, is_index: false },
    layers: {
      added: [{ digest: "sha256:cccc", size: 350 }],
      removed: [{ digest: "sha256:aaaa", size: 100 }],
      common_count: 1,
      size_delta_bytes: 250,
    },
    config: {
      available: true,
      env: {
        added: ["EXTRA=1"],
        removed: [],
        changed: [{ key: "NODE_ENV", from: "prod", to: "stage" }],
      },
      cmd_changed: true,
      from_cmd: ["nginx"],
      to_cmd: ["nginx", "-g", "daemon off;"],
      entrypoint_changed: false,
      exposed_ports_added: ["8443/tcp"],
      exposed_ports_removed: [],
    },
    packages: {
      available: true,
      added: [{ name: "curl", version: "8.5" }],
      removed: [{ name: "wget", version: "1.21" }],
      changed: [{ name: "openssl", from_version: "3.0.1", to_version: "3.0.2" }],
    },
    vulnerabilities: {
      available: true,
      added: [{ cve: "CVE-2024-C", severity: "MEDIUM", package: "curl" }],
      removed: [{ cve: "CVE-2024-A", severity: "HIGH", package: "openssl" }],
    },
  };
}

function renderView() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <CompareTagsView org="acme" repo="api" from="v1" to="v2" />
    </QueryClientProvider>,
  );
}

describe("CompareTagsView", () => {
  beforeEach(() => {
    mockUseImageDiff.mockReset();
  });

  it("renders every section's deltas when all data is available", () => {
    mockUseImageDiff.mockReturnValue({
      data: fullDiff(),
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    renderView();

    // Layer counts.
    expect(screen.getByText(/1 added/i)).toBeInTheDocument();
    expect(screen.getByText(/1 removed/i)).toBeInTheDocument();
    // Config: the changed ENV key surfaces.
    expect(screen.getByText(/NODE_ENV/)).toBeInTheDocument();
    // Packages: the version-changed package renders both versions. (openssl
    // and curl also appear in the vulns section, so match on the unique
    // version strings instead of the package names.)
    expect(screen.getByText(/3\.0\.1/)).toBeInTheDocument();
    expect(screen.getByText(/3\.0\.2/)).toBeInTheDocument();
    expect(screen.getByText(/8\.5/)).toBeInTheDocument();
    // Vulnerabilities: introduced + fixed CVEs both listed.
    expect(screen.getByText("CVE-2024-C")).toBeInTheDocument();
    expect(screen.getByText("CVE-2024-A")).toBeInTheDocument();
    expect(screen.getByText(/1 introduced/i)).toBeInTheDocument();
    expect(screen.getByText(/1 fixed/i)).toBeInTheDocument();
  });

  it("renders per-section reasons when data is unavailable", () => {
    const d = fullDiff();
    d.config = { available: false, reason: "config diff requires registry-core", env: { added: [], removed: [], changed: [] }, cmd_changed: false, entrypoint_changed: false, exposed_ports_added: [], exposed_ports_removed: [] };
    d.packages = { available: false, reason: "the target tag has no SBOM", added: [], removed: [], changed: [] };
    d.vulnerabilities = { available: false, reason: "the target tag has not been scanned", added: [], removed: [] };
    mockUseImageDiff.mockReturnValue({ data: d, isLoading: false, isError: false, refetch: vi.fn() });
    renderView();

    expect(screen.getByText(/config diff requires registry-core/i)).toBeInTheDocument();
    expect(screen.getByText(/no SBOM/i)).toBeInTheDocument();
    expect(screen.getByText(/has not been scanned/i)).toBeInTheDocument();
    // Layers still render even when the scan-dependent sections don't.
    expect(screen.getByText(/1 added/i)).toBeInTheDocument();
  });

  it("shows an error state with retry when the query errors", () => {
    mockUseImageDiff.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    });
    renderView();
    expect(screen.getByText(/couldn't load the comparison/i)).toBeInTheDocument();
  });
});
