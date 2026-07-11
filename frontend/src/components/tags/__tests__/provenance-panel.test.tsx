import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { ProvenancePanel } from "../provenance-panel";
import type { ManifestDetail, ImageProvenance } from "@/lib/api/manifest";

// Beacon — ProvenancePanel tests (Tier 2 #4).
//
// We mock the useManifest hook so no network is hit, then drive the panel
// through its surfaces: populated (with a linkified commit), populated
// WITHOUT a linkable source (commit rendered as plain text), and the empty
// state when no provenance block is present.

let hookResult: {
  data?: ManifestDetail | null;
  isLoading: boolean;
  isError: boolean;
  refetch: () => void;
};

vi.mock("@/lib/api/manifest", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/manifest")>(
      "@/lib/api/manifest",
    );
  return {
    ...actual,
    useManifest: () => hookResult,
  };
});

// manifestWith builds a minimal ManifestDetail carrying the given provenance
// block. The non-provenance fields are filler the panel never reads.
function manifestWith(provenance?: ImageProvenance): ManifestDetail {
  return {
    digest: "sha256:deadbeef",
    media_type: "application/vnd.oci.image.manifest.v1+json",
    size_bytes: 1,
    created_at: "2026-07-11T00:00:00Z",
    is_index: false,
    config: { digest: "sha256:cfg", size: 1, media_type: "application/json" },
    layers: [],
    manifests: [],
    provenance,
  };
}

const fullProvenance: ImageProvenance = {
  created: "2026-07-11T00:00:00Z",
  source: "https://github.com/acme/widget",
  revision: "abcdef1234567890abcdef1234567890abcdef12", // 40-char SHA
  url: "https://acme.example.com",
  documentation: "https://docs.example.com/widget",
  vendor: "Acme Corp",
  version: "1.2.3",
  licenses: "Apache-2.0",
  title: "widget",
  base_name: "docker.io/library/alpine:3.20",
  base_digest: "sha256:basedigest",
  annotations: {
    "org.opencontainers.image.source": "https://github.com/acme/widget",
    "com.acme.build.pipeline": "ci-9001",
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  hookResult = {
    data: manifestWith(fullProvenance),
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  };
});

describe("ProvenancePanel", () => {
  it("renders the populated provenance surface", () => {
    render(<ProvenancePanel org="acme" repo="web" tag="v1" />);

    // Curated fields render.
    expect(screen.getByText("Acme Corp")).toBeInTheDocument();
    expect(screen.getByText("1.2.3")).toBeInTheDocument();
    expect(screen.getByText("Apache-2.0")).toBeInTheDocument();
    expect(screen.getByText("widget")).toBeInTheDocument();
    expect(
      screen.getByText("docker.io/library/alpine:3.20"),
    ).toBeInTheDocument();

    // The source repo renders as a live https anchor.
    const sourceLink = screen.getByRole("link", {
      name: "https://github.com/acme/widget",
    });
    expect(sourceLink).toHaveAttribute(
      "href",
      "https://github.com/acme/widget",
    );
  });

  it("linkifies the commit to <source>/commit/<revision> when source+revision are both present", () => {
    render(<ProvenancePanel org="acme" repo="web" tag="v1" />);

    // The commit short-SHA (first 12 chars) is the link text, pointing at the
    // constructed GitHub-style commit URL.
    const commitLink = screen.getByRole("link", { name: "abcdef123456" });
    expect(commitLink).toHaveAttribute(
      "href",
      "https://github.com/acme/widget/commit/abcdef1234567890abcdef1234567890abcdef12",
    );
  });

  it("renders the commit as plain text (no link) when source is absent", () => {
    // revision present but NO source → nothing to build a commit URL from.
    hookResult = {
      data: manifestWith({
        revision: "abcdef1234567890abcdef1234567890abcdef12",
      }),
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    render(<ProvenancePanel org="acme" repo="web" tag="v1" />);

    // The short SHA is present as text but NOT wrapped in an anchor.
    expect(screen.getByText("abcdef123456")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "abcdef123456" }),
    ).not.toBeInTheDocument();
  });

  it("shows the empty state when no provenance block is present", () => {
    hookResult = {
      data: manifestWith(undefined),
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    render(<ProvenancePanel org="acme" repo="web" tag="v1" />);
    expect(
      screen.getByText("No provenance annotations"),
    ).toBeInTheDocument();
  });
});
