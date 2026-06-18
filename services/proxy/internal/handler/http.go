package handler

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
	aescrypto "github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/proxy/internal/repository"
	"github.com/steveokay/oci-janus/services/proxy/internal/upstream"
)

// digestRE validates OCI/Docker content digests per the OCI Distribution Spec.
// Only sha256 digests with exactly 64 lowercase hex characters are accepted.
// This mirrors the same pattern used in registry-core.
var digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// HTTPHandler serves the OCI pull-through cache HTTP API.
type HTTPHandler struct {
	repo      *repository.Repository
	auth      *authClient
	storage   storagev1.StorageServiceClient
	upstream  *upstream.Client
	pub       *publisher.Publisher // nil when RabbitMQ is not configured
	key       []byte               // 32-byte AES key for credential decryption
	authRealm string               // URL Docker clients use to fetch tokens (AUTH_REALM)
}

// NewHTTPHandler constructs the pull-through cache HTTP handler.
// pub may be nil; when nil, failed background stores are only logged and not retried.
func NewHTTPHandler(
	repo *repository.Repository,
	authConn *grpc.ClientConn,
	rdb *redis.Client,
	storageConn *grpc.ClientConn,
	upstreamClient *upstream.Client,
	pub *publisher.Publisher,
	credentialKeyHex string,
	authRealm string,
) (*HTTPHandler, error) {
	key, err := hexToKey(credentialKeyHex)
	if err != nil {
		return nil, err
	}
	return &HTTPHandler{
		repo:      repo,
		auth:      newAuthClient(authConn, rdb),
		storage:   storagev1.NewStorageServiceClient(storageConn),
		upstream:  upstreamClient,
		pub:       pub,
		key:       key,
		authRealm: authRealm,
	}, nil
}

// Register mounts all proxy routes onto mux.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v2/", h.dispatch)
}

