package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
)

var (
	repoNameRE = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*/[a-z0-9]+([._-][a-z0-9]+)*$`)
	digestRE   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// eventPublisher is the subset of *publisher.Publisher used by Registry so
// that tests can supply a no-op implementation without a real AMQP broker.
type eventPublisher interface {
	Publish(ctx context.Context, routingKey string, event events.Event) error
}

// Ensure *publisher.Publisher satisfies the interface at compile time.
var _ eventPublisher = (*publisher.Publisher)(nil)

// pullSampler decides whether to publish a pull.image event for a given pull.
// Kept as an injectable function (not a raw float) so tests can pin the
// decision deterministically without seeding the global rand.
type pullSampler func() bool

// Registry is the core OCI registry service.
type Registry struct {
	metadata  metadatav1.MetadataServiceClient
	storage   storagev1.StorageServiceClient
	uploads   *UploadStore
	referrers *ReferrerStore
	publisher eventPublisher
	// pullSample (FE-API-042) gates pull.image publishes. Defaults to a
	// rand.Float64() < sampleRate check, but tests inject deterministic samplers.
	pullSample pullSampler
	// signer (futures.md Tier 1 #3) is optional. When nil, repositories
	// with `require_signature=true` skip the admission check with a
	// warning log — the alternative (failing every pull from those
	// repos because the signer wasn't wired) would be a worse default.
	// `services/core` should ALWAYS dial the signer in production; this
	// nil-tolerance is purely a dev-stack convenience for the cases
	// where the operator hasn't started `registry-signer` yet.
	signer signerv1.SignerServiceClient
}

// NewRegistry constructs a Registry. PullEventSampleRate=1.0 publishes every
// pull (the default); <1.0 thins the stream to dial back analytics CPU/IO on
// massive registries.
func NewRegistry(
	metaConn *grpc.ClientConn,
	storageConn *grpc.ClientConn,
	uploads *UploadStore,
	referrers *ReferrerStore,
	pub *publisher.Publisher,
	pullEventSampleRate float64,
) *Registry {
	return &Registry{
		metadata:   metadatav1.NewMetadataServiceClient(metaConn),
		storage:    storagev1.NewStorageServiceClient(storageConn),
		uploads:    uploads,
		referrers:  referrers,
		publisher:  pub,
		pullSample: defaultPullSampler(pullEventSampleRate),
	}
}

// WithSigner wires the optional signer gRPC client so the GetManifest
// admission path can call ListSignatures. When this option is NOT
// applied, signature admission warns + allows (see
// checkSignatureAdmission).
//
// Futures.md Tier 1 #3 — Signed-image admission.
func (r *Registry) WithSigner(signerConn *grpc.ClientConn) *Registry {
	if signerConn != nil {
		r.signer = signerv1.NewSignerServiceClient(signerConn)
	}
	return r
}

// defaultPullSampler returns a sampler that uses the package-level rand source.
// Edge cases are short-circuited so 0.0 never publishes and 1.0 always does —
// rand.Float64() returns values in [0, 1), so a naive comparison would skip
// a sample at exactly 1.0 on rare boundary values.
func defaultPullSampler(rate float64) pullSampler {
	if rate <= 0.0 {
		return func() bool { return false }
	}
	if rate >= 1.0 {
		return func() bool { return true }
	}
	return func() bool { return rand.Float64() < rate } //nolint:gosec // analytics sampling, not crypto
}

// ValidateName ensures the name is org/repo format and matches allowed characters.
func ValidateName(name string) error {
	if !repoNameRE.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

// ValidateDigest ensures the digest is sha256:<hex64>.
func ValidateDigest(digest string) error {
	if !digestRE.MatchString(digest) {
		return ErrInvalidDigest
	}
	return nil
}

// blobKey returns the storage key for a blob.
func blobKey(tenantID, digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	return fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, hex[:2], hex)
}

// --- Blob operations ---

// BlobExists checks whether a blob exists in storage.
func (r *Registry) BlobExists(ctx context.Context, tenantID, digest string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := r.storage.BlobExists(ctx, &storagev1.BlobExistsRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	})
	if err != nil {
		return false, 0, fmt.Errorf("blob exists rpc: %w", err)
	}
	if !resp.GetExists() {
		return false, 0, nil
	}
	stat, err := r.storage.StatBlob(ctx, &storagev1.StatBlobRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	})
	if err != nil {
		return true, 0, nil
	}
	return true, stat.GetSize(), nil
}

// GetBlob streams a blob from storage into w. Returns (size, contentType, error).
func (r *Registry) GetBlob(ctx context.Context, tenantID, digest string, w io.Writer) (int64, error) {
	stream, err := r.storage.GetBlob(ctx, &storagev1.GetBlobRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("get blob rpc: %w", err)
	}

	var total int64
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("recv blob chunk: %w", err)
		}
		n, werr := w.Write(chunk.GetChunk())
		total += int64(n)
		if werr != nil {
			return total, fmt.Errorf("write blob chunk: %w", werr)
		}
	}
	return total, nil
}

// DeleteBlob removes a blob from storage and unlinks it from the repository.
func (r *Registry) DeleteBlob(ctx context.Context, tenantID, repoID, digest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := r.storage.DeleteBlob(ctx, &storagev1.DeleteBlobRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	}); err != nil {
		if !isGRPCNotFound(err) {
			return fmt.Errorf("delete blob rpc: %w", err)
		}
	}

	if _, err := r.metadata.UnlinkBlob(ctx, &metadatav1.UnlinkBlobRequest{
		RepoId:     repoID,
		BlobDigest: digest,
	}); err != nil && !isGRPCNotFound(err) {
		return fmt.Errorf("unlink blob rpc: %w", err)
	}
	return nil
}

// --- Upload (chunked blob push) ---

// InitiateUpload creates a new upload session and returns the upload UUID.
func (r *Registry) InitiateUpload(ctx context.Context, tenantID, repoName string) (string, error) {
	id := uuid.New().String()
	if err := r.uploads.Create(ctx, UploadState{
		UUID:     id,
		TenantID: tenantID,
		RepoName: repoName,
		Offset:   0,
	}); err != nil {
		return "", fmt.Errorf("create upload state: %w", err)
	}
	return id, nil
}

// GetUpload returns current upload state.
func (r *Registry) GetUpload(ctx context.Context, uploadUUID string) (*UploadState, error) {
	return r.uploads.Get(ctx, uploadUUID)
}

// AppendChunk stores a PATCH chunk as a temp blob object and advances the offset.
// Each chunk is stored under uploads/{tenantID}/{uploadUUID}/{seq} and its key is
// appended to UploadState.ChunkKeys. CompleteUpload then assembles them in order.
// We avoid UploadPart (MinIO multipart) because that requires a MinIO-issued upload
// ID from NewMultipartUpload, which we do not have — the registry UUID is not valid.
func (r *Registry) AppendChunk(ctx context.Context, uploadUUID string, chunk io.Reader, _ int64) (int64, error) {
	st, err := r.uploads.Get(ctx, uploadUUID)
	if err != nil {
		return 0, err
	}

	// Store this chunk as its own object; key encodes sequence position.
	seqKey := fmt.Sprintf("uploads/%s/%s/%d", st.TenantID, uploadUUID, len(st.ChunkKeys))

	stream, err := r.storage.PutBlob(ctx)
	if err != nil {
		return 0, fmt.Errorf("open put-blob stream: %w", err)
	}

	if err := stream.Send(&storagev1.PutBlobRequest{
		Data: &storagev1.PutBlobRequest_Meta{
			Meta: &storagev1.PutBlobMeta{
				Key:         seqKey,
				ContentType: "application/octet-stream",
				TenantId:    st.TenantID,
			},
		},
	}); err != nil {
		return 0, fmt.Errorf("send chunk meta: %w", err)
	}

	buf := make([]byte, 64*1024)
	var written int64
	for {
		n, rerr := chunk.Read(buf)
		if n > 0 {
			if err := stream.Send(&storagev1.PutBlobRequest{
				Data: &storagev1.PutBlobRequest_Chunk{Chunk: buf[:n]},
			}); err != nil {
				return written, fmt.Errorf("send chunk data: %w", err)
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return written, fmt.Errorf("read chunk: %w", rerr)
		}
	}

	if _, err := stream.CloseAndRecv(); err != nil {
		return written, fmt.Errorf("close chunk stream: %w", err)
	}

	st.ChunkKeys = append(st.ChunkKeys, seqKey)
	st.Offset += written
	if err := r.uploads.Update(ctx, st); err != nil {
		return written, fmt.Errorf("update upload state: %w", err)
	}
	return written, nil
}

// CompleteUpload assembles all PATCH chunks (if any) followed by the final PUT body,
// verifies the digest, writes the canonical blob, and cleans up temp chunk objects.
func (r *Registry) CompleteUpload(ctx context.Context, uploadUUID, expectedDigest string, finalChunk io.Reader, _ int64) (string, int64, error) {
	st, err := r.uploads.Get(ctx, uploadUUID)
	if err != nil {
		return "", 0, err
	}

	key := blobKey(st.TenantID, expectedDigest)

	stream, err := r.storage.PutBlob(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("open put-blob stream: %w", err)
	}

	if err := stream.Send(&storagev1.PutBlobRequest{
		Data: &storagev1.PutBlobRequest_Meta{
			Meta: &storagev1.PutBlobMeta{
				Key:         key,
				ContentType: "application/octet-stream",
				TenantId:    st.TenantID,
			},
		},
	}); err != nil {
		return "", 0, fmt.Errorf("send put-blob meta: %w", err)
	}

	hash := sha256.New()
	var totalBytes int64

	// Replay any PATCH chunks that were stored as individual temp objects.
	for _, chunkKey := range st.ChunkKeys {
		chunkStream, err := r.storage.GetBlob(ctx, &storagev1.GetBlobRequest{
			Key:      chunkKey,
			TenantId: st.TenantID,
		})
		if err != nil {
			return "", 0, fmt.Errorf("get chunk %s: %w", chunkKey, err)
		}
		for {
			resp, err := chunkStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", 0, fmt.Errorf("recv chunk %s: %w", chunkKey, err)
			}
			data := resp.GetChunk()
			hash.Write(data)
			if err := stream.Send(&storagev1.PutBlobRequest{
				Data: &storagev1.PutBlobRequest_Chunk{Chunk: data},
			}); err != nil {
				return "", 0, fmt.Errorf("forward chunk data: %w", err)
			}
			totalBytes += int64(len(data))
		}
	}

	// Stream the PUT body (non-empty for monolithic uploads or a final PATCH+PUT).
	buf := make([]byte, 64*1024)
	for {
		n, rerr := finalChunk.Read(buf)
		if n > 0 {
			hash.Write(buf[:n])
			if err := stream.Send(&storagev1.PutBlobRequest{
				Data: &storagev1.PutBlobRequest_Chunk{Chunk: buf[:n]},
			}); err != nil {
				return "", 0, fmt.Errorf("send final chunk: %w", err)
			}
			totalBytes += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, fmt.Errorf("read final chunk: %w", rerr)
		}
	}

	if _, err := stream.CloseAndRecv(); err != nil {
		return "", 0, fmt.Errorf("close put-blob stream: %w", err)
	}

	actualDigest := fmt.Sprintf("sha256:%x", hash.Sum(nil))
	if expectedDigest != "" && actualDigest != expectedDigest {
		// The request context may already be cancelled (e.g. client disconnected), so
		// we must use a fresh context for this cleanup DeleteBlob — otherwise the storage
		// call would be rejected immediately and the partially-written blob would leak.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = r.storage.DeleteBlob(cleanupCtx, &storagev1.DeleteBlobRequest{Key: key, TenantId: st.TenantID})
		return "", 0, ErrDigestMismatch
	}

	// Detach from request context: temp chunk cleanup is best-effort and must not
	// block the response. A 30-second timeout prevents goroutine leaks if the
	// storage service is slow. Failures here are silent — GC will reclaim orphaned
	// chunk objects on its next run.
	if len(st.ChunkKeys) > 0 {
		chunkKeys := st.ChunkKeys
		tenantID := st.TenantID
		go func() {
			ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, ck := range chunkKeys {
				_, _ = r.storage.DeleteBlob(ctx2, &storagev1.DeleteBlobRequest{Key: ck, TenantId: tenantID})
			}
		}()
	}

	_ = r.uploads.Delete(ctx, uploadUUID)
	return actualDigest, totalBytes, nil
}

// CancelUpload removes upload state.
func (r *Registry) CancelUpload(ctx context.Context, uploadUUID string) error {
	return r.uploads.Delete(ctx, uploadUUID)
}

// --- Manifest operations ---

// manifestFields is used to extract subject/artifactType/annotations from a manifest body.
// Per OCI spec §6.2, artifactType falls back to config.mediaType when the top-level field is absent.
type manifestFields struct {
	Subject *struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"subject"`
	ArtifactType string `json:"artifactType"`
	Config       *struct {
		MediaType string `json:"mediaType"`
	} `json:"config"`
	Annotations map[string]string `json:"annotations"`
}

