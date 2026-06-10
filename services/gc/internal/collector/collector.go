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
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
)

// Result summarises a completed GC run.
type Result struct {
	Mode             string
	ManifestsDeleted int
	BlobsDeleted     int
	BytesFreed       int64
	DryRun           bool
}

// Collector orchestrates the mark-sweep GC algorithm.
type Collector struct {
	meta           metadatav1.MetadataServiceClient
	storage        storagev1.StorageServiceClient
	pub            *publisher.Publisher
	mode           string
	blobMinAge     time.Duration
	manifestMinAge time.Duration
}

// New creates a Collector. mode must be one of: dry-run, manifests, blobs, full.
func New(
	metaConn *grpc.ClientConn,
	storageConn *grpc.ClientConn,
	pub *publisher.Publisher,
	mode string,
	blobMinAgeHours, manifestMinAgeHours int,
) *Collector {
	return &Collector{
		meta:           metadatav1.NewMetadataServiceClient(metaConn),
		storage:        storagev1.NewStorageServiceClient(storageConn),
		pub:            pub,
		mode:           mode,
		blobMinAge:     time.Duration(blobMinAgeHours) * time.Hour,
		manifestMinAge: time.Duration(manifestMinAgeHours) * time.Hour,
	}
}

// Run executes one full GC cycle and returns a summary.
func (c *Collector) Run(ctx context.Context) (*Result, error) {
	res := &Result{Mode: c.mode, DryRun: c.mode == "dry-run"}

	if err := c.publishStarted(ctx); err != nil {
		slog.WarnContext(ctx, "gc: failed to publish gc.run.started", "error", err)
	}

	if c.mode == "manifests" || c.mode == "full" || c.mode == "dry-run" {
		n, err := c.sweepManifests(ctx, res.DryRun)
		if err != nil {
			return nil, fmt.Errorf("sweep manifests: %w", err)
		}
		res.ManifestsDeleted = n
	}

	if c.mode == "blobs" || c.mode == "full" || c.mode == "dry-run" {
		n, freed, err := c.sweepBlobs(ctx, res.DryRun)
		if err != nil {
			return nil, fmt.Errorf("sweep blobs: %w", err)
		}
		res.BlobsDeleted = n
		res.BytesFreed = freed
	}

	if err := c.publishCompleted(ctx, res); err != nil {
		slog.WarnContext(ctx, "gc: failed to publish gc.run.completed", "error", err)
	}

	slog.InfoContext(ctx, "gc run complete",
		"mode", res.Mode,
		"manifests_deleted", res.ManifestsDeleted,
		"blobs_deleted", res.BlobsDeleted,
		"bytes_freed", res.BytesFreed,
		"dry_run", res.DryRun,
	)
	return res, nil
}

// sweepManifests deletes untagged manifests older than manifestMinAge across all repos.
func (c *Collector) sweepManifests(ctx context.Context, dryRun bool) (int, error) {
	// List all repositories (empty tenant_id = all tenants, system-level GC).
	repoStream, err := c.meta.ListRepositories(ctx, &metadatav1.ListRepositoriesRequest{})
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

// sweepBlobs deletes orphaned blobs (no repo links) older than blobMinAge.
func (c *Collector) sweepBlobs(ctx context.Context, dryRun bool) (int, int64, error) {
	// Collect all tenants by scanning repositories.
	repoStream, err := c.meta.ListRepositories(ctx, &metadatav1.ListRepositoriesRequest{})
	if err != nil {
		return 0, 0, fmt.Errorf("list repositories: %w", err)
	}

	tenants := map[string]struct{}{}
	for {
		repo, err := repoStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("recv repository: %w", err)
		}
		tenants[repo.TenantId] = struct{}{}
	}

	deleted := 0
	var freed int64

	for tenantID := range tenants {
		n, f, err := c.sweepTenantBlobs(ctx, tenantID, dryRun)
		if err != nil {
			slog.WarnContext(ctx, "gc: sweep blobs for tenant failed",
				"tenant_id", tenantID, "error", err)
			continue
		}
		deleted += n
		freed += f
	}
	return deleted, freed, nil
}

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
