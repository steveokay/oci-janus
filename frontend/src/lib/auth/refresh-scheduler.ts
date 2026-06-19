import { refreshNow } from "@/lib/api/client";
import { authStore } from "@/lib/auth/store";
import { isExpiringSoon } from "@/lib/auth/jwt";

// Schedules a silent refresh ~60s before the current token expires.
// Re-arms itself on every token change (login, refresh, logout).
//
// The scheduler is intentionally separate from the React tree — keeping it
// here means a route mount/unmount doesn't restart the timer.

let timer: ReturnType<typeof setTimeout> | null = null;

function arm(): void {
  if (timer) {
    clearTimeout(timer);
    timer = null;
  }
  const claims = authStore.getClaims();
  if (!claims) return;
  // Fire 60s before expiry — but never less than 5s out so we don't busy-loop
  // when a stale token slips in.
  const msUntilRefresh = Math.max(
    5_000,
    (claims.exp - Math.floor(Date.now() / 1000) - 60) * 1000,
  );
  timer = setTimeout(async () => {
    // If somehow the token expired before we got here, the next API call
    // will trigger refreshOnce via the 401 path; just attempt proactively.
    if (isExpiringSoon(claims, 65)) {
      await refreshNow();
    }
  }, msUntilRefresh);
}

// Wire the scheduler to the store: any time the token changes, re-arm.
// This is the only `subscribe` call we make against the auth store at the
// module level.
import { useAuthStore } from "@/lib/auth/store";

let started = false;

export function startRefreshScheduler(): void {
  if (started) return;
  started = true;
  // Arm immediately for the initial token (if any) and on every change.
  arm();
  useAuthStore.subscribe(() => arm());
}
