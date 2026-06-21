import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — webhook hooks (FE-API-021..024 all live backend-side).
//
// The BFF surfaces eight calls:
//   GET    /api/v1/webhooks
//   POST   /api/v1/webhooks                       (returns plaintext secret once)
//   PATCH  /api/v1/webhooks/{id}
//   DELETE /api/v1/webhooks/{id}
//   GET    /api/v1/webhooks/{id}/deliveries
//   POST   /api/v1/webhooks/{id}/test             (synchronous)
//   POST   /api/v1/webhooks/{id}/rotate-secret    (returns plaintext secret once)

// ── Types ───────────────────────────────────────────────────────────────────

export interface WebhookEndpoint {
  endpoint_id: string;
  url: string;
  events: string[];
  active: boolean;
  created_at: string;
}

// Create + rotate hand the secret back exactly once. The dialog that
// receives this is responsible for displaying + offering copy; we never
// persist the plaintext anywhere on the client.
export interface WebhookSecretEnvelope {
  endpoint_id?: string;
  secret: string;
  created_at?: string;
}

export type DeliveryStatus = "pending" | "delivered" | "failed" | "dead";

export interface WebhookDelivery {
  delivery_id: string;
  endpoint_id: string;
  event_type: string;
  status: DeliveryStatus;
  attempts: number;
  max_attempts: number;
  last_error?: string;
  next_attempt_at?: string | null;
  created_at: string;
  delivered_at?: string | null;
}

export interface TestDispatchResult {
  status_code: number;
  error: string;
  duration_ms: number;
}

interface ListResponse {
  endpoints: WebhookEndpoint[];
}

interface DeliveriesResponse {
  deliveries: WebhookDelivery[];
}

// ── Event catalog ───────────────────────────────────────────────────────────
// Operator-facing routing keys. We deliberately omit internal events
// (scan.queued, webhook.queued/delivered/failed, store.queued, tenant.*)
// since those are infrastructure plumbing, not user signals.
//
// Source of truth: libs/rabbitmq/events/events.go in this monorepo.
export const WEBHOOK_EVENT_CATALOG: Array<{
  key: string;
  label: string;
  description: string;
}> = [
  {
    key: "push.completed",
    label: "Push completed",
    description: "An image successfully landed in a repository.",
  },
  // FE-API-042 — fires on every successful manifest GET. Sampling is
  // controlled server-side via PULL_EVENT_SAMPLE_RATE; subscribers should
  // expect high volume on busy registries and rate-limit accordingly.
  {
    key: "pull.image",
    label: "Image pulled",
    description:
      "An image was successfully pulled. Fires from the registry on every manifest GET (sampling configurable server-side).",
  },
  {
    key: "push.failed",
    label: "Push failed",
    description: "A push attempt was rejected (quota, RBAC, malformed manifest).",
  },
  {
    key: "manifest.deleted",
    label: "Manifest deleted",
    description: "A manifest was removed from a repository.",
  },
  {
    key: "tag.deleted",
    label: "Tag deleted",
    description: "A tag pointer was removed; manifest survives by digest.",
  },
  {
    key: "scan.completed",
    label: "Scan completed",
    description: "A vulnerability scan finished — payload carries severity_counts.",
  },
  {
    key: "scan.policy_blocked",
    label: "Scan policy blocked",
    description: "A push was blocked by the tenant's block-on-severity policy.",
  },
  {
    key: "image.signed",
    label: "Image signed",
    description: "A Cosign or Notary v2 signature was attached to an image.",
  },
  // FE-API-041 — retention lifecycle. Operators subscribe to surface
  // "this policy is about to delete N manifests" notifications in their
  // incident channels rather than tailing gc_runs.
  {
    key: "retention.evaluated",
    label: "Retention evaluated",
    description:
      "A retention sweep evaluated a policy and computed the would-delete set.",
  },
  {
    key: "retention.applied",
    label: "Retention applied",
    description:
      "A retention sweep soft-deleted manifests; grace window starts now.",
  },
  {
    key: "retention.grace_completed",
    label: "Retention grace completed",
    description:
      "A retention grace sweep hard-deleted manifests + freed blobs.",
  },
];

// ── Key factory ─────────────────────────────────────────────────────────────

