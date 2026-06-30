# ADR-0002: RabbitMQ over Kafka

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

The event broker had to provide durability without the operational burden Kafka imposes (broker count, ZooKeeper/KRaft, partition rebalancing).

## Decision

Use RabbitMQ 3.13 with Quorum Queues for all async events; publishers run in confirm mode and consumers manual-ack.

## Consequences

Lower operational complexity for self-hosters; ordering is per-queue rather than per-partition. `libs/rabbitmq/publisher.Publisher` enforces confirm mode so a publish never returns success before broker durability.

## Verified by

`libs/rabbitmq/publisher/publisher.go` — publisher mandates confirm mode; switching brokers would require replacing this and every `libs/rabbitmq/consumer` consumer.
