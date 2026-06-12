// Package service_test exercises the UploadStore using an in-process Redis
// (miniredis). No real Redis server is required.
package service

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedis starts a miniredis instance and returns a configured redis.Client
// along with a cleanup func. Tests call defer cleanup() to stop the server.
func newTestRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, func() {
		_ = rdb.Close()
		mr.Close()
	}
}

// TestUploadKey_format verifies the Redis key prefix for uploads so any
// accidental rename is immediately caught.
func TestUploadKey_format(t *testing.T) {
	const id = "upload-uuid-1234"
	got := uploadKey(id)
	const want = "upload:" + id
	if got != want {
		t.Errorf("uploadKey(%q) = %q, want %q", id, got, want)
	}
}

// TestUploadStore_createAndGet verifies that a stored UploadState can be
// retrieved with all fields intact.
func TestUploadStore_createAndGet(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewUploadStore(rdb)
	ctx := context.Background()

	st := UploadState{
		UUID:      "test-upload-1",
		TenantID:  "tenant-abc",
		RepoName:  "myorg/myrepo",
		Offset:    0,
		ChunkKeys: nil,
	}

	if err := store.Create(ctx, st); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(ctx, "test-upload-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UUID != st.UUID {
		t.Errorf("UUID: got %q, want %q", got.UUID, st.UUID)
	}
	if got.TenantID != st.TenantID {
		t.Errorf("TenantID: got %q, want %q", got.TenantID, st.TenantID)
	}
	if got.RepoName != st.RepoName {
		t.Errorf("RepoName: got %q, want %q", got.RepoName, st.RepoName)
	}
}

// TestUploadStore_getNotFound verifies that looking up a non-existent upload
// returns ErrUploadNotFound.
func TestUploadStore_getNotFound(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewUploadStore(rdb)
	_, err := store.Get(context.Background(), "does-not-exist")
	if err != ErrUploadNotFound {
		t.Errorf("expected ErrUploadNotFound, got %v", err)
	}
}

// TestUploadStore_update verifies that Update persists modified state correctly
// and that Get returns the updated version.
func TestUploadStore_update(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewUploadStore(rdb)
	ctx := context.Background()

	st := UploadState{
		UUID:      "test-upload-update",
		TenantID:  "tenant-xyz",
		RepoName:  "org/repo",
		Offset:    0,
		ChunkKeys: []string{},
	}
	if err := store.Create(ctx, st); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate appending a chunk: update offset and chunk keys.
	st.Offset = 1024
	st.ChunkKeys = []string{"uploads/tenant-xyz/test-upload-update/0"}
	if err := store.Update(ctx, &st); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get(ctx, "test-upload-update")
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.Offset != 1024 {
		t.Errorf("Offset: got %d, want 1024", got.Offset)
	}
	if len(got.ChunkKeys) != 1 {
		t.Errorf("ChunkKeys len: got %d, want 1", len(got.ChunkKeys))
	}
}

// TestUploadStore_delete verifies that after Delete the entry is gone and Get
// returns ErrUploadNotFound.
func TestUploadStore_delete(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewUploadStore(rdb)
	ctx := context.Background()

	st := UploadState{UUID: "test-delete", TenantID: "t", RepoName: "o/r"}
	if err := store.Create(ctx, st); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Delete(ctx, "test-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := store.Get(ctx, "test-delete")
	if err != ErrUploadNotFound {
		t.Errorf("expected ErrUploadNotFound after Delete, got %v", err)
	}
}
