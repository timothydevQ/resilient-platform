# ADR-003: Outbox Pattern for Reliable Event Publishing

## Status
Accepted

## Decision
Use the outbox pattern: write events transactionally with business data, publish asynchronously via background worker. Failed entries move to DLQ after max_attempts.

## Rationale
Transactional outbox guarantees at-least-once delivery without distributed transactions. At-least-once with idempotent consumers is industry standard.

## Consequences
- Events may be delivered more than once — consumers must be idempotent
- DLQ requires monitoring and replay capability
<!-- context -->
<!-- dlq -->
<!-- alternatives -->