// PutManifest stores a manifest, creates/updates the tag if reference is a tag, and publishes push.completed.
// Returns (digest, subjectDigest, error); subjectDigest is non-empty when the manifest contains a subject field.
func (r *Registry) PutManifest(ctx context.Context, tenantID, repoID, repoName, reference, mediaType string, rawJSON []byte, pushedBy string) (string, string, error) {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(rawJSON))

	ctx5, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Tag immutability gate (futures.md Tier 1 #2). Only checked when
	// the reference is a tag — pushes addressed by digest are
	// content-addressable and can't accidentally move existing tags.
	// We do the check BEFORE PutManifest so a rejected push doesn't
	// leave a manifest row in the DB without a tag pointing at it.
	// Inserting the manifest row would be harmless (it's content-
	// addressable, the next push of the same content is a no-op) but
	// declining cleanly keeps the rejection legible from the operator's
	// perspective: "the request you sent was rejected end-to-end".
	if !digestRE.MatchString(reference) {
		if err := r.checkTagImmutable(ctx5, tenantID, repoID, reference, digest); err != nil {
			return "", "", err
		}
	}

	_, err := r.metadata.PutManifest(ctx5, &metadatav1.PutManifestRequest{
		RepoId:    repoID,
		TenantId:  tenantID,
		Digest:    digest,
		MediaType: mediaType,
		RawJson:   rawJSON,
		SizeBytes: int64(len(rawJSON)),
	})
	if err != nil {
		return "", "", fmt.Errorf("put manifest rpc: %w", err)
	}

	// if reference is a tag name, also upsert the tag
	if !digestRE.MatchString(reference) {
		if _, err := r.metadata.PutTag(ctx5, &metadatav1.PutTagRequest{
			RepoId:         repoID,
			TenantId:       tenantID,
			Name:           reference,
			ManifestDigest: digest,
		}); err != nil {
			return "", "", fmt.Errorf("put tag rpc: %w", err)
		}
	}

	// Parse manifest for subject field; if present, register this manifest as a referrer.
	var subjectDigest string
	var mf manifestFields
	if json.Unmarshal(rawJSON, &mf) == nil && mf.Subject != nil && digestRE.MatchString(mf.Subject.Digest) {
		subjectDigest = mf.Subject.Digest
		// Effective artifact type: top-level field takes precedence; fall back to
		// config.mediaType per OCI spec §6.2 so config-based artifacts filter correctly.
		effectiveArtifactType := mf.ArtifactType
		if effectiveArtifactType == "" && mf.Config != nil {
			effectiveArtifactType = mf.Config.MediaType
		}
		desc := ReferrerDescriptor{
			MediaType:    mediaType,
			Digest:       digest,
			Size:         int64(len(rawJSON)),
			ArtifactType: effectiveArtifactType,
			Annotations:  mf.Annotations,
		}
		if err := r.referrers.Add(ctx5, tenantID, repoName, subjectDigest, desc); err != nil {
			// best-effort — referrer tracking failure does not fail the push
			slog.WarnContext(ctx5, "store referrer failed (best-effort)", "error", err)
		}
	}

	// Publish push.completed event so downstream services (scanner, audit, webhook)
	// can react to the push. Use the request context so traces are connected and
	// the publish is cancelled if the broker is unreachable within the deadline.
	// A failure here must not fail the push — the manifest is already committed.
	//
	// 2026-06-22: RepoID was missing from this payload, which silently
	// broke auto-scan-on-push: the scanner consumed the event, enqueued
	// the job with an empty repo_id, and metadata's GetManifest rejected
	// the empty string as not a valid UUID (SQLSTATE 22P02). The actual
	// error was masked by mapErr's coarse Internal fallback until the
	// observability fix landed earlier today.
	// S-MAINT-1 Batch 5 (P6): derive the artifact-type discriminator from
	// the manifest's config.mediaType so the scanner can short-circuit
	// non-image artifacts before enqueueing. Same mapping as
	// services/metadata/internal/repository.deriveArtifactType — kept in
	// sync by the constant set on the proto. Empty string when the
	// manifest had no parseable config block; the consumer treats empty
	// as "unknown — scan anyway" to stay compatible with older pushes.
	artifactType := deriveArtifactType(extractConfigMediaType(rawJSON))
	payload, _ := json.Marshal(events.PushCompletedPayload{
		RepositoryName: repoName,
		RepoID:         repoID,
		Tag:            reference,
		ManifestDigest: digest,
		PushedBy:       pushedBy,
		SizeBytes:      int64(len(rawJSON)),
		ArtifactType:   artifactType,
	})
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingPushCompleted,
		TenantID:   tenantID,
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}
	// Derive a bounded publish context from the request context so the trace
	// span is correctly parented and the operation cannot outlive the request.
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()
	if err := r.publisher.Publish(pubCtx, events.RoutingPushCompleted, evt); err != nil {
		// Log but do not fail — the push itself succeeded and the event is best-effort.
		slog.WarnContext(ctx, "push.completed publish failed (best-effort)", "error", err)
	}

	return digest, subjectDigest, nil
}

