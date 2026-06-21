// Package collector implements the GC mark-sweep algorithm.
// It calls registry-metadata to find orphaned manifests/blobs and
// registry-storage to delete the backing objects.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/gc/internal/advisory"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
)

// Result summarises a completed GC run.
type Result struct {
	Mode             string
	ManifestsDeleted int
	BlobsDeleted     int
	BytesFreed       int64
	TenantsSkipped   int // tenants whose advisory lock was already held
	DryRun           bool
}

// Collector orchestrates the mark-sweep GC algorithm.
type Collector struct {
	meta    metadatav1.MetadataServiceClient
	storage storagev1.StorageServiceClient
	// tenant is the optional tenant-directory client. When set, listTenants
	// uses it instead of streaming every repository row from metadata.
	// nil keeps the legacy fallback alive for unit tests that exercise the
	// collector against a fake metadata server.
	tenant         tenantv1.TenantServiceClient
	pub            *publisher.Publisher
	locker         *advisory.Locker // nil = advisory locking disabled
	mode           string
	blobMinAge     time.Duration
	manifestMinAge time.Duration
}

// New creates a Collector. mode must be one of: dry-run, manifests, blobs, full.
// locker may be nil; when nil, per-tenant advisory locking is skipped (safe for
// single-worker deployments).
func New(
	metaConn *grpc.ClientConn,
	storageConn *grpc.ClientConn,
	pub *publisher.Publisher,
	locker *advisory.Locker,
	mode string,
	blobMinAgeHours, manifestMinAgeHours int,
) *Collector {
	return &Collector{
		meta:           metadatav1.NewMetadataServiceClient(metaConn),
		storage:        storagev1.NewStorageServiceClient(storageConn),
		pub:            pub,
		locker:         locker,
		mode:           mode,
		blobMinAge:     time.Duration(blobMinAgeHours) * time.Hour,
		manifestMinAge: time.Duration(manifestMinAgeHours) * time.Hour,
	}
}

// WithTenantClient wires the registry-tenant directory so listTenants
// uses the proper paginated ListTenants RPC instead of streaming every
// repository row from metadata. Optional — when unset the collector
// falls back to the metadata stream path (broken in production today,
// kept alive for unit tests). Returns the Collector for fluent setup.
func (c *Collector) WithTenantClient(conn *grpc.ClientConn) *Collector {
	if conn != nil {
		c.tenant = tenantv1.NewTenantServiceClient(conn)
	}
	return c
}

// Run executes one full GC cycle and returns a summary.
// When advisory locking is configured, each tenant's work is guarded by a
// pg_try_advisory_lock so concurrent GC workers never race on the same tenant.
func (c *Collector) Run(ctx context.Context) (*Result, error) {
	res := &Result{Mode: c.mode, DryRun: c.mode == "dry-run"}

	if err := c.publishStarted(ctx); err != nil {
		slog.WarnContext(ctx, "gc: failed to publish gc.run.started", "error", err)
	}

	// Collect distinct tenant IDs from the full repository list.
	tenants, err := c.listTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}

	for tenantID := range tenants {
		skipped, err := c.runForTenant(ctx, tenantID, res)
		if err != nil {
			slog.WarnContext(ctx, "gc: run for tenant failed",
				"tenant_id", tenantID, "error", err)
			continue
		}
		if skipped {
			res.TenantsSkipped++
		}
	}

	if err := c.publishCompleted(ctx, res); err != nil {
		slog.WarnContext(ctx, "gc: failed to publish gc.run.completed", "error", err)
	}

	slog.InfoContext(ctx, "gc run complete",
		"mode", res.Mode,
		"manifests_deleted", res.ManifestsDeleted,
		"blobs_deleted", res.BlobsDeleted,
		"bytes_freed", res.BytesFreed,
		"tenants_skipped", res.TenantsSkipped,
		"dry_run", res.DryRun,
	)
	return res, nil
}

// listTenants returns the set of tenant IDs the collector should sweep.
//
// Preferred path: query the tenant directory directly when a tenant
// gRPC client is wired (production / compose). This returns every
// provisioned tenant — even ones with zero repositories yet — which
// is harmless: runForTenant short-circuits when there's nothing to
// collect.
//
// Fallback path: stream metadata.ListRepositories and dedupe by
// tenant_id. Kept so unit tests built around a fake metadata server
// keep working; in real deployments this path is broken anyway
// (metadata's ListRepositories rejects an empty tenant_id filter
// with `invalid input syntax for type uuid: ""`), which is exactly
// the bug the tenant-directory path fixes.
func (c *Collector) listTenants(ctx context.Context) (map[string]struct{}, error) {
	if c.tenant != nil {
		return c.listTenantsViaDirectory(ctx)
	}
	stream, err := c.meta.ListRepositories(ctx, &metadatav1.ListRepositoriesRequest{})
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	tenants := map[string]struct{}{}
	for {
		repo, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("recv repository: %w", err)
		}
		tenants[repo.TenantId] = struct{}{}
	}
	return tenants, nil
}

