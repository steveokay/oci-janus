#!/usr/bin/env python3
# FUT-083 — Helm routing-contract golden test.
#
# Renders the umbrella chart with `helm template` and asserts the deploy
# artifacts a Helm install needs to serve the dashboard end-to-end:
#
#   1. a registry-management Deployment + Service exist (the BFF subchart);
#   2. a registry-frontend Deployment + Service exist (the SPA subchart);
#   3. the gateway IngressRoute splits /api/v1 exactly like frontend/nginx.conf
#      instead of dumping everything on registry-auth:
#        - auth-owned prefixes (login/logout/token/apikeys/users/
#          service-accounts/access/auth) + /auth/ + /.well-known/ -> auth
#        - the four BFF exceptions that live UNDER an auth prefix
#          (users/me/notification-preferences, access/oidc-trust,
#          access/token-policy, access/review) -> management, at a HIGHER
#          priority than the auth prefix they shadow
#        - the /api/v1/ catch-all -> management (this is the bug: it used to
#          be registry-auth)
#        - /v2/ -> core, /v2/cache/ -> proxy
#        - the root / catch-all -> frontend, at the LOWEST priority
#
# This is a structural golden test, not a live-cluster test: it proves the
# rendered manifests encode the contract. It cannot prove Traefik's runtime
# matcher precedence (no kind/kubectl locally) — priorities are asserted
# numerically instead so the ordering intent is pinned.
#
# Usage: python routing_contract_test.py   (run from anywhere; resolves the
# chart dir relative to this file). Exits non-zero on the first failure.

import subprocess
import sys
from pathlib import Path

import yaml

CHART_DIR = Path(__file__).resolve().parent.parent

failures = []


def check(cond, msg):
    if cond:
        print(f"  PASS  {msg}")
    else:
        print(f"  FAIL  {msg}")
        failures.append(msg)


def render():
    out = subprocess.run(
        ["helm", "template", "."],
        cwd=CHART_DIR,
        capture_output=True,
        text=True,
    )
    if out.returncode != 0:
        print("helm template failed:")
        print(out.stderr)
        sys.exit(2)
    return list(yaml.safe_load_all(out.stdout))


def docs_of_kind(docs, kind):
    return [d for d in docs if d and d.get("kind") == kind]


def named(docs, kind, name):
    return any(
        d.get("metadata", {}).get("name") == name for d in docs_of_kind(docs, kind)
    )


def ingressroute_routes(docs):
    """Flatten every route across all IngressRoute objects."""
    routes = []
    for ir in docs_of_kind(docs, "IngressRoute"):
        for r in ir.get("spec", {}).get("routes", []):
            routes.append(r)
    return routes


def service_names(route):
    return {s.get("name") for s in route.get("services", [])}


def route_for(routes, substr):
    """First route whose match string contains substr, else None."""
    for r in routes:
        if substr in r.get("match", ""):
            return r
    return None


def main():
    docs = render()

    # 1. management subchart resources
    check(named(docs, "Deployment", "registry-management"),
          "registry-management Deployment is rendered")
    check(named(docs, "Service", "registry-management"),
          "registry-management Service is rendered")

    # 2. frontend subchart resources
    check(named(docs, "Deployment", "registry-frontend"),
          "registry-frontend Deployment is rendered")
    check(named(docs, "Service", "registry-frontend"),
          "registry-frontend Service is rendered")

    routes = ingressroute_routes(docs)
    check(len(routes) > 0, "gateway renders at least one IngressRoute route")

    # 3a. /api/v1/ catch-all must go to management, NOT auth (the bug)
    api_catchall = route_for(routes, "PathPrefix(`/api/v1/`)")
    check(api_catchall is not None and service_names(api_catchall) == {"registry-management"},
          "/api/v1/ catch-all routes to registry-management (not registry-auth)")

    # 3b. auth-owned prefixes route to auth
    for pfx in ["/api/v1/login", "/api/v1/apikeys", "/api/v1/users",
                "/api/v1/service-accounts", "/api/v1/access", "/api/v1/auth"]:
        r = route_for(routes, f"PathPrefix(`{pfx}`)")
        check(r is not None and service_names(r) == {"registry-auth"},
              f"{pfx} routes to registry-auth")

    # 3c. the four BFF exceptions route to management, above their auth prefix
    exceptions = {
        "/api/v1/users/me/notification-preferences": "/api/v1/users",
        "/api/v1/access/oidc-trust": "/api/v1/access",
        "/api/v1/access/token-policy": "/api/v1/access",
        "/api/v1/access/review": "/api/v1/access",
    }
    for exc, parent in exceptions.items():
        er = route_for(routes, f"PathPrefix(`{exc}`)")
        pr = route_for(routes, f"PathPrefix(`{parent}`)")
        check(er is not None and service_names(er) == {"registry-management"},
              f"{exc} routes to registry-management")
        if er is not None and pr is not None:
            check(er.get("priority", 0) > pr.get("priority", 0),
                  f"{exc} has higher priority than its parent {parent}")

    # 3d. OCI + auth token surfaces unchanged
    core_route = route_for(routes, "PathPrefix(`/v2/`)")
    check(core_route is not None and "registry-core" in service_names(core_route),
          "/v2/ routes to registry-core")
    cache_route = route_for(routes, "PathPrefix(`/v2/cache/`)")
    check(cache_route is not None and "registry-proxy" in service_names(cache_route),
          "/v2/cache/ routes to registry-proxy")
    auth_surface = route_for(routes, "/auth/")
    check(auth_surface is not None and "registry-auth" in service_names(auth_surface),
          "/auth/ + /.well-known/ route to registry-auth")

    # 3e. root catch-all -> frontend, lowest priority of all routes.
    # Find the explicit root route (its match ends in PathPrefix(`/`)).
    root = None
    for r in routes:
        m = r.get("match", "")
        if m.rstrip().endswith("PathPrefix(`/`)"):
            root = r
            break
    check(root is not None and service_names(root) == {"registry-frontend"},
          "root / catch-all routes to registry-frontend")
    if root is not None:
        # Compare only against the other *path* routes on the websecure
        # entrypoint. The HTTP->HTTPS redirect IngressRoute is a host-only rule
        # on a different entrypoint (no PathPrefix, no priority) and is not part
        # of this routing-precedence table.
        others = [
            r.get("priority", 0)
            for r in routes
            if r is not root and "PathPrefix(" in r.get("match", "")
        ]
        check(all(root.get("priority", 0) < p for p in others),
              "root / has the lowest priority of all path routes")

    print()
    if failures:
        print(f"FAILED: {len(failures)} assertion(s)")
        sys.exit(1)
    print("OK: routing contract satisfied")


if __name__ == "__main__":
    main()
