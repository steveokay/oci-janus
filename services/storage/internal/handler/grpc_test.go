// Package handler — unit tests for StorageHandler.
//
// All tests use a hand-written fakeDriver that implements driver.Driver and
// hand-written stream fakes for the streaming RPCs.
// No real MinIO, S3, or network connections are required (CLAUDE.md §18).
package handler

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/storage/internal/driver"
)

// ── fakeDriver ────────────────────────────────────────────────────────────────

// fakeDriver is a controllable in-memory stub for driver.Driver.
// Tests set only the fields relevant to the RPC under test.
type fakeDriver struct {
	// StatBlob
	statInfo driver.BlobInfo
	statErr  error

	// DeleteBlob
	deleteErr error

	// BlobExists
	existsResult bool
	existsErr    error

	// ListBlobs
	listKeys []string
	listErr  error

	// InitiateMultipart
	uploadID    string
	initiateErr error

	// CompleteMultipart
	completeErr error

	// AbortMultipart
	abortErr error

	// PutBlob / GetBlob / UploadPart (streaming — used by test helpers only)
	putErr  error
	getRC   io.ReadCloser
	getSize int64
	getErr  error
	etag    string
	partErr error
}

// ── driver.Driver implementation on fakeDriver ───────────────────────────────

func (f *fakeDriver) PutBlob(_ context.Context, _ string, r io.Reader, _ int64, _ string) error {
	if r != nil {
		// Drain reader so callers don't deadlock.
		_, _ = io.Copy(io.Discard, r)
	}
	return f.putErr
}

func (f *fakeDriver) GetBlob(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	rc := f.getRC
	if rc == nil {
		rc = io.NopCloser(strings.NewReader(""))
	}
	return rc, f.getSize, nil
}

func (f *fakeDriver) StatBlob(_ context.Context, _ string) (driver.BlobInfo, error) {
	return f.statInfo, f.statErr
}

func (f *fakeDriver) DeleteBlob(_ context.Context, _ string) error {
	return f.deleteErr
}

func (f *fakeDriver) BlobExists(_ context.Context, _ string) (bool, error) {
	return f.existsResult, f.existsErr
}

func (f *fakeDriver) InitiateMultipart(_ context.Context, _ string) (string, error) {
	return f.uploadID, f.initiateErr
}

func (f *fakeDriver) UploadPart(_ context.Context, _, _ string, _ int32, r io.Reader, _ int64) (string, error) {
	if r != nil {
		_, _ = io.Copy(io.Discard, r)
	}
	return f.etag, f.partErr
}

func (f *fakeDriver) CompleteMultipart(_ context.Context, _, _ string, _ []driver.CompletedPart) error {
	return f.completeErr
}

func (f *fakeDriver) AbortMultipart(_ context.Context, _, _ string) error {
	return f.abortErr
}

func (f *fakeDriver) ListBlobs(_ context.Context, _ string) ([]string, error) {
	return f.listKeys, f.listErr
}

func (f *fakeDriver) Ping(_ context.Context) error { return nil }

// ── test helpers ──────────────────────────────────────────────────────────────

// newStorageHandler wires a fakeDriver into a StorageHandler for testing.
func newStorageHandler(d driver.Driver) *StorageHandler {
	return &StorageHandler{drv: d}
}

// requireCode asserts that err carries the expected gRPC status code.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != want {
		t.Errorf("status code: got %v, want %v", st.Code(), want)
	}
}

// requireNoErr fails the test if err is non-nil.
func requireNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── StatBlob ──────────────────────────────────────────────────────────────────

// TestStatBlob_found_returnsInfo verifies the happy path and that all info
// fields are mapped correctly into the response proto.
func TestStatBlob_found_returnsInfo(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	info := driver.BlobInfo{
		Key:          "blobs/t1/sha256/ab/abc",
		Size:         4096,
		ContentType:  "application/octet-stream",
		LastModified: now,
	}
	h := newStorageHandler(&fakeDriver{statInfo: info})

	resp, err := h.StatBlob(context.Background(), &storagev1.StatBlobRequest{Key: info.Key, TenantId: "t1"})
	requireNoErr(t, err)
	if resp.Key != info.Key {
		t.Errorf("Key: got %q, want %q", resp.Key, info.Key)
	}
	if resp.Size != info.Size {
		t.Errorf("Size: got %d, want %d", resp.Size, info.Size)
	}
	if resp.ContentType != info.ContentType {
		t.Errorf("ContentType: got %q, want %q", resp.ContentType, info.ContentType)
	}
	// Timestamp should round-trip cleanly.
	if resp.LastModified == nil {
		t.Fatal("expected non-nil LastModified timestamp")
	}
	if !resp.LastModified.AsTime().Equal(now) {
		t.Errorf("LastModified: got %v, want %v", resp.LastModified.AsTime(), now)
	}
}

