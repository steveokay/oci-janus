import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — current-user hooks (FE-API-011/012/013).
//
// Backend surfaces:
//   GET   /api/v1/users/me                  → MeResponse
//   PATCH /api/v1/users/me                  body: { display_name?, email? }
//   POST  /api/v1/users/me/password         body: { current_password, new_password }
//
// All three live on registry-auth. Vite proxy already routes /api/v1/login
// and friends to :8080 — these paths are folded under the same prefix.

export interface Membership {
  scope_type: string;
  scope_value: string;
  role: string;
}

// ServiceAccountFields is the nested object present on saCallerResponse (T16).
// Only populated when type === "service_account".
export interface ServiceAccountFields {
  id: string;
  name: string;
  description: string;
  allowed_scopes: string[];
}

// MeResponse is the polymorphic shape returned by GET /api/v1/users/me.
//
// FE-API-048 T16 made the response polymorphic:
//   - type === "user"            → human caller; human-only fields present.
//   - type === "service_account" → SA shadow user; service_account nested object
//                                  is present; email is always null; user_id /
//                                  username / created_at / memberships are absent.
//
// `type` defaults to "user" defensively when the field is absent (old backends).
export interface MeResponse {
  // type discriminates human vs service-account callers (FE-API-048 T16).
  // Absent on pre-T16 backends — treat as "user" when undefined.
  type?: "user" | "service_account";
  // id is returned only for service_account callers (the shadow user UUID).
  id?: string;
  // user_id is returned only for human callers.
  user_id?: string;
  username?: string;
  email: string | null;
  display_name: string | null;
  created_at?: string;
  last_login_at?: string | null;
  tenant_id: string;
  // REDESIGN-001 Phase 4.3 — true means the user has dismissed or completed the wizard.
  onboarding_complete?: boolean;
  roles: string[];
  memberships?: Membership[];
  // service_account is present only when type === "service_account".
  service_account?: ServiceAccountFields;
}

export const meKeys = {
  all: ["me"] as const,
};

export function useMe() {
  return useQuery({
    queryKey: meKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<MeResponse>("/users/me");
      return data;
    },
    staleTime: 30_000,
  });
}

interface UpdateMeBody {
  display_name?: string | null;
  email?: string | null;
}

export function useUpdateMe() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateMeBody) => {
      const { data } = await apiClient.patch<MeResponse>("/users/me", body);
      return data;
    },
    onSuccess: (data) => {
      // Write the fresh user back into the cache so the page doesn't
      // flicker through a stale value before the next refetch.
      qc.setQueryData(meKeys.all, data);
    },
  });
}

interface ChangePasswordBody {
  current_password: string;
  new_password: string;
}

export function useChangePassword() {
  return useMutation({
    mutationFn: async (body: ChangePasswordBody) => {
      await apiClient.post("/users/me/password", body);
    },
  });
}

// REDESIGN-001 Phase 4.3 — mark the first-run onboarding wizard complete.
//
// Backend:
//   POST /api/v1/users/me/onboarding/complete  → 204 No Content
//
// Called by:
//   - /getting-started "Open repositories" / "Skip for now" buttons.
//   - (Future) a "Replay onboarding" link on Settings, once we wire one.
//
// The success handler patches the cached MeResponse so the very next render
// of `useMe()` reflects truth without waiting for a refetch — important
// because the post-wizard navigation lands on `/` which immediately reads
// `me.onboarding_complete` to decide whether to re-route. We still
// invalidate the key as a safety net in case the backend ever extends the
// payload past what we patched here.
export function useCompleteOnboarding() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await apiClient.post("/users/me/onboarding/complete");
    },
    onSuccess: () => {
      qc.setQueryData<MeResponse>(meKeys.all, (prev) =>
        prev ? { ...prev, onboarding_complete: true } : prev,
      );
      void qc.invalidateQueries({ queryKey: meKeys.all });
    },
  });
}
