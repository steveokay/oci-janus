// Package handler — unit tests pinning the three-state `storage_quota_bytes`
// field on updateRepositoryBody (Tier 2 #2, quota-override slice).
//
// Like description / immutable_tags, storage_quota_bytes is a pointer so the
// handler can tell "key absent → leave the quota alone" from "key present →
// apply it". A plain int64 would decode an absent key to 0 and the handler
// would reset the repo's quota to zero on every unrelated PATCH.
package handler

import (
	"encoding/json"
	"testing"
)

func TestUpdateRepositoryBody_StorageQuota_absent_leavesNil(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"description": "x"}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.StorageQuotaBytes != nil {
		t.Errorf("StorageQuotaBytes should be nil when the key is absent, got %d", *b.StorageQuotaBytes)
	}
}

func TestUpdateRepositoryBody_StorageQuota_present_setsValue(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"storage_quota_bytes": 5368709120}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.StorageQuotaBytes == nil || *b.StorageQuotaBytes != 5368709120 {
		t.Errorf("StorageQuotaBytes: got %v, want *5368709120", b.StorageQuotaBytes)
	}
}

func TestUpdateRepositoryBody_StorageQuota_nonInteger_rejected(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"storage_quota_bytes": "5G"}`), &b); err == nil {
		t.Error("expected error for non-integer storage_quota_bytes, got nil")
	}
}
