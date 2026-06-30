# ADR-0005: Pluggable scanner interface (external process only, no Go `.so`)

**Status:** ACCEPTED.
**Date:** 2026-06-09.
**Phase:** Initial.

## Context

Vulnerability scanners (Trivy, Grype, Snyk, etc.) evolve quickly and have incompatible licenses; locking the platform to one would force users into a vendor relationship.

## Decision

Define a JSON-RPC scanner interface in `libs/scanner/plugin` and host plugins as external processes with checksum-validated binaries. Go's in-process plugin path (`.so`) is explicitly ruled out as unsafe.

## Consequences

Every scanner gets the same supervision (timeouts, checksum, restart); plugin authors are not tied to the Go ABI. Removing the external-process model would require resurrecting `plugin.Open` and re-litigating the security review.

## Verified by

`libs/scanner/plugin/plugin.go` (interface) and `services/scanner/internal/plugin/process.go` (external-process host).
