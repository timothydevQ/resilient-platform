# Technical Roadmap

## v1.0 — Current: Single-Region MVP
- 6 microservices with HTTP APIs
- Circuit breaker + retry + timeout on all inter-service calls
- Graceful degradation on order creation
- Idempotency tokens on all mutating endpoints
- Outbox pattern + DLQ for notifications
- GitHub Actions CI/CD → GHCR → ArgoCD
- Prometheus + Grafana + Jaeger observability

## v2.0 — Q3 2026: Persistent Storage
- PostgreSQL per service (replace in-memory stores)
- Redis for idempotency key storage with TTL
- Kafka for outbox event publishing
- Persistent DLQ with replay UI

## v3.0 — Q4 2026: Advanced Resilience
- Adaptive circuit breaker thresholds based on error budget
- Retry budgets — per-request vs per-service retry pools
- Bulkhead pattern — isolate payment thread pool from order thread pool
- Chaos testing with LitmusChaos in staging pipeline

## v4.0 — Q1 2027: Multi-Region Production
- Active-active across two AWS regions
- Global load balancer with latency-based routing
- Cross-region event replication
- Automated failover with health check integration
- RTO: < 30s, RPO: < 60s

## v5.0 — Q2 2027: Platform Maturity
- Service mesh (Linkerd) for mTLS and traffic management
- gRPC between internal services
- Schema registry for event contracts
- Self-service SLO dashboard for product teams
<!-- versions -->
<!-- v2 details -->
<!-- v3 details -->
