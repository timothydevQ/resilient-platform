# Runbook: Dead Letter Queue Investigation

## Context
Notifications move to the DLQ after 5 failed delivery attempts. DLQ growth indicates a persistent delivery provider outage.

## Steps

### 1. Check DLQ size
```bash
curl http://localhost:8083/v1/dlq | jq .count
```

### 2. Inspect DLQ entries
```bash
curl http://localhost:8083/v1/dlq | jq '.dlq[:5]'
```
Look at `last_error` field to identify root cause.

### 3. Check provider status
```bash
curl http://localhost:8083/healthz/ready
curl http://localhost:8083/v1/stats
```

### 4. If provider is restored — replay DLQ
There is no automatic replay. For each DLQ entry:
```bash
# Re-send the notification manually
curl -X POST http://localhost:8083/v1/notifications \
  -H "Content-Type: application/json" \
  -d '{"user_id":"<user_id>","type":"<type>","subject":"<subject>","body":"<body>"}'
```

### 5. Monitor DLQ does not grow further
```bash
watch -n 5 'curl -s http://localhost:8083/v1/stats | jq .dlq'
```
<!-- steps -->
