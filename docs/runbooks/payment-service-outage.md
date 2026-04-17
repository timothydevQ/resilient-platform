# Runbook: Payment Service Outage

## Severity
P0 if error rate >10% | P1 if 1-10% | P2 if <1%

## Immediate Steps

### 1. Confirm scope
```bash
curl http://localhost:8082/healthz/ready
curl http://localhost:8000/health | jq .upstreams
```

### 2. Check orders are degrading gracefully
```bash
curl http://localhost:8080/v1/stats | jq .publisher
```
Orders should show `pending_payment` status — not hard failures.

### 3. Check circuit breaker state
```bash
curl http://localhost:8000/health | jq '.upstreams["payment-service"]'
```
If `open` — CB is protecting; system is degrading correctly.

### 4. Restart the payment service
```bash
docker compose restart payment-service-a
# or Kubernetes:
kubectl rollout restart deployment/payment-service -n resilient-platform
```

### 5. Verify recovery
```bash
curl -X POST http://localhost:8000/api/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"recovery-test","items":[{"product_id":"p1","quantity":1,"unit_price":10}]}'
```
Should return `status: confirmed` (not `pending_payment`).

### 6. Process pending_payment orders
Pending orders created during outage need payment retry. Trigger reconciliation:
```bash
curl http://localhost:8080/v1/stats | jq .total_orders
```

## Escalation
If service does not recover within 15 minutes — escalate to on-call engineer.
