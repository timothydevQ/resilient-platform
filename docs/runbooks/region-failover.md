# Runbook: Region Failover

## When to use
When region-a is completely unavailable and traffic must be rerouted to region-b.

## Steps

### 1. Confirm region-a is down
```bash
curl http://localhost:8000/healthz/ready
# Expected: connection refused or 503
```

### 2. Verify region-b is healthy
```bash
curl http://localhost:9000/healthz/ready
curl http://localhost:9000/health
```

### 3. Update load balancer / DNS to point to region-b
In Kubernetes with NGINX ingress:
```bash
kubectl patch ingress resilient-platform-ingress -n resilient-platform \
  -p '{"spec":{"rules":[{"host":"api.resilient-platform.local","http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":"api-gateway-b","port":{"number":9000}}}}]}}]}}'
```

### 4. Validate orders are processed in region-b
```bash
curl -X POST http://localhost:9000/api/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"failover-test","items":[{"product_id":"p1","quantity":1,"unit_price":9.99}]}'
```

### 5. Restore region-a and rebalance
```bash
docker compose start api-gateway-a order-service-a payment-service-a
# Verify healthy
curl http://localhost:8000/healthz/ready
```

## Post-incident
File a postmortem within 48 hours. Measure: how long was region-a down? How many orders went through region-b?
<!-- steps -->
<!-- post -->
