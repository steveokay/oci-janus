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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aescrypto "github.com/steveokay/oci-janus/libs/crypto/aes"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/proxy/internal/repository"
	"github.com/steveokay/oci-janus/services/proxy/internal/upstream"
)

// HTTPHandler serves the OCI pull-through cache HTTP API.
type HTTPHandler struct {
	repo     *repository.Repository
	auth     *authClient
	storage  storagev1.StorageServiceClient
	upstream *upstream.Client
	key      []byte // 32-byte AES key for credential decryption
}

// NewHTTPHandler constructs the pull-through cache HTTP handler.
func NewHTTPHandler(
	repo *repository.Repository,
	authConn *grpc.ClientConn,
	rdb *redis.Client,
	storageConn *grpc.ClientConn,
	upstreamClient *upstream.Client,
	credentialKeyHex string,
) (*HTTPHandler, error) {
	key, err := hexToKey(credentialKeyHex)
	if err != nil {
		return nil, err
	}
	return &HTTPHandler{
		repo:     repo,
		auth:     newAuthClient(authConn, rdb),
		storage:  storagev1.NewStorageServiceClient(storageConn),
		upstream: upstreamClient,
		key:      key,
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

	// Tee to both client and background store pipe.
	pr, pw := io.Pipe()
	tee := io.TeeReader(rc, pw)

	bgCtx := context.WithoutCancel(r.Context())
	go func() {
		if err := h.storeBlobFromReader(bgCtx, key, tenantID.String(), ct, pr); err != nil {
			slog.Error("background blob store failed", "err", err, "digest", digest)
		}
	}()

	if _, err := io.Copy(w, tee); err != nil {
		slog.Debug("client connection closed during blob stream", "err", err)
	}
	// Signal pipe writer end so background goroutine can proceed.
	pw.Close()
}

// handleHeadBlob checks whether a blob is accessible in local storage.
func (h *HTTPHandler) handleHeadBlob(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, digest string) {
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
func (h *HTTPHandler) storeBlobFromReader(ctx context.Context, key, tenantID, contentType string, r io.Reader) error {
	stream, err := h.storage.PutBlob(ctx)
	if err != nil {
		return fmt.Errorf("open put-blob stream: %w", err)
	}

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
			break
		}
		if rerr != nil {
			return fmt.Errorf("read blob: %w", rerr)
		}
	}

	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("close put-blob stream: %w", err)
	}
	return nil
}

// cacheManifest stores a fetched manifest in the DB cache (background).
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
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		return h.auth.validateBearer(r.Context(), token)
	}
	if strings.HasPrefix(authHeader, "Basic ") {
		encoded := strings.TrimPrefix(authHeader, "Basic ")
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
	realm := "https://" + r.Host + "/v2/token"
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="registry-proxy"`, realm))
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
