# ADR-004: Active-Active Multi-Region

## Status
Accepted

## Decision
Deploy stateless services in active-active mode across region-a and region-b. Each region handles requests independently with region-local state.

## Rationale
Active-passive wastes 50% capacity. Active-active means region-b is always warm — no cold start on failover.

## Consequences
- In-memory state not shared between regions
- Eventual consistency between regions for non-critical data
<!-- context -->
