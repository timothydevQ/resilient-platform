package resilience

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrCircuitOpen    = errors.New("circuit breaker is open")
	ErrMaxRetries     = errors.New("max retries exceeded")
	ErrTimeout        = errors.New("operation timed out")
)

// ── Circuit Breaker ───────────────────────────────────────────────────────────

type CBState int

const (
	CBClosed   CBState = iota // normal operation
	CBOpen                    // failing fast
	CBHalfOpen                // testing recovery
)

func (s CBState) String() string {
	switch s {
	case CBClosed:
		return "closed"
	case CBOpen:
		return "open"
	case CBHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

type CircuitBreakerConfig struct {
	FailureThreshold int           // failures before opening
	SuccessThreshold int           // successes in half-open before closing
	Timeout          time.Duration // how long to stay open
	Name             string
}

func DefaultCBConfig(name string) CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Timeout:          30 * time.Second,
		Name:             name,
	}
}

type CircuitBreaker struct {
	mu             sync.Mutex
	cfg            CircuitBreakerConfig
	state          CBState
	failures       int
	successes      int
	lastFailure    time.Time
	totalRequests  int64
	totalFailures  int64
	totalSuccesses int64
}

func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{cfg: cfg, state: CBClosed}
}

func (cb *CircuitBreaker) State() CBState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

func (cb *CircuitBreaker) Stats() map[string]any {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return map[string]any{
		"name":            cb.cfg.Name,
		"state":           cb.state.String(),
		"total_requests":  cb.totalRequests,
		"total_failures":  cb.totalFailures,
		"total_successes": cb.totalSuccesses,
	}
}

func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()
	cb.totalRequests++

	switch cb.state {
	case CBOpen:
		if time.Since(cb.lastFailure) > cb.cfg.Timeout {
			cb.state = CBHalfOpen
			cb.successes = 0
		} else {
			cb.mu.Unlock()
			return ErrCircuitOpen
		}
	}
	cb.mu.Unlock()

	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.totalFailures++
		cb.failures++
		cb.successes = 0
		cb.lastFailure = time.Now()

		if cb.state == CBHalfOpen || cb.failures >= cb.cfg.FailureThreshold {
			cb.state = CBOpen
			cb.failures = 0
		}
		return err
	}

	cb.totalSuccesses++
	cb.failures = 0

	if cb.state == CBHalfOpen {
		cb.successes++
		if cb.successes >= cb.cfg.SuccessThreshold {
			cb.state = CBClosed
			cb.successes = 0
		}
	}
	return nil
}

// ── Retry with Exponential Backoff + Jitter ───────────────────────────────────

type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Multiplier:  2.0,
	}
}

func Retry(cfg RetryConfig, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		// Don't sleep after last attempt
		if attempt < cfg.MaxAttempts-1 {
			delay := backoffDelay(cfg, attempt)
			time.Sleep(delay)
		}
	}
	return ErrMaxRetries
}

func backoffDelay(cfg RetryConfig, attempt int) time.Duration {
	base := float64(cfg.BaseDelay) * math.Pow(cfg.Multiplier, float64(attempt))
	// Add jitter: ±25% of computed delay
	jitter := base * 0.25 * (rand.Float64()*2 - 1)
	delay := time.Duration(base + jitter)
	if delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}
	return delay
}

// ── Timeout Wrapper ───────────────────────────────────────────────────────────

func WithTimeout(d time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return ErrTimeout
	}
}

// ── Resilient Client (retry + circuit breaker + timeout) ─────────────────────

type ResilientClient struct {
	cb      *CircuitBreaker
	retry   RetryConfig
	timeout time.Duration
}

func NewResilientClient(cbCfg CircuitBreakerConfig, retryCfg RetryConfig, timeout time.Duration) *ResilientClient {
	return &ResilientClient{
		cb:      NewCircuitBreaker(cbCfg),
		retry:   retryCfg,
		timeout: timeout,
	}
}

func (r *ResilientClient) Do(fn func() error) error {
	return r.cb.Execute(func() error {
		return Retry(r.retry, func() error {
			return WithTimeout(r.timeout, fn)
		})
	})
}

func (r *ResilientClient) State() CBState  { return r.cb.State() }
func (r *ResilientClient) Stats() map[string]any { return r.cb.Stats() }
// cb state
// cb config
// default config
// cb struct
// cb execute
// cb open