export const webhookKeys = {
  all: ["webhooks"] as const,
  list: () => [...webhookKeys.all, "list"] as const,
  detail: (id: string) => [...webhookKeys.all, "detail", id] as const,
  deliveries: (id: string) => [...webhookKeys.all, "deliveries", id] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

export function useWebhooks() {
  return useQuery({
    queryKey: webhookKeys.list(),
    queryFn: async () => {
      const { data } = await apiClient.get<ListResponse>("/webhooks");
      return data.endpoints;
    },
    staleTime: 15_000,
  });
}

// Detail-by-id pulls from the cached list. The BFF doesn't expose a
// per-endpoint GET right now; the list response carries everything we need
// for the detail view, so we slice from the cache + fall back to a list
// refetch when the cache is empty.
export function useWebhook(id: string) {
  const qc = useQueryClient();
  return useQuery({
    queryKey: webhookKeys.detail(id),
    queryFn: async (): Promise<WebhookEndpoint | undefined> => {
      const cached = qc.getQueryData<WebhookEndpoint[]>(webhookKeys.list());
      const hit = cached?.find((w) => w.endpoint_id === id);
      if (hit) return hit;
      const { data } = await apiClient.get<ListResponse>("/webhooks");
      qc.setQueryData(webhookKeys.list(), data.endpoints);
      return data.endpoints.find((w) => w.endpoint_id === id);
    },
    staleTime: 15_000,
    enabled: Boolean(id),
  });
}

interface CreateBody {
  url: string;
  events: string[];
  secret?: string;     // optional — the BFF generates one when omitted
}

export function useCreateWebhook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateBody) => {
      const { data } = await apiClient.post<
        WebhookEndpoint & WebhookSecretEnvelope
      >("/webhooks", body);
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: webhookKeys.list() });
    },
  });
}

interface UpdateBody {
  id: string;
  url?: string;
  events?: string[];
  active?: boolean;
}

export function useUpdateWebhook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, ...rest }: UpdateBody) => {
      const { data } = await apiClient.patch<WebhookEndpoint>(
        `/webhooks/${encodeURIComponent(id)}`,
        rest,
      );
      return data;
    },
    onSuccess: (_, vars) => {
      void qc.invalidateQueries({ queryKey: webhookKeys.list() });
      void qc.invalidateQueries({ queryKey: webhookKeys.detail(vars.id) });
    },
  });
}

export function useDeleteWebhook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(`/webhooks/${encodeURIComponent(id)}`);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: webhookKeys.list() });
    },
  });
}

interface DeliveriesParams {
  id: string;
  since?: string;
  limit?: number;
}

export function useDeliveries({ id, since, limit }: DeliveriesParams) {
  return useQuery({
    queryKey: [...webhookKeys.deliveries(id), { since, limit }],
    queryFn: async () => {
      const params: Record<string, string> = {};
      if (since) params.since = since;
      if (limit) params.limit = String(limit);
      const { data } = await apiClient.get<DeliveriesResponse>(
        `/webhooks/${encodeURIComponent(id)}/deliveries`,
        { params },
      );
      return data.deliveries;
    },
    staleTime: 10_000,
    enabled: Boolean(id),
  });
}

export function useTestWebhook() {
  return useMutation({
    mutationFn: async (id: string) => {
      const { data } = await apiClient.post<TestDispatchResult>(
        `/webhooks/${encodeURIComponent(id)}/test`,
        {},
      );
      return data;
    },
  });
}

export function useRotateSecret() {
  return useMutation({
    mutationFn: async (id: string) => {
      const { data } = await apiClient.post<WebhookSecretEnvelope>(
        `/webhooks/${encodeURIComponent(id)}/rotate-secret`,
        {},
      );
      return data;
    },
  });
}

// URL validation — must be HTTPS, must parse, must not look like a private
// IP literal. Backend enforces SSRF properly; the client side is just to
// catch typos early. Returns null when valid, or a message string.
export function validateWebhookURL(url: string): string | null {
  try {
    const u = new URL(url);
    if (u.protocol !== "https:" && u.protocol !== "http:") {
      return "URL must use http or https.";
    }
    if (
      u.hostname === "localhost" ||
      /^127\./.test(u.hostname) ||
      /^10\./.test(u.hostname) ||
      /^192\.168\./.test(u.hostname) ||
      /^169\.254\./.test(u.hostname)
    ) {
      return "Private / loopback hosts are blocked by the backend SSRF guard.";
    }
    return null;
  } catch {
    return "Enter a valid URL (https://example.com/hook).";
  }
}
