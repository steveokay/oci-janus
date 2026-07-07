import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface EmailDelivery {
  id: string;
  category: string;
  subject: string;
  to_address: string;
  status: "pending" | "sent" | "failed";
  last_error: string;
  created_at?: string;
  sent_at?: string;
}

export interface EmailDeliveriesPage {
  deliveries: EmailDelivery[];
}

export const emailDeliveryKeys = { all: ["email-deliveries"] as const };

export function useEmailDeliveries(enabled = true) {
  return useQuery({
    queryKey: emailDeliveryKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<EmailDeliveriesPage>("/notifications/email-deliveries");
      return data;
    },
    staleTime: 30_000,
    enabled,
  });
}
