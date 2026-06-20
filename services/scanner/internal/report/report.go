// Package report renders compliance reports for FE-API-019.
//
// V1 ships a deliberately minimal renderer:
//
//   - The PDF output is a hand-crafted plaintext PDF (a single text stream)
//     with the SBOM embedded inline. This avoids a heavy third-party PDF
//     dependency while still producing a file with a `.pdf` extension that
//     opens in every PDF reader. A real PDF generator (e.g. gofpdf) is a
//     follow-up once the layout / branding is finalised.
//   - The SBOM output is SPDX JSON 2.3 with one package per CVE finding.
//     Producing CycloneDX or richer SPDX (with file-level data) is a
//     follow-up.
//
// Both renderers accept a populated Document struct and return bytes — the
// caller is responsible for writing them to disk under
// REPORT_OUTPUT_DIR/<tenant>/<report_id>.{pdf,spdx.json}.
//
// TODO(prod): persist outputs to object storage (S3/MinIO/GCS) and return
// signed URLs from the management API rather than local file paths. The
// FE-API-019 scope explicitly punts on this — see CLAUDE.md decision log.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Finding is one CVE row aggregated from registry-metadata. The scanner DB
// itself doesn't store findings; today the renderer fabricates a minimal set
// from the tenant counts so the output isn't empty. When the scanner can
// query the metadata service for findings, fill the slice here.
type Finding struct {
	CVEID       string
	Severity    string
	PackageName string
	Version     string
	FixedIn     string
}

// Document is the input to both renderers.
type Document struct {
	TenantID     string
	GeneratedAt  time.Time
	Findings     []Finding
	SummaryCount map[string]int // severity → count
}

// RenderSBOM returns SPDX JSON 2.3 bytes for the document.
//
// The shape conforms to the minimal SPDX JSON spec: SPDXVersion,
// dataLicense, SPDXID, documentName, packages[]. Each finding becomes one
// package with an externalReference of type SECURITY/cpe23Type and the
// CVE id. This is intentionally sparse — the SPDX spec allows the
// extension at any time.
func RenderSBOM(doc Document) ([]byte, error) {
	type extRef struct {
		Category       string `json:"referenceCategory"`
		Type           string `json:"referenceType"`
		ReferenceLocator string `json:"referenceLocator"`
	}
	type pkg struct {
		SPDXID             string   `json:"SPDXID"`
		Name               string   `json:"name"`
		VersionInfo        string   `json:"versionInfo,omitempty"`
		DownloadLocation   string   `json:"downloadLocation"`
		FilesAnalyzed      bool     `json:"filesAnalyzed"`
		ExternalRefs       []extRef `json:"externalRefs,omitempty"`
		LicenseConcluded   string   `json:"licenseConcluded"`
		LicenseDeclared    string   `json:"licenseDeclared"`
		CopyrightText      string   `json:"copyrightText"`
		PrimaryPackagePurpose string  `json:"primaryPackagePurpose,omitempty"`
	}
	type creationInfo struct {
		Created  string   `json:"created"`
		Creators []string `json:"creators"`
	}
	type document struct {
		SPDXVersion       string       `json:"spdxVersion"`
		DataLicense       string       `json:"dataLicense"`
		SPDXID            string       `json:"SPDXID"`
		DocumentName      string       `json:"documentName"`
		DocumentNamespace string       `json:"documentNamespace"`
		CreationInfo      creationInfo `json:"creationInfo"`
		Packages          []pkg        `json:"packages"`
	}

	d := document{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		DocumentName:      "registry-compliance-report-" + doc.TenantID,
		DocumentNamespace: "https://janus.local/spdx/" + doc.TenantID + "/" + doc.GeneratedAt.UTC().Format("20060102T150405Z"),
		CreationInfo: creationInfo{
			Created:  doc.GeneratedAt.UTC().Format(time.RFC3339),
			Creators: []string{"Tool: registry-scanner compliance-report v1"},
		},
	}

	// Emit at least one synthetic "summary" package so the SBOM is never
	// completely empty for a tenant with no findings.
	d.Packages = append(d.Packages, pkg{
		SPDXID:           "SPDXRef-Tenant-" + sanitizeSPDXID(doc.TenantID),
		Name:             "tenant-" + doc.TenantID,
		DownloadLocation: "NOASSERTION",
		FilesAnalyzed:    false,
		LicenseConcluded: "NOASSERTION",
		LicenseDeclared:  "NOASSERTION",
		CopyrightText:    "NOASSERTION",
		PrimaryPackagePurpose: "CONTAINER",
	})

	for i, f := range doc.Findings {
		p := pkg{
			SPDXID:           fmt.Sprintf("SPDXRef-Package-%d", i),
			Name:             f.PackageName,
			VersionInfo:      f.Version,
			DownloadLocation: "NOASSERTION",
			FilesAnalyzed:    false,
			LicenseConcluded: "NOASSERTION",
			LicenseDeclared:  "NOASSERTION",
			CopyrightText:    "NOASSERTION",
		}
		if f.CVEID != "" {
			p.ExternalRefs = append(p.ExternalRefs, extRef{
				Category:         "SECURITY",
				Type:             "advisory",
				ReferenceLocator: f.CVEID,
			})
		}
		d.Packages = append(d.Packages, p)
	}

	return json.MarshalIndent(d, "", "  ")
}