// GetReferrers returns the list of manifests that reference the given subject digest.
// If artifactType is non-empty the list is filtered to that type and filtered=true is returned.
func (r *Registry) GetReferrers(ctx context.Context, tenantID, repoName, subjectDigest, artifactType string) ([]ReferrerDescriptor, bool, error) {
	all, err := r.referrers.List(ctx, tenantID, repoName, subjectDigest)
	if err != nil {
		return nil, false, err
	}
	if artifactType == "" {
		return all, false, nil
	}
	filtered := make([]ReferrerDescriptor, 0, len(all))
	for _, d := range all {
		if d.ArtifactType == artifactType {
			filtered = append(filtered, d)
		}
	}
	return filtered, true, nil
}

// RecordPull (FE-API-042) publishes a pull.image event for a successful manifest
// GET. The call is fire-and-forget — it never returns an error and never blocks
// the response — because:
//
//   - the pull itself has already succeeded (the response body was written),
//   - a slow/dead broker must not latency-poison the pull hot path,
//   - the analytics + retention consumers can tolerate dropped events.
//
// The sample-rate gate (`pullSample()`) and the bounded 2s publish context
// together cap the worst-case overhead a misconfigured broker can impose.
//
// `manifestID` and `tag` are optional — pass empty strings when the handler
// resolved the pull by digest or the metadata service did not surface the
// internal UUID. `actorID` is empty for anonymous public pulls; downstream
// consumers (audit) record those as actor_id="anonymous".
func (r *Registry) RecordPull(
	ctx context.Context,
	tenantID, repoID, repoName, manifestDigest, manifestID, tag, actorID string,
) {
	// Skip publish entirely when the sample rate gate trips. Cheaper than
	// building the payload and bailing later, and matches the spec's
	// "if rand.Float64() < cfg.PullEventSampleRate { publish }" shape.
	if r.pullSample == nil || !r.pullSample() {
		return
	}

	payload, err := json.Marshal(events.PullImagePayload{
		TenantID:       tenantID,
		RepositoryID:   repoID,
		RepositoryName: repoName,
		ManifestDigest: manifestDigest,
		ManifestID:     manifestID,
		Tag:            tag,
		ActorID:        actorID,
		PulledAt:       time.Now().UTC(),
	})
	if err != nil {
		// Marshalling our own struct cannot realistically fail, but log so a
		// future schema change doesn't silently break analytics.
		slog.WarnContext(ctx, "pull.image marshal failed (best-effort)", "error", err)
		return
	}

	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingPullImage,
		TenantID:   tenantID,
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}

	// 2s timeout deliberately tighter than the 5s used by push.completed.
	// A pull is a read-mostly operation and we want to keep the worst-case
	// added latency well below the HTTP read timeout (30s) on a flaky broker.
	pubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.publisher.Publish(pubCtx, events.RoutingPullImage, evt); err != nil {
		// Best-effort: the manifest body has already been written to the
		// client. Logging at WARN matches the push.completed pattern.
		slog.WarnContext(ctx, "pull.image publish failed (best-effort)", "error", err)
	}
}