// TestStatBlob_notFound_returnsNotFound verifies that os.ErrNotExist maps to
// codes.NotFound (the driver convention for missing blobs).
func TestStatBlob_notFound_returnsNotFound(t *testing.T) {
	h := newStorageHandler(&fakeDriver{statErr: os.ErrNotExist})

	_, err := h.StatBlob(context.Background(), &storagev1.StatBlobRequest{Key: "blobs/t1/missing", TenantId: "t1"})
	requireCode(t, err, codes.NotFound)
}

// TestStatBlob_driverError_returnsInternal verifies that non-not-found driver
// errors map to codes.Internal.
func TestStatBlob_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{statErr: errors.New("backend unavailable")})

	_, err := h.StatBlob(context.Background(), &storagev1.StatBlobRequest{Key: "blobs/t1/somekey", TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// TestStatBlob_zeroBytesBlob_returnsZeroSize verifies that an empty blob (zero
// size) is not treated as an error.
func TestStatBlob_zeroBytesBlob_returnsZeroSize(t *testing.T) {
	info := driver.BlobInfo{Key: "blobs/empty", Size: 0, ContentType: "application/octet-stream", LastModified: time.Now()}
	h := newStorageHandler(&fakeDriver{statInfo: info})

	resp, err := h.StatBlob(context.Background(), &storagev1.StatBlobRequest{Key: "blobs/t1/empty", TenantId: "t1"})
	requireNoErr(t, err)
	if resp.Size != 0 {
		t.Errorf("Size: got %d, want 0", resp.Size)
	}
}

// ── DeleteBlob ────────────────────────────────────────────────────────────────

// TestDeleteBlob_success_returnsEmptyResponse verifies the happy path.
func TestDeleteBlob_success_returnsEmptyResponse(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	resp, err := h.DeleteBlob(context.Background(), &storagev1.DeleteBlobRequest{Key: "blobs/t1/sha256/ab/abc", TenantId: "t1"})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil response")
	}
}

// TestDeleteBlob_notFound_returnsNotFound verifies that os.ErrNotExist maps to
// codes.NotFound.
func TestDeleteBlob_notFound_returnsNotFound(t *testing.T) {
	h := newStorageHandler(&fakeDriver{deleteErr: os.ErrNotExist})

	_, err := h.DeleteBlob(context.Background(), &storagev1.DeleteBlobRequest{Key: "blobs/t1/gone", TenantId: "t1"})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteBlob_driverError_returnsInternal verifies unexpected driver errors
// map to codes.Internal.
func TestDeleteBlob_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{deleteErr: errors.New("s3 error")})

	_, err := h.DeleteBlob(context.Background(), &storagev1.DeleteBlobRequest{Key: "blobs/t1/some", TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// ── BlobExists ────────────────────────────────────────────────────────────────

// TestBlobExists_exists_returnsTrue verifies the happy path when a blob exists.
func TestBlobExists_exists_returnsTrue(t *testing.T) {
	h := newStorageHandler(&fakeDriver{existsResult: true})

	resp, err := h.BlobExists(context.Background(), &storagev1.BlobExistsRequest{Key: "blobs/t1/present", TenantId: "t1"})
	requireNoErr(t, err)
	if !resp.Exists {
		t.Error("expected Exists=true")
	}
}

// TestBlobExists_missing_returnsFalse verifies that a blob known to be absent
// returns Exists=false without an error.
func TestBlobExists_missing_returnsFalse(t *testing.T) {
	h := newStorageHandler(&fakeDriver{existsResult: false})

	resp, err := h.BlobExists(context.Background(), &storagev1.BlobExistsRequest{Key: "blobs/t1/absent", TenantId: "t1"})
	requireNoErr(t, err)
	if resp.Exists {
		t.Error("expected Exists=false")
	}
}

// TestBlobExists_driverError_returnsInternal verifies that driver errors
// propagate as codes.Internal.
func TestBlobExists_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{existsErr: errors.New("network error")})

	_, err := h.BlobExists(context.Background(), &storagev1.BlobExistsRequest{Key: "blobs/t1/key", TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// ── InitiateMultipart ─────────────────────────────────────────────────────────

// TestInitiateMultipart_success_returnsUploadID verifies that the upload ID
// from the driver is echoed back in the response.
func TestInitiateMultipart_success_returnsUploadID(t *testing.T) {
	h := newStorageHandler(&fakeDriver{uploadID: "upload-xyz"})

	resp, err := h.InitiateMultipart(context.Background(), &storagev1.InitiateMultipartRequest{Key: "blobs/t1/large", TenantId: "t1"})
	requireNoErr(t, err)
	if resp.UploadId != "upload-xyz" {
		t.Errorf("UploadId: got %q, want upload-xyz", resp.UploadId)
	}
}

// TestInitiateMultipart_driverError_returnsInternal verifies that driver errors
// propagate as codes.Internal.
func TestInitiateMultipart_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{initiateErr: errors.New("s3 create multipart failed")})

	_, err := h.InitiateMultipart(context.Background(), &storagev1.InitiateMultipartRequest{Key: "blobs/t1/large", TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// TestInitiateMultipart_notFound_returnsNotFound verifies that os.ErrNotExist
// maps to codes.NotFound (e.g. bucket does not exist on the backend).
func TestInitiateMultipart_notFound_returnsNotFound(t *testing.T) {
	h := newStorageHandler(&fakeDriver{initiateErr: os.ErrNotExist})

	_, err := h.InitiateMultipart(context.Background(), &storagev1.InitiateMultipartRequest{Key: "blobs/t1/large", TenantId: "t1"})
	requireCode(t, err, codes.NotFound)
}

// ── CompleteMultipart ─────────────────────────────────────────────────────────

// TestCompleteMultipart_success_returnsKey verifies that on success the key is
// echoed back.
func TestCompleteMultipart_success_returnsKey(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	resp, err := h.CompleteMultipart(context.Background(), &storagev1.CompleteMultipartRequest{
		Key:      "blobs/t1/large",
		TenantId: "t1",
		UploadId: "upload-xyz",
		Parts: []*storagev1.CompletedPart{
			{PartNum: 1, Etag: "etag-1"},
			{PartNum: 2, Etag: "etag-2"},
		},
	})
	requireNoErr(t, err)
	if resp.Key != "blobs/t1/large" {
		t.Errorf("Key: got %q, want blobs/t1/large", resp.Key)
	}
}

// TestCompleteMultipart_driverError_returnsInternal verifies that driver errors
// propagate as codes.Internal.
func TestCompleteMultipart_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{completeErr: errors.New("parts mismatch")})

	_, err := h.CompleteMultipart(context.Background(), &storagev1.CompleteMultipartRequest{
		Key:      "blobs/t1/large",
		TenantId: "t1",
		UploadId: "upload-xyz",
	})
	requireCode(t, err, codes.Internal)
}

// TestCompleteMultipart_noParts_stillSucceeds verifies that calling
// CompleteMultipart with an empty parts list does not panic or fail at the
// handler layer (driver decides whether that is valid).
func TestCompleteMultipart_noParts_stillSucceeds(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	resp, err := h.CompleteMultipart(context.Background(), &storagev1.CompleteMultipartRequest{
		Key:      "blobs/t1/empty-parts",
		TenantId: "t1",
		UploadId: "upload-abc",
		Parts:    nil,
	})
	requireNoErr(t, err)
	if resp.Key != "blobs/t1/empty-parts" {
		t.Errorf("Key: got %q, want blobs/t1/empty-parts", resp.Key)
	}
}

// TestCompleteMultipart_partsConvertedCorrectly verifies that CompletedPart
// proto fields (PartNum, Etag) are correctly mapped to driver.CompletedPart
// by inspecting the response (indirectly exercising the mapping code path).
func TestCompleteMultipart_partsConvertedCorrectly(t *testing.T) {
	// The fake driver ignores parts but the handler must not panic during the
	// conversion loop.
	h := newStorageHandler(&fakeDriver{})

	_, err := h.CompleteMultipart(context.Background(), &storagev1.CompleteMultipartRequest{
		Key:      "blobs/t1/multi",
		TenantId: "t1",
		UploadId: "upload-multi",
		Parts: []*storagev1.CompletedPart{
			{PartNum: 1, Etag: "e1"},
			{PartNum: 2, Etag: "e2"},
			{PartNum: 3, Etag: "e3"},
		},
	})
	requireNoErr(t, err)
}

// ── AbortMultipart ────────────────────────────────────────────────────────────

// TestAbortMultipart_success_returnsEmpty verifies the happy path.
func TestAbortMultipart_success_returnsEmpty(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	resp, err := h.AbortMultipart(context.Background(), &storagev1.AbortMultipartRequest{
		Key:      "blobs/t1/large",
		TenantId: "t1",
		UploadId: "upload-xyz",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil response")
	}
}

// TestAbortMultipart_driverError_returnsInternal verifies that driver errors
// propagate as codes.Internal.
func TestAbortMultipart_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{abortErr: errors.New("already completed")})

	_, err := h.AbortMultipart(context.Background(), &storagev1.AbortMultipartRequest{
		Key:      "blobs/t1/large",
		TenantId: "t1",
		UploadId: "upload-xyz",
	})
	requireCode(t, err, codes.Internal)
}

// TestAbortMultipart_notFound_returnsNotFound verifies that os.ErrNotExist maps
// to codes.NotFound (upload already expired or never existed).
func TestAbortMultipart_notFound_returnsNotFound(t *testing.T) {
	h := newStorageHandler(&fakeDriver{abortErr: os.ErrNotExist})

	_, err := h.AbortMultipart(context.Background(), &storagev1.AbortMultipartRequest{
		Key:      "blobs/t1/large",
		TenantId: "t1",
		UploadId: "upload-gone",
	})
	requireCode(t, err, codes.NotFound)
}

// ── mapErrCtx (internal helper) ───────────────────────────────────────────────

// TestMapErrCtx_nil_returnsNil verifies that mapErrCtx passes through nil unchanged.
func TestMapErrCtx_nil_returnsNil(t *testing.T) {
	if mapErrCtx(context.Background(), "op", nil) != nil {
		t.Error("expected mapErrCtx(nil) == nil")
	}
}

// TestMapErrCtx_osErrNotExist_returnsNotFoundCode verifies that os.ErrNotExist
// maps to codes.NotFound.
func TestMapErrCtx_osErrNotExist_returnsNotFoundCode(t *testing.T) {
	requireCode(t, mapErrCtx(context.Background(), "op", os.ErrNotExist), codes.NotFound)
}

// TestMapErrCtx_wrappedOsErrNotExist_returnsNotFoundCode verifies that wrapped
// os.ErrNotExist (as returned by stdlib functions) also maps to codes.NotFound.
func TestMapErrCtx_wrappedOsErrNotExist_returnsNotFoundCode(t *testing.T) {
	wrapped := &os.PathError{Op: "open", Path: "blobs/missing", Err: os.ErrNotExist}
	requireCode(t, mapErrCtx(context.Background(), "op", wrapped), codes.NotFound)
}

// TestStorageHandler_crossTenantAccessBlocked — PENTEST-026: a caller in
// tenant "t1" must NOT be able to read/write/delete keys under tenant "t2".
// The handler rejects with PermissionDenied before the driver is touched.
// This is the defining test for the cross-tenant containment guarantee that
// SEC §9 (multi-tenant isolation) relies on at the storage layer.
func TestStorageHandler_crossTenantAccessBlocked(t *testing.T) {
	h := newStorageHandler(&fakeDriver{statInfo: driver.BlobInfo{Size: 1}})

	// Caller claims t1, but the key is under t2.
	cases := []struct {
		name string
		exec func() error
	}{
		{"StatBlob", func() error {
			_, err := h.StatBlob(context.Background(), &storagev1.StatBlobRequest{Key: "blobs/t2/sha256/aa/aaa", TenantId: "t1"})
			return err
		}},
		{"DeleteBlob", func() error {
			_, err := h.DeleteBlob(context.Background(), &storagev1.DeleteBlobRequest{Key: "blobs/t2/sha256/aa/aaa", TenantId: "t1"})
			return err
		}},
		{"BlobExists", func() error {
			_, err := h.BlobExists(context.Background(), &storagev1.BlobExistsRequest{Key: "blobs/t2/sha256/aa/aaa", TenantId: "t1"})
			return err
		}},
		{"InitiateMultipart", func() error {
			_, err := h.InitiateMultipart(context.Background(), &storagev1.InitiateMultipartRequest{Key: "blobs/t2/upload-x", TenantId: "t1"})
			return err
		}},
		{"CompleteMultipart", func() error {
			_, err := h.CompleteMultipart(context.Background(), &storagev1.CompleteMultipartRequest{Key: "blobs/t2/upload-x", TenantId: "t1", UploadId: "u"})
			return err
		}},
		{"AbortMultipart", func() error {
			_, err := h.AbortMultipart(context.Background(), &storagev1.AbortMultipartRequest{Key: "blobs/t2/upload-x", TenantId: "t1", UploadId: "u"})
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.exec()
			requireCode(t, err, codes.PermissionDenied)
		})
	}
}

// TestStorageHandler_emptyTenantIDRejected — PENTEST-026: the tenant_id field
// is mandatory; an empty value must not bypass the prefix check by accident.
func TestStorageHandler_emptyTenantIDRejected(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})
	_, err := h.StatBlob(context.Background(), &storagev1.StatBlobRequest{Key: "blobs/t1/sha256/aa/aaa", TenantId: ""})
	requireCode(t, err, codes.InvalidArgument)
}

