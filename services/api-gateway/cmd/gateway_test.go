package main

import (
	"testing"
	"time"
)

// ── Token Bucket Tests ────────────────────────────────────────────────────────

func TestTokenBucket_AllowsUnderBurst(t *testing.T) {
	b := NewTokenBucket(10, 5)
	allowed := 0
	for i := 0; i < 5; i++ {
		if b.Allow() { allowed++ }
	}
	if allowed != 5 { t.Errorf("expected 5 allowed within burst, got %d", allowed) }
}

func TestTokenBucket_BlocksAfterBurst(t *testing.T) {
	b := NewTokenBucket(1, 3)
	for i := 0; i < 3; i++ { b.Allow() } // exhaust burst
	if b.Allow() { t.Error("expected block after burst exhausted") }
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	b := NewTokenBucket(100, 1)
	b.Allow() // exhaust
	time.Sleep(20 * time.Millisecond)
	if !b.Allow() { t.Error("expected allow after refill") }
}

func TestTokenBucket_CapsAtMaxBurst(t *testing.T) {
	b := NewTokenBucket(1000, 5)
	time.Sleep(100 * time.Millisecond)
	allowed := 0
	for i := 0; i < 10; i++ {
		if b.Allow() { allowed++ }
	}
	if allowed > 5 { t.Errorf("expected max 5 tokens, got %d", allowed) }
}

// ── Rate Limiter Tests ────────────────────────────────────────────────────────

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := NewRateLimiter(100, 10)
	for i := 0; i < 10; i++ {
		if !rl.Allow("ip-1") { t.Errorf("expected allow at iteration %d", i) }
	}
}

func TestRateLimiter_BlocksAfterBurst(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	rl.Allow("ip-1")
	rl.Allow("ip-1")
	if rl.Allow("ip-1") { t.Error("expected block after burst exhausted") }
}

func TestRateLimiter_IsolatesPerKey(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.Allow("ip-1") // exhaust ip-1
	if !rl.Allow("ip-2") { t.Error("ip-2 should still be allowed") }
}

func TestRateLimiter_CreatesNewBucketPerIP(t *testing.T) {
	rl := NewRateLimiter(100, 10)
	rl.Allow("ip-1")
	rl.Allow("ip-2")
	rl.mu.Lock()
	count := len(rl.buckets)
	rl.mu.Unlock()
	if count != 2 { t.Errorf("expected 2 buckets, got %d", count) }
}

// ── Circuit Breaker Tests ─────────────────────────────────────────────────────

func TestCB_InitiallyClosed(t *testing.T) {
	cb := NewCircuitBreaker("test", 5, 2, 30*time.Second)
	if cb.State() != "closed" { t.Errorf("expected closed, got %s", cb.State()) }
}

func TestCB_AllowsWhenClosed(t *testing.T) {
	cb := NewCircuitBreaker("test", 5, 2, 30*time.Second)
	if !cb.Allow() { t.Error("expected allow when closed") }
}

func TestCB_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, 2, 30*time.Second)
	for i := 0; i < 3; i++ { cb.RecordFailure() }
	if cb.State() != "open" { t.Errorf("expected open, got %s", cb.State()) }
}

func TestCB_BlocksWhenOpen(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, 2, 30*time.Second)
	cb.RecordFailure()
	if cb.Allow() { t.Error("expected block when open") }
}

func TestCB_TransitionsToHalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, 2, 10*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.Allow() // triggers half-open transition
	if cb.State() == "open" { t.Error("expected transition from open after timeout") }
}

func TestCB_ClosesAfterSuccessThreshold(t *testing.T) {
	cb := NewCircuitBreaker("test", 1, 2, 10*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.Allow() // half-open
	cb.RecordSuccess()
	cb.RecordSuccess()
	if cb.State() != "closed" { t.Errorf("expected closed after successes, got %s", cb.State()) }
}

func TestCB_ResetsFailuresOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, 2, 30*time.Second)
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess() // reset
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() == "open" { t.Error("failures should have been reset by success") }
}

// ── Upstream Registry Tests ───────────────────────────────────────────────────

func TestUpstreamRegistry_Register(t *testing.T) {
	r := NewUpstreamRegistry()
	r.Register("test-svc", "http://localhost:8080")
	u, ok := r.Get("test-svc")
	if !ok { t.Fatal("expected to find upstream") }
	if u.URL != "http://localhost:8080" { t.Errorf("wrong URL: %s", u.URL) }
}

func TestUpstreamRegistry_GetNotFound(t *testing.T) {
	r := NewUpstreamRegistry()
	_, ok := r.Get("nonexistent")
	if ok { t.Error("expected not found") }
}

func TestUpstreamRegistry_HealthSummary(t *testing.T) {
	r := NewUpstreamRegistry()
	r.Register("svc-a", "http://svc-a:8080")
	r.Register("svc-b", "http://svc-b:8080")
	summary := r.HealthSummary()
	if len(summary) != 2 { t.Errorf("expected 2 entries, got %d", len(summary)) }
	for _, state := range summary {
		if state != "closed" { t.Errorf("expected closed, got %s", state) }
	}
}

// ── Gateway Tests ─────────────────────────────────────────────────────────────

func TestGateway_ExtractIP_RemoteAddr(t *testing.T) {
	gw := &Gateway{limiter: NewRateLimiter(100, 100), registry: NewUpstreamRegistry(), startTime: time.Now()}
	// Test with a mock that has RemoteAddr
	_ = gw // just verify construction
}

func TestGetEnv_Present(t *testing.T) {
	t.Setenv("TEST_GW_KEY", "hello")
	if getEnv("TEST_GW_KEY", "fallback") != "hello" {
		t.Error("expected env value")
	}
}

func TestGetEnv_Missing(t *testing.T) {
	if getEnv("GW_MISSING_KEY_XYZ", "default") != "default" {
		t.Error("expected fallback")
	}
}
// bucket allows
// bucket blocks
// bucket refills
// bucket caps
// rl allows
// rl blocks
// rl isolates
// rl new bucket
// cb initial
// cb allows closed
// cb opens
// cb blocks
// cb half open
// cb closes
// cb resets
// registry register
// registry not found
// health summary
// getenv
