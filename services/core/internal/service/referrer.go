package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ReferrerDescriptor is a single entry in an OCI referrers response.
type ReferrerDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// ReferrerStore stores and retrieves OCI referrer relationships in Redis.
// Key layout: refs:<tenantID>:<repoName>:<subjectDigest> → Redis set of JSON descriptors.
type ReferrerStore struct {
	rdb *redis.Client
}

// NewReferrerStore creates a ReferrerStore backed by the given Redis client.
func NewReferrerStore(rdb *redis.Client) *ReferrerStore {
	return &ReferrerStore{rdb: rdb}
}

func referrerKey(tenantID, repoName, subjectDigest string) string {
	return fmt.Sprintf("refs:%s:%s:%s", tenantID, repoName, subjectDigest)
}

// Add stores a referrer descriptor for the given subject digest.
func (s *ReferrerStore) Add(ctx context.Context, tenantID, repoName, subjectDigest string, desc ReferrerDescriptor) error {
	b, err := json.Marshal(desc)
	if err != nil {
		return fmt.Errorf("marshal referrer: %w", err)
	}
	return s.rdb.SAdd(ctx, referrerKey(tenantID, repoName, subjectDigest), string(b)).Err()
}

// List returns all referrer descriptors for the given subject digest.
func (s *ReferrerStore) List(ctx context.Context, tenantID, repoName, subjectDigest string) ([]ReferrerDescriptor, error) {
	members, err := s.rdb.SMembers(ctx, referrerKey(tenantID, repoName, subjectDigest)).Result()
	if err != nil {
		return nil, fmt.Errorf("list referrers: %w", err)
	}
	descs := make([]ReferrerDescriptor, 0, len(members))
	for _, m := range members {
		var desc ReferrerDescriptor
		if json.Unmarshal([]byte(m), &desc) == nil {
			descs = append(descs, desc)
		}
	}
	return descs, nil
}
