// Package handler — unit tests for the FUT-021 repository PATCH shape.
//
// The load-bearing invariant this file pins is the three-state detection of
// max_cvss_score in the PATCH body:
//   - key absent      → don't touch (MaxCVSSScoreSet=false)
//   - key present nil → clear the gate (SQL NULL)
//   - key present int → set the threshold
// encoding/json alone can't distinguish (a) from (b) for a nullable pointer,
// so updateRepositoryBody has a custom UnmarshalJSON that pairs the pointer
// with a Set flag. Regressions here would silently break operators trying to
// clear a threshold with `{"max_cvss_score": null}`.
package handler

import (
	"encoding/json"
	"testing"
)

func TestUpdateRepositoryBody_MaxCVSSScore_absent(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"description": "hello"}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.MaxCVSSScoreSet {
		t.Errorf("MaxCVSSScoreSet should be false when key is absent")
	}
	if b.MaxCVSSScore != nil {
		t.Errorf("MaxCVSSScore should be nil when key is absent")
	}
	if b.Description != "hello" {
		t.Errorf("Description: got %q, want %q", b.Description, "hello")
	}
}

func TestUpdateRepositoryBody_MaxCVSSScore_explicitNull_clearsGate(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"max_cvss_score": null}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !b.MaxCVSSScoreSet {
		t.Errorf("MaxCVSSScoreSet should be true for explicit null (clear intent)")
	}
	if b.MaxCVSSScore != nil {
		t.Errorf("MaxCVSSScore should stay nil for explicit null")
	}
}

func TestUpdateRepositoryBody_MaxCVSSScore_setValue(t *testing.T) {
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"max_cvss_score": 70}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !b.MaxCVSSScoreSet {
		t.Errorf("MaxCVSSScoreSet should be true when value is present")
	}
	if b.MaxCVSSScore == nil || *b.MaxCVSSScore != 70 {
		t.Errorf("MaxCVSSScore: got %v, want *70", b.MaxCVSSScore)
	}
}

func TestUpdateRepositoryBody_MaxCVSSScore_setZero_distinguishedFromClear(t *testing.T) {
	// Threshold=0 is a legal (if extreme) operator choice — every scan
	// with any finding fails. Must not collapse to "clear the gate".
	var b updateRepositoryBody
	if err := json.Unmarshal([]byte(`{"max_cvss_score": 0}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !b.MaxCVSSScoreSet {
		t.Errorf("MaxCVSSScoreSet should be true for value=0")
	}
	if b.MaxCVSSScore == nil || *b.MaxCVSSScore != 0 {
		t.Errorf("MaxCVSSScore: got %v, want *0", b.MaxCVSSScore)
	}
}

func TestUpdateRepositoryBody_MaxCVSSScore_nonInteger_rejected(t *testing.T) {
	// A string here should surface an error from encoding/json so the
	// handler returns 400 rather than silently ignoring the field.
	var b updateRepositoryBody
	err := json.Unmarshal([]byte(`{"max_cvss_score": "70"}`), &b)
	if err == nil {
		t.Error("expected error for non-integer max_cvss_score, got nil")
	}
}