// dispatch routes /v2/cache/<upstream>/<image.../manifests/<ref>
//                  and /v2/cache/<upstream>/<image.../blobs/<digest>
func (h *HTTPHandler) dispatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")

	path := strings.TrimPrefix(r.URL.Path, "/v2")
	path = strings.Trim(path, "/")

	if path == "" {
		// OCI version check — require valid auth.
		claims, err := h.authenticate(r)
		if err != nil || claims == nil {
			h.challengeAuth(w, r)
			return
		}
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		return
	}

	segments := strings.Split(path, "/")
	n := len(segments)

	// All proxy paths start with "cache/<upstreamName>/..."
	if n < 4 || segments[0] != "cache" {
		ociErr(w, http.StatusNotFound, "NAME_UNKNOWN", "not a proxy cache path")
		return
	}

	upstreamName := segments[1]

	// Require auth for all proxy operations.
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w, r)
		return
	}

	tenantID, err := uuid.Parse(claims.tenantID)
	if err != nil {
		ociErr(w, http.StatusInternalServerError, "INTERNAL", "invalid tenant context")
		return
	}

	// Look up upstream registry config.
	upstream, err := h.repo.GetUpstreamByName(r.Context(), tenantID, upstreamName)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			ociErr(w, http.StatusNotFound, "NAME_UNKNOWN", "upstream registry not configured")
			return
		}
		ociErr(w, http.StatusInternalServerError, "INTERNAL", "lookup upstream")
		return
	}

	// Remaining segments after "cache/<upstreamName>": <image...>/<operation>/<ref>
	// e.g. ["library","ubuntu","manifests","22.04"] or ["library","ubuntu","blobs","sha256:..."]
	rest := segments[2:]
	rn := len(rest)

	switch {
	case rn >= 3 && rest[rn-2] == "manifests":
		image := strings.Join(rest[:rn-2], "/")
		ref := rest[rn-1]
		switch r.Method {
		case http.MethodGet:
			h.handleGetManifest(w, r, upstream, tenantID, image, ref)
		case http.MethodHead:
			h.handleHeadManifest(w, r, upstream, tenantID, image, ref)
		default:
			ociErr(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		}

	case rn >= 3 && rest[rn-2] == "blobs":
		image := strings.Join(rest[:rn-2], "/")
		digest := rest[rn-1]
		switch r.Method {
		case http.MethodGet:
			h.handleGetBlob(w, r, upstream, tenantID, image, digest)
		case http.MethodHead:
			h.handleHeadBlob(w, r, tenantID, digest)
		default:
			ociErr(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		}

	default:
		ociErr(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown proxy cache path")
	}
}

// handleGetManifest serves a cached manifest or fetches it from upstream.
func (h *HTTPHandler) handleGetManifest(w http.ResponseWriter, r *http.Request, up *repository.UpstreamRecord, tenantID uuid.UUID, image, reference string) {
	// Try cache first.
	cached, err := h.repo.GetManifest(r.Context(), tenantID, up.UpstreamID, image, reference, up.TTLSeconds)
	if err == nil {
		w.Header().Set("Content-Type", cached.MediaType)
		w.Header().Set("Docker-Content-Digest", cached.Digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(cached.Body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached.Body)
		return
	}
	if !errors.Is(err, repository.ErrNotFound) {
		slog.Error("manifest cache lookup failed", "err", err)
	}

	// Cache miss — fetch from upstream.
	creds := h.upstreamCreds(up)
	result, err := h.upstream.FetchManifest(r.Context(), up.URL, image, reference, creds)
	if err != nil {
		if errors.Is(err, upstream.ErrNotFound) {
			ociErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found in upstream")
			return
		}
		slog.Error("upstream manifest fetch failed", "err", err, "upstream", up.Name)
		ociErr(w, http.StatusBadGateway, "UPSTREAM_ERROR", "upstream fetch failed")
		return
	}

	// Serve to client immediately.
	w.Header().Set("Content-Type", result.MediaType)
	w.Header().Set("Docker-Content-Digest", result.Digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(result.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Body)

	// Cache in background — do not block client.
	go h.cacheManifest(tenantID, up.UpstreamID, image, reference, result)
}

// handleHeadManifest checks cache and falls through to upstream if stale.
func (h *HTTPHandler) handleHeadManifest(w http.ResponseWriter, r *http.Request, up *repository.UpstreamRecord, tenantID uuid.UUID, image, reference string) {
	cached, err := h.repo.GetManifest(r.Context(), tenantID, up.UpstreamID, image, reference, up.TTLSeconds)
	if err == nil {
		w.Header().Set("Content-Type", cached.MediaType)
		w.Header().Set("Docker-Content-Digest", cached.Digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(cached.Body)))
		w.WriteHeader(http.StatusOK)
		return
	}

	// Fetch from upstream to check existence.
	creds := h.upstreamCreds(up)
	result, err := h.upstream.FetchManifest(r.Context(), up.URL, image, reference, creds)
	if err != nil {
		if errors.Is(err, upstream.ErrNotFound) {
			ociErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found in upstream")
			return
		}
		ociErr(w, http.StatusBadGateway, "UPSTREAM_ERROR", "upstream fetch failed")
		return
	}

	w.Header().Set("Content-Type", result.MediaType)
	w.Header().Set("Docker-Content-Digest", result.Digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(result.Body)))
	w.WriteHeader(http.StatusOK)

	go h.cacheManifest(tenantID, up.UpstreamID, image, reference, result)
}

// handleGetBlob streams a blob from cache storage or fetches from upstream.
func (h *HTTPHandler) handleGetBlob(w http.ResponseWriter, r *http.Request, up *repository.UpstreamRecord, tenantID uuid.UUID, image, digest string) {
	// SEC-013: Validate digest format before doing any work.
	// Rejects malformed or path-traversal-style digest strings before they
	// reach storage or upstream calls.
	if !digestRE.MatchString(digest) {
		ociErr(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest format")
		return
	}

	key := blobKey(tenantID.String(), digest)

	// Check if we already have it in storage.
	existResp, err := h.storage.BlobExists(r.Context(), &storagev1.BlobExistsRequest{
		Key:      key,
		TenantId: tenantID.String(),
	})
	if err == nil && existResp.GetExists() {
		// Serve from storage.
		h.streamBlobFromStorage(w, r.Context(), key, tenantID.String(), digest)
		return
	}

	// Fetch from upstream — stream to client AND store in background.
	creds := h.upstreamCreds(up)
	rc, size, ct, err := h.upstream.FetchBlob(r.Context(), up.URL, image, digest, creds)
	if err != nil {
		if errors.Is(err, upstream.ErrNotFound) {
			ociErr(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found in upstream")
			return
		}
		slog.Error("upstream blob fetch failed", "err", err, "upstream", up.Name)
		ociErr(w, http.StatusBadGateway, "UPSTREAM_ERROR", "upstream fetch failed")
		return
	}
	defer rc.Close()

	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Docker-Content-Digest", digest)
	if size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}
	w.WriteHeader(http.StatusOK)

	// Pipe the upstream body to both the HTTP client and the background store goroutine.
	// TeeReader splits the stream: io.Copy drives the read, tee writes each byte to pw,
	// and the background goroutine reads from pr.
	pr, pw := io.Pipe()
	tee := io.TeeReader(rc, pw)

	// SEC-028: Detach from request context intentionally — this goroutine caches the
	// blob after the client response is complete. Using context.WithoutCancel ensures
	// the store is not abandoned if the HTTP request context is cancelled at response end.
	// A 30-second outer bound (applied inside storeBlobFromReader) prevents goroutine leaks.
	bgCtx := context.WithoutCancel(r.Context())
	go func() {
		if err := h.storeBlobFromReader(bgCtx, key, tenantID.String(), ct, pr); err != nil {
			slog.Error("background blob store failed, queuing retry", "err", err, "digest", digest)
			// Publish a durable retry event so the consumer can re-fetch from upstream.
			h.publishStoreQueued(bgCtx, tenantID.String(), up.Name, image, digest)
		}
	}()

	// SEC-012: Capture the copy error and propagate it to the pipe.
	// If the client disconnects mid-stream, io.Copy returns a non-nil error.
	// Calling pw.CloseWithError signals the background goroutine via the pipe that
	// the stream is broken and it must NOT call CloseAndRecv (which would commit a
	// truncated blob under the correct digest key in storage).
	// If the copy succeeds, pw.Close sends a clean EOF so the goroutine can finalise.
	_, copyErr := io.Copy(w, tee)
	if copyErr != nil {
		// Client disconnected mid-stream: poison the pipe so storeBlobFromReader
		// returns an error, logs it, and queues a retry rather than storing garbage.
		slog.Debug("client connection closed during blob stream", "err", copyErr)
		pw.CloseWithError(copyErr)
	} else {
		// Clean stream end: let the background goroutine drain the pipe and commit.
		pw.Close()
	}
}

// handleHeadBlob checks whether a blob is accessible in local storage.
func (h *HTTPHandler) handleHeadBlob(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, digest string) {
	// SEC-013: Validate digest format before making any storage calls.
	// Prevents malformed digests from being forwarded to the storage service.
	if !digestRE.MatchString(digest) {
		ociErr(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest format")
		return
	}

	key := blobKey(tenantID.String(), digest)

	existResp, err := h.storage.BlobExists(r.Context(), &storagev1.BlobExistsRequest{
		Key:      key,
		TenantId: tenantID.String(),
	})
	if err == nil && existResp.GetExists() {
		stat, serr := h.storage.StatBlob(r.Context(), &storagev1.StatBlobRequest{
			Key:      key,
			TenantId: tenantID.String(),
		})
		if serr == nil {
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.GetSize()))
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	ociErr(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
}

// streamBlobFromStorage sends a blob already in storage to the client.
func (h *HTTPHandler) streamBlobFromStorage(w http.ResponseWriter, ctx context.Context, key, tenantID, digest string) {
	stream, err := h.storage.GetBlob(ctx, &storagev1.GetBlobRequest{
		Key:      key,
		TenantId: tenantID,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			ociErr(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
			return
		}
		ociErr(w, http.StatusInternalServerError, "INTERNAL", "storage error")
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Error("blob stream recv error", "err", err)
			return
		}
		if _, werr := w.Write(chunk.GetChunk()); werr != nil {
			return
		}
	}
}

// storeBlobFromReader sends data from r to the storage service via PutBlob.
// SEC-012: If r is an io.Pipe reader and the writer was closed with an error
// (i.e. the HTTP client disconnected mid-stream), Read returns that error here.
// In that case we must NOT call CloseAndRecv, because doing so would commit a
// truncated blob under the correct digest key. Instead we return the error
// immediately so the caller can queue a retry.
func (h *HTTPHandler) storeBlobFromReader(ctx context.Context, key, tenantID, contentType string, r io.Reader) error {
	stream, err := h.storage.PutBlob(ctx)
	if err != nil {
		return fmt.Errorf("open put-blob stream: %w", err)
	}

	// Send the metadata frame first so the storage service knows the target key.
	if err := stream.Send(&storagev1.PutBlobRequest{
		Data: &storagev1.PutBlobRequest_Meta{
			Meta: &storagev1.PutBlobMeta{
				Key:         key,
				ContentType: contentType,
				TenantId:    tenantID,
			},
		},
	}); err != nil {
		return fmt.Errorf("send put-blob meta: %w", err)
	}

	buf := make([]byte, 64*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if err := stream.Send(&storagev1.PutBlobRequest{
				Data: &storagev1.PutBlobRequest_Chunk{Chunk: buf[:n]},
			}); err != nil {
				return fmt.Errorf("send blob chunk: %w", err)
			}
		}
		if rerr == io.EOF {
			// Clean end of stream: fall through to CloseAndRecv to commit.
			break
		}
		if rerr != nil {
			// SEC-012: Non-EOF read error means the source pipe was closed with an
			// error (client disconnected). Drain is not needed because io.Pipe
			// discards further reads after CloseWithError. Do NOT call CloseAndRecv
			// here — that would finalise a partial blob in storage under the full
			// digest key, which would cause incorrect cache hits for future pulls.
			return fmt.Errorf("read blob: %w", rerr)
		}
	}

	// Only reached on clean EOF — safe to commit the blob.
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("close put-blob stream: %w", err)
	}
	return nil
}

// cacheManifest stores a fetched manifest in the DB cache (background).
// SEC-028: context.Background() is used intentionally here — the caller has already
// written the HTTP response and its request context may be cancelled. The 30-second
// timeout prevents this goroutine from leaking if the database is slow.
func (h *HTTPHandler) cacheManifest(tenantID, upstreamID uuid.UUID, image, reference string, result *upstream.ManifestResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.repo.UpsertManifest(ctx, tenantID, upstreamID, image, reference,
		result.Digest, result.MediaType, result.Body); err != nil {
		slog.Error("cache manifest upsert failed", "err", err)
	}
}

// upstreamCreds decrypts the credentials for the given upstream record.
func (h *HTTPHandler) upstreamCreds(up *repository.UpstreamRecord) upstream.Credentials {
	creds := upstream.Credentials{Type: up.AuthType, Username: up.Username}
	if len(up.PasswordEnc) > 0 {
		plain, err := aescrypto.Decrypt(up.PasswordEnc, h.key)
		if err != nil {
			slog.Error("decrypt upstream credentials failed", "upstream", up.Name)
		} else {
			creds.Password = string(plain)
		}
	}
	return creds
}

// blobKey returns the storage key for a blob — mirrors the core service layout.
func blobKey(tenantID, digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	return fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, hex[:2], hex)
}

// --- RabbitMQ store.queued helpers ---

// publishStoreQueued emits a store.queued event so the consumer can retry a
// failed blob cache write. No-op when the publisher is not configured.
func (h *HTTPHandler) publishStoreQueued(ctx context.Context, tenantID, upstreamName, image, blobDigest string) {
	if h.pub == nil {
		return
	}
	payload, _ := json.Marshal(events.StoreQueuedPayload{
		TenantID:     tenantID,
		UpstreamName: upstreamName,
		BlobDigest:   blobDigest,
		Image:        image,
	})
	if err := h.pub.Publish(ctx, events.RoutingStoreQueued, events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingStoreQueued,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}); err != nil {
		slog.ErrorContext(ctx, "publish store.queued failed", "error", err, "blob_digest", blobDigest)
	}
}

// HandleStoreQueued is the consumer.Handler for store.queued events.
// It re-fetches the blob from the upstream registry and writes it to storage.
// Returning a non-nil error causes the consumer to NACK and retry (up to MaxRetries).
func (h *HTTPHandler) HandleStoreQueued(ctx context.Context, event events.Event) error {
	var payload events.StoreQueuedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal store.queued payload: %w", err)
	}
	if payload.BlobDigest == "" || payload.Image == "" {
		// Manifest-level store events — not handled here.
		return nil
	}
	return h.retryStoreBlob(ctx, payload)
}

