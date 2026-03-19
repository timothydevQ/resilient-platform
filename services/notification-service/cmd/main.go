package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
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

type NotificationType string

const (
	NotifEmail NotificationType = "email"
	NotifSMS   NotificationType = "sms"
	NotifPush  NotificationType = "push"
)

type NotificationStatus string

const (
	NotifPending  NotificationStatus = "pending"
	NotifSent     NotificationStatus = "sent"
	NotifFailed   NotificationStatus = "failed"
	NotifRetrying NotificationStatus = "retrying"
	NotifDead     NotificationStatus = "dead"
)

type Notification struct {
	ID          string             `json:"id"`
	UserID      string             `json:"user_id"`
	Type        NotificationType   `json:"type"`
	Subject     string             `json:"subject"`
	Body        string             `json:"body"`
	Metadata    map[string]string  `json:"metadata,omitempty"`
	Status      NotificationStatus `json:"status"`
	Attempts    int                `json:"attempts"`
	MaxAttempts int                `json:"max_attempts"`
	LastError   string             `json:"last_error,omitempty"`
	ScheduledAt time.Time          `json:"scheduled_at"`
	SentAt      *time.Time         `json:"sent_at,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
}

// ── Notification Store ────────────────────────────────────────────────────────

type NotificationStore struct {
	mu            sync.RWMutex
	notifications map[string]*Notification
	dlq           []*Notification
	maxDLQ        int
}

func NewNotificationStore() *NotificationStore {
	return &NotificationStore{
		notifications: make(map[string]*Notification),
		maxDLQ:        1000,
	}
}

func (s *NotificationStore) Create(n *Notification) {
	s.mu.Lock()
	s.notifications[n.ID] = n
	s.mu.Unlock()
}

func (s *NotificationStore) Get(id string) (*Notification, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.notifications[id]
	return n, ok
}

func (s *NotificationStore) Update(id string, fn func(*Notification)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notifications[id]
	if !ok {
		return fmt.Errorf("notification %s not found", id)
	}
	fn(n)
	return nil
}

func (s *NotificationStore) GetPending() []*Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Notification
	for _, n := range s.notifications {
		if n.Status == NotifPending || n.Status == NotifRetrying {
			out = append(out, n)
		}
	}
	return out
}

func (s *NotificationStore) MoveToDLQ(n *Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n.Status = NotifDead
	s.dlq = append(s.dlq, n)
	if len(s.dlq) > s.maxDLQ {
		s.dlq = s.dlq[len(s.dlq)-s.maxDLQ:]
	}
}

func (s *NotificationStore) GetDLQ() []*Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]*Notification, len(s.dlq))
	copy(cp, s.dlq)
	return cp
}

func (s *NotificationStore) CountByStatus(status NotificationStatus) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, n := range s.notifications {
		if n.Status == status {
			count++
		}
	}
	return count
}

// ── Delivery Provider (stub) ──────────────────────────────────────────────────

type ProviderStatus int

const (
	ProviderHealthy  ProviderStatus = iota
	ProviderDegraded
	ProviderDown
)

type DeliveryProvider struct {
	mu     sync.Mutex
	status ProviderStatus
}

func NewDeliveryProvider() *DeliveryProvider {
	return &DeliveryProvider{status: ProviderHealthy}
}

func (p *DeliveryProvider) SetStatus(s ProviderStatus) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

func (p *DeliveryProvider) Send(n *Notification) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.status {
	case ProviderDown:
		return fmt.Errorf("delivery provider unavailable")
	case ProviderDegraded:
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// ── Notification Service ──────────────────────────────────────────────────────

type NotificationService struct {
	store    *NotificationStore
	provider *DeliveryProvider
	region   string
}

func NewNotificationService(region string) *NotificationService {
	svc := &NotificationService{
		store:    NewNotificationStore(),
		provider: NewDeliveryProvider(),
		region:   region,
	}
	go svc.processLoop()
	return svc
}

func (s *NotificationService) processLoop() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		s.processPending()
	}
}

func (s *NotificationService) processPending() {
	pending := s.store.GetPending()
	for _, n := range pending {
		s.deliver(n)
	}
}

func (s *NotificationService) deliver(n *Notification) {
	if err := s.provider.Send(n); err != nil {
		slog.Warn("Notification delivery failed", "id", n.ID, "attempt", n.Attempts+1, "err", err)
		s.store.Update(n.ID, func(notif *Notification) {
			notif.Attempts++
			notif.LastError = err.Error()
			if notif.Attempts >= notif.MaxAttempts {
				s.store.MoveToDLQ(notif)
				slog.Error("Notification moved to DLQ", "id", notif.ID, "user_id", notif.UserID)
			} else {
				notif.Status = NotifRetrying
			}
		})
		return
	}
	now := time.Now()
	s.store.Update(n.ID, func(notif *Notification) {
		notif.Status = NotifSent
		notif.SentAt = &now
		notif.Attempts++
	})
	slog.Info("Notification sent", "id", n.ID, "type", n.Type, "user_id", n.UserID)
}

type SendRequest struct {
	UserID   string            `json:"user_id"`
	Type     NotificationType  `json:"type"`
	Subject  string            `json:"subject"`
	Body     string            `json:"body"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (s *NotificationService) Send(req SendRequest) (*Notification, error) {
	if req.UserID == "" {
		return nil, fmt.Errorf("user_id required")
	}
	if req.Type == "" {
		return nil, fmt.Errorf("type required")
	}
	if req.Body == "" {
		return nil, fmt.Errorf("body required")
	}
	validTypes := map[NotificationType]bool{
		NotifEmail: true, NotifSMS: true, NotifPush: true,
	}
	if !validTypes[req.Type] {
		return nil, fmt.Errorf("invalid type: %s", req.Type)
	}

	n := &Notification{
		ID:          newID(),
		UserID:      req.UserID,
		Type:        req.Type,
		Subject:     req.Subject,
		Body:        req.Body,
		Metadata:    req.Metadata,
		Status:      NotifPending,
		MaxAttempts: 5,
		ScheduledAt: time.Now(),
		CreatedAt:   time.Now(),
	}

	s.store.Create(n)

	// Attempt immediate delivery
	s.deliver(n)

	// Return current state
	current, _ := s.store.Get(n.ID)
	if current != nil {
		return current, nil
	}
	return n, nil
}

func (s *NotificationService) Get(id string) (*Notification, error) {
	n, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("notification %s not found", id)
	}
	return n, nil
}

