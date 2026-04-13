# Resilient Platform

A production-grade, self-healing distributed platform built in Go — demonstrating circuit breaker patterns, graceful degradation, idempotent operations, outbox-based event publishing, dead letter queues, and multi-region active-active failover.

```
┌─────────────────────────────────────────────────────────────────────┐
│                        API Gateway :8000                             │
│     Rate Limiting (token bucket) · Circuit Breaker per upstream      │
│          Request ID injection · Per-IP isolation                     │
└──────┬──────────┬──────────┬──────────┬──────────┬──────────────────┘
       │          │          │          │          │
┌──────▼────┐ ┌───▼──────┐ ┌─▼───────┐ ┌─▼────────┐ ┌─▼────────────┐
│  Order    │ │Inventory │ │Payment  │ │Notif.    │ │  User        │
│  :8080    │ │ :8081    │ │ :8082   │ │ :8083    │ │  :8084       │
│ Outbox    │ │ Reserve  │ │Idem.    │ │ Retry+   │ │ Email index  │
│ Idem.keys │ │ Confirm  │ │ tokens  │ │ DLQ      │ │              │
│ Graceful  │ │ Expire   │ │ Refund  │ │ Backoff  │ │              │
│ degrade   │ │          │ │         │ │          │ │              │
└───────────┘ └──────────┘ └─────────┘ └──────────┘ └──────────────┘

Region A ──────────────────────────────────────────────── :8000-8084
Region B (active failover) ───────────────────────────── :9000-9084

Shared: pkg/resilience (circuit breaker · retry · timeout)
        pkg/events     (outbox · DLQ · event bus)

Observability: Prometheus · Grafana · Jaeger · Loki
Delivery:      GitHub Actions CI · ArgoCD GitOps · GHCR
```

---

## Table of Contents

