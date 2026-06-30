# ADR-0010: Distroless final Docker image

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Container attack surface is dominated by what's in the base image; a full distro ships shells, package managers, and libc utilities that enable RCE escalation after a foothold.

## Decision

Every service's final stage is `gcr.io/distroless/static-debian12:nonroot`. No shell, no package manager, nonroot UID.

## Consequences

Even if an attacker achieves code execution inside a service, there's no `/bin/sh` to pivot from. Dockerfiles cannot use shell-form `RUN` in the final stage; debugging requires `kubectl debug` with an ephemeral sidecar.

## Verified by

`services/core/Dockerfile` (and every other service's Dockerfile) — final stage `FROM gcr.io/distroless/static-debian12:nonroot`.
