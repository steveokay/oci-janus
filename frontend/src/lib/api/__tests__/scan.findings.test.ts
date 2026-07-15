import { describe, expect, it } from "vitest";

import { parseFindings } from "../scan";

// The management BFF passes the scanner's findings straight through as a Go
// `[]byte` (the findings_json JSONB column), which Go serializes to a
// base64-encoded string. The decoded JSON uses the scanner's capitalized
// field names (plugin.Finding has no json tags): CVE / Severity / Package /
// Version / FixedIn / Description / References. These tests pin both quirks —
// before the fix, parseFindings did JSON.parse on the base64 string directly
// and returned [], so the findings table was always empty.

const sample = [
  {
    CVE: "CVE-2024-58251",
    Severity: "MEDIUM",
    Package: "busybox",
    Version: "1.36.1-r20",
    FixedIn: "1.36.1-r21",
    Description: "A flaw in busybox",
    References: ["https://example.test/CVE-2024-58251"],
  },
  {
    CVE: "CVE-2024-9999",
    Severity: "HIGH",
    Package: "openssl",
    Version: "3.0.1",
    FixedIn: "3.0.2",
  },
];

function toBase64(obj: unknown): string {
  const json = JSON.stringify(obj);
  const bytes = new TextEncoder().encode(json);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

describe("parseFindings", () => {
  it("decodes base64-encoded findings_json with capitalized keys", () => {
    const raw = toBase64(sample);
    const findings = parseFindings(raw);
    expect(findings).toHaveLength(2);
    expect(findings[0].CVE).toBe("CVE-2024-58251");
    expect(findings[0].Severity).toBe("MEDIUM");
    expect(findings[0].Package).toBe("busybox");
    expect(findings[0].Version).toBe("1.36.1-r20");
    expect(findings[0].FixedIn).toBe("1.36.1-r21");
    expect(findings[0].References?.[0]).toBe(
      "https://example.test/CVE-2024-58251",
    );
  });

  it("preserves UTF-8 in descriptions through the base64 decode", () => {
    const raw = toBase64([
      { CVE: "CVE-1", Severity: "LOW", Description: "café — naïve façade" },
    ]);
    const findings = parseFindings(raw);
    expect(findings[0].Description).toBe("café — naïve façade");
  });

  it("falls back to plain JSON when the payload is not base64", () => {
    const findings = parseFindings(JSON.stringify(sample));
    expect(findings).toHaveLength(2);
    expect(findings[1].CVE).toBe("CVE-2024-9999");
  });

  it("returns [] for undefined input", () => {
    expect(parseFindings(undefined)).toEqual([]);
  });

  it("returns [] for unparseable garbage", () => {
    expect(parseFindings("not!base64!and!not!json")).toEqual([]);
  });
});