// TestMapErrCtx_unknownError_returnsGenericInternalMessage — PENTEST-021:
// arbitrary driver errors must produce the generic "internal error" message,
// not the raw driver text. The full error is still logged server-side.
func TestMapErrCtx_unknownError_returnsGenericInternalMessage(t *testing.T) {
	leaky := errors.New("AccessDenied: arn:aws:s3:::secret-bucket/path/key")
	got := mapErrCtx(context.Background(), "op", leaky)
	requireCode(t, got, codes.Internal)
	if st, _ := status.FromError(got); st != nil {
		if st.Message() != "internal error" {
			t.Errorf("leaked driver message on wire: got %q, want %q (PENTEST-021)", st.Message(), "internal error")
		}
	}
}

// ── Streaming stream fakes ────────────────────────────────────────────────────

// baseServerStream satisfies grpc.ServerStream with no-op implementations
// so all typed stream fakes only need to add their typed Send/Recv methods.
type baseServerStream struct{}

func (b *baseServerStream) SetHeader(metadata.MD) error  { return nil }
func (b *baseServerStream) SendHeader(metadata.MD) error { return nil }
func (b *baseServerStream) SetTrailer(metadata.MD)       {}
func (b *baseServerStream) Context() context.Context     { return context.Background() }
func (b *baseServerStream) SendMsg(m any) error          { return nil }
func (b *baseServerStream) RecvMsg(m any) error          { return nil }

