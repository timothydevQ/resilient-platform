# ADR-005: Client-Provided Idempotency Keys

## Status
Accepted

## Decision
Accept `Idempotency-Key` header on all mutating endpoints. Cache key → result mappings and return cached result on duplicate requests.

## Rationale
Client-provided keys (the Stripe model) allow clients to generate stable keys and retry safely without coordination. No extra round-trip vs server-generated keys.
<!-- context -->
<!-- consequences -->