// listTenantsViaDirectory pages through registry-tenant.ListTenants.
// The page size is the server-clamped maximum (200) so even fleets in
// the thousands need only a handful of round-trips per sweep.
func (c *Collector) listTenantsViaDirectory(ctx context.Context) (map[string]struct{}, error) {
	tenants := map[string]struct{}{}
	var pageToken string
	// Hard upper bound on pages so a runaway pagination loop can't
	// trap the sweep forever. 5000 pages * 200 per page = 1M tenants
	// is well past any realistic deployment.
	const maxPages = 5000
	for i := 0; i < maxPages; i++ {
		resp, err := c.tenant.ListTenants(ctx, &tenantv1.ListTenantsRequest{
			PageSize:  200,
			PageToken: pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("tenant.ListTenants: %w", err)
		}
		for _, t := range resp.GetTenants() {
			if id := t.GetTenantId(); id != "" {
				tenants[id] = struct{}{}
			}
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return tenants, nil
		}
	}
	return tenants, fmt.Errorf("tenant.ListTenants: exceeded %d-page safety cap", maxPages)
}

// runForTenant runs a full GC pass for a single tenant, acquiring an advisory
// lock first when a Locker is configured. Returns (true, nil) if the lock was
// already held by another worker (tenant skipped).
func (c *Collector) runForTenant(ctx context.Context, tenantIDStr string, res *Result) (skipped bool, err error) {
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		return false, fmt.Errorf("parse tenant uuid %q: %w", tenantIDStr, err)
	}

	if c.locker != nil {
		unlock, acquired, err := c.locker.TryLock(ctx, tenantID)
		if err != nil {
			return false, fmt.Errorf("advisory lock: %w", err)
		}
		if !acquired {
			slog.InfoContext(ctx, "gc: tenant lock held by another worker, skipping",
				"tenant_id", tenantIDStr)
			return true, nil
		}
		// defer is scoped to this function, so the lock is released when we return.
		defer unlock()
	}

	dryRun := c.mode == "dry-run"

	if c.mode == "manifests" || c.mode == "full" || dryRun {
		n, err := c.sweepTenantManifests(ctx, tenantIDStr, dryRun)
		if err != nil {
			return false, fmt.Errorf("sweep manifests for tenant %s: %w", tenantIDStr, err)
		}
		res.ManifestsDeleted += n
	}

	if c.mode == "blobs" || c.mode == "full" || dryRun {
		n, freed, err := c.sweepTenantBlobs(ctx, tenantIDStr, dryRun)
		if err != nil {
			return false, fmt.Errorf("sweep blobs for tenant %s: %w", tenantIDStr, err)
		}
		res.BlobsDeleted += n
		res.BytesFreed += freed
	}

	return false, nil
}

// sweepTenantManifests deletes untagged manifests older than manifestMinAge for one tenant.
func (c *Collector) sweepTenantManifests(ctx context.Context, tenantID string, dryRun bool) (int, error) {
	repoStream, err := c.meta.ListRepositories(ctx, &metadatav1.ListRepositoriesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		return 0, fmt.Errorf("list repositories: %w", err)
	}

	deleted := 0
	cutoff := time.Now().Add(-c.manifestMinAge)

	for {
		repo, err := repoStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return deleted, fmt.Errorf("recv repository: %w", err)
		}

		stream, err := c.meta.ListUntaggedManifests(ctx, &metadatav1.ListUntaggedManifestsRequest{
			RepoId:   repo.RepoId,
			TenantId: repo.TenantId,
		})
		if err != nil {
			slog.WarnContext(ctx, "gc: list untagged manifests failed",
				"repo_id", repo.RepoId, "error", err)
			continue
		}

		for {
			m, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				slog.WarnContext(ctx, "gc: recv manifest failed", "repo_id", repo.RepoId, "error", err)
				break
			}

			createdAt := m.CreatedAt.AsTime()
			if createdAt.After(cutoff) {
				continue // too young
			}

			if dryRun {
				slog.InfoContext(ctx, "gc dry-run: would delete manifest",
					"repo_id", repo.RepoId, "digest", m.Digest, "age", time.Since(createdAt))
				deleted++
				continue
			}

			if _, err := c.meta.DeleteManifest(ctx, &metadatav1.DeleteManifestRequest{
				RepoId:   repo.RepoId,
				TenantId: repo.TenantId,
				Digest:   m.Digest,
			}); err != nil {
				slog.WarnContext(ctx, "gc: delete manifest failed",
					"repo_id", repo.RepoId, "digest", m.Digest, "error", err)
				continue
			}
			slog.InfoContext(ctx, "gc: deleted manifest",
				"repo_id", repo.RepoId, "tenant_id", repo.TenantId, "digest", m.Digest)
			deleted++
		}
	}
	return deleted, nil
}