// GetManifest retrieves a manifest by digest or tag reference.
func (r *Registry) GetManifest(ctx context.Context, tenantID, repoID, reference string) (*metadatav1.Manifest, error) {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// resolve tag → digest if needed
	if !digestRE.MatchString(reference) {
		tag, err := r.metadata.GetTag(ctx5, &metadatav1.GetTagRequest{
			RepoId:   repoID,
			TenantId: tenantID,
			Name:     reference,
		})
		if err != nil {
			if isGRPCNotFound(err) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("get tag rpc: %w", err)
		}
		reference = tag.GetManifestDigest()
	}

	m, err := r.metadata.GetManifest(ctx5, &metadatav1.GetManifestRequest{
		RepoId:    repoID,
		TenantId:  tenantID,
		Reference: reference,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get manifest rpc: %w", err)
	}

	// Signed-image admission (futures.md Tier 1 #3). Check AFTER the
	// manifest fetch — we only want to gate manifests that actually
	// exist (a 404 from above should stay a 404, not flip to a
	// "signature required" message that confirms the digest exists).
	// Repo-fetch failures here fail OPEN (warn + allow) for the same
	// reason as checkTagImmutable: a transient metadata blip shouldn't
	// suddenly block every pull from every repo.
	if err := r.checkSignatureAdmission(ctx5, tenantID, repoID, m.GetDigest()); err != nil {
		return nil, err
	}

	// CVSS-gated admission (futures.md FUT-021). Same "after manifest
	// fetch" ordering rationale as the signature check — a 404 stays a
	// 404. Ordered AFTER the signature check so a repo that requires
	// both signed AND scan-clean images surfaces the signature error
	// first (the signature gate is the more "structural" policy — no
	// signature at all is a bigger red flag than a HIGH finding).
	if err := r.checkCVSSAdmission(ctx5, tenantID, repoID, m.GetDigest()); err != nil {
		return nil, err
	}
	return m, nil
}

// checkCVSSAdmission rejects pulls from repos with a non-null
// `max_cvss_score` when the latest scan result for the manifest carries
// a top CVSS score that exceeds the threshold. Returns
// ErrCVSSThresholdExceeded (wrapped with numeric context) on rejection
// so the HTTP layer can map to 403 DENIED with an operator-actionable
// body.
//
// Load-bearing invariants (see TestCheckCVSSAdmission_* in
// registry_test.go):
//  1. Repo fetch fails (metadata blip)     → warn + allow (fail-OPEN).
//  2. max_cvss_score is null (default)     → allow (no gate).
//  3. Scan result missing (NotFound)       → info + allow (fail-OPEN,
//     first pull of a manifest before the scanner catches up).
//  4. Scan RPC unreachable                 → warn + allow (fail-OPEN;
//     operators can flip to fail-CLOSED via env in a follow-up).
//  5. top_cvss > threshold                 → deny (fail-CLOSED).
//  6. top_cvss == threshold                → allow (`>` not `>=`).
//
// CVSS derivation for v1: the scanner's plugin.Finding shape does not
// yet carry a numeric CVSS score, so we derive top CVSS from the
// SeverityCounts map using standard v3.1 band midpoints (LOW=39,
// MEDIUM=69, HIGH=89, CRITICAL=100). This means:
//
//	threshold 100 → blocks nothing (opt-in default)
//	threshold  89 → blocks CRITICAL only
//	threshold  69 → blocks HIGH + CRITICAL
//	threshold  39 → blocks MEDIUM + HIGH + CRITICAL
//
// The plugin.Finding.CVSS numeric field is a Phase 2 follow-up; once
// findings carry the raw score, this function switches to reading the
// JSON directly.
func (r *Registry) checkCVSSAdmission(ctx context.Context, tenantID, repoID, manifestDigest string) error {
	repo, err := r.metadata.GetRepository(ctx, &metadatav1.GetRepositoryRequest{
		RepoId:   repoID,
		TenantId: tenantID,
	})
	if err != nil {
		// Same fail-OPEN posture as checkSignatureAdmission's repo lookup.
		slog.WarnContext(ctx, "cvss admission: GetRepository failed (failing open)",
			"err", err, "repo_id", repoID,
		)
		return nil
	}
	// max_cvss_score is a proto Int32Value wrapper — nil means the
	// column is NULL (no gate configured). This is the common case and
	// short-circuits with zero extra work.
	if repo.GetMaxCvssScore() == nil {
		return nil
	}
	threshold := repo.GetMaxCvssScore().GetValue()

	scan, err := r.metadata.GetScanResult(ctx, &metadatav1.GetScanResultRequest{
		TenantId:       tenantID,
		RepoId:         repoID,
		ManifestDigest: manifestDigest,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			// First-time pull on this manifest, or the scanner hasn't
			// finished the initial scan yet. Fail-OPEN so operators
			// don't block their own CI on scanner queue depth. Logged
			// at Info because it's expected transient state, not an
			// error worth an operator page.
			slog.InfoContext(ctx, "cvss admission: no scan result yet, allowing pull",
				"repo_id", repoID, "manifest_digest", manifestDigest,
				"threshold", threshold,
			)
			return nil
		}
		// Scanner / metadata reachable failure — fail-OPEN like the
		// signature admission path. Logged at Warn because a persistent
		// failure here means the gate is degraded.
		slog.WarnContext(ctx, "cvss admission: GetScanResult failed (failing open)",
			"err", err, "repo_id", repoID, "manifest_digest", manifestDigest,
		)
		return nil
	}

	topCVSS := topCVSSFromSeverity(scan.GetSeverityCounts())
	if topCVSS > threshold {
		slog.WarnContext(ctx, "cvss admission: rejecting pull, top CVSS exceeds threshold",
			"repo_id", repoID, "manifest_digest", manifestDigest,
			"top_cvss", topCVSS, "threshold", threshold,
		)
		// Wrap with %w so callers can errors.Is(err, ErrCVSSThresholdExceeded)
		// AND read the numeric context via err.Error(). The HTTP layer
		// includes err.Error() in the response body so CI tooling can
		// decide next steps (waive? patch? rebuild?).
		return fmt.Errorf("%w: top CVSS %d exceeds threshold %d",
			ErrCVSSThresholdExceeded, topCVSS, threshold)
	}
	return nil
}

// topCVSSFromSeverity derives a top CVSS integer (0-100) from the
// scanner's SeverityCounts map using standard v3.1 band midpoints.
// Only counts a band when it has at least one finding — an all-zero
// map produces 0 (clean scan, always allows).
//
// Kept as a package-private helper so unit tests can pin the mapping
// without a running scanner. The chosen midpoints intentionally sit
// BELOW the band ceilings so a threshold set at the ceiling ALLOWS the
// band (e.g. threshold=89 lets HIGH through because 89 > 89 is false).
func topCVSSFromSeverity(counts map[string]int32) int32 {
	if counts["CRITICAL"] > 0 {
		return 100
	}
	if counts["HIGH"] > 0 {
		return 89
	}
	if counts["MEDIUM"] > 0 {
		return 69
	}
	if counts["LOW"] > 0 {
		return 39
	}
	return 0
}

// checkSignatureAdmission rejects pulls from repos with
// `require_signature=true` when the manifest has no recorded
// signatures (Phase 1) — or, when the repo has a non-empty trusted-key
// allowlist, when none of the recorded signatures came from an
// allowed key_id (Phase 2). Returns ErrSignatureRequired on
// rejection so the HTTP layer can map to 403 DENIED.
//
// Allows the pull in four "no policy / no service" scenarios:
//  1. Repo fetch fails (metadata blip) → warn + allow.
//  2. Repo.require_signature is false (the default) → allow.
//  3. Signer not wired (CORE_SIGNER_GRPC_ADDR unset in dev) → warn + allow.
//  4. Trusted-keys fetch fails (metadata blip on a separate read) → warn + allow.
//
// Phase 2 contract (empty allowlist = Phase 1 fallback):
//   - allowlist=[]                  → any signature passes
//   - allowlist=[k1, k2], sig=k1   → pass
//   - allowlist=[k1, k2], sig=k3   → reject (key not approved)
//   - allowlist=[k1], sig=none     → reject (no signature at all)
//
// Futures.md Tier 1 #3 — Signed-image admission (Phase 1 + Phase 2).
func (r *Registry) checkSignatureAdmission(ctx context.Context, tenantID, repoID, manifestDigest string) error {
	repo, err := r.metadata.GetRepository(ctx, &metadatav1.GetRepositoryRequest{
		RepoId:   repoID,
		TenantId: tenantID,
	})
	if err != nil {
		slog.WarnContext(ctx, "signature admission: GetRepository failed (failing open)",
			"err", err, "repo_id", repoID,
		)
		return nil
	}
	if !repo.GetRequireSignature() {
		return nil
	}
	if r.signer == nil {
		// Dev-stack convenience — see Registry struct comment for the
		// rationale. Production must always wire the signer dial; the
		// boot path can hard-fail when `CORE_SIGNER_GRPC_ADDR` is unset
		// and the operator wants the gate to be load-bearing.
		slog.WarnContext(ctx, "signature admission: signer not wired — allowing pull",
			"repo_id", repoID, "manifest_digest", manifestDigest,
		)
		return nil
	}
	sigs, err := r.signer.ListSignatures(ctx, &signerv1.ListSignaturesRequest{
		TenantId:       tenantID,
		ManifestDigest: manifestDigest,
	})
	if err != nil {
		// Signer reachable failure — fail OPEN like the metadata path.
		// Logging the error gives operators a signal that the gate is
		// degraded; rejecting every pull would be worse than briefly
		// missing the check.
		slog.WarnContext(ctx, "signature admission: ListSignatures failed (failing open)",
			"err", err, "manifest_digest", manifestDigest,
		)
		return nil
	}
	if len(sigs.GetSignatures()) == 0 {
		slog.WarnContext(ctx, "signature admission: rejecting unsigned manifest",
			"repo_id", repoID, "manifest_digest", manifestDigest,
		)
		return ErrSignatureRequired
	}

	// Phase 2 — narrow the gate to the trusted-key allowlist when the
	// repo has one. Empty list means "any signature passes" by design
	// so an operator can flip require_signature on first and pin keys
	// incrementally without breaking pulls in the gap.
	trusted, err := r.metadata.ListRepositoryTrustedKeys(ctx, &metadatav1.ListRepositoryTrustedKeysRequest{
		TenantId: tenantID,
		RepoId:   repoID,
	})
	if err != nil {
		slog.WarnContext(ctx, "signature admission: ListRepositoryTrustedKeys failed (failing open)",
			"err", err, "repo_id", repoID,
		)
		return nil
	}
	if len(trusted.GetKeys()) == 0 {
		// Phase 1 fallback — any signature passes. We already confirmed
		// at least one signature exists above, so we're done.
		return nil
	}
	approved := make(map[string]struct{}, len(trusted.GetKeys()))
	for _, k := range trusted.GetKeys() {
		approved[k.GetKeyId()] = struct{}{}
	}
	for _, s := range sigs.GetSignatures() {
		if _, ok := approved[s.GetKeyId()]; ok {
			return nil
		}
	}
	// Signed, but not by any approved key. Collect the seen key_ids so
	// the operator gets actionable feedback in the registry-core logs
	// — the rejection body is generic enough not to leak the
	// allowlist over the wire, but the log line is for them.
	seen := make([]string, 0, len(sigs.GetSignatures()))
	for _, s := range sigs.GetSignatures() {
		seen = append(seen, s.GetKeyId())
	}
	slog.WarnContext(ctx, "signature admission: rejecting — no signature from a trusted key",
		"repo_id", repoID, "manifest_digest", manifestDigest,
		"approved_count", len(approved), "seen_key_ids", seen,
	)
	return ErrSignatureRequired
}

// DeleteManifest removes a manifest by digest, or only a tag by name.
// Per OCI spec §4.4: when the reference is a tag, ONLY the tag is deleted;
// the underlying manifest remains accessible by digest.
func (r *Registry) DeleteManifest(ctx context.Context, tenantID, repoID, reference string) error {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if !digestRE.MatchString(reference) {
		if _, err := r.metadata.DeleteTag(ctx5, &metadatav1.DeleteTagRequest{
			RepoId:   repoID,
			TenantId: tenantID,
			Name:     reference,
		}); err != nil {
			if isGRPCNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("delete tag rpc: %w", err)
		}
		return nil
	}

	if _, err := r.metadata.DeleteManifest(ctx5, &metadatav1.DeleteManifestRequest{
		RepoId:   repoID,
		TenantId: tenantID,
		Digest:   reference,
	}); err != nil {
		if isGRPCNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("delete manifest rpc: %w", err)
	}
	return nil
}

// --- Tags ---

// ListTags returns all tag names for the repository, with pagination.
func (r *Registry) ListTags(ctx context.Context, tenantID, repoID string, n int32, last string) ([]string, error) {
	stream, err := r.metadata.ListTags(ctx, &metadatav1.ListTagsRequest{
		RepoId:    repoID,
		TenantId:  tenantID,
		PageSize:  n,
		PageToken: last,
	})
	if err != nil {
		return nil, fmt.Errorf("list tags rpc: %w", err)
	}

	var tags []string
	for {
		tag, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("recv tag: %w", err)
		}
		tags = append(tags, tag.GetName())
	}
	return tags, nil
}