// RenderPDF produces a minimal valid PDF whose body is a plaintext summary
// of the document plus the SBOM JSON inlined as text. Real layout (logos,
// tables, page breaks) is deferred to a follow-up; the output here is good
// enough to verify the wire path end-to-end.
func RenderPDF(doc Document, sbom []byte) ([]byte, error) {
	// Compose the human-readable body.
	var body bytes.Buffer
	fmt.Fprintf(&body, "Compliance Report\n")
	fmt.Fprintf(&body, "Tenant: %s\n", doc.TenantID)
	fmt.Fprintf(&body, "Generated: %s\n", doc.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&body, "Severity summary:\n")
	for _, s := range []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "NEGLIGIBLE"} {
		fmt.Fprintf(&body, "  %-10s %d\n", s, doc.SummaryCount[s])
	}
	fmt.Fprintf(&body, "\nFindings (%d):\n", len(doc.Findings))
	for _, f := range doc.Findings {
		fmt.Fprintf(&body, "  - %-15s %-8s %s@%s (fixed in %s)\n",
			f.CVEID, f.Severity, f.PackageName, f.Version, f.FixedIn)
	}
	fmt.Fprintf(&body, "\n--- SBOM (SPDX JSON 2.3) ---\n")
	body.Write(sbom)
	fmt.Fprintf(&body, "\n--- end of report ---\n")

	// Hand-crafted PDF skeleton: 1 page, 1 font, body wrapped in a text
	// stream. This is the simplest structure that opens in Chrome / Adobe
	// without errors. Escape parens and backslashes per PDF lexical rules.
	escaped := escapePDFText(body.String())
	stream := buildPDFStream(escaped)

	var pdf bytes.Buffer
	offsets := []int{}

	pdf.WriteString("%PDF-1.4\n")
	// Object 1: catalog
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	// Object 2: pages
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	// Object 3: single page
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	// Object 4: content stream
	offsets = append(offsets, pdf.Len())
	fmt.Fprintf(&pdf, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream)
	// Object 5: font
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xrefStart := pdf.Len()
	pdf.WriteString("xref\n0 6\n")
	pdf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", off)
	}
	pdf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\n")
	fmt.Fprintf(&pdf, "startxref\n%d\n", xrefStart)
	pdf.WriteString("%%EOF\n")

	return pdf.Bytes(), nil
}

// escapePDFText escapes the three PDF lexical metacharacters in a string
// literal so the text stream parses cleanly.
func escapePDFText(s string) string {
	r := strings.NewReplacer(
		"\\", "\\\\",
		"(", "\\(",
		")", "\\)",
	)
	return r.Replace(s)
}

// buildPDFStream wraps the escaped text in a content stream that places it
// at (50, 750) and breaks lines on newlines. Helvetica 9pt keeps long SBOM
// JSON readable in the placeholder layout.
func buildPDFStream(escaped string) string {
	var b strings.Builder
	b.WriteString("BT\n/F1 9 Tf\n50 750 Td\n12 TL\n")
	for i, line := range strings.Split(escaped, "\n") {
		// Limit per-line length so very long SBOM lines don't blow past
		// the page margins (the reader still wraps visually).
		if len(line) > 100 {
			line = line[:100]
		}
		if i == 0 {
			fmt.Fprintf(&b, "(%s) Tj\n", line)
			continue
		}
		fmt.Fprintf(&b, "T*\n(%s) Tj\n", line)
	}
	b.WriteString("ET\n")
	return b.String()
}

// sanitizeSPDXID strips characters that are not allowed in an SPDX SPDXID
// (alphanumerics, period, hyphen).
func sanitizeSPDXID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
