# ADR-002: Circuit Breakers at Gateway AND Service Layer

## Status
Accepted

## Decision
Place circuit breakers at two layers: API Gateway (per upstream) and Order Service (per downstream). Gateway CBs fail fast with 503. Service CBs trigger the graceful degradation path.

## Rationale
Single-layer protection leaves gaps. Dual placement means gateway fails fast when a service is clearly down, order service degrades gracefully when intermittently slow.
<!-- context -->
