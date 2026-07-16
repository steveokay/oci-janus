// mcp_bff_routes_test.go — FUT-082 tests for the three BFF routes that
// close the gaps in the registry-mcp read-only tool surface:
//
//	GET /api/v1/service-accounts   → auth.ListServiceAccounts
//	GET /api/v1/audit              → audit.ListAuditEvents (tenant-wide)
//	GET /api/v1/promotions         → metadata.ListPromotions (tenant-wide)
//
// service-accounts + audit reuse the shared newTestEnv fakes (fakeAuthServer
// / fakeAuditServer gain ListServiceAccounts / ListAuditEvents below).
// The tenant-wide promotions test uses newPromoteTestEnv so it can assert the
// BFF forwarded an EMPTY org/repo (the metadata signal for "whole tenant").
package handler_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// lastAuditListLimit captures the limit the BFF forwarded on the most recent
// ListAuditEvents call so the cap test can assert coercion. int32 to match the
// proto field width.
var lastAuditListLimit atomic.Int32

// ---------------------------------------------------------------------------
// GET /api/v1/service-accounts
// ---------------------------------------------------------------------------

func TestListServiceAccountsBFF_returnsInventory(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/service-accounts", readerToken)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		ServiceAccounts []struct {
			ID             string   `json:"id"`
			Name           string   `json:"name"`
			AllowedScopes  []string `json:"allowed_scopes"`
			Disabled       bool     `json:"disabled"`
			ActiveKeyCount int      `json:"active_key_count"`
			Origin         string   `json:"origin"`
		} `json:"service_accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.ServiceAccounts) != 1 {
		t.Fatalf("service_accounts len = %d, want 1", len(body.ServiceAccounts))
	}
	sa := body.ServiceAccounts[0]
	if sa.Name != "ci-bot" || sa.ActiveKeyCount != 2 || !sa.Disabled {
		t.Errorf("unexpected SA row: %+v", sa)
	}
	// MCP provenance: the origin field must surface through the BFF DTO.
	if sa.Origin != "mcp-connect" {
		t.Errorf("Origin = %q, want mcp-connect", sa.Origin)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/audit
// ---------------------------------------------------------------------------

func TestListAuditEventsBFF_returnsEvents(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/audit?limit=10&actor_id=u1&action_prefix=image.pushed", readerToken)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Events []struct {
			ID         string `json:"id"`
			OccurredAt string `json:"occurred_at"`
			Action     string `json:"action"`
			ActorID    string `json:"actor_id"`
			ActorKind  string `json:"actor_kind"`
			Resource   string `json:"resource"`
			Outcome    string `json:"outcome"`
			IPAddress  string `json:"ip_address"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(body.Events))
	}
	e := body.Events[0]
	if e.Action != "image.pushed" || e.ActorKind != "user" || e.IPAddress != "10.0.0.1" {
		t.Errorf("unexpected audit row: %+v", e)
	}
}

// TestListAuditEventsBFF_limitCapped verifies the BFF coerces an oversized
// limit down to the 500 cap before forwarding to the audit service — mirrors
// the client-side cap in services/mcp so neither layer can pull the whole
// trail in one call.
func TestListAuditEventsBFF_limitCapped(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/audit?limit=99999", readerToken)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := lastAuditListLimit.Load(); got != 500 {
		t.Errorf("forwarded limit = %d, want 500 (capped)", got)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/promotions (tenant-wide)
// ---------------------------------------------------------------------------

func TestListPromotionsTenantWide_forwardsEmptyOrgRepo(t *testing.T) {
	env := newPromoteTestEnv(t)
	env.meta.listFunc = func(_ context.Context, req *metadatav1.ListPromotionsRequest) (*metadatav1.ListPromotionsResponse, error) {
		return &metadatav1.ListPromotionsResponse{
			Promotions: []*metadatav1.Promotion{
				{
					Id:         "p1",
					SrcOrg:     "dev",
					SrcRepo:    "api",
					SrcTag:     "v1",
					DstOrg:     "prod",
					DstRepo:    "api",
					DstTag:     "v1",
					PromotedAt: timestamppb.Now(),
				},
			},
		}, nil
	}

	resp := env.get(t, "/api/v1/promotions", readerToken)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Promotions []struct {
			ID     string `json:"id"`
			SrcOrg string `json:"src_org"`
			DstOrg string `json:"dst_org"`
		} `json:"promotions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Promotions) != 1 || body.Promotions[0].SrcOrg != "dev" || body.Promotions[0].DstOrg != "prod" {
		t.Fatalf("unexpected promotions body: %+v", body.Promotions)
	}
	// The tenant-wide route MUST forward an empty org + repo — that's the
	// metadata signal that switches ListPromotions to the whole-tenant query.
	if len(env.meta.listCalls) != 1 {
		t.Fatalf("listCalls = %d, want 1", len(env.meta.listCalls))
	}
	if env.meta.listCalls[0].GetOrg() != "" || env.meta.listCalls[0].GetRepo() != "" {
		t.Errorf("forwarded org=%q repo=%q, want both empty",
			env.meta.listCalls[0].GetOrg(), env.meta.listCalls[0].GetRepo())
	}
}

// ---------------------------------------------------------------------------
// Fake-server extensions for the three new RPCs. Kept in this file so the
// FUT-082 surface is self-contained; the shared fakeAuthServer /
// fakeAuditServer types are declared in handler_test.go.
// ---------------------------------------------------------------------------

// ListAuditEvents returns one canned row and records the forwarded limit so
// the cap test can assert coercion. actor_id / action are echoed back where
// useful; the row uses actor_type=user to exercise the actor_kind remap.
func (s *fakeAuditServer) ListAuditEvents(_ context.Context, req *auditv1.ListAuditEventsRequest) (*auditv1.ListAuditEventsResponse, error) {
	lastAuditListLimit.Store(req.GetLimit())
	return &auditv1.ListAuditEventsResponse{
		Events: []*auditv1.AuditEventRecord{
			{
				Id:         "e1",
				TenantId:   req.GetTenantId(),
				ActorId:    "u1",
				ActorType:  "user",
				ActorIp:    "10.0.0.1",
				Action:     "image.pushed",
				Resource:   "prod/api:v1",
				Outcome:    "success",
				OccurredAt: timestamppb.Now(),
			},
		},
	}, nil
}

// ListServiceAccounts returns a single canned CI-bot row so the BFF mapping
// (proto → JSON) is exercised end to end.
func (s *fakeAuthServer) ListServiceAccounts(_ context.Context, req *authv1.ListServiceAccountsRequest) (*authv1.ListServiceAccountsResponse, error) {
	return &authv1.ListServiceAccountsResponse{
		ServiceAccounts: []*authv1.ServiceAccountSummary{
			{
				Id:             "sa-1",
				TenantId:       req.GetTenantId(),
				Name:           "ci-bot",
				Description:    "build pipeline",
				AllowedScopes:  []string{"repo:push", "repo:pull"},
				Disabled:       true,
				ActiveKeyCount: 2,
				CreatedAt:      timestamppb.Now(),
				Origin:         "mcp-connect",
			},
		},
	}, nil
}