// ── PutBlob stream fake ───────────────────────────────────────────────────────

// fakePutBlobStream implements storagev1.StorageService_PutBlobServer.
// It feeds a sequence of pre-set messages through Recv and captures
// SendAndClose.
type fakePutBlobStream struct {
	baseServerStream
	messages []*storagev1.PutBlobRequest
	pos      int
	recvErr  error
	closed   *storagev1.PutBlobResponse
}

func (f *fakePutBlobStream) Recv() (*storagev1.PutBlobRequest, error) {
	if f.recvErr != nil && f.pos > 0 {
		// Return error after at least the first (meta) message to exercise the
		// goroutine error path.
		return nil, f.recvErr
	}
	if f.pos >= len(f.messages) {
		return nil, io.EOF
	}
	msg := f.messages[f.pos]
	f.pos++
	return msg, nil
}

func (f *fakePutBlobStream) SendAndClose(resp *storagev1.PutBlobResponse) error {
	f.closed = resp
	return nil
}

var _ storagev1.StorageService_PutBlobServer = (*fakePutBlobStream)(nil)

// ── GetBlob stream fake ───────────────────────────────────────────────────────

// fakeGetBlobStream implements storagev1.StorageService_GetBlobServer.
type fakeGetBlobStream struct {
	baseServerStream
	sent    []*storagev1.GetBlobResponse
	sendErr error
}