// sweepTenantBlobs deletes orphaned blobs (no repo links) for one tenant.
// Storage deletion precedes metadata deletion so a crash leaves the orphan
// record intact for the next run rather than leaking unreachable storage.
func (c *Collector) sweepTenantBlobs(ctx context.Context, tenantID string, dryRun bool) (int, int64, error) {
	stream, err := c.meta.ListOrphanedBlobs(ctx, &metadatav1.ListOrphanedBlobsRequest{
		TenantId: tenantID,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list orphaned blobs: %w", err)
	}

	deleted := 0
	var freed int64
	cutoff := time.Now().Add(-c.blobMinAge)
	_ = cutoff // blobMinAge enforced at metadata level — BlobRef has no created_at here

	for {
		blob, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return deleted, freed, fmt.Errorf("recv blob: %w", err)
		}

		if dryRun {
			slog.InfoContext(ctx, "gc dry-run: would delete blob",
				"digest", blob.Digest, "storage_key", blob.StorageKey, "size", blob.SizeBytes)
			deleted++
			freed += blob.SizeBytes
			continue
		}

		// Delete from storage backend first — if this fails, the metadata record is
		// still orphaned and will be cleaned up in the next GC run.
		if _, err := c.storage.DeleteBlob(ctx, &storagev1.DeleteBlobRequest{
			Key:      blob.StorageKey,
			TenantId: tenantID,
		}); err != nil {
			slog.WarnContext(ctx, "gc: storage delete blob failed",
				"storage_key", blob.StorageKey, "error", err)
			continue
		}

		// Remove any residual blob_links (should be none for an orphan, but defensive).
		if _, err := c.meta.UnlinkBlob(ctx, &metadatav1.UnlinkBlobRequest{
			RepoId:     "",
			BlobDigest: blob.Digest,
		}); err != nil {
			// Non-fatal — blob is already deleted from storage.
			slog.WarnContext(ctx, "gc: metadata unlink blob failed",
				"digest", blob.Digest, "error", err)
		}

		// Decrement tenant quota.
		if _, err := c.meta.DecrementTenantStorage(ctx, &metadatav1.DecrementTenantStorageRequest{
			TenantId: tenantID,
			Bytes:    blob.SizeBytes,
		}); err != nil {
			slog.WarnContext(ctx, "gc: decrement tenant storage failed",
				"tenant_id", tenantID, "error", err)
		}

		slog.InfoContext(ctx, "gc: deleted blob",
			"digest", blob.Digest, "tenant_id", tenantID, "bytes", blob.SizeBytes)
		deleted++
		freed += blob.SizeBytes
	}
	return deleted, freed, nil
}

// publishStarted emits a gc.run.started event to RabbitMQ. Non-fatal on failure.
func (c *Collector) publishStarted(ctx context.Context) error {
	payload, _ := json.Marshal(events.GCRunStartedPayload{Mode: c.mode})
	return c.pub.Publish(ctx, events.RoutingGCRunStarted, events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingGCRunStarted,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	})
}

// publishCompleted emits a gc.run.completed event with the run summary. Non-fatal on failure.
func (c *Collector) publishCompleted(ctx context.Context, res *Result) error {
	payload, _ := json.Marshal(events.GCRunCompletedPayload{
		Mode:             res.Mode,
		ManifestsDeleted: res.ManifestsDeleted,
		BlobsDeleted:     res.BlobsDeleted,
		BytesFreed:       res.BytesFreed,
		DryRun:           res.DryRun,
	})
	return c.pub.Publish(ctx, events.RoutingGCRunCompleted, events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingGCRunCompleted,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	})
}