// --- Repository ---

// GetRepository looks up a repository by tenant + "org/repo" name without creating it.
// Returns ErrNotFound when the repository does not exist — read paths and delete paths
// must use this so an unauthorized or speculative request cannot silently populate the
// metadata table with empty repositories under arbitrary names.
func (r *Registry) GetRepository(ctx context.Context, tenantID, repoName string) (*metadatav1.Repository, error) {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	repo, err := r.metadata.GetRepositoryByName(ctx5, &metadatav1.GetRepositoryByNameRequest{
		TenantId: tenantID,
		Name:     repoName,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get repository by name rpc: %w", err)
	}
	return repo, nil
}

// GetOrCreateRepository returns the repo from metadata, creating it if it doesn't exist.
// Only call this on the manifest PUT path — the act of pushing a manifest is what
// brings a repository into existence. Read and delete paths must use GetRepository.
func (r *Registry) GetOrCreateRepository(ctx context.Context, tenantID, repoName string) (*metadatav1.Repository, error) {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// CreateRepository is idempotent in the metadata service — when the row already
	// exists (per the (org_id, name) unique constraint) the existing record is returned.
	repo, err := r.metadata.CreateRepository(ctx5, &metadatav1.CreateRepositoryRequest{
		TenantId: tenantID,
		Name:     repoName,
		IsPublic: false,
	})
	if err != nil {
		return nil, fmt.Errorf("create repository rpc: %w", err)
	}
	return repo, nil
}

// CheckQuota verifies the tenant has enough remaining quota for uploadSize bytes.
func (r *Registry) CheckQuota(ctx context.Context, tenantID string, uploadSize int64) error {
	ctx5, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	usage, err := r.metadata.GetTenantQuotaUsage(ctx5, &metadatav1.GetTenantQuotaUsageRequest{
		TenantId: tenantID,
	})
	if err != nil {
		// fail open if quota service unreachable — don't block pushes
		return nil
	}
	if usage.GetUsedBytes()+uploadSize > usage.GetQuotaBytes() {
		return ErrQuotaExceeded
	}
	return nil
}

// isGRPCNotFound returns true if the gRPC error is a NotFound status.
func isGRPCNotFound(err error) bool {
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.NotFound
	}
	return false
}

