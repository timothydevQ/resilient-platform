import http from "k6/http";
import { check, sleep, group } from "k6";
import { Rate, Trend, Counter } from "k6/metrics";

// ── Custom Metrics ─────────────────────────────────────────────────────────────
const orderErrors       = new Rate("order_errors");
const orderLatency      = new Trend("order_latency_ms");
const paymentErrors     = new Rate("payment_errors");
const degradedOrders    = new Counter("degraded_orders");
const idempotentHits    = new Counter("idempotent_hits");

// ── SLO Thresholds ─────────────────────────────────────────────────────────────
export const options = {
  scenarios: {
    // Scenario 1: Sustained normal load
    sustained_load: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "1m", target: 50 },
        { duration: "3m", target: 50 },
        { duration: "30s", target: 0 },
      ],
      tags: { scenario: "sustained" },
    },
    // Scenario 2: Traffic spike (simulate flash sale)
    traffic_spike: {
      executor: "ramping-vus",
      startTime: "5m",
      startVUs: 0,
      stages: [
        { duration: "10s", target: 300 },
        { duration: "1m", target: 300 },
        { duration: "10s", target: 0 },
      ],
      tags: { scenario: "spike" },
    },
    // Scenario 3: Idempotency stress test
    idempotency_test: {
      executor: "constant-vus",
      vus: 10,
      duration: "2m",
      startTime: "7m",
      tags: { scenario: "idempotency" },
    },
  },
  thresholds: {
    http_req_duration: ["p(99)<500", "p(95)<200"],
    order_errors:      ["rate<0.01"],
    payment_errors:    ["rate<0.01"],
    http_req_failed:   ["rate<0.05"],
  },
};

const BASE_A = "http://localhost:8000";  // region-a gateway
const BASE_B = "http://localhost:9000";  // region-b gateway (failover)

function getBase() {
  // Simple round-robin: use region-b 20% of the time
  return Math.random() < 0.2 ? BASE_B : BASE_A;
}

function randomID() {
  return Math.random().toString(36).substring(2, 10);
}

// ── Create Order Flow ──────────────────────────────────────────────────────────
function createOrder(base, idempotencyKey) {
  const payload = JSON.stringify({
    user_id: `user-${randomID()}`,
    items: [
      { product_id: `prod-${Math.floor(Math.random() * 10)}`, quantity: Math.ceil(Math.random() * 5), unit_price: parseFloat((Math.random() * 100 + 5).toFixed(2)) },
      { product_id: `prod-${Math.floor(Math.random() * 10)}`, quantity: 1, unit_price: 9.99 },
    ],
    idempotency_key: idempotencyKey || randomID(),
  });

  const headers = { "Content-Type": "application/json" };
  const res = http.post(`${base}/api/orders`, payload, { headers });

  orderLatency.add(res.timings.duration);

  const ok = check(res, {
    "order: status 201 or 200": (r) => r.status === 201 || r.status === 200,
    "order: has order_id":       (r) => { try { return JSON.parse(r.body).order?.id !== undefined; } catch { return false; } },
  });

  orderErrors.add(!ok);

  if (res.status === 200) idempotentHits.add(1);

  try {
    const body = JSON.parse(res.body);
    if (body.order?.degraded_mode) degradedOrders.add(1);
  } catch {}

  return res;
}

// ── Charge Payment ─────────────────────────────────────────────────────────────
function chargePayment(base, orderID) {
  const res = http.post(`${base}/api/payments`, JSON.stringify({
    order_id: orderID,
    user_id: `user-${randomID()}`,
    amount: parseFloat((Math.random() * 200 + 10).toFixed(2)),
    currency: "USD",
    idempotency_key: `pay-${orderID}`,
  }), { headers: { "Content-Type": "application/json" } });

  const ok = check(res, { "payment: status 2xx": (r) => r.status >= 200 && r.status < 300 });
  paymentErrors.add(!ok);
  return res;
}

// ── Health Check ───────────────────────────────────────────────────────────────
function checkHealth(base) {
  const res = http.get(`${base}/health`);
  check(res, { "gateway: healthy": (r) => r.status === 200 });
}

// ── Main VU Function ───────────────────────────────────────────────────────────
export default function () {
  const base = getBase();
  const scenario = __ENV.K6_SCENARIO || "default";

  if (scenario === "idempotency") {
    // Repeat same idempotency key to test safe retries
    const key = `idem-${__VU}`;
    createOrder(base, key);
    sleep(0.5);
    createOrder(base, key); // should return same order
    sleep(0.5);
    return;
  }

  group("health_check", () => checkHealth(base));
  sleep(0.1);

  group("create_order", () => {
    const res = createOrder(base, null);
    if (res.status === 201) {
      try {
        const body = JSON.parse(res.body);
        if (body.order?.id) {
          sleep(0.2);
          group("charge_payment", () => chargePayment(base, body.order.id));
        }
      } catch {}
    }
  });

  sleep(Math.random() * 0.5 + 0.1); // realistic think time 100-600ms
}

export function handleSummary(data) {
  const passed = data.metrics.order_errors?.values?.rate < 0.01 &&
                 data.metrics.http_req_duration?.values?.["p(99)"] < 500;

  return {
    stdout: `
=== Resilient Platform Load Test Summary ===
Status:          ${passed ? "PASS ✓" : "FAIL ✗"}
Total Requests:  ${data.metrics.http_reqs?.values?.count || 0}
P95 Latency:     ${Math.round(data.metrics.http_req_duration?.values?.["p(95)"] || 0)}ms
P99 Latency:     ${Math.round(data.metrics.http_req_duration?.values?.["p(99)"] || 0)}ms
Order Error Rate: ${((data.metrics.order_errors?.values?.rate || 0) * 100).toFixed(2)}%
Degraded Orders: ${data.metrics.degraded_orders?.values?.count || 0}
Idempotent Hits: ${data.metrics.idempotent_hits?.values?.count || 0}
==========================================
`,
  };
}
// options
// sustained
// spike
// idempotency
// create order
// summary
// multi region
// think time
// error inject
// notification test
// user create
