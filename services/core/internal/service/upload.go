package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const uploadTTL = time.Hour

// UploadState is persisted in Redis for the lifetime of a chunked blob upload.
type UploadState struct {
	UUID     string `json:"uuid"`
	TenantID string `json:"tenant_id"`
	RepoName string `json:"repo_name"`
	Offset   int64  `json:"offset"`
}

// UploadStore manages upload state in Redis.
type UploadStore struct {
	rdb *redis.Client
}

// NewUploadStore constructs an UploadStore.
func NewUploadStore(rdb *redis.Client) *UploadStore {
	return &UploadStore{rdb: rdb}
}

func uploadKey(uuid string) string {
	return "upload:" + uuid
}

// Create stores a new upload state entry and returns it.
func (s *UploadStore) Create(ctx context.Context, st UploadState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal upload state: %w", err)
	}
	return s.rdb.Set(ctx, uploadKey(st.UUID), b, uploadTTL).Err()
}

// Get retrieves upload state by UUID. Returns ErrUploadNotFound if missing.
func (s *UploadStore) Get(ctx context.Context, uuid string) (*UploadState, error) {
	b, err := s.rdb.Get(ctx, uploadKey(uuid)).Bytes()
	if err == redis.Nil {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get upload state: %w", err)
	}
	var st UploadState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("unmarshal upload state: %w", err)
	}
	return &st, nil
}

// Update writes back modified state, resetting the TTL.
func (s *UploadStore) Update(ctx context.Context, st *UploadState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal upload state: %w", err)
	}
	return s.rdb.Set(ctx, uploadKey(st.UUID), b, uploadTTL).Err()
}

// Delete removes upload state (after completion or cancellation).
func (s *UploadStore) Delete(ctx context.Context, uuid string) error {
	return s.rdb.Del(ctx, uploadKey(uuid)).Err()
}
