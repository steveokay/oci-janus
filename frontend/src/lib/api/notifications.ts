import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
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
  // S11 slice 5 — retention.* events are emitted by the gc service
  // executor (FE-API-041) and routed through audit into the
  // notifications surface. Keep this list in sync with the BFF and
  // audit allowlists in services/{management,audit}/internal/handler/
  // notifications.go — the BFF rejects unknown values with 400.
  "retention.evaluated",
  "retention.applied",
  "retention.grace_completed",
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
  "retention.evaluated": "Retention evaluated",
  "retention.applied": "Retention applied",
  "retention.grace_completed": "Retention grace completed",
};

export interface Notification {
  event_id: string;
  event_type: string;
  occurred_at: string;
  actor_id: string;
  actor_username: string;
  // REM-018-followup: populated by the BFF after a batch
  // auth.LookupUsernames join. Empty when the actor_id is a non-UUID
  // sentinel ("system" / "anonymous" / "") or auth was unreachable.
  // The UserCell consumer treats empty as "fall back to @username".
  actor_display_name: string;
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

// useInfiniteNotifications — paginated variant of useNotifications for the
// /activity feed. The BFF returns a `next_page_token` cursor; this hook walks
// it via useInfiniteQuery so a "Load older" button can append pages instead of
// the feed being stuck at a single page. The topbar bell keeps using the
// single-page useNotifications (it only ever wants the freshest 10).
//
// `since` / `event_types` are part of the query key, so changing a filter
// starts a fresh paginated query (page cursor resets) rather than mixing
// filtered + unfiltered pages.
export function useInfiniteNotifications(
  params: Omit<ListParams, "page_token"> = {},
) {
  return useInfiniteQuery({
    // Reuse the list key shape (minus page_token, which the cursor owns) so
    // invalidation via notificationKeys.all still reaches this query.
    queryKey: notificationKeys.list(params),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const q: Record<string, string> = {};
      if (params.since) q.since = params.since;
      if (params.limit) q.limit = String(params.limit);
      if (pageParam) q.page_token = pageParam;
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
    // Empty / absent token means the server has no more pages.
    getNextPageParam: (lastPage) => lastPage.next_page_token || undefined,
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
