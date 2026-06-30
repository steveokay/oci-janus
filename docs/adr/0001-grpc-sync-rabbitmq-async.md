# ADR-0001: gRPC for sync, RabbitMQ for async

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Internal service-to-service calls need strong contracts, mTLS-capable transport, and durable async fan-out for events like push completion and scan results.

## Decision

Use gRPC over mTLS for all synchronous internal calls; use RabbitMQ topic exchange `registry.events` with quorum queues + DLX for asynchronous fan-out.

## Consequences

Splits the wire surface in two — `proto/*/v1/*.proto` is the sync contract, `libs/rabbitmq/events` is the async contract. Removing either transport would require rewriting every cross-service caller.

## Verified by

`libs/rabbitmq/publisher` and `proto/gen/go/` (committed gRPC stubs); both are imported by every service entrypoint.
