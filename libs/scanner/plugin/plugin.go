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

type ScanRequest struct {
	TenantID       string
	RepositoryName string
	ManifestDigest string
	Layers         []LayerRef
	StorageFetcher BlobFetcher
}

type LayerRef struct {
	Digest    string
	MediaType string
	Size      int64
}

type ScanResult struct {
	ScannerName    string
	ScannerVersion string
	Findings       []Finding
	SeverityCounts map[string]int
	ScannedAt      time.Time
}

type Finding struct {
	CVE         string
	Severity    string // CRITICAL|HIGH|MEDIUM|LOW|NEGLIGIBLE
	Package     string
	Version     string
	FixedIn     string
	Description string
	References  []string
}
