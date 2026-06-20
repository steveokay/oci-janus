import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

// FE-API-008 — tenant-wide notifications feed.
//
// Backend: GET /api/v1/notifications?since=&limit=&page_token=&event_types=&unread_only=
//
// The audit service stores no per-user read state. The frontend persists a
// `last_seen_at` timestamp in localStorage; the unread count is just the
// number of events on the page after `last_seen_at`. Marking-as-read is a
// localStorage update — no server round-trip — so it's cheap, instant,
// and survives across tabs of the same browser.

export const NOTIFICATION_EVENT_TYPES = [
  "push.image",
  "push.failed",
  "delete.manifest",
  "delete.tag",
  "scan.completed",
  "scan.policy_blocked",
  "image.signed",
  "webhook.delivery_failed",
] as const;

export type NotificationEventType = (typeof NOTIFICATION_EVENT_TYPES)[number];

// Friendly labels for the filter chip row.
export const NOTIFICATION_EVENT_LABELS: Record<NotificationEventType, string> = {
  "push.image": "Push completed",
  "push.failed": "Push failed",
  "delete.manifest": "Manifest deleted",
  "delete.tag": "Tag deleted",
  "scan.completed": "Scan completed",
  "scan.policy_blocked": "Scan policy blocked",
  "image.signed": "Image signed",
  "webhook.delivery_failed": "Webhook failed",
};

export interface Notification {
  event_id: string;
  event_type: string;
  occurred_at: string;
  actor_id: string;
  actor_username: string;
  title: string;
  summary: string;
  link: string;
  metadata: Record<string, string>;
}

export interface NotificationsPage {
  notifications: Notification[];
  next_page_token?: string;
  unread_count: number;
}

interface ListParams {
  since?: string;
  limit?: number;
  page_token?: string;
  event_types?: NotificationEventType[];
  unread_only?: boolean;
}

export const notificationKeys = {
  all: ["notifications"] as const,
  list: (params: ListParams) => [...notificationKeys.all, "list", params] as const,
};

export function useNotifications(params: ListParams = {}) {
  return useQuery({
    queryKey: notificationKeys.list(params),
    queryFn: async () => {
      const q: Record<string, string> = {};
      if (params.since) q.since = params.since;
      if (params.limit) q.limit = String(params.limit);
      if (params.page_token) q.page_token = params.page_token;
      if (params.event_types && params.event_types.length > 0) {
        q.event_types = params.event_types.join(",");
      }
      if (params.unread_only) q.unread_only = "true";
      const { data } = await apiClient.get<NotificationsPage>(
        "/notifications",
        { params: q },
      );
      return data;
    },
    staleTime: 15_000,
    refetchInterval: 60_000,
  });
}

// ── Last-seen-at persistence ────────────────────────────────────────────
//
// The audit service doesn't track per-user read state, so the frontend
// owns it locally. We key on tenant_id so logging into a second tenant
// doesn't bleed unread state from the first.

const LAST_SEEN_KEY_PREFIX = "beacon.notifications.last_seen.";

export function loadLastSeen(tenantID: string | undefined): string | null {
  if (!tenantID || typeof window === "undefined") return null;
  return window.localStorage.getItem(LAST_SEEN_KEY_PREFIX + tenantID);
}

// useMarkAllSeen — bumps the localStorage cursor to "now" and invalidates
// the notifications cache so the unread badge refreshes immediately.
export function useMarkAllSeen(tenantID: string | undefined) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      if (!tenantID || typeof window === "undefined") return;
      const now = new Date().toISOString();
      window.localStorage.setItem(LAST_SEEN_KEY_PREFIX + tenantID, now);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}

// countUnread — derived from the page + the localStorage cursor. We do
// the count client-side because the backend returns `unread_count` as
// "len of this page" (it doesn't know our last_seen_at).
export function countUnread(
  page: NotificationsPage | undefined,
  lastSeenAt: string | null,
): number {
  if (!page) return 0;
  if (!lastSeenAt) return page.notifications.length;
  const since = Date.parse(lastSeenAt);
  if (Number.isNaN(since)) return page.notifications.length;
  return page.notifications.filter((n) => Date.parse(n.occurred_at) > since).length;
}
