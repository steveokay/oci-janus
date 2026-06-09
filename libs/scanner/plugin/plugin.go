// Package plugin defines the vulnerability scanner plugin interface.
// Scanners run as external processes (Trivy, Grype, etc.) and communicate
// with the orchestrator over stdin/stdout JSON-RPC. Go .so plugins are not supported.
package plugin

import (
	"context"
	"io"
	"time"
)

// Scanner is the interface all vulnerability scanner plugins must implement.
// Plugins run as external processes communicating via stdin/stdout JSON-RPC.
// Go plugin (.so) loading is explicitly NOT supported.
type Scanner interface {
	Name() string
	Version() string
	Scan(ctx context.Context, req ScanRequest) (*ScanResult, error)
}

// BlobFetcher is injected by the orchestrator so the plugin can retrieve layer blobs
// without needing storage credentials directly.
type BlobFetcher interface {
	FetchBlob(ctx context.Context, digest string) (io.ReadCloser, error)
}

// ScanRequest describes the image layers to scan. StorageFetcher is injected by
// the orchestrator so the plugin can pull blobs without holding storage credentials.
type ScanRequest struct {
	TenantID       string
	RepositoryName string
	ManifestDigest string
	Layers         []LayerRef
	StorageFetcher BlobFetcher
}

// LayerRef identifies a single image layer by its content-addressable digest.
type LayerRef struct {
	Digest    string
	MediaType string
	Size      int64
}

// ScanResult is the structured output returned by a scanner plugin.
type ScanResult struct {
	ScannerName    string
	ScannerVersion string
	Findings       []Finding
	SeverityCounts map[string]int
	ScannedAt      time.Time
}

// Finding describes a single vulnerability detected in a package layer.
type Finding struct {
	CVE         string
	Severity    string // CRITICAL|HIGH|MEDIUM|LOW|NEGLIGIBLE
	Package     string
	Version     string
	FixedIn     string
	Description string
	References  []string
}
