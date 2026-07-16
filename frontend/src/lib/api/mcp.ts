import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";
import { saKeys } from "./service-accounts";

// MCP one-click connect — mint a dedicated read-only service account + API key
// and compose the Bearer token the MCP server expects, so an operator never has
// to hand-assemble `key.<id>.<secret>` or dig the key UUID out of the UI.

// MCP_KEY_SCOPES is the read-only scope set stamped on the generated SA + key.
// The MCP read routes are tenant-scoped and do not gate on scope, so these are
// primarily a least-privilege signal visible on the Service Accounts page (the
// auth handler validates scope *format* only, not membership of a fixed set).
export const MCP_KEY_SCOPES = [
  "repo:read",
  "scan:read",
  "audit:read",
  "access:read",
  "signer:read",
];

// composeApiKeyToken assembles the Bearer token services/auth's parseAPIKeyBearer
// expects: the literal `key.` prefix + the API key's own UUID + the raw secret.
// The middle segment is the KEY id returned by the issue-key call — NOT the
// service-account id and NOT the shadow_user_id (the exact mix-up this feature
// exists to remove).
export function composeApiKeyToken(keyId: string, secret: string): string {
  return `key.${keyId}.${secret}`;
}

// mcpSaName derives a unique, regex-valid service-account name from a timestamp.
// The auth handler requires ^[a-z0-9]+([._-][a-z0-9]+)*$; base36 of the epoch
// millis is lowercase alphanumeric, so `mcp-agent-<base36>` always matches and
// two clicks never collide on the name's uniqueness constraint.
export function mcpSaName(nowMs: number): string {
  return `mcp-agent-${nowMs.toString(36)}`;
}

// GeneratedMcpKey is the result of a successful one-click mint: the ready-to-paste
// token plus the SA identity so the UI can link the operator to manage/revoke it.
export interface GeneratedMcpKey {
  // token is the composed `key.<id>.<secret>` Bearer value — shown once.
  token: string;
  // saId / saName identify the created service account for later management.
  saId: string;
  saName: string;
  // keyId is the API key's UUID (the token's middle segment).
  keyId: string;
}

// useGenerateMcpKey mints a read-only service account and one API key under it,
// then composes the MCP Bearer token. Two sequential calls:
//   1. POST /service-accounts            → { id }
//   2. POST /service-accounts/{id}/api-keys → { id, key }
// The plaintext secret exists only in step 2's response and is never persisted
// server-side, so the returned token cannot be re-fetched — the caller must
// surface it immediately (shown-once). Invalidates the SA list so the new
// account appears on the Service Accounts page.
export function useGenerateMcpKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (nowMs: number): Promise<GeneratedMcpKey> => {
      const name = mcpSaName(nowMs);
      const { data: sa } = await apiClient.post<{ id: string; name: string }>(
        "/service-accounts",
        {
          name,
          description: "Read-only key for an MCP agent (Settings › Integrations)",
          allowed_scopes: MCP_KEY_SCOPES,
          // Stamp the SA so the Service Accounts list can badge MCP-minted
          // accounts. The backend echoes this back on the list response.
          origin: "mcp-connect",
        },
      );
      const { data: key } = await apiClient.post<{ id: string; key: string }>(
        `/service-accounts/${encodeURIComponent(sa.id)}/api-keys`,
        { name: "mcp-key", scopes: MCP_KEY_SCOPES },
      );
      return {
        token: composeApiKeyToken(key.id, key.key),
        saId: sa.id,
        saName: sa.name,
        keyId: key.id,
      };
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: saKeys.all });
    },
  });
}