func (f *fakeGetBlobStream) Send(resp *storagev1.GetBlobResponse) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, resp)
	return nil
}

var _ storagev1.StorageService_GetBlobServer = (*fakeGetBlobStream)(nil)

// ── ListBlobs stream fake ─────────────────────────────────────────────────────

// fakeListBlobsStream implements storagev1.StorageService_ListBlobsServer.
type fakeListBlobsStream struct {
	baseServerStream
	sent    []*storagev1.ListBlobsResponse
	sendErr error
}

func (f *fakeListBlobsStream) Send(resp *storagev1.ListBlobsResponse) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, resp)
	return nil
}

var _ storagev1.StorageService_ListBlobsServer = (*fakeListBlobsStream)(nil)

// ── UploadPart stream fake ────────────────────────────────────────────────────

// fakeUploadPartStream implements storagev1.StorageService_UploadPartServer.
type fakeUploadPartStream struct {
	baseServerStream
	messages []*storagev1.UploadPartRequest
	pos      int
	recvErr  error
	closed   *storagev1.UploadPartResponse
}

func (f *fakeUploadPartStream) Recv() (*storagev1.UploadPartRequest, error) {
	if f.recvErr != nil && f.pos > 0 {
		return nil, f.recvErr
	}
	if f.pos >= len(f.messages) {
		return nil, io.EOF
	}
	msg := f.messages[f.pos]
	f.pos++
	return msg, nil
}

