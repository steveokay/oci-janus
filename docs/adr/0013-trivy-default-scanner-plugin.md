# ADR-0013: Trivy as default scanner plugin

**Status:** ACCEPTED.
**Date:** 2026-06-09.
**Phase:** Initial.

## Context

The scanner interface (ADR-0005) needs a default implementation so out-of-box installations have working vulnerability scanning.

## Decision

Ship Trivy as the default scanner plugin. Active upstream maintenance, broad CVE coverage, and a permissive license (Apache 2.0) were the deciders.

## Consequences

Trivy CLI is baked into the scanner Dockerfile and surfaced via the external-process plugin host. Operators can swap to another plugin by changing the scanner config without rebuilding the image.

## Verified by

`services/scanner/Dockerfile` (Trivy install) and `services/scanner/internal/registry/registry.go` (default plugin registration).
