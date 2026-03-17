package resilience

import (
	"errors"
	"testing"
	"time"
)

var errTest = errors.New("test error")

// ── Circuit Breaker Tests ─────────────────────────────────────────────────────

func TestCB_InitialStateClosed(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCBConfig("test"))
	if cb.State() != CBClosed {
		t.Errorf("expected closed, got %s", cb.State())
	}
}

func TestCB_SuccessDoesNotOpen(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCBConfig("test"))
	for i := 0; i < 10; i++ {
		cb.Execute(func() error { return nil })
	}
	if cb.State() != CBClosed {
		t.Errorf("expected closed after successes, got %s", cb.State())
	}
}

func TestCB_OpensAfterThreshold(t *testing.T) {
	cfg := DefaultCBConfig("test")
	cfg.FailureThreshold = 3
	cb := NewCircuitBreaker(cfg)
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return errTest })
	}
	if cb.State() != CBOpen {
		t.Errorf("expected open after 3 failures, got %s", cb.State())
	}
}

func TestCB_RejectsWhenOpen(t *testing.T) {
	cfg := DefaultCBConfig("test")
	cfg.FailureThreshold = 1
	cb := NewCircuitBreaker(cfg)
	cb.Execute(func() error { return errTest })
	err := cb.Execute(func() error { return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCB_HalfOpenAfterTimeout(t *testing.T) {
	cfg := DefaultCBConfig("test")
	cfg.FailureThreshold = 1
	cfg.Timeout = 10 * time.Millisecond
	cb := NewCircuitBreaker(cfg)
	cb.Execute(func() error { return errTest })
	time.Sleep(20 * time.Millisecond)
	cb.Execute(func() error { return nil })
	// After timeout, should transition to half-open
	if cb.State() == CBOpen {
		t.Error("expected circuit to transition from open after timeout")
	}
}

func TestCB_ClosesAfterSuccessThreshold(t *testing.T) {
	cfg := DefaultCBConfig("test")
	cfg.FailureThreshold = 1
	cfg.SuccessThreshold = 2
	cfg.Timeout = 10 * time.Millisecond
	cb := NewCircuitBreaker(cfg)
	cb.Execute(func() error { return errTest })
	time.Sleep(20 * time.Millisecond)
	cb.Execute(func() error { return nil })
	cb.Execute(func() error { return nil })
	if cb.State() != CBClosed {
		t.Errorf("expected closed after success threshold in half-open, got %s", cb.State())
	}
}

func TestCB_StatsTracked(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCBConfig("test"))
	cb.Execute(func() error { return nil })
	cb.Execute(func() error { return errTest })
	stats := cb.Stats()
	if stats["total_requests"].(int64) != 2 {
		t.Errorf("expected 2 total requests, got %v", stats["total_requests"])
	}
	if stats["total_failures"].(int64) != 1 {
		t.Errorf("expected 1 failure, got %v", stats["total_failures"])
	}
}

func TestCB_StateString(t *testing.T) {
	if CBClosed.String() != "closed" { t.Error("expected closed") }
	if CBOpen.String() != "open" { t.Error("expected open") }
	if CBHalfOpen.String() != "half-open" { t.Error("expected half-open") }
}

func TestCB_ResetFailuresOnSuccess(t *testing.T) {
	cfg := DefaultCBConfig("test")
	cfg.FailureThreshold = 3
	cb := NewCircuitBreaker(cfg)
	cb.Execute(func() error { return errTest })
	cb.Execute(func() error { return errTest })
	cb.Execute(func() error { return nil }) // reset
	cb.Execute(func() error { return errTest })
	cb.Execute(func() error { return errTest })
	if cb.State() == CBOpen {
		t.Error("should not open — failures were reset by success")
	}
}

// ── Retry Tests ───────────────────────────────────────────────────────────────

func TestRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	err := Retry(DefaultRetryConfig(), func() error {
		calls++
		return nil
	})
	if err != nil { t.Errorf("expected nil, got %v", err) }
	if calls != 1 { t.Errorf("expected 1 call, got %d", calls) }
}

func TestRetry_RetriesOnFailure(t *testing.T) {
	calls := 0
	cfg := DefaultRetryConfig()
	cfg.MaxAttempts = 3
	cfg.BaseDelay = time.Millisecond
	Retry(cfg, func() error {
		calls++
		return errTest
	})
	if calls != 3 { t.Errorf("expected 3 calls, got %d", calls) }
}

func TestRetry_SuccessOnSecondAttempt(t *testing.T) {
	calls := 0
	cfg := DefaultRetryConfig()
	cfg.BaseDelay = time.Millisecond
	err := Retry(cfg, func() error {
		calls++
		if calls < 2 { return errTest }
		return nil
	})
	if err != nil { t.Errorf("expected success, got %v", err) }
	if calls != 2 { t.Errorf("expected 2 calls, got %d", calls) }
}

func TestRetry_ReturnsMaxRetriesError(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxAttempts = 2
	cfg.BaseDelay = time.Millisecond
	err := Retry(cfg, func() error { return errTest })
	if !errors.Is(err, ErrMaxRetries) {
		t.Errorf("expected ErrMaxRetries, got %v", err)
	}
}

func TestRetry_ZeroAttempts(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxAttempts = 0
	err := Retry(cfg, func() error { return nil })
	if err != ErrMaxRetries {
		t.Errorf("expected ErrMaxRetries for 0 attempts, got %v", err)
	}
}

// ── Timeout Tests ─────────────────────────────────────────────────────────────

func TestWithTimeout_CompletesInTime(t *testing.T) {
	err := WithTimeout(100*time.Millisecond, func() error {
		return nil
	})
	if err != nil { t.Errorf("expected nil, got %v", err) }
}

func TestWithTimeout_TimesOut(t *testing.T) {
	err := WithTimeout(10*time.Millisecond, func() error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

func TestWithTimeout_PropagatesError(t *testing.T) {
	err := WithTimeout(100*time.Millisecond, func() error {
		return errTest
	})
	if !errors.Is(err, errTest) {
		t.Errorf("expected errTest, got %v", err)
	}
}

// ── Resilient Client Tests ────────────────────────────────────────────────────

func TestResilientClient_SuccessPassesThrough(t *testing.T) {
	rc := NewResilientClient(
		DefaultCBConfig("test"),
		RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2},
		time.Second,
	)
	err := rc.Do(func() error { return nil })
	if err != nil { t.Errorf("expected nil, got %v", err) }
}

func TestResilientClient_CBOpensAfterFailures(t *testing.T) {
	cbCfg := DefaultCBConfig("test")
	cbCfg.FailureThreshold = 2
	rc := NewResilientClient(
		cbCfg,
		RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2},
		time.Second,
	)
	rc.Do(func() error { return errTest })
	rc.Do(func() error { return errTest })
	if rc.State() != CBOpen {
		t.Errorf("expected CB open, got %s", rc.State())
	}
}

func TestBackoffDelay_CappedAtMax(t *testing.T) {
	cfg := RetryConfig{
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   200 * time.Millisecond,
		Multiplier: 10,
	}
	delay := backoffDelay(cfg, 5)
	if delay > 300*time.Millisecond { // allowing for jitter
		t.Errorf("delay should be capped near max, got %v", delay)
	}
}
// cb initial closed
// cb success no open
// cb opens
// cb rejects open
