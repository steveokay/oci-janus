import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// FUT-019 Phase 2 — per-user notification preferences. The BFF
// always returns a complete matrix (one row per known category) so
// the FE never has to materialise defaults locally — bell=true /
// email=false / webhook=false on unset rows is the server's
// responsibility.

export interface NotificationPreferenceRow {
  key: string;
  label: string;
  description: string;
  shipped_in: string;
  bell_enabled: boolean;
  email_enabled: boolean;
  webhook_enabled: boolean;
}

export interface NotificationPreferencesPage {
  preferences: NotificationPreferenceRow[];
}

interface PatchRow {
  category: string;
  bell_enabled: boolean;
  email_enabled: boolean;
  webhook_enabled: boolean;
}

interface PatchBody {
  preferences: PatchRow[];
}

export const notificationPreferenceKeys = {
  all: ["notification-preferences"] as const,
};

export function useNotificationPreferences() {
  return useQuery({
    queryKey: notificationPreferenceKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<NotificationPreferencesPage>(
        "/users/me/notification-preferences",
      );
      return data;
    },
    staleTime: 30_000,
  });
}

export function useUpdateNotificationPreferences() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: PatchBody): Promise<NotificationPreferencesPage> => {
      const { data } = await apiClient.patch<NotificationPreferencesPage>(
        "/users/me/notification-preferences",
        body,
      );
      return data;
    },
    onSuccess: (data) => {
      // Server returns the full matrix — seed the query cache
      // directly so the FE doesn't need a follow-up GET.
      qc.setQueryData(notificationPreferenceKeys.all, data);
    },
  });
}
