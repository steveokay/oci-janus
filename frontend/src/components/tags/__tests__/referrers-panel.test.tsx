import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { AxiosError } from "axios";
import { ReferrersPanel } from "../referrers-panel";
import {
  referrerTypeLabel,
  type Referrer,
  type ReferrersResponse,
} from "@/lib/api/referrers";

// Beacon — ReferrersPanel tests.
//
// We mock the useReferrers hook so no network is hit, then drive the panel
// through its four surfaces (populated / empty / 404-not-wired / real error)
// and unit-test the referrerTypeLabel classifier's branches directly.

// Mutable query result the mocked useReferrers reads from — tests reassign it
// before each render to exercise a specific surface.
let hookResult: {
  data?: ReferrersResponse;
  isLoading: boolean;
  isError: boolean;
  error?: unknown;
  refetch: () => void;
};

vi.mock("@/lib/api/referrers", async () => {
  // Keep the real helpers (referrerTypeLabel / referrerTypeTone / types) and
  // only override the hook — the panel + this test both import the classifier.
  const actual = await vi.importActual<typeof import("@/lib/api/referrers")>(
    "@/lib/api/referrers",
  );
  return {
    ...actual,
    useReferrers: () => hookResult,
  };
});

const sigReferrer: Referrer = {
  digest:
    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  media_type: "application/vnd.oci.image.manifest.v1+json",
  artifact_type: "application/vnd.dev.cosign.artifact.sig.v1+json",
  size: 1234,
  annotations: { "org.opencontainers.image.created": "2026-07-05" },
};

const sbomReferrer: Referrer = {
  digest:
    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  media_type: "application/vnd.oci.image.manifest.v1+json",
  artifact_type: "application/spdx+json",
  size: 9876,
};

beforeEach(() => {
  vi.clearAllMocks();
  hookResult = {
    data: { referrers: [sigReferrer, sbomReferrer], filtered: false },
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  };
});

describe("ReferrersPanel", () => {
  it("renders rows with friendly type labels for signature + SBOM referrers", () => {
    render(<ReferrersPanel org="acme" repo="web" tag="v1" />);

    expect(screen.getByText("Cosign signature")).toBeInTheDocument();
    expect(screen.getByText("SBOM (SPDX)")).toBeInTheDocument();
    // Short digest form (algo + first 8 hex + ellipsis) is shown.
    expect(screen.getByText("sha256:aaaaaaaa…")).toBeInTheDocument();
    // Compact annotation key/value, not raw JSON.
    expect(screen.getByText("image.created")).toBeInTheDocument();
    expect(screen.getByText("2026-07-05")).toBeInTheDocument();
  });

  it("shows an empty state when there are no referrers", () => {
    hookResult = {
      data: { referrers: [], filtered: false },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    render(<ReferrersPanel org="acme" repo="web" tag="v1" />);
    expect(screen.getByText("No referrers")).toBeInTheDocument();
  });

  it("renders a 'not wired' info card on a 404 rather than an error", () => {
    const err = new AxiosError("not found");
    // Minimal AxiosError.response shape the panel inspects.
    err.response = { status: 404 } as AxiosError["response"];
    hookResult = {
      isLoading: false,
      isError: true,
      error: err,
      refetch: vi.fn(),
    };
    render(<ReferrersPanel org="acme" repo="web" tag="v1" />);
    expect(
      screen.getByText("Referrers view isn't enabled on this control plane"),
    ).toBeInTheDocument();
  });

  it("renders an error state (with retry) on a non-404 failure", () => {
    const err = new AxiosError("boom");
    err.response = { status: 500 } as AxiosError["response"];
    hookResult = {
      isLoading: false,
      isError: true,
      error: err,
      refetch: vi.fn(),
    };
    render(<ReferrersPanel org="acme" repo="web" tag="v1" />);
    expect(screen.getByText("Couldn't load referrers")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("referrerTypeLabel", () => {
  // Helper to build a Referrer with a given artifact_type / media_type.
  function ref(artifact_type: string, media_type = ""): Referrer {
    return { digest: "sha256:x", media_type, artifact_type, size: 0 };
  }

  it("classifies cosign signatures", () => {
    expect(
      referrerTypeLabel(ref("application/vnd.dev.cosign.artifact.sig.v1+json")),
    ).toBe("Cosign signature");
    // The generic ".sig" suffix path also resolves to a signature.
    expect(referrerTypeLabel(ref("application/vnd.example.sig"))).toBe(
      "Cosign signature",
    );
  });

  it("classifies SBOM formats", () => {
    expect(referrerTypeLabel(ref("application/spdx+json"))).toBe("SBOM (SPDX)");
    expect(referrerTypeLabel(ref("application/vnd.cyclonedx+json"))).toBe(
      "SBOM (CycloneDX)",
    );
    expect(referrerTypeLabel(ref("application/example.sbom"))).toBe("SBOM");
  });

  it("classifies attestations", () => {
    expect(referrerTypeLabel(ref("application/vnd.in-toto+json"))).toBe(
      "Attestation",
    );
    expect(referrerTypeLabel(ref("application/vnd.dsse.envelope"))).toBe(
      "Attestation",
    );
    expect(referrerTypeLabel(ref("application/example.attestation"))).toBe(
      "Attestation",
    );
  });

  it("classifies scan results", () => {
    expect(referrerTypeLabel(ref("application/vnd.example.scan"))).toBe(
      "Scan result",
    );
    expect(referrerTypeLabel(ref("application/vnd.example.vuln.report"))).toBe(
      "Scan result",
    );
  });

  it("labels a plain image manifest with no artifact_type", () => {
    expect(
      referrerTypeLabel(ref("", "application/vnd.oci.image.manifest.v1+json")),
    ).toBe("Image manifest");
  });

  it("falls back to the raw artifact_type, then a generic label", () => {
    expect(referrerTypeLabel(ref("application/vnd.custom.thing"))).toBe(
      "application/vnd.custom.thing",
    );
    // No artifact_type and an unrecognised media_type → generic "Artifact".
    expect(referrerTypeLabel(ref("", "application/octet-stream"))).toBe(
      "Artifact",
    );
  });
});
