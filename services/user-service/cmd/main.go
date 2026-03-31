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

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	Phone     string    `json:"phone,omitempty"`
	Region    string    `json:"region"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserStore struct {
	mu    sync.RWMutex
	users map[string]*User
	byEmail map[string]string // email → id
}

func NewUserStore() *UserStore {
	return &UserStore{users: make(map[string]*User), byEmail: make(map[string]string)}
}

func (s *UserStore) Create(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byEmail[u.Email]; exists {
		return fmt.Errorf("email already registered")
	}
	s.users[u.ID] = u
	s.byEmail[u.Email] = u.ID
	return nil
}

func (s *UserStore) Get(id string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	return u, ok
}

func (s *UserStore) GetByEmail(email string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byEmail[email]
	if !ok { return nil, false }
	u, ok := s.users[id]
	return u, ok
}

func (s *UserStore) Update(id string, fn func(*User)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok { return fmt.Errorf("user %s not found", id) }
	fn(u)
	u.UpdatedAt = time.Now()
	return nil
}

func (s *UserStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

type UserService struct {
	store  *UserStore
	region string
}

func NewUserService(region string) *UserService {
	return &UserService{store: NewUserStore(), region: region}
}

type CreateUserRequest struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Phone     string `json:"phone,omitempty"`
}

func (s *UserService) CreateUser(req CreateUserRequest) (*User, error) {
	if req.Email == "" { return nil, fmt.Errorf("email required") }
	if req.FirstName == "" { return nil, fmt.Errorf("first_name required") }
	if !strings.Contains(req.Email, "@") { return nil, fmt.Errorf("invalid email format") }
	u := &User{
		ID: newID(), Email: req.Email, FirstName: req.FirstName,
		LastName: req.LastName, Phone: req.Phone,
		Region: s.region, Active: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.store.Create(u); err != nil { return nil, err }
	slog.Info("User created", "user_id", u.ID, "email", u.Email)
	return u, nil
}

func (s *UserService) GetUser(id string) (*User, error) {
	u, ok := s.store.Get(id)
	if !ok { return nil, fmt.Errorf("user %s not found", id) }
	return u, nil
}

func (s *UserService) UpdateUser(id string, req CreateUserRequest) (*User, error) {
	if err := s.store.Update(id, func(u *User) {
		if req.FirstName != "" { u.FirstName = req.FirstName }
		if req.LastName != "" { u.LastName = req.LastName }
		if req.Phone != "" { u.Phone = req.Phone }
	}); err != nil {
		return nil, err
	}
	return s.store.Get(id)
}

func (s *UserService) DeactivateUser(id string) error {
	return s.store.Update(id, func(u *User) { u.Active = false })
}

func (s *UserService) Stats() map[string]any {
	return map[string]any{"total_users": s.store.Count(), "region": s.region}
}

type handler struct{ svc *UserService }

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	u, err := h.svc.CreateUser(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (h *handler) getUser(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/users/")
	u, err := h.svc.GetUser(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *handler) liveness(w http.ResponseWriter, _ *http.Request)  { writeJSON(w, http.StatusOK, map[string]string{"status": "alive"}) }
func (h *handler) readiness(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]string{"status": "ready"}) }
func (h *handler) stats(w http.ResponseWriter, _ *http.Request)     { writeJSON(w, http.StatusOK, h.svc.Stats()) }
func (h *handler) metrics(w http.ResponseWriter, _ *http.Request) {
	stats := h.svc.Stats()
	fmt.Fprintf(w, "user_service_total %v\n", stats["total_users"])
}

func newID() string { b := make([]byte, 8); rand.Read(b); return fmt.Sprintf("%x", b) }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" { return v }
	return fallback
}
func methodHandler(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[strings.ToUpper(r.Method)]; ok { h(w, r); return }
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func main() {
	region := getEnv("REGION", "region-a")
	svc := NewUserService(region)
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/users", methodHandler(map[string]http.HandlerFunc{"POST": h.createUser}))
	mux.HandleFunc("/v1/users/", methodHandler(map[string]http.HandlerFunc{"GET": h.getUser}))
	mux.HandleFunc("/v1/stats", methodHandler(map[string]http.HandlerFunc{"GET": h.stats}))
	mux.HandleFunc("/healthz/live", h.liveness)
	mux.HandleFunc("/healthz/ready", h.readiness)
	mux.HandleFunc("/metrics", h.metrics)
	port := getEnv("HTTP_PORT", "8084")
	srv := &http.Server{Addr: net.JoinHostPort("", port), Handler: mux, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	go func() {
		slog.Info("User service started", "port", port, "region", region)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed { os.Exit(1) }
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
// user struct
// user store
// create
// get
// update
// deactivate
// service
// handlers
// slog startup
// net join
// feat_29:18
// feat_13:48
// feat_58:18
// feat_42:48
// feat_27:18
// fix_12:48
// fix_57:18
// fix_41:48
// fix_26:18
// ref_25:03