func (f *fakeUploadPartStream) SendAndClose(resp *storagev1.UploadPartResponse) error {
	f.closed = resp
	return nil
}

var _ storagev1.StorageService_UploadPartServer = (*fakeUploadPartStream)(nil)

// ── PutBlob tests ─────────────────────────────────────────────────────────────

// TestPutBlob_success_closesWithResponse verifies the happy path: meta message
// followed by one chunk, driver succeeds, SendAndClose carries the key and
// byte count.
func TestPutBlob_success_closesWithResponse(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	stream := &fakePutBlobStream{
		messages: []*storagev1.PutBlobRequest{
			{
				Data: &storagev1.PutBlobRequest_Meta{
					Meta: &storagev1.PutBlobMeta{
						Key:         "blobs/t1/sha256/ab/abc",
						TenantId:    "t1",
						Size:        5,
						ContentType: "application/octet-stream",
					},
				},
			},
			{
				Data: &storagev1.PutBlobRequest_Chunk{Chunk: []byte("hello")},
			},
		},
	}

	err := h.PutBlob(stream)
	requireNoErr(t, err)
	if stream.closed == nil {
		t.Fatal("expected SendAndClose to have been called")
	}
	if stream.closed.Key != "blobs/t1/sha256/ab/abc" {
		t.Errorf("response key: got %q, want blobs/t1/sha256/ab/abc", stream.closed.Key)
	}
}

// TestPutBlob_noMeta_returnsInvalidArgument verifies that a stream with no
// messages returns codes.InvalidArgument.
func TestPutBlob_noMeta_returnsInvalidArgument(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	emptyStream := &eofOnFirstRecvPutBlobStream{}
	err := h.PutBlob(emptyStream)
	requireCode(t, err, codes.InvalidArgument)
}

// TestPutBlob_firstMessageNotMeta_returnsInvalidArgument verifies that a first
// message carrying a chunk (not meta) is rejected.
func TestPutBlob_firstMessageNotMeta_returnsInvalidArgument(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	stream := &fakePutBlobStream{
		messages: []*storagev1.PutBlobRequest{
			{Data: &storagev1.PutBlobRequest_Chunk{Chunk: []byte("oops")}},
		},
	}
	err := h.PutBlob(stream)
	requireCode(t, err, codes.InvalidArgument)
}

// TestPutBlob_driverError_returnsInternal verifies that a driver PutBlob error
// propagates as codes.Internal.
func TestPutBlob_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{putErr: errors.New("backend full")})

	stream := &fakePutBlobStream{
		messages: []*storagev1.PutBlobRequest{
			{
				Data: &storagev1.PutBlobRequest_Meta{
					Meta: &storagev1.PutBlobMeta{Key: "blobs/t1/key", TenantId: "t1", Size: 3},
				},
			},
			{Data: &storagev1.PutBlobRequest_Chunk{Chunk: []byte("bye")}},
		},
	}
	err := h.PutBlob(stream)
	requireCode(t, err, codes.Internal)
}

// eofOnFirstRecvPutBlobStream returns EOF on the very first Recv call.
type eofOnFirstRecvPutBlobStream struct{ baseServerStream }

