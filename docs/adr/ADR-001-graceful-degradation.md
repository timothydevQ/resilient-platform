# ADR-001: Graceful Degradation Over Hard Failures

## Status
Accepted

## Context
When a downstream service is unavailable, we have two choices: fail the entire request or complete as much value as possible and handle the failure asynchronously.

## Decision
Implement graceful degradation at the order service boundary. If inventory or payment is unavailable, accept the order as `pending_payment` and retry asynchronously. Core order creation never fails due to a downstream outage.

## Rationale
An order in `pending_payment` generates revenue once processed. A 503 generates nothing and damages trust. At scale, downstream blips are frequent — designing for hard failure means designing for constant outages.

## Consequences
- Requires a background reconciliation job for pending_payment orders
- Requires idempotent downstream calls so retries do not double-charge
<!-- context -->
