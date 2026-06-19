import { create } from "zustand";
import { decodeJanusJwt, type JanusJwtClaims } from "./jwt";

// Beacon — auth store.
//
// The JWT lives in memory only. We deliberately do not persist it to
// localStorage / sessionStorage — that's the FE-SEC-006/009 requirement
// in status.md. Reloading the tab logs the user out; that's the trade.
//
// `setToken` decodes the JWT once and stores the claims alongside it so
// downstream consumers (sidebar role gates, the API client, the refresh
// scheduler) don't repeatedly decode.

interface AuthState {
  token: string | null;
  claims: JanusJwtClaims | null;
  // The setter we register from the API client so axios can always read the
  // current token without subscribing to React state.
  setToken: (token: string | null) => void;
  clear: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  token: null,
  claims: null,
  setToken: (token) => {
    if (!token) {
      set({ token: null, claims: null });
      return;
    }
    try {
      const claims = decodeJanusJwt(token);
      set({ token, claims });
    } catch {
      // Malformed token — treat as logout so the user lands back on /login.
      set({ token: null, claims: null });
    }
  },
  clear: () => set({ token: null, claims: null }),
}));

// Imperative accessors — for use outside React components (axios interceptors,
// refresh scheduler). Subscribing through `useAuthStore` would couple those
// modules to React's lifecycle.
export const authStore = {
  getToken: () => useAuthStore.getState().token,
  getClaims: () => useAuthStore.getState().claims,
  setToken: (token: string | null) => useAuthStore.getState().setToken(token),
  clear: () => useAuthStore.getState().clear(),
};