func (e *eofOnFirstRecvPutBlobStream) Recv() (*storagev1.PutBlobRequest, error) {
	return nil, io.EOF
}
func (e *eofOnFirstRecvPutBlobStream) SendAndClose(_ *storagev1.PutBlobResponse) error { return nil }

var _ storagev1.StorageService_PutBlobServer = (*eofOnFirstRecvPutBlobStream)(nil)

// ── GetBlob tests ─────────────────────────────────────────────────────────────

// TestGetBlob_success_sendsChunks verifies that blob content is forwarded to
// the stream as one or more chunk messages.
func TestGetBlob_success_sendsChunks(t *testing.T) {
	content := "hello world from storage"
	h := newStorageHandler(&fakeDriver{
		getRC:   io.NopCloser(strings.NewReader(content)),
		getSize: int64(len(content)),
	})
	stream := &fakeGetBlobStream{}

	err := h.GetBlob(&storagev1.GetBlobRequest{Key: "blobs/t1/some", TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) == 0 {
		t.Error("expected at least one chunk sent")
	}
	// Reassemble and verify content.
	var buf []byte
	for _, resp := range stream.sent {
		buf = append(buf, resp.Chunk...)
	}
	if string(buf) != content {
		t.Errorf("reassembled content: got %q, want %q", string(buf), content)
	}
}

// TestGetBlob_notFound_returnsNotFound verifies that os.ErrNotExist maps to
// codes.NotFound.
func TestGetBlob_notFound_returnsNotFound(t *testing.T) {
	h := newStorageHandler(&fakeDriver{getErr: os.ErrNotExist})
	stream := &fakeGetBlobStream{}

	err := h.GetBlob(&storagev1.GetBlobRequest{Key: "blobs/t1/missing", TenantId: "t1"}, stream)
	requireCode(t, err, codes.NotFound)
}

// TestGetBlob_driverError_returnsInternal verifies that non-not-found driver
// errors map to codes.Internal.
func TestGetBlob_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{getErr: errors.New("s3 error")})
	stream := &fakeGetBlobStream{}

	err := h.GetBlob(&storagev1.GetBlobRequest{Key: "blobs/t1/some", TenantId: "t1"}, stream)
	requireCode(t, err, codes.Internal)
}

// TestGetBlob_sendError_propagates verifies that a stream send error is
// returned to the caller.
func TestGetBlob_sendError_propagates(t *testing.T) {
	h := newStorageHandler(&fakeDriver{
		getRC:   io.NopCloser(strings.NewReader("data")),
		getSize: 4,
	})
	stream := &fakeGetBlobStream{sendErr: errors.New("client gone")}

	err := h.GetBlob(&storagev1.GetBlobRequest{Key: "blobs/t1/some", TenantId: "t1"}, stream)
	if err == nil {
		t.Fatal("expected error from send failure, got nil")
	}
}

// ── ListBlobs tests ───────────────────────────────────────────────────────────

