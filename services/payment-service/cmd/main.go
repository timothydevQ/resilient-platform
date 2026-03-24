package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Domain ────────────────────────────────────────────────────────────────────

type PaymentStatus string

const (
	PaymentPending   PaymentStatus = "pending"
	PaymentSucceeded PaymentStatus = "succeeded"
	PaymentFailed    PaymentStatus = "failed"
	PaymentRefunded  PaymentStatus = "refunded"
)

type Payment struct {
	ID             string        `json:"id"`
	OrderID        string        `json:"order_id"`
	UserID         string        `json:"user_id"`
	Amount         float64       `json:"amount"`
	Currency       string        `json:"currency"`
	Status         PaymentStatus `json:"status"`
	IdempotencyKey string        `json:"idempotency_key"`
	FailureReason  string        `json:"failure_reason,omitempty"`
	Region         string        `json:"region"`
	Attempts       int           `json:"attempts"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// ── Idempotency Store ─────────────────────────────────────────────────────────

type IdempotencyStore struct {
	mu      sync.RWMutex
	records map[string]string // key → paymentID
}

func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{records: make(map[string]string)}
}

func (s *IdempotencyStore) GetOrSet(key, paymentID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok {
		return existing, true
	}
	s.records[key] = paymentID
	return paymentID, false
}

// ── Payment Store ─────────────────────────────────────────────────────────────

type PaymentStore struct {
	mu       sync.RWMutex
	payments map[string]*Payment
}

func NewPaymentStore() *PaymentStore {
	return &PaymentStore{payments: make(map[string]*Payment)}
}

func (s *PaymentStore) Create(p *Payment) {
	s.mu.Lock()
	s.payments[p.ID] = p
	s.mu.Unlock()
}

func (s *PaymentStore) Get(id string) (*Payment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.payments[id]
	return p, ok
}

func (s *PaymentStore) GetByOrderID(orderID string) (*Payment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.payments {
		if p.OrderID == orderID {
			return p, true
		}
	}
	return nil, false
}

func (s *PaymentStore) Update(id string, fn func(*Payment)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return fmt.Errorf("payment %s not found", id)
	}
	fn(p)
	p.UpdatedAt = time.Now()
	return nil
}

func (s *PaymentStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.payments)
}

func (s *PaymentStore) CountByStatus(status PaymentStatus) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, p := range s.payments {
		if p.Status == status {
			count++
		}
	}
	return count
}

// ── Payment Processor (stub for external gateway) ─────────────────────────────

type GatewayStatus int

const (
	GatewayHealthy  GatewayStatus = iota
	GatewayDegraded               // slow responses
	GatewayDown                   // not available
)

type PaymentGateway struct {
	mu        sync.Mutex
	status    GatewayStatus
	failRate  float64 // 0.0-1.0 probability of failure
}

func NewPaymentGateway() *PaymentGateway {
	return &PaymentGateway{status: GatewayHealthy}
}

func (g *PaymentGateway) SetStatus(s GatewayStatus) {
	g.mu.Lock()
	g.status = s
	g.mu.Unlock()
}

func (g *PaymentGateway) SetFailRate(rate float64) {
	g.mu.Lock()
	g.failRate = rate
	g.mu.Unlock()
}

func (g *PaymentGateway) Charge(amount float64, idempotencyKey string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.status {
	case GatewayDown:
		return fmt.Errorf("payment gateway unavailable")
	case GatewayDegraded:
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func (g *PaymentGateway) Refund(paymentID string, amount float64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status == GatewayDown {
		return fmt.Errorf("payment gateway unavailable for refund")
	}
	return nil
}

// ── Payment Service ───────────────────────────────────────────────────────────

type PaymentService struct {
	store       *PaymentStore
	idempotency *IdempotencyStore
	gateway     *PaymentGateway
	region      string
}

func NewPaymentService(region string) *PaymentService {
	return &PaymentService{
		store:       NewPaymentStore(),
		idempotency: NewIdempotencyStore(),
		gateway:     NewPaymentGateway(),
		region:      region,
	}
}

type ChargeRequest struct {
	OrderID        string  `json:"order_id"`
	UserID         string  `json:"user_id"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"currency"`
	IdempotencyKey string  `json:"idempotency_key"`
}

func (s *PaymentService) Charge(req ChargeRequest) (*Payment, error) {
	if req.OrderID == "" {
		return nil, fmt.Errorf("order_id is required")
	}
	if req.Amount <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}

	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}

	paymentID := newID()

	// Idempotency check — safe to retry
	if req.IdempotencyKey != "" {
		if existingID, exists := s.idempotency.GetOrSet(req.IdempotencyKey, paymentID); exists {
			if existing, ok := s.store.Get(existingID); ok {
				slog.Info("Idempotent payment request", "key", req.IdempotencyKey, "payment_id", existingID)
				return existing, nil
			}
		}
	}

	payment := &Payment{
		ID:             paymentID,
		OrderID:        req.OrderID,
		UserID:         req.UserID,
		Amount:         req.Amount,
		Currency:       currency,
		Status:         PaymentPending,
		IdempotencyKey: req.IdempotencyKey,
		Region:         s.region,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	s.store.Create(payment)

	// Attempt charge through gateway
	if err := s.gateway.Charge(req.Amount, req.IdempotencyKey); err != nil {
		slog.Warn("Payment gateway charge failed", "payment_id", payment.ID, "err", err)
		s.store.Update(payment.ID, func(p *Payment) {
			p.Status = PaymentFailed
			p.FailureReason = err.Error()
			p.Attempts++
		})
		return payment, fmt.Errorf("payment failed: %w", err)
	}

	s.store.Update(payment.ID, func(p *Payment) {
		p.Status = PaymentSucceeded
		p.Attempts++
	})

	slog.Info("Payment succeeded", "payment_id", payment.ID, "order_id", req.OrderID, "amount", req.Amount)
	return payment, nil
}

func (s *PaymentService) Refund(paymentID string) error {
	payment, ok := s.store.Get(paymentID)
	if !ok {
		return fmt.Errorf("payment %s not found", paymentID)
	}
	if payment.Status != PaymentSucceeded {
		return fmt.Errorf("can only refund succeeded payments, current status: %s", payment.Status)
	}

	if err := s.gateway.Refund(payment.ID, payment.Amount); err != nil {
		return fmt.Errorf("refund failed: %w", err)
	}

	s.store.Update(payment.ID, func(p *Payment) {
		p.Status = PaymentRefunded
	})

	slog.Info("Payment refunded", "payment_id", paymentID)
	return nil
}

func (s *PaymentService) GetPayment(id string) (*Payment, error) {
	p, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("payment %s not found", id)
	}
	return p, nil
}

func (s *PaymentService) GetByOrderID(orderID string) (*Payment, error) {
	p, ok := s.store.GetByOrderID(orderID)
	if !ok {
		return nil, fmt.Errorf("no payment found for order %s", orderID)
	}
	return p, nil
}

func (s *PaymentService) Stats() map[string]any {
	return map[string]any{
		"total":     s.store.Count(),
		"succeeded": s.store.CountByStatus(PaymentSucceeded),
		"failed":    s.store.CountByStatus(PaymentFailed),
		"pending":   s.store.CountByStatus(PaymentPending),
		"refunded":  s.store.CountByStatus(PaymentRefunded),
		"region":    s.region,
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct{ svc *PaymentService }

func (h *handler) charge(w http.ResponseWriter, r *http.Request) {
	var req ChargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		req.IdempotencyKey = key
	}

	payment, err := h.svc.Charge(req)
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error":   err.Error(),
			"payment": payment,
		})
		return
	}
	writeJSON(w, http.StatusCreated, payment)
}

func (h *handler) getPayment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/payments/")
	payment, err := h.svc.GetPayment(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payment)
}

func (h *handler) refund(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("payment_id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payment_id required"})
		return
	}
	if err := h.svc.Refund(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "refunded"})
}

func (h *handler) getStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.Stats())
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) metrics(w http.ResponseWriter, _ *http.Request) {
	stats := h.svc.Stats()
	fmt.Fprintf(w, "payment_total %v\n", stats["total"])
	fmt.Fprintf(w, "payment_succeeded %v\n", stats["succeeded"])
	fmt.Fprintf(w, "payment_failed %v\n", stats["failed"])
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

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

func methodHandler(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[strings.ToUpper(r.Method)]; ok {
			h(w, r)
			return
		}
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	region := getEnv("REGION", "region-a")
	svc := NewPaymentService(region)
	h := &handler{svc: svc}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/payments", methodHandler(map[string]http.HandlerFunc{"POST": h.charge}))
	mux.HandleFunc("/v1/payments/", methodHandler(map[string]http.HandlerFunc{"GET": h.getPayment}))
	mux.HandleFunc("/v1/refunds", methodHandler(map[string]http.HandlerFunc{"POST": h.refund}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.getStats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metrics)

	port := getEnv("HTTP_PORT", "8082")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Payment service started", "port", port, "region", region)
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
// payment status
