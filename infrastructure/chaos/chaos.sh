#!/usr/bin/env bash
# chaos.sh — Simulate failure scenarios and observe recovery
# Usage: bash chaos.sh [scenario]
# Scenarios: payment-crash, region-failover, inventory-slowdown, spike

set -euo pipefail

BASE_A="http://localhost:8000"
BASE_B="http://localhost:9000"

print_header() { echo ""; echo "=== $1 ==="; echo ""; }
wait_for() { echo "Waiting ${1}s for recovery..."; sleep "$1"; }

check_orders() {
  echo "Testing order creation on $1..."
  result=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$1/api/orders" \
    -H "Content-Type: application/json" \
    -d '{"user_id":"chaos-test","items":[{"product_id":"p1","quantity":1,"unit_price":9.99}]}')
  echo "  Order endpoint: HTTP $result"
}

# ── Scenario 1: Payment Service Crash ─────────────────────────────────────────
scenario_payment_crash() {
  print_header "SCENARIO 1: Payment Service Crash"
  echo "Step 1: Baseline — create order (should succeed fully)"
  check_orders "$BASE_A"

  echo ""
  echo "Step 2: Kill payment service"
  docker compose stop payment-service-a || true
  sleep 2

  echo ""
  echo "Step 3: Create order (should degrade gracefully to pending_payment)"
  check_orders "$BASE_A"

  echo ""
  echo "Step 4: Check gateway circuit breaker state"
  curl -s "$BASE_A/health" | python3 -m json.tool 2>/dev/null || curl -s "$BASE_A/health"

  wait_for 10

  echo ""
  echo "Step 5: Restart payment service — observe recovery"
  docker compose start payment-service-a || true
  wait_for 5

  echo ""
  echo "Step 6: Create order again — should succeed fully"
  check_orders "$BASE_A"
  print_header "SCENARIO 1 COMPLETE"
}

# ── Scenario 2: Region A Failure — Failover to Region B ───────────────────────
scenario_region_failover() {
  print_header "SCENARIO 2: Region A Failure — Failover to Region B"
  echo "Step 1: Verify region-b is healthy"
  curl -s "$BASE_B/healthz/ready" || echo "Region B not running — start with: docker compose up -d"

  echo ""
  echo "Step 2: Stop region-a API gateway"
  docker compose stop api-gateway-a || true
  sleep 2

  echo ""
  echo "Step 3: Traffic rerouted to region-b — create order"
  check_orders "$BASE_B"

  wait_for 15

  echo ""
  echo "Step 4: Restore region-a"
  docker compose start api-gateway-a || true
  wait_for 5

  echo "Step 5: Both regions healthy"
  check_orders "$BASE_A"
  check_orders "$BASE_B"
  print_header "SCENARIO 2 COMPLETE"
}

# ── Scenario 3: Inventory Slowdown ────────────────────────────────────────────
scenario_inventory_slowdown() {
  print_header "SCENARIO 3: Inventory Service Degraded"
  echo "Simulating degraded inventory by stopping it"
  docker compose stop inventory-service-a || true
  sleep 2

  echo ""
  echo "Create order — should degrade gracefully (inventory unavailable)"
  for i in 1 2 3; do
    echo "  Attempt $i:"
    check_orders "$BASE_A"
    sleep 1
  done

  echo ""
  echo "Restore inventory service"
  docker compose start inventory-service-a || true
  wait_for 5

  echo "Create order — should recover fully"
  check_orders "$BASE_A"
  print_header "SCENARIO 3 COMPLETE"
}

# ── Scenario 4: Traffic Spike ──────────────────────────────────────────────────
scenario_spike() {
  print_header "SCENARIO 4: Traffic Spike"
  echo "Sending 50 concurrent requests to simulate spike..."
  for i in $(seq 1 50); do
    curl -s -o /dev/null -X POST "$BASE_A/api/orders" \
      -H "Content-Type: application/json" \
      -d "{\"user_id\":\"spike-$i\",\"items\":[{\"product_id\":\"p1\",\"quantity\":1,\"unit_price\":9.99}]}" &
  done
  wait
  echo "Spike complete — checking health"
  curl -s "$BASE_A/healthz/ready"
  print_header "SCENARIO 4 COMPLETE"
}

# ── Main ───────────────────────────────────────────────────────────────────────
case "${1:-help}" in
  payment-crash)     scenario_payment_crash ;;
  region-failover)   scenario_region_failover ;;
  inventory-slowdown) scenario_inventory_slowdown ;;
  spike)             scenario_spike ;;
  all)
    scenario_payment_crash
    scenario_inventory_slowdown
    scenario_spike
    ;;
  *)
    echo "Usage: bash chaos.sh [payment-crash|region-failover|inventory-slowdown|spike|all]"
    echo ""
    echo "Scenarios:"
    echo "  payment-crash       Kill payment service and observe graceful degradation"
    echo "  region-failover     Fail region-a and reroute traffic to region-b"
    echo "  inventory-slowdown  Stop inventory service and observe pending_payment flow"
    echo "  spike               Send 50 concurrent requests to test rate limiting"
    echo "  all                 Run all scenarios in sequence"
    ;;
esac
// payment crash
// region failover
// inventory slow
// spike
// health check