// retryStoreBlob re-fetches a blob from the upstream registry and stores it.
// Called by HandleStoreQueued on consumer retry; uses a fresh HTTP connection so
// no state from the original failed attempt is carried over.
func (h *HTTPHandler) retryStoreBlob(ctx context.Context, payload events.StoreQueuedPayload) error {
	tenantID, err := uuid.Parse(payload.TenantID)
	if err != nil {
		return fmt.Errorf("parse tenant id %q: %w", payload.TenantID, err)
	}
	up, err := h.repo.GetUpstreamByName(ctx, tenantID, payload.UpstreamName)
	if err != nil {
		return fmt.Errorf("get upstream %q: %w", payload.UpstreamName, err)
	}

	creds := h.upstreamCreds(up)
	rc, _, ct, err := h.upstream.FetchBlob(ctx, up.URL, payload.Image, payload.BlobDigest, creds)
	if err != nil {
		return fmt.Errorf("fetch blob from upstream: %w", err)
	}
	defer rc.Close()

	if ct == "" {
		ct = "application/octet-stream"
	}
	key := blobKey(payload.TenantID, payload.BlobDigest)
	return h.storeBlobFromReader(ctx, key, payload.TenantID, ct, rc)
}

// --- Auth helpers ---

type tokenClaims struct {
	userID   string
	tenantID string
}

