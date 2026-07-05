// lockoutMessage maps a failed-login error to a human "account is locked, retry
// in N" string — but ONLY when the backend answered a 423 ACCOUNT_LOCKED (the
// scoped PENTEST-005 reversal for the interactive login page). Every other
// failure returns null so the caller falls back to the generic no-leak error
// (a wrong password must never reveal whether an account exists).
//
// Lives in its own module (not the login route) so it's a plain unit-testable
// function and doesn't trip the route file's react-refresh export rule. Duck-
// types the axios error so we don't need the AxiosError import.
export function lockoutMessage(e: unknown): string | null {
  const resp = (
    e as {
      response?: {
        status?: number;
        data?: { errors?: Array<{ code?: string; retry_after_seconds?: number }> };
      };
    }
  )?.response;
  const first = resp?.data?.errors?.[0];
  if (resp?.status !== 423 || first?.code !== "ACCOUNT_LOCKED") return null;
  const secs = first?.retry_after_seconds ?? 0;
  // Round up to whole minutes above a minute; show seconds below that.
  const when =
    secs >= 60
      ? `~${Math.ceil(secs / 60)} minute${Math.ceil(secs / 60) === 1 ? "" : "s"}`
      : secs > 0
        ? `~${secs} seconds`
        : "a little while";
  return `This account is temporarily locked after too many failed attempts. Try again in ${when}.`;
}
