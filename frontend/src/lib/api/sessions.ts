// Beacon — active session list (Tier-1 #1 session management).
// Wire shape from services/auth GET/DELETE/POST /api/v1/users/me/sessions.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// One signed-in device/session as returned by services/auth. `current` flags
// the session that owns the caller's own JWT — revoking it logs *this* browser
// out (the backend kills the sid, the next request 401s).
export interface Session {
  sid: string;
  device_label: string;
  user_agent: string;
  ip: string;
  created_at: string;
  last_active_at: string;
  current: boolean;
}

export const sessionKeys = {
  all: ["sessions"] as const,
};

// useSessions loads the caller's active sessions. The endpoint wraps the list
// in `{ sessions: [...] }`; we unwrap it (defaulting to [] so an empty/nullish
// body doesn't crash the table).
export function useSessions() {
  return useQuery({
    queryKey: sessionKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<{ sessions: Session[] }>(
        "/users/me/sessions",
      );
      return data.sessions ?? [];
    },
    staleTime: 20_000,
  });
}

// useRevokeSession revokes a single session by sid (DELETE → 204). On success
// we invalidate the list so the revoked row drops out on the next refetch.
export function useRevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (sid: string) => {
      await apiClient.delete(`/users/me/sessions/${encodeURIComponent(sid)}`);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sessionKeys.all });
    },
  });
}

// useRevokeOtherSessions signs out every session except the caller's own and
// returns the count revoked (for the confirmation toast).
export function useRevokeOtherSessions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ revoked: number }>(
        "/users/me/sessions/revoke-others",
        {},
      );
      return data.revoked;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sessionKeys.all });
    },
  });
}
