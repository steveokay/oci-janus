import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface NotificationWebhookConfig {
  url: string;
  enabled: boolean;
  has_secret: boolean;
  enabled_categories: string[];
  last_test_at?: string;
  last_test_ok?: boolean;
  last_test_error?: string;
}

export interface NotificationWebhookPut {
  url: string;
  enabled: boolean;
  secret: string; // empty = keep existing
  enabled_categories: string[];
}

export const notificationWebhookKeys = { all: ["notification-webhook"] as const };

export function useNotificationWebhook() {
  return useQuery({
    queryKey: notificationWebhookKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<NotificationWebhookConfig>("/notifications/webhook-config");
      return data;
    },
    staleTime: 30_000,
  });
}

export function useUpdateNotificationWebhook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: NotificationWebhookPut) => {
      const { data } = await apiClient.put<NotificationWebhookConfig>("/notifications/webhook-config", body);
      return data;
    },
    onSuccess: (data) => {
      qc.setQueryData(notificationWebhookKeys.all, data);
      // The matrix Webhook column reads from this config — refresh it too.
      void qc.invalidateQueries({ queryKey: ["notification-preferences"] });
    },
  });
}

export function useSendTestNotificationWebhook() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ ok: boolean; error: string }>(
        "/notifications/webhook-config/test",
      );
      return data;
    },
  });
}
