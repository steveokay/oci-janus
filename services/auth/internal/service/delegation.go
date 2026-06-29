// Package service — delegation guards (REDESIGN-001 Phase 5.3).
//
// This file enforces the "delegator dominates delegatee" rule that every mature
// RBAC system uses: a caller cannot mint authority that exceeds the authority
// they themselves hold. Two surfaces apply the rule today:
//
//   - GrantRole (handler/grpc.go): the granter must hold an equal-or-higher
//     role at the same scope (or an ancestor scope) as the role they want to
//     grant. Without this guard, an admin-of-orgA could grant owner on orgA
//     (rank promotion) or admin on orgB (cross-tenant escalation).
//
//   - CreateServiceAccount (service/service_account.go): the requested
//     AllowedScopes must be a subset of the creator's effective scope set.
//     Without this guard, a reader on repoX could mint an SA with push
//     authority on repoX — laundering authority through a machine identity.
//
// Both surfaces share the same canonical role-rank table and the same
// scope-dominance rule (an org-scope assignment dominates any repo within
// that org). Keeping the helpers in one file makes the security contract
// easy to audit and prevents one path drifting from the other.
package service

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// roleRank returns the privilege rank of a role name; a higher rank dominates
// a lower one. The ordering owner > admin > writer > reader is the canonical
// hierarchy used across services/auth (mirrored by actionsForRole in the
// handler layer). Unknown role names return 0 so a typo can never out-rank a
// legitimate role — the dominance check below treats rank 0 as "no power".
func roleRank(role string) int {
	switch role {
	case "owner":
		return 4
	case "admin":
		return 3
	case "writer":
		return 2
	case "reader":
		return 1
	default:
		return 0
	}
}

// scopeDominates reports whether a role assignment held at
// (holderScopeType, holderScopeValue) covers the target scope
// (targetScopeType, targetScopeValue).
//
// Dominance rules (matches Phase 5.3 of the redesign plan):
//
//   - Same (type, value) pair dominates itself.
//
//   - A tenant-level assignment dominates any org and any repo within the
//     same tenant. All RBAC queries are already tenant-bounded (the gRPC
//     handler scopes assignments to the active tenant before calling this
//     helper), so any "org" or "repo" target seen here is implicitly within
//     the holder's tenant — checking holder type alone is sufficient.
//
//   - An org-level assignment dominates any repo within the same org. Repo
//     scope_value is encoded as "<org>/<repo>"; we match by prefix on
//     "<org>/" so that "myorg" does not accidentally cover "myorg-2/foo".
//
// Repo-level holders never dominate an org- or tenant-level target —
// granting at a higher tier requires a higher-tier holder.
func scopeDominates(holderType, holderValue, targetType, targetValue string) bool {
	if holderType == targetType && holderValue == targetValue {
		return true
	}
	// Tenant-scope assignment covers every org and repo in the tenant.
	// Required by the existing tenant-admin → org-admin elevation flow
	// (handler/tenant_users.go handleElevateToOrgAdmin); without this rule
	// a tenant admin cannot promote an org admin even though they hold the
	// strictly stronger role. The handler scopes assignments to the active
	// tenant before calling this helper so we never need to compare tenant
	// identifiers here.
	if holderType == "tenant" && (targetType == "org" || targetType == "repo") {
		return true
	}
	// Org-scope assignment covers any repo whose scope_value starts with
	// "<org>/". The trailing slash is load-bearing — without it "myorg"
	// would (incorrectly) dominate "myorg-prod/foo".
	if holderType == "org" && targetType == "repo" {
		return strings.HasPrefix(targetValue, holderValue+"/")
	}
	return false
}

