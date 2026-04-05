package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Rate Limiter (token bucket per IP) ───────────────────────────────────────

type TokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	maxBurst float64
	rate     float64 // tokens per second
	lastFill time.Time
}

func NewTokenBucket(rate, burst float64) *TokenBucket {
	return &TokenBucket{
		tokens:   burst,
		maxBurst: burst,
		rate:     rate,
		lastFill: time.Now(),
	}
}

func (b *TokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.maxBurst {
		b.tokens = b.maxBurst
	}
	b.lastFill = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*TokenBucket
	rate    float64
	burst   float64
}

func NewRateLimiter(rate, burst float64) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*TokenBucket),
		rate:    rate,
		burst:   burst,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	bucket, ok := rl.buckets[key]
	if !ok {
		bucket = NewTokenBucket(rl.rate, rl.burst)
		rl.buckets[key] = bucket
	}
	rl.mu.Unlock()
	return bucket.Allow()
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		// Remove buckets that haven't been used recently
		for k, b := range rl.buckets {
			b.mu.Lock()
			if time.Since(b.lastFill) > 10*time.Minute {
				delete(rl.buckets, k)
			}
			b.mu.Unlock()
		}
		rl.mu.Unlock()
	}
}

// ── Circuit Breaker ───────────────────────────────────────────────────────────

type CBState int

const (
	CBClosed   CBState = iota
	CBOpen
	CBHalfOpen
)

type CircuitBreaker struct {
	mu          sync.Mutex
	state       CBState
	failures    int
	successes   int
	threshold   int
	successReq  int
	timeout     time.Duration
	lastFailure time.Time
	name        string
}

func NewCircuitBreaker(name string, threshold, successReq int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		name:       name,
		threshold:  threshold,
		successReq: successReq,
		timeout:    timeout,
		state:      CBClosed,
	}
}

func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case CBOpen:
		return "open"
	case CBHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case CBOpen:
		if time.Since(cb.lastFailure) > cb.timeout {
			cb.state = CBHalfOpen
			cb.successes = 0
			return true
		}
		return false
	default:
		return true
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	if cb.state == CBHalfOpen {
		cb.successes++
		if cb.successes >= cb.successReq {
			cb.state = CBClosed
		}
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.successes = 0
	cb.lastFailure = time.Now()
	if cb.state == CBHalfOpen || cb.failures >= cb.threshold {
		cb.state = CBOpen
		cb.failures = 0
		slog.Warn("Circuit breaker opened", "service", cb.name)
	}
}

// ── Upstream Registry ─────────────────────────────────────────────────────────

type Upstream struct {
	Name    string
	URL     string
	CB      *CircuitBreaker
	Healthy bool
}

type UpstreamRegistry struct {
	mu        sync.RWMutex
	upstreams map[string]*Upstream
}

func NewUpstreamRegistry() *UpstreamRegistry {
	return &UpstreamRegistry{upstreams: make(map[string]*Upstream)}
}

func (r *UpstreamRegistry) Register(name, rawURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upstreams[name] = &Upstream{
		Name:    name,
		URL:     rawURL,
		CB:      NewCircuitBreaker(name, 5, 2, 30*time.Second),
		Healthy: true,
	}
}

func (r *UpstreamRegistry) Get(name string) (*Upstream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.upstreams[name]
	return u, ok
}

func (r *UpstreamRegistry) HealthSummary() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]string)
	for name, u := range r.upstreams {
		result[name] = u.CB.State()
	}
	return result
}

// ── Gateway Handler ───────────────────────────────────────────────────────────

type Gateway struct {
	limiter   *RateLimiter
	registry  *UpstreamRegistry
	startTime time.Time
	requests  int64
	mu        sync.Mutex
}

func NewGateway() *Gateway {
	registry := NewUpstreamRegistry()
	registry.Register("order-service", getEnv("ORDER_SERVICE_URL", "http://order-service:8080"))
	registry.Register("inventory-service", getEnv("INVENTORY_SERVICE_URL", "http://inventory-service:8081"))
	registry.Register("payment-service", getEnv("PAYMENT_SERVICE_URL", "http://payment-service:8082"))
	registry.Register("notification-service", getEnv("NOTIFICATION_SERVICE_URL", "http://notification-service:8083"))
	registry.Register("user-service", getEnv("USER_SERVICE_URL", "http://user-service:8084"))

	return &Gateway{
		limiter:   NewRateLimiter(100, 200), // 100 req/s, burst 200
		registry:  registry,
		startTime: time.Now(),
	}
}

