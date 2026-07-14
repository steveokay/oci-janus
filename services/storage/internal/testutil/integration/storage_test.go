//go:build integration

// Package integration contains end-to-end gRPC tests for registry-storage.
// Each test starts a real MinIO container via testcontainers, wires the MinIO
// driver into the full handler stack, and serves it in-process over a bufconn
// listener so no real TCP port is required.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/storage/internal/driver"
	"github.com/steveokay/oci-janus/services/storage/internal/handler"
)

const (
	// bufSize is the in-memory buffer capacity for the bufconn gRPC listener.
	bufSize = 1024 * 1024 // 1 MiB

	// testTenant is the tenant id used by every request in this suite. Under
	// PENTEST-026 the handler requires a non-empty tenant_id and rejects any
	// key that does not live under "<root>/<tenant_id>/" — so every test key
	// below is constructed as "blobs/test-tenant/…" to match this value.
	testTenant = "test-tenant"
)

// buildTestEnv starts a MinIO container, creates the MinIO driver, wires it
// into the StorageHandler, and serves it in-process over a bufconn listener.
// All resources (container, gRPC server, connection) are registered with
// t.Cleanup — callers do not need to perform any teardown.
func buildTestEnv(t *testing.T) storagev1.StorageServiceClient {
	t.Helper()

	ctx := context.Background()

	// 1. Start a real MinIO container and obtain connection details.
	minioCfg := containers.MinIO(t)

	// 2. Create the MinIO storage driver backed by the test container.
	//    UseSSL=false because the container exposes a plain HTTP endpoint.
	drv, err := driver.NewMinIO(
		minioCfg.Endpoint,
		minioCfg.AccessKey,
		minioCfg.SecretKey,
		minioCfg.Bucket,
		"", // region not required for MinIO
		false,
	)
	if err != nil {
		t.Fatalf("driver.NewMinIO: %v", err)
	}

	// 3. Ping the driver — this also creates the bucket if it doesn't exist yet.
	if err := drv.Ping(ctx); err != nil {
		t.Fatalf("driver.Ping: %v", err)
	}

	// 4. Start an in-process gRPC server via bufconn — no TCP port needed.
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	storagev1.RegisterStorageServiceServer(srv, handler.New(drv))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 5. Dial the in-process server using lis.DialContext as the context dialer.
	//    Wrapped to match grpc.WithContextDialer's `func(ctx, target) (net.Conn, error)`
	//    signature — bufconn's DialContext takes only ctx.
	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return storagev1.NewStorageServiceClient(conn)
}

// ── Test: PutBlob / BlobExists / StatBlob round-trip ────────────────────────

// TestPutBlob_BlobExists_StatBlob_RoundTrip stores a small blob via the
// streaming PutBlob RPC, then verifies BlobExists returns true and StatBlob
// reports the correct byte count.
func TestPutBlob_BlobExists_StatBlob_RoundTrip(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	payload := []byte("hello registry storage")
	key := "blobs/test-tenant/sha256/ha/helloworld"

	mustPutBlob(t, client, key, payload)

	// BlobExists must return true.
	existsResp, err := client.BlobExists(ctx, &storagev1.BlobExistsRequest{Key: key, TenantId: testTenant})
	if err != nil {
		t.Fatalf("BlobExists: %v", err)
	}
	if !existsResp.Exists {
		t.Fatal("BlobExists: want true, got false")
	}

	// StatBlob must report the correct size.
	statResp, err := client.StatBlob(ctx, &storagev1.StatBlobRequest{Key: key, TenantId: testTenant})
	if err != nil {
		t.Fatalf("StatBlob: %v", err)
	}
	if statResp.Size != int64(len(payload)) {
		t.Fatalf("StatBlob.Size: want %d, got %d", len(payload), statResp.Size)
	}
}

// ── Test: PutBlob / GetBlob bytes match ──────────────────────────────────────

// TestPutBlob_GetBlob_BytesMatch stores a blob and then streams it back,
// verifying the reassembled bytes are identical to what was uploaded.
func TestPutBlob_GetBlob_BytesMatch(t *testing.T) {
	client := buildTestEnv(t)

	payload := []byte("the quick brown fox jumps over the lazy dog")
	key := "blobs/test-tenant/sha256/th/the-quick-fox"

	mustPutBlob(t, client, key, payload)

	// Stream the blob back.
	got := mustGetBlob(t, client, key)

	if !bytes.Equal(got, payload) {
		t.Fatalf("GetBlob content mismatch: got %q, want %q", got, payload)
	}
}

// ── Test: DeleteBlob ──────────────────────────────────────────────────────────

// TestDeleteBlob_BlobGoneAfterDelete stores a blob, deletes it, and confirms
// that BlobExists returns false and a subsequent GetBlob returns codes.NotFound.
func TestDeleteBlob_BlobGoneAfterDelete(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	payload := []byte("to be deleted")
	key := "blobs/test-tenant/sha256/to/tobedeleted"

	mustPutBlob(t, client, key, payload)

	// Delete the blob.
	if _, err := client.DeleteBlob(ctx, &storagev1.DeleteBlobRequest{Key: key, TenantId: testTenant}); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}

	// BlobExists must return false.
	existsResp, err := client.BlobExists(ctx, &storagev1.BlobExistsRequest{Key: key, TenantId: testTenant})
	if err != nil {
		t.Fatalf("BlobExists after delete: %v", err)
	}
	if existsResp.Exists {
		t.Fatal("BlobExists after delete: want false, got true")
	}

	// GetBlob must return codes.NotFound.
	stream, err := client.GetBlob(ctx, &storagev1.GetBlobRequest{Key: key, TenantId: testTenant})
	if err != nil {
		t.Fatalf("GetBlob (after delete) stream open: %v", err)
	}
	_, recvErr := stream.Recv()
	if status.Code(recvErr) != codes.NotFound {
		t.Fatalf("GetBlob after delete: want codes.NotFound, got %v", recvErr)
	}
}