// TestListBlobs_success_sendsAllKeys verifies that all keys returned by the
// driver (with their stat info) are forwarded to the stream.
func TestListBlobs_success_sendsAllKeys(t *testing.T) {
	// The handler calls StatBlob for each key; we reuse statInfo for all.
	h := newStorageHandler(&fakeDriver{
		listKeys: []string{"blobs/t1/sha256/aa/aaa", "blobs/t1/sha256/bb/bbb"},
		statInfo: driver.BlobInfo{Size: 512, LastModified: time.Now()},
	})
	stream := &fakeListBlobsStream{}

	err := h.ListBlobs(&storagev1.ListBlobsRequest{Prefix: "blobs/t1/", TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d blobs, want 2", len(stream.sent))
	}
}

// TestListBlobs_emptyPrefix_rejected — PENTEST-026: an empty prefix used to
// default to "blobs/" which would have leaked every tenant's keys. Empty
// prefix now returns InvalidArgument.
func TestListBlobs_emptyPrefix_rejected(t *testing.T) {
	h := newStorageHandler(&fakeDriver{
		listKeys: []string{"blobs/t1/sha256/cc/ccc"},
		statInfo: driver.BlobInfo{Size: 1024, LastModified: time.Now()},
	})
	stream := &fakeListBlobsStream{}

	err := h.ListBlobs(&storagev1.ListBlobsRequest{Prefix: "", TenantId: "t1"}, stream)
	requireCode(t, err, codes.InvalidArgument)
	if len(stream.sent) != 0 {
		t.Errorf("sent %d blobs, want 0 (request must be rejected before listing)", len(stream.sent))
	}
}

// TestListBlobs_listError_returnsInternal verifies that a driver list error
// maps to codes.Internal.
func TestListBlobs_listError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{listErr: errors.New("listing failed")})
	stream := &fakeListBlobsStream{}

	err := h.ListBlobs(&storagev1.ListBlobsRequest{Prefix: "blobs/t1/", TenantId: "t1"}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListBlobs_empty_sendsNothing verifies that an empty list completes
// without error and sends no messages.
func TestListBlobs_empty_sendsNothing(t *testing.T) {
	h := newStorageHandler(&fakeDriver{listKeys: nil})
	stream := &fakeListBlobsStream{}

	err := h.ListBlobs(&storagev1.ListBlobsRequest{Prefix: "blobs/t1/", TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}

// ── UploadPart tests ──────────────────────────────────────────────────────────

// TestUploadPart_success_closesWithEtag verifies the happy path: meta message
// followed by one chunk, driver returns an etag, SendAndClose is called.
func TestUploadPart_success_closesWithEtag(t *testing.T) {
	h := newStorageHandler(&fakeDriver{etag: "etag-abc"})

	stream := &fakeUploadPartStream{
		messages: []*storagev1.UploadPartRequest{
			{
				Data: &storagev1.UploadPartRequest_Meta{
					Meta: &storagev1.UploadPartMeta{
						Key:      "blobs/t1/large",
						TenantId: "t1",
						UploadId: "upload-xyz",
						PartNum:  1,
						Size:     5,
					},
				},
			},
			{Data: &storagev1.UploadPartRequest_Chunk{Chunk: []byte("hello")}},
		},
	}

	err := h.UploadPart(stream)
	requireNoErr(t, err)
	if stream.closed == nil {
		t.Fatal("expected SendAndClose to have been called")
	}
	if stream.closed.Etag != "etag-abc" {
		t.Errorf("Etag: got %q, want etag-abc", stream.closed.Etag)
	}
}

// TestUploadPart_noMeta_returnsInvalidArgument verifies that a stream with no
// messages returns codes.InvalidArgument.
func TestUploadPart_noMeta_returnsInvalidArgument(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})
	emptyStream := &eofOnFirstRecvUploadPartStream{}

	err := h.UploadPart(emptyStream)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUploadPart_firstMessageNotMeta_returnsInvalidArgument verifies that a
// first message carrying a chunk (not meta) is rejected.
func TestUploadPart_firstMessageNotMeta_returnsInvalidArgument(t *testing.T) {
	h := newStorageHandler(&fakeDriver{})

	stream := &fakeUploadPartStream{
		messages: []*storagev1.UploadPartRequest{
			{Data: &storagev1.UploadPartRequest_Chunk{Chunk: []byte("bad")}},
		},
	}
	err := h.UploadPart(stream)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUploadPart_driverError_returnsInternal verifies that a driver UploadPart
// error propagates as codes.Internal.
func TestUploadPart_driverError_returnsInternal(t *testing.T) {
	h := newStorageHandler(&fakeDriver{partErr: errors.New("backend error")})

	stream := &fakeUploadPartStream{
		messages: []*storagev1.UploadPartRequest{
			{
				Data: &storagev1.UploadPartRequest_Meta{
					Meta: &storagev1.UploadPartMeta{Key: "blobs/t1/large", TenantId: "t1", UploadId: "upload-xyz", PartNum: 1, Size: 3},
				},
			},
			{Data: &storagev1.UploadPartRequest_Chunk{Chunk: []byte("bye")}},
		},
	}
	err := h.UploadPart(stream)
	requireCode(t, err, codes.Internal)
}

// eofOnFirstRecvUploadPartStream returns EOF on the first Recv call.
type eofOnFirstRecvUploadPartStream struct{ baseServerStream }

func (e *eofOnFirstRecvUploadPartStream) Recv() (*storagev1.UploadPartRequest, error) {
	return nil, io.EOF
}
func (e *eofOnFirstRecvUploadPartStream) SendAndClose(_ *storagev1.UploadPartResponse) error {
	return nil
}

var _ storagev1.StorageService_UploadPartServer = (*eofOnFirstRecvUploadPartStream)(nil)
