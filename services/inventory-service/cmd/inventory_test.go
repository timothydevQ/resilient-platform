package main

import (
	"testing"
	"time"
)

func newTestInventorySvc() *InventoryService {
	svc := &InventoryService{store: NewInventoryStore(), region: "region-a"}
	return svc
}

func addTestProduct(svc *InventoryService, id string, stock int) *Product {
	p := &Product{ID: id, Name: "Test " + id, SKU: id, Stock: stock, LowThreshold: 5}
	svc.AddProduct(p)
	return p
}

// ── Product Tests ─────────────────────────────────────────────────────────────

func TestAddProduct_Success(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 100)
	p, err := svc.GetProduct("p1")
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if p.Stock != 100 { t.Errorf("expected stock 100, got %d", p.Stock) }
	if p.Available != 100 { t.Errorf("expected available 100, got %d", p.Available) }
}

func TestAddProduct_SetsRegion(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 10)
	p, _ := svc.GetProduct("p1")
	if p.Region != "region-a" { t.Errorf("expected region-a, got %s", p.Region) }
}

func TestGetProduct_NotFound(t *testing.T) {
	svc := newTestInventorySvc()
	_, err := svc.GetProduct("nonexistent")
	if err == nil { t.Error("expected error for missing product") }
}

// ── Reservation Tests ─────────────────────────────────────────────────────────

func TestReserve_Success(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 50)
	res, err := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 10})
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if res.Status != ReservationActive { t.Errorf("expected active, got %s", res.Status) }
	if res.Quantity != 10 { t.Errorf("expected qty 10, got %d", res.Quantity) }
}

func TestReserve_ReducesAvailable(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 50)
	svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 15})
	p, _ := svc.GetProduct("p1")
	if p.Available != 35 { t.Errorf("expected 35 available, got %d", p.Available) }
	if p.Reserved != 15 { t.Errorf("expected 15 reserved, got %d", p.Reserved) }
}

func TestReserve_InsufficientStock(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 5)
	_, err := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 10})
	if err == nil { t.Error("expected error for insufficient stock") }
}

func TestReserve_ProductNotFound(t *testing.T) {
	svc := newTestInventorySvc()
	_, err := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "nonexistent", Quantity: 1})
	if err == nil { t.Error("expected error for missing product") }
}

func TestReserve_MissingOrderID(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 10)
	_, err := svc.Reserve(ReserveRequest{ProductID: "p1", Quantity: 1})
	if err == nil { t.Error("expected error for missing order_id") }
}

func TestReserve_ZeroQuantity(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 10)
	_, err := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 0})
	if err == nil { t.Error("expected error for zero quantity") }
}

func TestReserve_SetsExpiry(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 10)
	res, _ := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 1})
	if res.ExpiresAt.Before(time.Now()) {
		t.Error("expected ExpiresAt to be in the future")
	}
}

// ── Confirm Tests ─────────────────────────────────────────────────────────────

func TestConfirmReservation_Success(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 20)
	res, _ := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 5})
	err := svc.ConfirmReservation(res.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	p, _ := svc.GetProduct("p1")
	if p.Stock != 15 { t.Errorf("expected stock 15 after confirm, got %d", p.Stock) }
}

func TestConfirmReservation_NotFound(t *testing.T) {
	svc := newTestInventorySvc()
	err := svc.ConfirmReservation("nonexistent")
	if err == nil { t.Error("expected error for nonexistent reservation") }
}

// ── Release Tests ─────────────────────────────────────────────────────────────

func TestReleaseReservation_RestoresAvailable(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 20)
	res, _ := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 10})
	svc.ReleaseReservation(res.ID)
	p, _ := svc.GetProduct("p1")
	if p.Available != 20 { t.Errorf("expected 20 available after release, got %d", p.Available) }
}

func TestReleaseReservation_Idempotent(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 20)
	res, _ := svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 5})
	svc.ReleaseReservation(res.ID)
	err := svc.ReleaseReservation(res.ID) // second release should not error
	if err != nil { t.Errorf("expected idempotent release, got error: %v", err) }
}

func TestReleaseReservation_NotFound(t *testing.T) {
	svc := newTestInventorySvc()
	err := svc.ReleaseReservation("nonexistent")
	if err == nil { t.Error("expected error for nonexistent reservation") }
}

// ── Expire Tests ──────────────────────────────────────────────────────────────

func TestExpireStale_ReleasesExpiredReservations(t *testing.T) {
	store := NewInventoryStore()
	p := &Product{ID: "p1", Stock: 20, Available: 20}
	store.products["p1"] = p
	res, _ := store.Reserve("o1", "p1", 10, 0) // 0 TTL = already expired
	_ = res
	expired := store.ExpireStale(time.Now().Add(time.Second))
	if expired != 1 { t.Errorf("expected 1 expired, got %d", expired) }
	if p.Available != 20 { t.Errorf("expected 20 available after expiry, got %d", p.Available) }
}

// ── Low Stock Tests ───────────────────────────────────────────────────────────

func TestLowStockProducts(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 100) // high stock
	p2 := &Product{ID: "p2", Stock: 3, LowThreshold: 5}
	svc.AddProduct(p2)
	low := svc.store.LowStockProducts()
	if len(low) != 1 { t.Errorf("expected 1 low stock product, got %d", len(low)) }
	if low[0].ID != "p2" { t.Errorf("expected p2, got %s", low[0].ID) }
}

// ── Stats Tests ───────────────────────────────────────────────────────────────

func TestStats_ReturnsData(t *testing.T) {
	svc := newTestInventorySvc()
	addTestProduct(svc, "p1", 50)
	svc.Reserve(ReserveRequest{OrderID: "o1", ProductID: "p1", Quantity: 5})
	stats := svc.Stats()
	if stats["products"].(int) != 1 { t.Errorf("expected 1 product, got %v", stats["products"]) }
	if stats["reservations_active"].(int) != 1 { t.Errorf("expected 1 active, got %v", stats["reservations_active"]) }
}
// add product
// sets region
// not found
// reserve success
// reserve reduces
// insufficient
// product not found
// missing order
// zero qty
// expiry
// confirm
// confirm not found
// release restores
// release idem
// expire
// low stock
