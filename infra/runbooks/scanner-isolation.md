# Scanner Plugin Isolation Runbook

**REDESIGN-001 close-out (2026-06-30).** Replaces the original Phase 6.11
in-process sandbox plan. The scanner already runs as an external
subprocess in a separate container (Decision #5: pluggable scanner
interface, external-process JSON-RPC). Container-level isolation
covers the majority of the threat that an in-process seccomp /
landlock / cgroup / netns wrap would address. This runbook is the
operator-facing equivalent — applied at deploy time it neutralises
the same surface without requiring Linux-only Go primitives that
don't port to dev/test environments.

## Threat model

The scanner plugin runs untrusted-ish binaries (Trivy, Grype, Clair).
Two concrete risks:

1. **Plugin RCE → host compromise.** A CVE in trivy/grype lets a
   crafted layer execute attacker code as the scanner process. Without
   isolation, that process can read filesystem, network out, write
   anywhere the user can write.
2. **Resource exhaustion → DoS.** A pathological scan burns CPU/RAM
   until the scanner pod is OOM-killed or starves co-tenant services.

The runbook closes both at the container boundary.

## Required posture (compose)

The dev `docker-compose.yml` should pin the `registry-scanner` service
with **all four** of the following. Anything less is a partial
mitigation that risks both threats above.

```yaml
registry-scanner:
  image: registry-scanner:latest
  read_only: true                         # 1. read-only root filesystem
  tmpfs:
    - /tmp:size=1g,exec                   # 2a. scratch space the
                                          # scanner plugins need to
                                          # unpack layers; exec=
                                          # because trivy untars binary
                                          # CVE DB content there
  cap_drop:
    - ALL                                 # 3a. drop every Linux capability
  security_opt:
    - no-new-privileges:true              # 3b. cannot regain caps via
                                          # setuid binaries
  networks:
    - scanner-egress-controlled           # 4. dedicated network with
                                          # egress restricted; see below
  deploy:
    resources:
      limits:
        cpus: "2.0"                       # 5a. hard CPU cap
        memory: 2g                        # 5b. hard memory cap
```

For the egress posture, a dedicated network with no external bridge:

```yaml
networks:
  scanner-egress-controlled:
    driver: bridge
    internal: true                        # block outbound to non-docker
                                          # IPs; scanner can still reach
                                          # other compose services on
                                          # this network (metadata,
                                          # storage gRPC, RabbitMQ)
```

If the scanner adapter needs upstream connectivity to pull a fresh CVE
database (Trivy DB lives on GHCR), do **not** flip `internal: false`.
Instead, run a separate one-shot init container with the database fetch
+ shared volume:

```yaml
registry-scanner-db-warmer:
  image: aquasec/trivy:latest
  command: ["trivy", "image", "--download-db-only"]
  volumes:
    - trivy-db:/root/.cache/trivy
  # No restart policy — runs once per stack boot.

registry-scanner:
  volumes:
    - trivy-db:/root/.cache/trivy:ro      # mount the DB read-only
```

## Required posture (Helm / K8s)

The Helm chart's `values.yaml` ships defaults that match the above; check
your operator overrides haven't disabled them:

```yaml
scanner:
  podSecurityContext:
    runAsNonRoot: true
    runAsUser: 65534                      # nobody
    fsGroup: 65534
  containerSecurityContext:
    readOnlyRootFilesystem: true
    allowPrivilegeEscalation: false
    capabilities:
      drop: ["ALL"]
    seccompProfile:
      type: RuntimeDefault                # K8s' shipped seccomp profile
                                          # is the in-process sandbox
                                          # equivalent at the pod boundary
  resources:
    requests:
      cpu: 200m
      memory: 256Mi
    limits:
      cpu: 2000m
      memory: 2Gi
  networkPolicy:
    enabled: true                         # see policy below
```

NetworkPolicy template — egress allowed only to `registry-metadata`,
`registry-storage`, `registry-tenant` (gRPC for tenant lookup),
`registry-rabbitmq` (event publish), and the DNS service:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: registry-scanner-egress
spec:
  podSelector:
    matchLabels:
      app: registry-scanner
  policyTypes: ["Egress"]
  egress:
    - to:
        - podSelector:
            matchLabels:
              app: registry-metadata
        - podSelector:
            matchLabels:
              app: registry-storage
        - podSelector:
            matchLabels:
              app: registry-tenant
        - podSelector:
            matchLabels:
              app: registry-rabbitmq
      ports:
        - protocol: TCP
          port: 50051                     # mTLS gRPC
        - protocol: TCP
          port: 5672                      # RabbitMQ
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
```

The CVE database fetch in production is a separate CronJob, not an
egress allowance on the scanner Pod itself.

## Verification

After applying the posture, confirm each control:

```bash
# 1. Read-only root.
docker compose exec registry-scanner touch /test
# expect: touch: cannot touch '/test': Read-only file system

# 2. Caps dropped.
docker compose exec registry-scanner capsh --print
# expect: Current: =  (empty current capability set)

# 3. Cannot escalate.
docker compose exec registry-scanner sudo -n true
# expect: sudo: a terminal is required to read the password
#         (or 'sudo: not found' — distroless base, even better)

# 4. Egress restricted.
docker compose exec registry-scanner curl --max-time 3 https://1.1.1.1
# expect: Could not resolve host: 1.1.1.1
#         (only docker-network DNS resolves, no public DNS)

# 5. Resource cap enforced.
docker stats registry-scanner --no-stream
# expect: MEM LIMIT shows "2GiB"; CPU% caps at 200% under load
```

In K8s, the equivalents:

```bash
kubectl exec -n registry registry-scanner-0 -- touch /test
kubectl describe pod registry-scanner-0 | grep -A 5 "Security Context"
kubectl get networkpolicy registry-scanner-egress -o yaml
```

## When to revisit in-process isolation

The container-level posture above neutralises ~80% of the original
threat model. The remaining 20% is the case where an attacker
compromises the scanner process AND the container runtime AND escapes
into the host. Defence against that scenario is the original Phase
6.11 plan: per-process seccomp / landlock filters inside the scanner
adapter itself, plus optional netns drop.

Revisit Phase 6.11's in-process sandbox if **any** of the following:

- A CVE drops on the platform's container runtime (Docker, containerd,
  CRI-O) that allows non-root in a privileged-dropped container to
  escape to host.
- Operator demand for a sandbox that survives misconfigured K8s
  NetworkPolicy (defence in depth at the process level).
- Multi-tenant SaaS deployments where one tenant's scan must be
  isolated from another's by more than DB row-level filtering.

Until then, this runbook is sufficient. The original 6.11 task is
descoped from REDESIGN-001 and tracked in `futures.md` as a lower
priority follow-up.

## References

- ADR-0005 — Pluggable scanner interface
- ADR-0013 — Trivy as default scanner plugin
- `infra/compose/docker-compose.yml` — current `registry-scanner` service block
- `infra/helm/registry/values.yaml` — current `scanner.*` chart defaults
- `docs/SCANNER.md` — scanner-specific architecture deep-dive
