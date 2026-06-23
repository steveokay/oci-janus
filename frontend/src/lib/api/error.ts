import { AxiosError } from "axios";

// Beacon — error metadata extraction (DSGN-004).
//
// The BFF responds to errors with `{ "error": "..." }` and stamps an
// `X-Request-ID` header on every response (services/management's CORS
// middleware). The dashboard is self-hosted — the operator viewing an
// error toast or banner is the same person tailing the BFF logs — so
// surfacing the HTTP status + server message + correlation id turns
// "couldn't load — check the BFF logs" into something actionable.
//
// This module is the one place that knows how to dig those fields out of
// an unknown error value. ErrorState and toast helpers both call it so
// every surface speaks the same shape.

export interface ErrorMeta {
  // HTTP status code (>=400 means render the pill). undefined for
  // non-HTTP errors (network / parse / cancellation).
  code?: number;
  // Server-side error message — from response.data.error. Falls back
  // to the AxiosError.message when the body didn't carry one.
  detail?: string;
  // Correlation id from the X-Request-ID response header. Echo this to
  // the operator so they can grep BFF logs for it.
  requestId?: string;
  // The request URL the operator tried to hit. Includes baseURL prefix
  // when axios filled it in.
  requestUrl?: string;
}

// extractErrorMeta digs HTTP-level fields out of an unknown error. Safe
// to call on anything — including null / undefined / plain strings — and
// returns an empty object when there's nothing useful to surface.
export function extractErrorMeta(err: unknown): ErrorMeta {
  if (!err) return {};

  if (err instanceof AxiosError) {
    const code = err.response?.status;
    const body = err.response?.data;
    // BFF error body shape: `{ "error": "..." }`. Fall back to the axios
    // message ("Request failed with status code 500") when missing so
    // network errors still surface something readable.
    const detail =
      (typeof body === "object" && body !== null && "error" in body
        ? String((body as { error: unknown }).error)
        : undefined) ?? err.message;
    // Header names are lowercased by axios per the Fetch spec.
    const requestId =
      (err.response?.headers?.["x-request-id"] as string | undefined) ??
      undefined;
    const cfgUrl = err.config?.url;
    const cfgBase = err.config?.baseURL;
    const requestUrl =
      cfgUrl && cfgBase && !cfgUrl.startsWith("http")
        ? `${cfgBase.replace(/\/+$/, "")}/${cfgUrl.replace(/^\/+/, "")}`
        : cfgUrl;
    return { code, detail, requestId, requestUrl };
  }

  if (err instanceof Error) {
    return { detail: err.message };
  }

  if (typeof err === "string") {
    return { detail: err };
  }

  return {};
}