// VerifyDelegationBound enforces the delegator-dominates-delegatee rule for
// GrantRole. It scans the caller's role_assignments rows and returns nil iff
// the caller holds at least one assignment whose scope dominates the target
// (per scopeDominates) AND whose role rank is >= the rank of the role being
// granted (per roleRank).
//
// Failure returns a codes.PermissionDenied gRPC status error — NOT
// InvalidArgument — because the request shape is well-formed; the caller
// simply lacks the authority to fulfil it. An unknown grantedRole returns
// InvalidArgument so callers can distinguish "typo" from "you can't do that".
//
// Bootstrap / system grants: pass an empty callerAssignments slice with a
// caller that this helper does not see (e.g. the bootstrap CLI). The
// GrantRole handler decides whether to skip the call entirely (granted_by ==
// uuid.Nil) before invoking this helper.
func VerifyDelegationBound(
	callerAssignments []repository.RoleAssignment,
	grantedRole, grantedScopeType, grantedScopeValue string,
) error {
	grantedR := roleRank(grantedRole)
	if grantedR == 0 {
		// Unknown role names are a structural error in the request — the role
		// table is closed (owner/admin/writer/reader). Surfacing this as
		// InvalidArgument lets clients distinguish "you misspelled the role"
		// from "you lack authority to delegate this role".
		return status.Errorf(codes.InvalidArgument, "unknown role %q", grantedRole)
	}
	for _, a := range callerAssignments {
		if !scopeDominates(a.ScopeType, a.ScopeValue, grantedScopeType, grantedScopeValue) {
			continue
		}
		if roleRank(a.RoleName) >= grantedR {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied,
		"caller cannot delegate role %q on %s=%s: requires an ancestor-or-equal scope assignment with rank >= %s",
		grantedRole, grantedScopeType, grantedScopeValue, grantedRole)
}

// effectiveActionsForRole mirrors handler.actionsForRole at the service layer
// so VerifyAllowedScopesSubset does not have to reach into the handler
// package (which would create an import cycle). Must stay in lock-step with
// handler/grpc.go's actionsForRole — both encode the same role→action map.
func effectiveActionsForRole(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"push", "pull", "delete"}
	case "writer":
		return []string{"push", "pull"}
	case "reader":
		return []string{"pull"}
	default:
		return nil
	}
}

// effectiveActionSet returns the union of OCI actions implied by every role
// assignment the caller holds. This is the "effective scope" set the caller
// can delegate to a service account via AllowedScopes.
//
// Unknown role names contribute nothing — the default branch of
// effectiveActionsForRole returns nil so a typo cannot widen the set.
func effectiveActionSet(callerAssignments []repository.RoleAssignment) map[string]struct{} {
	out := make(map[string]struct{}, 4)
	for _, a := range callerAssignments {
		for _, act := range effectiveActionsForRole(a.RoleName) {
			out[act] = struct{}{}
		}
	}
	return out
}

// VerifyAllowedScopesSubset enforces the delegator-dominates-delegatee rule
// for CreateServiceAccount. The SA's requested AllowedScopes must be a
// subset of the union of actions implied by the creator's role assignments;
// an empty AllowedScopes is always allowed (an SA with no scope grants
// nothing, so no escalation is possible).
//
// Returns a codes.PermissionDenied gRPC status error naming the first scope
// that exceeds the caller's grant, or nil on success.
//
// We don't enforce per-scope scope-dominance (i.e. matching scope values to
// the caller's scope_value) here because AllowedScopes today are flat OCI
// action strings (pull/push/delete plus future repository:<repo>:<action>
// forms). Phase 5.4+ can tighten this further; today it is the same check
// the OCI auth flow applies before stamping a JWT.
func VerifyAllowedScopesSubset(
	callerAssignments []repository.RoleAssignment,
	requestedScopes []string,
) error {
	if len(requestedScopes) == 0 {
		// An empty scope set carries no authority. Always allowed — even a
		// reader-of-nothing can mint a no-op SA.
		return nil
	}
	allowed := effectiveActionSet(callerAssignments)
	for _, s := range requestedScopes {
		if _, ok := allowed[s]; !ok {
			return status.Errorf(codes.PermissionDenied,
				"caller cannot delegate scope %q to a service account: not present in caller's effective grant",
				s)
		}
	}
	return nil
}
