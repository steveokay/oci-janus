// Package handler — unit tests pinning the three-state `description` field on
// updateRepositoryBody.
//
// Description is a pointer so the handler can distinguish "key absent → leave
// the description alone" from "key present → set it (even to empty)". Before
// this, Description was a plain string: any PATCH that touched only a security
// flag (e.g. {"immutable_tags": true}, sent by the CVSS / immutability /
// signature Settings cards) decoded Description="" and the handler's
// unconditional UpdateRepository call blanked the repo's description — a latent
// data-loss bug this file guards against regressing.
package handler

import (
	"encoding/json"
	"testing"
)

func TestUpdateRepositoryBody_Description_absent_leavesNil(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"immutable_tags": true}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.Description != nil {
		t.Errorf("Description should be nil when the key is absent, got %q", *b.Description)
	}
}

func TestUpdateRepositoryBody_Description_present_setsValue(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"description": "hello world"}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.Description == nil || *b.Description != "hello world" {
		t.Errorf("Description: got %v, want *%q", b.Description, "hello world")
	}
}

func TestUpdateRepositoryBody_Description_emptyString_isExplicitClear(t *testing.T) {
	// An explicit "" is a deliberate "clear the description" — it must be a
	// non-nil pointer so the handler fires the update, distinct from absent.
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"description": ""}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.Description == nil {
		t.Fatal("Description should be non-nil for an explicit empty string")
	}
	if *b.Description != "" {
		t.Errorf("Description: got %q, want empty", *b.Description)
	}
}