// ── Test: PutBlob overwrite ───────────────────────────────────────────────────

// TestPutBlob_Overwrite_CleanlyReplaces uploads a blob under a key, then
// uploads different content to the same key and confirms the second content
// is returned by GetBlob — MinIO performs a clean overwrite.
func TestPutBlob_Overwrite_CleanlyReplaces(t *testing.T) {
	client := buildTestEnv(t)

	key := "blobs/test-tenant/sha256/ov/overwrite-test"

	first := []byte("first version of the blob")
	second := []byte("second version — completely replaced")

	mustPutBlob(t, client, key, first)
	mustPutBlob(t, client, key, second)

	got := mustGetBlob(t, client, key)
	if !bytes.Equal(got, second) {
		t.Fatalf("after overwrite: got %q, want %q", got, second)
	}
}

// ── Test: GetBlob missing key returns NotFound ────────────────────────────────

// TestGetBlob_MissingKey_ReturnsNotFound verifies that streaming a key that
// was never stored yields a codes.NotFound gRPC status.
func TestGetBlob_MissingKey_ReturnsNotFound(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	stream, err := client.GetBlob(ctx, &storagev1.GetBlobRequest{
		Key:      "blobs/test-tenant/sha256/no/nonexistent-key",
		TenantId: testTenant,
	})
	if err != nil {
		// Some servers return the error on stream open rather than first Recv.
		if status.Code(err) == codes.NotFound {
			return
		}
		t.Fatalf("GetBlob stream open: %v", err)
	}

	// Expect the error on the first Recv.
	_, recvErr := stream.Recv()
	if status.Code(recvErr) != codes.NotFound {
		t.Fatalf("GetBlob missing key: want codes.NotFound, got %v", recvErr)
	}
}

// ── Test: Large blob (2 MiB) push + pull ─────────────────────────────────────

// TestPutBlob_GetBlob_LargeBlob uploads a 2 MiB random blob and streams it
// back, verifying the reassembled bytes are identical.  This exercises the
// 256 KiB chunking logic in both the client helper and the server GetBlob
// implementation.
func TestPutBlob_GetBlob_LargeBlob(t *testing.T) {
	client := buildTestEnv(t)

	// Generate 2 MiB of random data.
	const size = 2 * 1024 * 1024
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	key := "blobs/test-tenant/sha256/la/large-blob-2mib"

	mustPutBlob(t, client, key, payload)

	got := mustGetBlob(t, client, key)

	if len(got) != size {
		t.Fatalf("large blob size mismatch: got %d bytes, want %d", len(got), size)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("large blob content mismatch")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// mustPutBlob sends payload to the storage service under key using the
// client-streaming PutBlob RPC.  The payload is split into 256 KiB chunks to
// exercise the chunking logic.  The test is failed immediately on any error.
func mustPutBlob(t *testing.T, client storagev1.StorageServiceClient, key string, payload []byte) {
	t.Helper()

	ctx := context.Background()
	const chunkSize = 256 * 1024 // 256 KiB

	// Open the client-streaming RPC.
	stream, err := client.PutBlob(ctx)
	if err != nil {
		t.Fatalf("PutBlob stream open: %v", err)
	}

	// First message: metadata.
	if err := stream.Send(&storagev1.PutBlobRequest{
		Data: &storagev1.PutBlobRequest_Meta{
			Meta: &storagev1.PutBlobMeta{
				Key:         key,
				TenantId:    testTenant,
				Size:        int64(len(payload)),
				ContentType: "application/octet-stream",
			},
		},
	}); err != nil {
		t.Fatalf("PutBlob send meta: %v", err)
	}

	// Subsequent messages: data chunks.
	for len(payload) > 0 {
		n := chunkSize
		if n > len(payload) {
			n = len(payload)
		}
		if err := stream.Send(&storagev1.PutBlobRequest{
			Data: &storagev1.PutBlobRequest_Chunk{Chunk: payload[:n]},
		}); err != nil {
			t.Fatalf("PutBlob send chunk: %v", err)
		}
		payload = payload[n:]
	}

	// Close the send side and wait for the server response.
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("PutBlob CloseAndRecv: %v", err)
	}
	if resp.Key != key {
		t.Fatalf("PutBlob response key: got %q, want %q", resp.Key, key)
	}
}

// mustGetBlob streams a blob from the storage service and returns the
// reassembled bytes.  The test is failed immediately on any error.
func mustGetBlob(t *testing.T, client storagev1.StorageServiceClient, key string) []byte {
	t.Helper()

	ctx := context.Background()

	stream, err := client.GetBlob(ctx, &storagev1.GetBlobRequest{Key: key, TenantId: testTenant})
	if err != nil {
		t.Fatalf("GetBlob stream open: %v", err)
	}

	var buf []byte
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("GetBlob stream.Recv: %v", err)
		}
		buf = append(buf, resp.Chunk...)
	}
	return buf
}