func (s *NotificationService) GetDLQ() []*Notification {
	return s.store.GetDLQ()
}

func (s *NotificationService) Stats() map[string]any {
	return map[string]any{
		"sent":     s.store.CountByStatus(NotifSent),
		"pending":  s.store.CountByStatus(NotifPending),
		"retrying": s.store.CountByStatus(NotifRetrying),
		"failed":   s.store.CountByStatus(NotifFailed),
		"dlq":      len(s.store.GetDLQ()),
		"region":   s.region,
	}
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct{ svc *NotificationService }

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	n, err := h.svc.Send(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (h *handler) getNotification(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/notifications/")
	n, err := h.svc.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *handler) getDLQ(w http.ResponseWriter, _ *http.Request) {
	dlq := h.svc.GetDLQ()
	if dlq == nil { dlq = []*Notification{} }
	writeJSON(w, http.StatusOK, map[string]any{"dlq": dlq, "count": len(dlq)})
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
	fmt.Fprintf(w, "notification_sent %v\n", stats["sent"])
	fmt.Fprintf(w, "notification_dlq %v\n", stats["dlq"])
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
	svc := NewNotificationService(region)
	h := &handler{svc: svc}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/notifications", methodHandler(map[string]http.HandlerFunc{"POST": h.send}))
	mux.HandleFunc("/v1/notifications/", methodHandler(map[string]http.HandlerFunc{"GET": h.getNotification}))
	mux.HandleFunc("/v1/dlq", methodHandler(map[string]http.HandlerFunc{"GET": h.getDLQ}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.getStats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metrics)

	port := getEnv("HTTP_PORT", "8083")
	srv := &http.Server{
		Addr:         net.JoinHostPort("", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Notification service started", "port", port, "region", region)
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

var _ = math.Pi
// notif type
// notif status
// notif struct
// notif store
// store create
// store get
// store update
// get pending
// move to dlq
// count status
// provider
// deliver
// dlq move
// process loop
// send
// handlers
// slog startup
// net join
// log dlq
// feat_14:28