// checkTagImmutable rejects a manifest push that would move an existing
// tag whose parent repository is marked immutable_tags OR whose own
// `immutable` flag is set. Returns ErrTagImmutable in either case so
// the HTTP layer can map to MANIFEST_INVALID per OCI Distribution
// Spec § 4.2.2.
//
// New-tag pushes (the tag doesn't exist yet) are allowed regardless of
// the repo flag — immutability gates re-writes, not initial pushes.
//
// Pushing the SAME digest to an already-present tag is also allowed
// (idempotent — a re-push of an unchanged content fingerprint is a
// no-op and not the dangerous "silently change what consumers pull"
// case the flag protects against).
//
// Repo-fetch failures fail OPEN with a warn log — the alternative
// (rejecting every push because metadata blipped) is worse than the
// occasional missed immutability check during a metadata outage.
// Tag-not-found is the new-tag case and returns nil.
//
// Futures.md Tier 1 #2 — Tag immutability + image promotion workflow.
func (r *Registry) checkTagImmutable(ctx context.Context, tenantID, repoID, tagName, newDigest string) error {
	// First: look up the existing tag. NotFound = new tag = allowed.
	tag, err := r.metadata.GetTag(ctx, &metadatav1.GetTagRequest{
		RepoId:   repoID,
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			return nil
		}
		// Metadata reachability failure — fail OPEN to avoid blocking
		// every push when the metadata service blips. Log so the issue
		// surfaces in monitoring.
		slog.WarnContext(ctx, "tag immutability check: GetTag failed (failing open)",
			"err", err, "repo_id", repoID, "tag", tagName,
		)
		return nil
	}

	// Idempotent re-push of the same digest is always allowed — the tag
	// isn't being "moved" in the operator-visible sense.
	if tag.GetManifestDigest() == newDigest {
		return nil
	}

	// Per-tag pin wins regardless of repo flag.
	if tag.GetImmutable() {
		slog.WarnContext(ctx, "tag push rejected by per-tag immutability pin",
			"repo_id", repoID, "tag", tagName, "existing_digest", tag.GetManifestDigest(), "new_digest", newDigest,
		)
		return ErrTagImmutable
	}

	// Otherwise, check the repo-wide flag. One extra RPC per push when
	// no per-tag pin caught it — but the path is only entered on tag
	// moves (skipped when the same-digest fast-path matched above), so
	// the steady-state cost is zero.
	repo, err := r.metadata.GetRepository(ctx, &metadatav1.GetRepositoryRequest{
		RepoId:   repoID,
		TenantId: tenantID,
	})
	if err != nil {
		// Same fail-open rationale as the tag lookup above.
		slog.WarnContext(ctx, "tag immutability check: GetRepository failed (failing open)",
			"err", err, "repo_id", repoID,
		)
		return nil
	}
	if repo.GetImmutableTags() {
		slog.WarnContext(ctx, "tag push rejected by repo immutable_tags flag",
			"repo_id", repoID, "tag", tagName, "existing_digest", tag.GetManifestDigest(), "new_digest", newDigest,
		)
		return ErrTagImmutable
	}
	return nil
}