func (g *Gateway) extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.Split(xff, ",")[0]
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (g *Gateway) injectRequestID(r *http.Request) string {
	id := r.Header.Get("X-Request-ID")
	if id == "" {
		b := make([]byte, 8)
		rand.Read(b)
		id = fmt.Sprintf("%x", b)
	}
	r.Header.Set("X-Request-ID", id)
	return id
}

func (g *Gateway) proxyHandler(upstreamName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := g.extractIP(r)
		reqID := g.injectRequestID(r)

		// Rate limiting
		if !g.limiter.Allow(ip) {
			slog.Warn("Rate limited", "ip", ip, "upstream", upstreamName)
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error":      "rate limit exceeded",
				"request_id": reqID,
			})
			return
		}

		upstream, ok := g.registry.Get(upstreamName)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "upstream not found"})
			return
		}

		// Circuit breaker check
		if !upstream.CB.Allow() {
			slog.Warn("Circuit breaker open", "upstream", upstreamName, "request_id", reqID)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":      "service temporarily unavailable",
				"request_id": reqID,
			})
			return
		}

		target, err := url.Parse(upstream.URL)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid upstream URL"})
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			upstream.CB.RecordFailure()
			slog.Error("Upstream error", "upstream", upstreamName, "err", err, "request_id", reqID)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":      "upstream service error",
				"request_id": reqID,
			})
		}

		// Track success
		wrapped := &responseWriter{ResponseWriter: w, status: 200}
		proxy.ServeHTTP(wrapped, r)

		g.mu.Lock()
		g.requests++
		g.mu.Unlock()

		if wrapped.status < 500 {
			upstream.CB.RecordSuccess()
		} else {
			upstream.CB.RecordFailure()
		}

		slog.Info("Proxied request", "upstream", upstreamName, "status", wrapped.status,
			"request_id", reqID, "method", r.Method, "path", r.URL.Path)
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (g *Gateway) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"uptime_ms": time.Since(g.startTime).Milliseconds(),
		"upstreams": g.registry.HealthSummary(),
	})
}

func (g *Gateway) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (g *Gateway) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (g *Gateway) metrics(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	reqs := g.requests
	g.mu.Unlock()
	fmt.Fprintf(w, "gateway_requests_total %d\n", reqs)
	fmt.Fprintf(w, "gateway_uptime_seconds %d\n", int(time.Since(g.startTime).Seconds()))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	gw := NewGateway()

	mux := http.NewServeMux()

	// Route to upstreams
	mux.HandleFunc("/api/orders", gw.proxyHandler("order-service"))
	mux.HandleFunc("/api/orders/", gw.proxyHandler("order-service"))
	mux.HandleFunc("/api/inventory/", gw.proxyHandler("inventory-service"))
	mux.HandleFunc("/api/payments", gw.proxyHandler("payment-service"))
	mux.HandleFunc("/api/payments/", gw.proxyHandler("payment-service"))
	mux.HandleFunc("/api/notifications", gw.proxyHandler("notification-service"))
	mux.HandleFunc("/api/users", gw.proxyHandler("user-service"))
	mux.HandleFunc("/api/users/", gw.proxyHandler("user-service"))

	// Gateway health
	mux.HandleFunc("/health", gw.health)
	mux.HandleFunc("/healthz/live", gw.liveness)
	mux.HandleFunc("/healthz/ready", gw.readiness)
	mux.HandleFunc("/metrics", gw.metrics)

	port := getEnv("HTTP_PORT", "8000")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		slog.Info("API Gateway started", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
// token bucket
// bucket allow
// rate limiter
// rl allow
// rl cleanup
// cb state
// cb struct
// cb allow
// cb success
// cb failure
// upstream
// registry
// health summary
// gateway struct
// extract ip
// inject request id
// proxy handler
// error handler
// response writer
// health endpoint
// routes
// slog startup
// log rate
// log cb
// feat_51:53
// feat_36:23
// feat_20:53
// feat_05:23
// feat_49:53
// fix_35:23
// fix_19:53
// fix_04:23