type authClient struct {
	grpc  authv1.AuthServiceClient
	redis *redis.Client
}

func newAuthClient(conn *grpc.ClientConn, rdb *redis.Client) *authClient {
	return &authClient{grpc: authv1.NewAuthServiceClient(conn), redis: rdb}
}

func (a *authClient) validateBearer(ctx context.Context, token string) (*tokenClaims, error) {
	cacheKey := "jwt:valid:" + token
	if cached, err := a.redis.Get(ctx, cacheKey).Result(); err == nil {
		parts := strings.SplitN(cached, ":", 2)
		if len(parts) == 2 {
			return &tokenClaims{userID: parts[0], tenantID: parts[1]}, nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := a.grpc.ValidateToken(ctx, &authv1.ValidateTokenRequest{Token: token})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			return nil, errUnauthorized
		}
		return nil, fmt.Errorf("validate token rpc: %w", err)
	}
	if !resp.GetValid() {
		return nil, errUnauthorized
	}

	claims := &tokenClaims{
		userID:   resp.GetUserId(),
		tenantID: resp.GetTenantId(),
	}

	if exp := resp.GetExpiresAt(); exp != nil {
		ttl := time.Until(exp.AsTime())
		if ttl > 0 {
			_ = a.redis.Set(ctx, cacheKey, claims.userID+":"+claims.tenantID, ttl).Err()
		}
	}
	return claims, nil
}

