// Package bootstrap holds the per-service bootstrap-tenant-id lookup that
// every backend service performs once at startup in DEPLOYMENT_MODE=single.
//
// REDESIGN-001 Phase 3.4 — rule-of-three extraction.
//
// services/auth (PR #162) and services/metadata (PR #164) each shipped a
// byte-for-byte clone of the post-dial RPC + parse logic. The code-review on
// #164 explicitly flagged: "lift to libs/tenant/bootstrap before the third
// service hits this." This package is that lift.
//
// The dial half (TENANT_GRPC_ADDR + mTLS creds) stays in each service so it
// can keep using the service's own config struct; this package only owns the
// "given a TenantServiceClient, fetch the UUID" step.
//
// Usage shape (every service):
//
//	tenantConn, err := grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(creds))
//	if err != nil { return fmt.Errorf("dial tenant: %w", err) }
//	defer tenantConn.Close()
//	id, err := bootstrap.FetchTenantID(ctx, tenantv1.NewTenantServiceClient(tenantConn))
//	if err != nil { return fmt.Errorf("phase 3.4 bootstrap: %w", err) }
//	interceptor := grpcmw.SingleTenantInjector(id)
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
)

// LookupTimeout bounds the GetDeploymentMetadata RPC so a stalled tenant
// service can't block service startup indefinitely. The tenant service must
// be reachable for single-mode startup anyway, so blocking longer just hides
// the misconfiguration.
const LookupTimeout = 5 * time.Second

// DeploymentMetadataKey is the well-known key under which the bootstrap CLI
// writes the deployment's tenant id. Centralised here so the key string is
// not duplicated at every call site.
const DeploymentMetadataKey = "bootstrap_tenant_id"

// FetchTenantID calls tenant.GetDeploymentMetadata(key="bootstrap_tenant_id")
// against the supplied client and returns the validated UUID string ready to
// hand to libs/middleware/grpc.SingleTenantInjector.
//
// Behaviour:
//   - tenant returns NotFound       → error (deployment not bootstrapped yet)
//   - tenant returns any other err  → error wrapped with the RPC name
//   - JSONB value is not a JSON str → error ("parse bootstrap_tenant_id JSON")
//   - JSON value isn't a UUID       → error ("bootstrap_tenant_id ... not a valid UUID")
//
// Callers wrap the returned error with a service-specific phase tag (e.g.
// `"phase 3.4 bootstrap tenant id lookup: %w"`) so logs can attribute the
// failure to the right service.
func FetchTenantID(ctx context.Context, client tenantv1.TenantServiceClient) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, LookupTimeout)
	defer cancel()

	resp, err := client.GetDeploymentMetadata(callCtx, &tenantv1.GetDeploymentMetadataRequest{
		Key: DeploymentMetadataKey,
	})
	if err != nil {
		return "", fmt.Errorf("GetDeploymentMetadata(%s): %w", DeploymentMetadataKey, err)
	}

	// The deployment_metadata table stores the value as JSONB; for
	// bootstrap_tenant_id specifically it's a JSON-encoded string
	// (`"<uuid>"`). Unmarshal into a string then validate as a UUID so a
	// typo'd or partially-written value fails here rather than silently
	// becoming a constant interceptor input.
	var idStr string
	if err := json.Unmarshal(resp.GetValue(), &idStr); err != nil {
		return "", fmt.Errorf("parse %s JSON: %w", DeploymentMetadataKey, err)
	}
	if _, err := uuid.Parse(idStr); err != nil {
		return "", fmt.Errorf("%s %q is not a valid UUID: %w", DeploymentMetadataKey, idStr, err)
	}
	return idStr, nil
}
