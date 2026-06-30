// credential-snippets — pure, side-effect-free renderers for the FUT-002
// helpers surface. Returned strings are copy-paste-ready; callers add
// nothing else.
//
// Hostname is the externally-reachable registry hostname returned by
// GET /api/v1/registry-info. saName is the human-readable service-account
// name (NOT the secret) — only used for the --username field so the snippet
// is recognisable in CI logs.
//
// The secret itself is NEVER baked into the snippet; every snippet
// references an env var ($REGISTRY_API_KEY) the operator has to provide
// out of band. That's both a security posture (the dashboard doesn't echo
// secret material) and a usability posture (the snippet is shareable).

export const SNIPPET_FORMATS = [
  "docker login",
  "kubernetes Secret",
  "terraform",
  "GitHub Actions",
] as const;

export type SnippetFormat = (typeof SNIPPET_FORMATS)[number];

export interface SnippetInputs {
  hostname: string;
  saName: string;
}

// sanitiseSAName — defence-in-depth allowlist mirroring the server-side
// SA-name regex (`^[a-z0-9]+([._-][a-z0-9]+)*$` at
// services/auth/internal/handler/http_service_accounts.go). Any character
// outside `[a-z0-9._-]` is stripped before the name lands in a shell or
// YAML context. An allowlist (not a blocklist) closes SEC-055 — a future
// loosening of the server-side regex can't silently widen the FE
// snippet's attack surface.
function sanitiseSAName(name: string): string {
  return name.replace(/[^a-z0-9._-]/g, "");
}

// buildSnippets — render all four snippet formats parameterised on the
// supplied hostname + service-account name.
export function buildSnippets({
  hostname,
  saName,
}: SnippetInputs): Record<SnippetFormat, string> {
  const safe = sanitiseSAName(saName);
  return {
    "docker login": [
      `# Authenticate Docker to the registry using your API key.`,
      `# Replace $REGISTRY_API_KEY with the secret you copied at key creation.`,
      `echo "$REGISTRY_API_KEY" | docker login ${hostname} \\`,
      `  --username ${safe} \\`,
      `  --password-stdin`,
    ].join("\n"),

    "kubernetes Secret": [
      `# Kubernetes pull secret — generated via kubectl.`,
      `kubectl create secret docker-registry regcred \\`,
      `  --docker-server=${hostname} \\`,
      `  --docker-username=${safe} \\`,
      `  --docker-password=$REGISTRY_API_KEY \\`,
      `  --dry-run=client -o yaml`,
    ].join("\n"),

    terraform: [
      `# Terraform Docker provider — authenticates with the registry.`,
      `provider "docker" {`,
      `  registry_auth {`,
      `    address  = "${hostname}"`,
      `    username = "${safe}"`,
      `    password = var.registry_api_key`,
      `  }`,
      `}`,
      ``,
      `variable "registry_api_key" {`,
      `  type      = string`,
      `  sensitive = true`,
      `}`,
    ].join("\n"),

    "GitHub Actions": [
      `# GitHub Actions — authenticate then push.`,
      `- name: Log in to registry`,
      `  uses: docker/login-action@v3`,
      `  with:`,
      `    registry: ${hostname}`,
      `    username: ${safe}`,
      `    password: \${{ secrets.REGISTRY_API_KEY }}`,
    ].join("\n"),
  };
}
