import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Activity hooks (FE-API-048 T23).
//
// Wraps GET /api/v1/access/activity — the principal activity feed from
// services/auth/internal/handler/http_access_activity.go.

// PrincipalActivity mirrors service.PrincipalActivity from
// services/auth/internal/service/activity.go.
//
// NOTE on Status: the spec task suggested "ok" | "denied", but the backend
// sources this field from audit metadata["outcome"] which is populated by the
// audit service with "success" | "failure". The TS type reflects what the
// backend actually returns, not the spec's proposed union.
export interface PrincipalActivity {
  // At is the ISO-8601 wall-clock time the event occurred.
  at: string;
  // Action is the audit action code (e.g. "push.image", "pull.image").
  action: string;
  // Repo is the repository name when the event is repository-scoped; empty otherwise.
  repo: string;
  // SourceIP is the initiating IP address when present in event metadata.
  source_ip: string;
  // APIKeyID is the key UUID when the request was authenticated with an API key.
  api_key_id: string;
  // Status is the audit outcome — "success" or "failure" — extracted from
  // audit metadata["outcome"]. Matches the backend's ActivityService.trimNotifications.
  status: "success" | "failure";
}

interface ActivityResponse {
  activity: PrincipalActivity[];
  next_page_token?: string;
}

// useActivity fetches the recent activity feed for a given principal.
//
// principalUserID is the users.id (UUID string) of the principal to query.
// Non-admin callers may only query their own ID; the backend enforces this
// server-side (returns 404 for unauthorised access, per spec §5.3).
//
// limit controls the maximum number of events returned (default 50). The
// backend caps this at 200.
export function useActivity(principalUserID?: string, limit = 50) {
  return useQuery({
    // Only fire once a principalUserID is available so components can mount
    // before the caller's user ID is resolved from the auth store.
    enabled: !!principalUserID,
    queryKey: ["access-activity", principalUserID, limit] as const,
    queryFn: async () => {
      const { data } = await apiClient.get<ActivityResponse>(
        "/access/activity",
        { params: { principal_user_id: principalUserID, limit } },
      );
      return data;
    },
    // 10-second stale window — activity data is informational and does not need
    // real-time freshness.
    staleTime: 10_000,
  });
}
