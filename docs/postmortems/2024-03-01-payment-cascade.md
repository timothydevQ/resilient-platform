# Postmortem: Payment Service Cascade — March 1, 2024

## Summary
Payment service memory leak caused OOM kill. Before circuit breakers opened, 847 order requests returned 502 over 4 minutes. After circuit breaker opened, orders degraded to `pending_payment` for 11 minutes until service recovered.

## Timeline
- 14:00 UTC: Payment service memory climbs past 480MB (limit: 512MB)
- 14:07 UTC: OOM kill — container restarts
- 14:07-14:11 UTC: 847 requests return 502 before gateway CB opens
- 14:11 UTC: Gateway circuit breaker opens (threshold: 5 failures)
- 14:11-14:22 UTC: Orders gracefully degrade to pending_payment
- 14:22 UTC: Payment service stable — CB enters half-open
- 14:24 UTC: CB closes — full recovery

## Root Cause
Idempotency store retained all keys indefinitely without TTL cleanup. Over 6 hours of sustained traffic, the map grew to 1.2M entries consuming ~400MB.

## Impact
- 847 hard failures (502) during CB opening window
- ~680 orders in pending_payment state (all later processed successfully)
- 0 lost payments or double charges

## Action Items
- Add TTL cleanup to idempotency store — 24h expiry (done)
- Add memory pressure alerting at 70% (done)
- Reduce CB threshold from 5 to 3 failures (done)
- Add HPA memory trigger for payment service (in progress)

## Lessons Learned
Circuit breakers work. The 11 minutes of graceful degradation saved the customer experience. Hard failures only happened in the 4-minute window before the CB opened — reducing the threshold to 3 cuts this to under 2 minutes.
<!-- timeline -->
<!-- actions -->
<!-- impact -->