func (a *authClient) validateAPIKey(ctx context.Context, keyID, secret string) (*tokenClaims, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := a.grpc.ValidateAPIKey(ctx, &authv1.ValidateAPIKeyRequest{
		KeyId:     keyID,
		RawSecret: secret,
	})
	if err != nil {
		return nil, fmt.Errorf("validate api key rpc: %w", err)
	}
	if !resp.GetValid() {
		return nil, errUnauthorized
	}
	return &tokenClaims{userID: resp.GetUserId(), tenantID: resp.GetTenantId()}, nil
}

var errUnauthorized = errors.New("unauthorized")

func (h *HTTPHandler) authenticate(r *http.Request) (*tokenClaims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, errUnauthorized
	}
	// PENTEST-013: case-insensitive scheme matching per RFC 7235.
	if token, ok := bearer.Extract(authHeader); ok {
		return h.auth.validateBearer(r.Context(), token)
	}
	if len(authHeader) > 6 && strings.EqualFold(authHeader[:6], "Basic ") {
		encoded := authHeader[6:]
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, errUnauthorized
			}
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, errUnauthorized
		}
		return h.auth.validateAPIKey(r.Context(), parts[0], parts[1])
	}
	return nil, errUnauthorized
}

func (h *HTTPHandler) challengeAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="registry-proxy"`, h.authRealm))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(ociErrBody("UNAUTHORIZED", "authentication required"))
}

// ociErr writes an OCI-compliant JSON error response.
func ociErr(w http.ResponseWriter, code int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ociErrBody(errCode, message))
}

func ociErrBody(code, message string) map[string]any {
	return map[string]any{
		"errors": []map[string]any{
			{"code": code, "message": message, "detail": nil},
		},
	}
}

func isGRPCNotFound(err error) bool {
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.NotFound
	}
	return false
}

func hexToKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode credential key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("credential key must be 32 bytes, got %d", len(key))
	}
	return key, nil
}