// extractConfigMediaType pulls `config.mediaType` out of an OCI manifest
// doc. Mirrors `services/metadata/internal/repository.parseConfigMediaType`
// — duplicated rather than imported because services/core sits above
// metadata in the dependency graph and we don't want to import a sister
// service's internal package.
//
// Returns "" on parse failure or missing fields; the caller's
// deriveArtifactType handles empty by emitting "" on the wire (which
// downstream consumers treat as unknown).
func extractConfigMediaType(rawJSON []byte) string {
	if len(rawJSON) == 0 {
		return ""
	}
	var doc struct {
		Config *struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
	}
	if err := json.Unmarshal(rawJSON, &doc); err != nil {
		return ""
	}
	if doc.Config == nil {
		return ""
	}
	return doc.Config.MediaType
}

// deriveArtifactType maps a raw `config.mediaType` to the stable
// discriminator the rest of the platform consumes. Source-of-truth
// mapping lives in services/metadata — keep these two in lockstep when
// a new artifact category appears.
//
// S-MAINT-1 Batch 5 (P6): emitted on PushCompletedPayload so the scanner
// can skip Helm / cosign / SBOM artifacts before enqueueing.
func deriveArtifactType(configMediaType string) string {
	if configMediaType == "" {
		return ""
	}
	switch configMediaType {
	case "application/vnd.docker.container.image.v1+json",
		"application/vnd.oci.image.config.v1+json":
		return "image"
	case "application/vnd.cncf.helm.config.v1+json":
		return "helm"
	case "application/vnd.dev.cosign.simplesigning.v1+json",
		"application/vnd.dsse.envelope.v1+json":
		return "signature"
	case "application/spdx+json",
		"application/vnd.cyclonedx+json":
		return "sbom"
	default:
		return "other"
	}
}