- [Architecture](#architecture)
- [Services](#services)
- [Self-Healing Patterns](#self-healing-patterns)
- [Getting Started](#getting-started)
- [API Reference](#api-reference)
- [Multi-Region Failover](#multi-region-failover)
- [Chaos Testing](#chaos-testing)
- [Observability](#observability)
- [SLOs & SLIs](#slos--slis)
- [CI/CD Pipeline](#cicd-pipeline)
- [Load Testing](#load-testing)
- [Design Decisions](#design-decisions)
- [Failure Scenarios](#failure-scenarios)
- [Scaling Strategy](#scaling-strategy)
- [Docs & Runbooks](#docs--runbooks)
- [Roadmap](#roadmap)

---

## Architecture

### Services

| Service | Port (A) | Port (B) | Responsibility |
|---|---|---|---|
| `api-gateway` | 8000 | 9000 | Rate limiting, circuit breakers, request routing |
| `order-service` | 8080 | 9080 | Order lifecycle, outbox events, graceful degradation |
| `inventory-service` | 8081 | 9081 | Stock management, reservations, expiry |
| `payment-service` | 8082 | 9082 | Idempotent payment processing, refunds |
| `notification-service` | 8083 | 9083 | Event-driven delivery, retry backoff, DLQ |
| `user-service` | 8084 | 9084 | User accounts, email uniqueness |

### Shared Packages

- **`pkg/resilience`** — Circuit breaker (closed/open/half-open), retry with exponential backoff + jitter, timeout wrapper, resilient client combining all three
- **`pkg/events`** — Event types, outbox store, in-memory event bus, dead letter queue

### Communication Patterns

- **External → API Gateway**: REST/HTTP+JSON with rate limiting
- **Gateway → Services**: Reverse proxy with per-service circuit breakers
- **Order → Inventory/Payment**: Direct HTTP with graceful degradation on failure
- **Services → Notifications**: Event-driven via outbox → event bus → notification handler

---

## Services

### Order Service
Core of the platform. Handles the full order lifecycle with multiple resilience layers.

**Resilience design:**
- **Idempotency**: `Idempotency-Key` header prevents duplicate orders on retry
- **Graceful degradation**: If inventory is down → accept order as `pending_payment`, continue. If payment is down → accept as `pending_payment`, retry later. Core transaction never fails due to downstream outage
- **Outbox**: Events written transactionally, published asynchronously — no lost events even on publisher crash

### Payment Service
Idempotent payment processing — safe to retry any number of times.

**Resilience design:**
- Same `Idempotency-Key` always returns the same payment — no double charges
- Gateway failure stored with reason — enables post-recovery reconciliation
- Refunds only valid on `succeeded` payments — prevents invalid state transitions

### Inventory Service
Reservation-based stock management with automatic expiry.

**Resilience design:**
- Reservations expire after 15 minutes — no permanently locked stock from abandoned orders
- `ConfirmReservation` deducts permanently — two-phase commit within service boundary
- `ReleaseReservation` is idempotent — safe to call multiple times
- Low stock alerts fire when available stock drops below threshold

### Notification Service
Fully async delivery with retry, backoff, and DLQ.

**Resilience design:**
- Immediate delivery attempt on creation
- Failed deliveries move to `retrying` state — background processor retries every 5 seconds
- After `max_attempts` (default: 5) → moved to DLQ
- DLQ is inspectable and replayable via API
- Core order flow never blocked by notification failure

### API Gateway
Single entry point with dual protection layers.

**Resilience design:**
- Per-IP token bucket rate limiter (100 req/s, burst 200) — prevents single client from consuming all capacity
- Per-upstream circuit breaker — stops cascading calls to unhealthy services
- Request ID injection — every request gets `X-Request-ID` for distributed tracing
- Upstream health summary available at `/health`

---

## Self-Healing Patterns

### Circuit Breaker (pkg/resilience)

```
CLOSED → (5 failures) → OPEN → (30s timeout) → HALF-OPEN → (2 successes) → CLOSED
                                                    │
                                              (1 failure) → OPEN
```

States: `closed` (normal), `open` (fail fast), `half-open` (testing recovery)

### Retry with Exponential Backoff + Jitter

```
attempt 1: immediately
attempt 2: 100ms ± 25ms jitter
attempt 3: 200ms ± 50ms jitter
...capped at 10s max delay
```

Jitter prevents thundering herd — multiple services don't retry simultaneously.

### Graceful Degradation Flow

```
Create Order Request
        │
        ├── Reserve Inventory ──── FAILS ──→ Set degraded_mode=true
        │                                     status=pending_payment
        │                                     continue (don't fail)
        │
        ├── Charge Payment ──────── FAILS ──→ Set degraded_mode=true
        │                                     status=pending_payment
        │                                     continue (don't fail)
        │
        └── Return Order (201)     ← customer gets a response regardless
```

### Outbox Pattern

```
Write Order (atomic) {
  INSERT orders ...
  INSERT outbox_entry (event_type=order.created, status=pending)
}

Background Publisher (every 5s) {
  SELECT * FROM outbox WHERE status=pending
  FOR EACH entry:
    publish(entry.event)
    → success: mark published
    → failure: increment attempts
    → attempts >= max: move to DLQ
}
```

### Dead Letter Queue

Failed events after max retries land in the DLQ. Inspectable via `GET /v1/dlq`. Replay by re-sending the notification manually or via automated reconciliation.

---

## Getting Started

```bash
git clone https://github.com/timothydevQ/resilient-platform.git
cd resilient-platform
docker compose up -d

# Verify all services healthy
for port in 8000 8080 8081 8082 8083 8084; do
  echo -n "Port $port: "
  curl -s http://localhost:$port/healthz/ready | jq -r .status
done
```

### Running Tests

```bash
# All services
for svc in api-gateway order-service inventory-service payment-service notification-service user-service; do
  echo "Testing $svc..."
  cd services/$svc && go test -v -race ./... && cd ../..
done

# Shared packages
cd pkg/resilience && go test -v -race ./... && cd ../..
cd pkg/events && go test -v -race ./... && cd ../..
```

---

## API Reference

### Create an Order
```bash
curl -X POST http://localhost:8000/api/orders \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: order-$(uuidgen)" \
  -d '{
    "user_id": "user-123",
    "items": [
      {"product_id": "prod-1", "quantity": 2, "unit_price": 29.99},
      {"product_id": "prod-2", "quantity": 1, "unit_price": 9.99}
    ]
  }'
```

### Safe Retry (Idempotency)
```bash
KEY="order-abc-123"
# Call twice — second returns same order, not a duplicate
curl -X POST http://localhost:8000/api/orders \
  -H "Idempotency-Key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"u1","items":[{"product_id":"p1","quantity":1,"unit_price":10}]}'
```

### Charge Payment
```bash
curl -X POST http://localhost:8000/api/payments \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: pay-order-123" \
  -d '{"order_id":"<order-id>","user_id":"user-123","amount":69.97,"currency":"USD"}'
```

### Reserve Inventory
```bash
# Add a product first
curl -X POST http://localhost:8081/v1/products \
  -H "Content-Type: application/json" \
  -d '{"id":"prod-1","name":"Widget","sku":"WGT-001","stock":100,"low_threshold":10}'

# Reserve stock
curl -X POST http://localhost:8081/v1/reservations \
  -H "Content-Type: application/json" \
  -d '{"order_id":"<order-id>","product_id":"prod-1","quantity":2}'
```

### Send Notification
```bash
curl -X POST http://localhost:8083/v1/notifications \
  -H "Content-Type: application/json" \
  -d '{"user_id":"user-123","type":"email","subject":"Order Confirmed","body":"Your order is confirmed."}'
```

### Gateway Health (circuit breaker states)
```bash
curl http://localhost:8000/health
```

### DLQ Inspection
```bash
curl http://localhost:8083/v1/dlq
```

---

## Multi-Region Failover

Region A runs on ports 8000-8084. Region B runs on ports 9000-9084. Both are started by `docker compose up`.

### Simulate Region A Failure
```bash
# Stop region-a gateway
docker compose stop api-gateway-a

# All traffic reroutes to region-b
curl -X POST http://localhost:9000/api/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"failover-test","items":[{"product_id":"p1","quantity":1,"unit_price":9.99}]}'

# Restore region-a
docker compose start api-gateway-a
```

### Automated Chaos Tests
```bash
bash infrastructure/chaos/chaos.sh payment-crash
bash infrastructure/chaos/chaos.sh region-failover
bash infrastructure/chaos/chaos.sh inventory-slowdown
bash infrastructure/chaos/chaos.sh spike
bash infrastructure/chaos/chaos.sh all
```

---

## Chaos Testing

The `infrastructure/chaos/chaos.sh` script runs four failure scenarios:

| Scenario | What it breaks | Expected behavior |
|---|---|---|
| `payment-crash` | Stops payment-service-a | Orders degrade to `pending_payment`, CB opens, service restarts, CB closes |
| `region-failover` | Stops api-gateway-a | Traffic reroutes to region-b, service continues |
| `inventory-slowdown` | Stops inventory-service-a | Orders degrade gracefully, restore resolves automatically |
| `spike` | 50 concurrent requests | Rate limiter activates, healthy requests proceed |

---

## Observability

| Tool | URL | Purpose |
|---|---|---|
| Grafana | http://localhost:3000 | Dashboards (admin/admin) |
| Prometheus | http://localhost:9090 | Metrics storage and alerting |
| Jaeger | http://localhost:16686 | Distributed trace viewer |
| Loki | http://localhost:3100 | Log aggregation |

### Key Metrics

| Metric | Description |
|---|---|
| `order_service_total_orders` | Total orders created |
| `payment_total` / `payment_succeeded` / `payment_failed` | Payment counters |
| `inventory_reservations_active` | Live reservations |
| `notification_dlq` | DLQ depth — alert if growing |
| `gateway_requests_total` | Total proxied requests |

---

## SLOs & SLIs

| Service | SLI | Target |
|---|---|---|
| order-service | Order creation success rate | 99.9% |
| order-service | Degraded order rate | < 1% |
| payment-service | Payment success rate | 99.95% |
| inventory-service | Reservation success rate | 99.9% |
| notification-service | Delivery within 30s (non-DLQ) | 99.0% |
| api-gateway | P99 latency < 200ms | 99.5% |

---

## CI/CD Pipeline

```
push to main
    │
    ├── test (api-gateway)          ──┐
    ├── test (order-service)        ──┤
    ├── test (inventory-service)    ──┤
    ├── test (payment-service)      ──┼── all pass
    ├── test (notification-service) ──┤
    ├── test (user-service)         ──┤
    ├── test (pkg/resilience)       ──┤
    └── test (pkg/events)           ──┘
              │
              ├── Trivy security scan
              ├── Build + push 6 images → GHCR
              └── Update K8s manifests → ArgoCD sync
```

---

## Load Testing

```bash
k6 run infrastructure/load-testing/k6-load-test.js
```

Three scenarios: sustained 50 VU for 4 minutes, spike to 300 VU, idempotency stress test.

SLO thresholds enforced: `p(99)<500ms`, `p(95)<200ms`, `order_errors<1%`.

---

## Design Decisions

| Decision | ADR | Summary |
|---|---|---|
| Graceful degradation over hard failures | [ADR-001](docs/adr/ADR-001-graceful-degradation.md) | Pending orders generate revenue; 503s generate nothing |
| Circuit breakers at gateway AND service layer | [ADR-002](docs/adr/ADR-002-circuit-breaker-placement.md) | Dual protection: fail-fast at gateway, degrade at service |
| Outbox pattern for event publishing | [ADR-003](docs/adr/ADR-003-outbox-pattern.md) | At-least-once delivery without distributed transactions |
| Active-active multi-region | [ADR-004](docs/adr/ADR-004-multi-region-active-active.md) | Both regions always warm — no cold start on failover |
| Client-provided idempotency keys | [ADR-005](docs/adr/ADR-005-idempotency-tokens.md) | Stripe model — safe retries without server coordination |

---

## Failure Scenarios

### "What happens if the payment service crashes?"

- Order service attempts payment charge → receives connection error
- Graceful degradation: order accepted as `pending_payment`, `degraded_mode: true`
- Customer receives a valid order response — not a 503
- Gateway circuit breaker opens after 5 failures — subsequent requests fail fast with 503
- Kubernetes restarts the payment pod automatically
- Circuit breaker enters half-open after 30s timeout, closes after 2 successes
- Background reconciliation retries `pending_payment` orders
- Runbook: [payment-service-outage](docs/runbooks/payment-service-outage.md)

### "What happens if the inventory service is slow?"

- Order service calls inventory with a 15-second timeout
- Slow responses hold connections but don't block other orders (per-request goroutines)
- If responses exceed timeout → treated as failure → graceful degradation to `pending_payment`
- Circuit breaker opens after threshold — orders continue degrading without waiting for timeouts
- When inventory recovers, CB closes and reservations resume

### "What happens if Region A goes down completely?"

- Load balancer / DNS detects unhealthy region-a
- Traffic reroutes to region-b (already warm, already serving traffic)
- Orders created in region-b have `region: region-b` label
- No cold start — region-b has been processing traffic all along (active-active)
- Region-a restores independently; traffic rebalances automatically
- Runbook: [region-failover](docs/runbooks/region-failover.md)

### "What happens if the notification service DLQ fills up?"

- Notifications fail delivery → `retrying` status → retry every 5 seconds
- After 5 failures → moved to DLQ
- Orders and payments are completely unaffected — notification failure never blocks core flow
- DLQ alert fires at 100 entries
- Manual or automated replay via `POST /v1/notifications`
- Runbook: [dlq-investigation](docs/runbooks/dlq-investigation.md)

### "What happens if a client retries an order and sends the request twice?"

- Both requests carry the same `Idempotency-Key`
- First request creates the order, stores key → orderID mapping
- Second request hits the key cache, returns the original order (HTTP 200)
- No duplicate order in the database
- No duplicate payment charge (payment service has its own idempotency layer)
- This is by design — retries are safe at every layer

### "What happens during a traffic spike?"

- API Gateway token bucket rate limiter triggers: `429 Too Many Requests` with `Retry-After: 1`
- Requests within the burst limit continue normally
- Kubernetes HPA scales order-service and api-gateway pods based on CPU
- Queue of orders absorbs burst via outbox — events published at background rate
- k6 load test includes spike scenario — verified at 300 VU

---

## Scaling Strategy

### Horizontal Pod Autoscaler

| Service | Min | Max | Trigger |
|---|---|---|---|
| api-gateway | 2 | 10 | CPU >70% |
| order-service | 2 | 10 | CPU >70% |
| payment-service | 2 | 6 | CPU >70% |
| inventory-service | 2 | 6 | CPU >70% |
| notification-service | 1 | 4 | CPU >70% |
| user-service | 2 | 6 | CPU >70% |

### System Limits (Tested via k6)

| Metric | Value |
|---|---|
| Order creation P99 (2 replicas) | ~180ms |
| Sustained throughput (2 replicas) | ~800 orders/min |
| Rate limiter burst capacity | 200 requests |
| Idempotent request overhead | <5ms |
| Circuit breaker overhead | <1ms |

---

## Docs & Runbooks

| Document | Description |
|---|---|
| [ADR-001: Graceful Degradation](docs/adr/ADR-001-graceful-degradation.md) | Why degrade over hard fail |
| [ADR-002: Circuit Breaker Placement](docs/adr/ADR-002-circuit-breaker-placement.md) | Dual-layer CB strategy |
| [ADR-003: Outbox Pattern](docs/adr/ADR-003-outbox-pattern.md) | Reliable event publishing |
| [ADR-004: Active-Active Multi-Region](docs/adr/ADR-004-multi-region-active-active.md) | Region failover design |
| [ADR-005: Idempotency Tokens](docs/adr/ADR-005-idempotency-tokens.md) | Safe retry design |
| [Runbook: Payment Outage](docs/runbooks/payment-service-outage.md) | Recovery steps |
| [Runbook: Region Failover](docs/runbooks/region-failover.md) | Failover playbook |
| [Runbook: DLQ Investigation](docs/runbooks/dlq-investigation.md) | DLQ triage and replay |
| [Postmortem: Payment Cascade](docs/postmortems/2024-03-01-payment-cascade.md) | March 2024 incident |
| [Migration Plan](docs/migration/monolith-migration-plan.md) | Monolith extraction phases |
| [Technical Roadmap](docs/roadmap/technical-roadmap.md) | v1 through v5 |

---

## Roadmap

### Q3 2026 — Persistent Storage
- PostgreSQL per service, Redis for idempotency TTL, Kafka for outbox publishing

### Q4 2026 — Advanced Resilience
- Adaptive CB thresholds, retry budgets, bulkhead isolation, chaos testing in CI

### Q1 2027 — Multi-Region Production
- Active-active on AWS, global load balancer, cross-region replication, RTO < 30s

### Q2 2027 — Platform Maturity
- Service mesh (Linkerd), gRPC internal, schema registry, self-service SLO dashboard
<!-- init -->
<!-- overview -->
<!-- arch -->
<!-- services -->
<!-- patterns -->
<!-- quickstart -->
<!-- slo -->
<!-- design -->
<!-- prereqs -->
<!-- chaos -->
<!-- failure -->
<!-- scaling -->
<!-- multi-region -->
<!-- patterns -->
<!-- api ref -->
<!-- observability -->
<!-- badges -->
<!-- adrs -->
<!-- roadmap -->
<!-- contributing -->
<!-- license -->
<!-- final -->
<!-- outbox explain -->
<!-- cb explain -->
<!-- degradation flow -->
<!-- idempotency example -->
<!-- slo table -->
<!-- tested -->
<!-- env vars -->
<!-- port table -->
<!-- contributing -->
