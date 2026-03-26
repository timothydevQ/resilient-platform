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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Domain ────────────────────────────────────────────────────────────────────

type OrderStatus string

const (
	StatusPending           OrderStatus = "pending"
	StatusPendingPayment    OrderStatus = "pending_payment" // degraded mode
	StatusConfirmed         OrderStatus = "confirmed"
	StatusPaymentFailed     OrderStatus = "payment_failed"
	StatusInventoryFailed   OrderStatus = "inventory_failed"
	StatusShipped           OrderStatus = "shipped"
	StatusDelivered         OrderStatus = "delivered"
	StatusCancelled         OrderStatus = "cancelled"
)

type OrderItem struct {
	ProductID string  `json:"product_id"`
	Quantity  int     `json:"quantity"`
	UnitPrice float64 `json:"unit_price"`
}

type Order struct {
	ID              string      `json:"id"`
	UserID          string      `json:"user_id"`
	Items           []OrderItem `json:"items"`
	TotalAmount     float64     `json:"total_amount"`
	Status          OrderStatus `json:"status"`
	IdempotencyKey  string      `json:"idempotency_key,omitempty"`
	Region          string      `json:"region"`
	DegradedMode    bool        `json:"degraded_mode"`
	FailureReason   string      `json:"failure_reason,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// ── Idempotency Store ─────────────────────────────────────────────────────────

type IdempotencyStore struct {
	mu      sync.RWMutex
	records map[string]string // key → orderID
}

func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{records: make(map[string]string)}
}

func (s *IdempotencyStore) GetOrSet(key, orderID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok {
		return existing, true // already exists
	}
	s.records[key] = orderID
	return orderID, false
}

func (s *IdempotencyStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// ── Order Store ───────────────────────────────────────────────────────────────

type OrderStore struct {
	mu     sync.RWMutex
	orders map[string]*Order
}

func NewOrderStore() *OrderStore {
	return &OrderStore{orders: make(map[string]*Order)}
}

func (s *OrderStore) Create(order *Order) {
	s.mu.Lock()
	s.orders[order.ID] = order
	s.mu.Unlock()
}

func (s *OrderStore) Get(id string) (*Order, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orders[id]
	return o, ok
}

func (s *OrderStore) Update(id string, fn func(*Order)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return fmt.Errorf("order %s not found", id)
	}
	fn(o)
	o.UpdatedAt = time.Now()
	return nil
}

func (s *OrderStore) ListByUser(userID string) []*Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Order
	for _, o := range s.orders {
		if o.UserID == userID {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *OrderStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.orders)
}

// ── Downstream Client Stubs ───────────────────────────────────────────────────

type DownstreamStatus int

const (
	DownstreamHealthy   DownstreamStatus = iota
	DownstreamDegraded
	DownstreamDown
)

// InventoryClient simulates calling the inventory service
type InventoryClient struct {
	mu     sync.Mutex
	status DownstreamStatus
}

func NewInventoryClient() *InventoryClient {
	return &InventoryClient{status: DownstreamHealthy}
}

func (c *InventoryClient) SetStatus(s DownstreamStatus) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}

func (c *InventoryClient) Reserve(orderID string, items []OrderItem) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.status {
	case DownstreamDown:
		return fmt.Errorf("inventory service unavailable")
	case DownstreamDegraded:
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// PaymentClient simulates calling the payment service
type PaymentClient struct {
	mu     sync.Mutex
	status DownstreamStatus
}

func NewPaymentClient() *PaymentClient {
	return &PaymentClient{status: DownstreamHealthy}
}

func (c *PaymentClient) SetStatus(s DownstreamStatus) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}

func (c *PaymentClient) Charge(orderID string, amount float64, idempotencyKey string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.status {
	case DownstreamDown:
		return fmt.Errorf("payment service unavailable")
	case DownstreamDegraded:
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

// ── Event Bus Stub ────────────────────────────────────────────────────────────

type OutboxEntry struct {
	ID        string
	EventType string
	Payload   any
	Status    string
	Attempts  int
	CreatedAt time.Time
}

type EventPublisher struct {
	mu      sync.RWMutex
	outbox  []*OutboxEntry
	dlq     []*OutboxEntry
	maxSize int
}

func NewEventPublisher() *EventPublisher {
	return &EventPublisher{maxSize: 10000}
}

func (p *EventPublisher) Publish(eventType string, payload any) {
	p.mu.Lock()
	p.outbox = append(p.outbox, &OutboxEntry{
		ID:        newID(),
		EventType: eventType,
		Payload:   payload,
		Status:    "pending",
		CreatedAt: time.Now(),
	})
	if len(p.outbox) > p.maxSize {
		p.outbox = p.outbox[len(p.outbox)-p.maxSize:]
	}
	p.mu.Unlock()
}

func (p *EventPublisher) Stats() map[string]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]int{"outbox": len(p.outbox), "dlq": len(p.dlq)}
}

// ── Order Service ─────────────────────────────────────────────────────────────

type OrderService struct {
	store       *OrderStore
	idempotency *IdempotencyStore
	inventory   *InventoryClient
	payment     *PaymentClient
	publisher   *EventPublisher
	region      string
}

func NewOrderService(region string) *OrderService {
	return &OrderService{
		store:       NewOrderStore(),
		idempotency: NewIdempotencyStore(),
		inventory:   NewInventoryClient(),
		payment:     NewPaymentClient(),
		publisher:   NewEventPublisher(),
		region:      region,
	}
}

type CreateOrderRequest struct {
	UserID         string      `json:"user_id"`
	Items          []OrderItem `json:"items"`
	IdempotencyKey string      `json:"idempotency_key"`
}

type CreateOrderResult struct {
	Order       *Order `json:"order"`
	Idempotent  bool   `json:"idempotent"` // true if this was a duplicate
}

func (s *OrderService) CreateOrder(req CreateOrderRequest) (*CreateOrderResult, error) {
	if req.UserID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("items cannot be empty")
	}

	// Calculate total
	var total float64
	for _, item := range req.Items {
		if item.Quantity <= 0 {
			return nil, fmt.Errorf("quantity must be positive for product %s", item.ProductID)
		}
		if item.UnitPrice < 0 {
			return nil, fmt.Errorf("unit price cannot be negative for product %s", item.ProductID)
		}
		total += float64(item.Quantity) * item.UnitPrice
	}

	orderID := newID()

	// Idempotency check
	if req.IdempotencyKey != "" {
		if existingID, exists := s.idempotency.GetOrSet(req.IdempotencyKey, orderID); exists {
			existing, ok := s.store.Get(existingID)
			if ok {
				return &CreateOrderResult{Order: existing, Idempotent: true}, nil
			}
		}
	}

	order := &Order{
		ID:             orderID,
		UserID:         req.UserID,
		Items:          req.Items,
		TotalAmount:    total,
		Status:         StatusPending,
		IdempotencyKey: req.IdempotencyKey,
		Region:         s.region,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	s.store.Create(order)
	s.publisher.Publish("order.created", order)
	slog.Info("Order created", "order_id", order.ID, "user_id", req.UserID, "total", total)

	// Try inventory reservation — graceful degradation if down
	if err := s.inventory.Reserve(order.ID, order.Items); err != nil {
		slog.Warn("Inventory reservation failed — continuing in degraded mode",
			"order_id", order.ID, "err", err)
		s.store.Update(order.ID, func(o *Order) {
			o.Status = StatusPendingPayment
			o.DegradedMode = true
			o.FailureReason = "inventory service unavailable"
		})
		s.publisher.Publish("order.degraded", order)
		return &CreateOrderResult{Order: order}, nil
	}

	s.store.Update(order.ID, func(o *Order) { o.Status = StatusConfirmed })

	// Try payment — graceful degradation if down
	if err := s.payment.Charge(order.ID, order.TotalAmount, order.IdempotencyKey); err != nil {
		slog.Warn("Payment charge failed — marking as pending_payment",
			"order_id", order.ID, "err", err)
		s.store.Update(order.ID, func(o *Order) {
			o.Status = StatusPendingPayment
			o.DegradedMode = true
			o.FailureReason = "payment service unavailable"
		})
		s.publisher.Publish("order.payment_pending", order)
		return &CreateOrderResult{Order: order}, nil
	}

	s.store.Update(order.ID, func(o *Order) { o.Status = StatusConfirmed })
	s.publisher.Publish("order.confirmed", order)
	slog.Info("Order confirmed", "order_id", order.ID)

	return &CreateOrderResult{Order: order}, nil
}

func (s *OrderService) GetOrder(id string) (*Order, error) {
	order, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("order %s not found", id)
	}
	return order, nil
}

func (s *OrderService) CancelOrder(id string) error {
	return s.store.Update(id, func(o *Order) {
		if o.Status == StatusDelivered || o.Status == StatusShipped {
			return
		}
		o.Status = StatusCancelled
		s.publisher.Publish("order.cancelled", o)
	})
}

func (s *OrderService) GetUserOrders(userID string) []*Order {
	return s.store.ListByUser(userID)
}

func (s *OrderService) Stats() map[string]any {
	return map[string]any{
		"total_orders":     s.store.Count(),
		"idempotency_keys": s.idempotency.Count(),
		"publisher":        s.publisher.Stats(),
		"region":           s.region,
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct{ svc *OrderService }

func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Support idempotency key from header
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		req.IdempotencyKey = key
	}

	result, err := h.svc.CreateOrder(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	status := http.StatusCreated
	if result.Idempotent {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h *handler) getOrder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/orders/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "order id required"})
		return
	}
	order, err := h.svc.GetOrder(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (h *handler) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/orders/")
	id = strings.TrimSuffix(id, "/cancel")
	if err := h.svc.CancelOrder(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *handler) getUserOrders(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id required"})
		return
	}
	orders := h.svc.GetUserOrders(userID)
	if orders == nil {
		orders = []*Order{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders, "count": len(orders)})
}

func (h *handler) getStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.Stats())
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "region": h.svc.region})
}

func (h *handler) metrics(w http.ResponseWriter, _ *http.Request) {
	stats := h.svc.Stats()
	fmt.Fprintf(w, "order_service_total_orders %v\n", stats["total_orders"])
	fmt.Fprintf(w, "order_service_idempotency_keys %v\n", stats["idempotency_keys"])
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
	svc := NewOrderService(region)
	h := &handler{svc: svc}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/orders", methodHandler(map[string]http.HandlerFunc{
		"POST": h.createOrder,
		"GET":  h.getUserOrders,
	}))
	mux.HandleFunc("/v1/orders/", methodHandler(map[string]http.HandlerFunc{
		"GET":  h.getOrder,
		"POST": h.cancelOrder,
	}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.getStats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metrics)

	port := getEnv("HTTP_PORT", "8080")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Order service started", "port", port, "region", region)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down order service...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
// scaffold
// order status
// order item
// order struct
// idempotency store
// get or set
// order store
// store create
// store get
// store update
// store list
// downstream status
// inventory client
// payment client
// event publisher
// order service
// create order
// idempotency check
// inventory degrade
// payment degrade
// cancel order
// stats
// create handler
// get handler
// cancel handler
// user orders handler
// health
// metrics
// routes
// server
// slog startup
// net join
// log degrade
// region event
// feat_23:13
// feat_06:43
// feat_51:13
// feat_35:43
// feat_20:13
// fix_05:43
// fix_50:13
