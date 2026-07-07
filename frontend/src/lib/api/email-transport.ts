import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface EmailTransportConfig {
  provider: "resend" | "smtp";
  enabled: boolean;
  from_address: string;
  from_name: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_tls_mode: "starttls" | "implicit" | "none";
  has_resend_key: boolean;
  has_smtp_password: boolean;
  last_test_at?: string;
  last_test_ok?: boolean;
  last_test_error?: string;
}

export interface EmailTransportPut {
  provider: "resend" | "smtp";
  enabled: boolean;
  from_address: string;
  from_name: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_tls_mode: "starttls" | "implicit" | "none";
  resend_api_key: string; // empty = keep existing
  smtp_password: string; // empty = keep existing
}

export const emailTransportKeys = { all: ["email-transport"] as const };

export function useEmailTransport() {
  return useQuery({
    queryKey: emailTransportKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<EmailTransportConfig>("/notifications/email-transport");
      return data;
    },
    staleTime: 30_000,
  });
}

export function useUpdateEmailTransport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: EmailTransportPut) => {
      const { data } = await apiClient.put<EmailTransportConfig>("/notifications/email-transport", body);
      return data;
    },
    onSuccess: (data) => qc.setQueryData(emailTransportKeys.all, data),
  });
}

export function useSendTestEmail() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ ok: boolean; error: string }>(
        "/notifications/email-transport/test",
      );
      return data;
    },
  });
}
