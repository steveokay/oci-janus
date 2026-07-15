// Package handler — unit tests pinning the three-state `is_public` field on
// updateRepositoryBody (Tier 2 #2, visibility slice).
//
// Like immutable_tags, is_public is a *bool so the handler can tell "key
// absent → leave visibility alone" from "key present → apply it". A plain bool
// would decode an absent key to false and flip every repo private on any
// unrelated PATCH.
package handler

import (
	"encoding/json"
	"testing"
)

func TestUpdateRepositoryBody_IsPublic_absent_leavesNil(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"description": "x"}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.IsPublic != nil {
		t.Errorf("IsPublic should be nil when the key is absent, got %v", *b.IsPublic)
	}
}

func TestUpdateRepositoryBody_IsPublic_true(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"is_public": true}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.IsPublic == nil || *b.IsPublic != true {
		t.Errorf("IsPublic: got %v, want *true", b.IsPublic)
	}
}

func TestUpdateRepositoryBody_IsPublic_false_isExplicit(t *testing.T) {
	// Explicit false must be a non-nil pointer (distinct from absent) so the
	// handler fires the flip to private.
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"is_public": false}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.IsPublic == nil {
		t.Fatal("IsPublic should be non-nil for an explicit false")
	}
	if *b.IsPublic != false {
		t.Errorf("IsPublic: got %v, want false", *b.IsPublic)
	}
}
