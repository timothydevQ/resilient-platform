# Migration Plan: Monolith to Resilient Microservices

## Phase 1 — Strangler Fig (Weeks 1-4)
Extract order creation behind a feature flag. Route 5% of traffic to new order-service, keep monolith handling 95%. Measure error rates. If stable after 48h, increase to 25% → 50% → 100%.

**Risk**: Data consistency between monolith DB and service-local store.
**Mitigation**: Dual-write during transition. Reconciliation job checks for divergence.

## Phase 2 — Payment Extraction (Weeks 5-8)
Extract payment processing. Highest risk phase due to financial implications. Requirements: idempotency keys on all operations, comprehensive audit logging, zero-downtime cutover.

**Rollback plan**: Feature flag routes all payment traffic back to monolith within 30 seconds.

## Phase 3 — Inventory + Notifications (Weeks 9-12)
Lower risk services. Extract inventory reservation system. Extract notification delivery with DLQ from day one.

## Phase 4 — Resilience Hardening (Weeks 13-16)
- Add circuit breakers to all service-to-service calls
- Add retry with jitter to all outbound HTTP
- Implement outbox pattern for all event publishing
- Load test at 3x projected peak volume

## Phase 5 — Multi-Region (Weeks 17-20)
- Deploy region-b as passive standby
- Test failover playbook
- Promote to active-active
- Define region-local data boundaries

## Dependency Map
```
monolith
├── order-service     (extracted first — lowest risk)
├── payment-service   (extracted second — highest risk)
├── inventory-service (extracted third)
├── notification-service (extracted fourth — fully async)
└── user-service      (extracted last — auth boundary)
```
<!-- phases -->
<!-- risk -->
<!-- rollback -->
<!-- dependency -->
<!-- testing -->
