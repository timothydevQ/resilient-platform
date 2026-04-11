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

type Product struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SKU          string    `json:"sku"`
	Stock        int       `json:"stock"`
	Reserved     int       `json:"reserved"`
	Available    int       `json:"available"` // stock - reserved
	LowThreshold int       `json:"low_threshold"`
	Region       string    `json:"region"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ReservationStatus string

const (
	ReservationActive    ReservationStatus = "active"
	ReservationConfirmed ReservationStatus = "confirmed"
	ReservationReleased  ReservationStatus = "released"
	ReservationExpired   ReservationStatus = "expired"
)

type Reservation struct {
	ID        string            `json:"id"`
	OrderID   string            `json:"order_id"`
	ProductID string            `json:"product_id"`
	Quantity  int               `json:"quantity"`
	Status    ReservationStatus `json:"status"`
	ExpiresAt time.Time         `json:"expires_at"`
	CreatedAt time.Time         `json:"created_at"`
}

// ── Inventory Store ───────────────────────────────────────────────────────────

type InventoryStore struct {
	mu           sync.RWMutex
	products     map[string]*Product
	reservations map[string]*Reservation // reservationID → reservation
}

func NewInventoryStore() *InventoryStore {
	return &InventoryStore{
		products:     make(map[string]*Product),
		reservations: make(map[string]*Reservation),
	}
}

func (s *InventoryStore) AddProduct(p *Product) {
	s.mu.Lock()
	p.Available = p.Stock - p.Reserved
	s.products[p.ID] = p
	s.mu.Unlock()
}

func (s *InventoryStore) GetProduct(id string) (*Product, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.products[id]
	return p, ok
}

func (s *InventoryStore) Reserve(orderID, productID string, quantity int, ttl time.Duration) (*Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	product, ok := s.products[productID]
	if !ok {
		return nil, fmt.Errorf("product %s not found", productID)
	}
	if product.Available < quantity {
		return nil, fmt.Errorf("insufficient stock: available=%d requested=%d", product.Available, quantity)
	}

	product.Reserved += quantity
	product.Available = product.Stock - product.Reserved
	product.UpdatedAt = time.Now()

	reservation := &Reservation{
		ID:        newID(),
		OrderID:   orderID,
		ProductID: productID,
		Quantity:  quantity,
		Status:    ReservationActive,
		ExpiresAt: time.Now().Add(ttl),
		CreatedAt: time.Now(),
	}
	s.reservations[reservation.ID] = reservation
	return reservation, nil
}

func (s *InventoryStore) ConfirmReservation(reservationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.reservations[reservationID]
	if !ok {
		return fmt.Errorf("reservation %s not found", reservationID)
	}
	if res.Status != ReservationActive {
		return fmt.Errorf("reservation is not active: %s", res.Status)
	}
	// Deduct from stock permanently
	product, ok := s.products[res.ProductID]
	if !ok {
		return fmt.Errorf("product not found")
	}
	product.Stock -= res.Quantity
	product.Reserved -= res.Quantity
	product.Available = product.Stock - product.Reserved
	product.UpdatedAt = time.Now()
	res.Status = ReservationConfirmed
	return nil
}

func (s *InventoryStore) ReleaseReservation(reservationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.reservations[reservationID]
	if !ok {
		return fmt.Errorf("reservation %s not found", reservationID)
	}
	if res.Status == ReservationReleased || res.Status == ReservationConfirmed {
		return nil // idempotent
	}
	product, ok := s.products[res.ProductID]
	if ok {
		product.Reserved -= res.Quantity
		if product.Reserved < 0 {
			product.Reserved = 0
		}
		product.Available = product.Stock - product.Reserved
		product.UpdatedAt = time.Now()
	}
	res.Status = ReservationReleased
	return nil
}

func (s *InventoryStore) GetReservationsByOrder(orderID string) []*Reservation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Reservation
	for _, r := range s.reservations {
		if r.OrderID == orderID {
			out = append(out, r)
		}
	}
	return out
}

func (s *InventoryStore) ExpireStale(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, res := range s.reservations {
		if res.Status == ReservationActive && now.After(res.ExpiresAt) {
			product, ok := s.products[res.ProductID]
			if ok {
				product.Reserved -= res.Quantity
				if product.Reserved < 0 {
					product.Reserved = 0
				}
				product.Available = product.Stock - product.Reserved
			}
			res.Status = ReservationExpired
			count++
		}
	}
	return count
}

func (s *InventoryStore) LowStockProducts() []*Product {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Product
	for _, p := range s.products {
		if p.LowThreshold > 0 && p.Available <= p.LowThreshold {
			out = append(out, p)
		}
	}
	return out
}

// ── Inventory Service ─────────────────────────────────────────────────────────

type InventoryService struct {
	store  *InventoryStore
	region string
}

func NewInventoryService(region string) *InventoryService {
	svc := &InventoryService{
		store:  NewInventoryStore(),
		region: region,
	}
	go svc.expireLoop()
	return svc
}

func (s *InventoryService) expireLoop() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		expired := s.store.ExpireStale(time.Now())
		if expired > 0 {
			slog.Info("Expired stale reservations", "count", expired)
		}
	}
}

func (s *InventoryService) AddProduct(p *Product) {
	p.Region = s.region
	s.store.AddProduct(p)
}

func (s *InventoryService) GetProduct(id string) (*Product, error) {
	p, ok := s.store.GetProduct(id)
	if !ok {
		return nil, fmt.Errorf("product %s not found", id)
	}
	return p, nil
}

type ReserveRequest struct {
	OrderID   string `json:"order_id"`
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func (s *InventoryService) Reserve(req ReserveRequest) (*Reservation, error) {
	if req.OrderID == "" {
		return nil, fmt.Errorf("order_id required")
	}
	if req.ProductID == "" {
		return nil, fmt.Errorf("product_id required")
	}
	if req.Quantity <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}
	res, err := s.store.Reserve(req.OrderID, req.ProductID, req.Quantity, 15*time.Minute)
	if err != nil {
		slog.Warn("Reservation failed", "order_id", req.OrderID, "product_id", req.ProductID, "err", err)
		return nil, err
	}
	slog.Info("Reserved inventory", "reservation_id", res.ID, "product_id", req.ProductID, "quantity", req.Quantity)

	// Check low stock alert
	if product, ok := s.store.GetProduct(req.ProductID); ok {
		if product.LowThreshold > 0 && product.Available <= product.LowThreshold {
			slog.Warn("Low stock alert", "product_id", req.ProductID, "available", product.Available)
		}
	}
	return res, nil
}

func (s *InventoryService) ConfirmReservation(id string) error {
	return s.store.ConfirmReservation(id)
}

func (s *InventoryService) ReleaseReservation(id string) error {
	return s.store.ReleaseReservation(id)
}

func (s *InventoryService) Stats() map[string]any {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	active, confirmed, released := 0, 0, 0
	for _, r := range s.store.reservations {
		switch r.Status {
		case ReservationActive:
			active++
		case ReservationConfirmed:
			confirmed++
		case ReservationReleased:
			released++
		}
	}
	return map[string]any{
		"products":              len(s.store.products),
		"reservations_active":   active,
		"reservations_confirmed": confirmed,
		"reservations_released": released,
		"low_stock_count":       len(s.store.LowStockProducts()),
		"region":                s.region,
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct{ svc *InventoryService }

func (h *handler) addProduct(w http.ResponseWriter, r *http.Request) {
	var p Product
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid product"})
		return
	}
	if p.ID == "" { p.ID = newID() }
	h.svc.AddProduct(&p)
	writeJSON(w, http.StatusCreated, p)
}

func (h *handler) getProduct(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/products/")
	p, err := h.svc.GetProduct(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) reserve(w http.ResponseWriter, r *http.Request) {
	var req ReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	res, err := h.svc.Reserve(req)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (h *handler) confirmReservation(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("reservation_id")
	if err := h.svc.ConfirmReservation(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}

func (h *handler) releaseReservation(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("reservation_id")
	if err := h.svc.ReleaseReservation(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
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
	fmt.Fprintf(w, "inventory_products %v\n", stats["products"])
	fmt.Fprintf(w, "inventory_reservations_active %v\n", stats["reservations_active"])
	fmt.Fprintf(w, "inventory_low_stock %v\n", stats["low_stock_count"])
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
	svc := NewInventoryService(region)
	h := &handler{svc: svc}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/products", methodHandler(map[string]http.HandlerFunc{"POST": h.addProduct}))
	mux.HandleFunc("/v1/products/", methodHandler(map[string]http.HandlerFunc{"GET": h.getProduct}))
	mux.HandleFunc("/v1/reservations", methodHandler(map[string]http.HandlerFunc{"POST": h.reserve}))
	mux.HandleFunc("/v1/reservations/confirm", methodHandler(map[string]http.HandlerFunc{"POST": h.confirmReservation}))
	mux.HandleFunc("/v1/reservations/release", methodHandler(map[string]http.HandlerFunc{"POST": h.releaseReservation}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.getStats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metrics)

	port := getEnv("HTTP_PORT", "8081")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Inventory service started", "port", port, "region", region)
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
// product struct
// reservation status
// reservation struct
// inventory store
// add product
// get product
// reserve
// reserve reduces
// confirm
// release
// expire stale
// low stock
// expire loop
// reserve handler
// confirm handler
// release handler
// product handler
// health routes
// slog startup
// net join
// log low stock
// feat_37:03
// feat_21:33
// feat_06:03
// feat_50:33
// feat_35:03
