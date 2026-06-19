import axios, {
  AxiosError,
  type AxiosInstance,
  type InternalAxiosRequestConfig,
} from "axios";
import { authStore } from "@/lib/auth/store";

// Beacon — HTTP client.
//
// Two axios instances:
//   `apiClient`        — the workhorse. Carries Bearer auth, 401→refresh→retry.
//   `apiClientRaw`     — bare instance with no interceptors. The refresh path
//                        uses this one so a failing refresh does not recurse.

const baseURL = import.meta.env.VITE_API_BASE_URL ?? "/api/v1";

export const apiClientRaw: AxiosInstance = axios.create({
  baseURL,
  withCredentials: false,
  timeout: 30_000,
});

export const apiClient: AxiosInstance = axios.create({
  baseURL,
  withCredentials: false,
  timeout: 30_000,
});

// Attach the current Bearer on every request. Reading from the store keeps
// the interceptor stateless — no need to re-register on login/logout.
apiClient.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  const token = authStore.getToken();
  if (token) {
    config.headers = config.headers ?? {};
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// 401 handling — try to refresh, replay original request once, otherwise
// hand back to the caller so the route guard can punt to /login.
//
// We use a module-level singleton promise so multiple concurrent 401s share
// one refresh round-trip instead of stampeding the auth service.
let refreshPromise: Promise<string | null> | null = null;

async function refreshOnce(): Promise<string | null> {
  if (refreshPromise) return refreshPromise;
  refreshPromise = (async () => {
    try {
      const current = authStore.getToken();
      if (!current) return null;
      const { data } = await apiClientRaw.post<{ token: string }>(
        "/token/refresh",
        { grant_type: "bearer_jwt" },
        { headers: { Authorization: `Bearer ${current}` } },
      );
      authStore.setToken(data.token);
      return data.token;
    } catch {
      authStore.clear();
      return null;
    } finally {
      // Allow the next 401 to attempt a fresh refresh.
      refreshPromise = null;
    }
  })();
  return refreshPromise;
}

apiClient.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const original = error.config as InternalAxiosRequestConfig & {
      _retried?: boolean;
    };
    if (error.response?.status !== 401 || !original || original._retried) {
      return Promise.reject(error);
    }
    // Don't try to refresh from auth endpoints themselves — would recurse.
    if (
      original.url?.includes("/login") ||
      original.url?.includes("/token/refresh") ||
      original.url?.includes("/logout")
    ) {
      return Promise.reject(error);
    }
    const fresh = await refreshOnce();
    if (!fresh) {
      return Promise.reject(error);
    }
    original._retried = true;
    original.headers = original.headers ?? {};
    original.headers.Authorization = `Bearer ${fresh}`;
    return apiClient.request(original);
  },
);

// Manual refresh — exported so the scheduler can pre-empt expiry.
export async function refreshNow(): Promise<string | null> {
  return refreshOnce();
}
