package main

import (
	"testing"
	"time"
)

func newTestSvc() *OrderService {
	return NewOrderService("region-a")
}

func validRequest() CreateOrderRequest {
	return CreateOrderRequest{
		UserID: "user-1",
		Items: []OrderItem{
			{ProductID: "prod-1", Quantity: 2, UnitPrice: 29.99},
			{ProductID: "prod-2", Quantity: 1, UnitPrice: 9.99},
		},
	}
}

// ── Order Creation ────────────────────────────────────────────────────────────

func TestCreateOrder_Success(t *testing.T) {
	svc := newTestSvc()
	result, err := svc.CreateOrder(validRequest())
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if result.Order == nil { t.Fatal("expected non-nil order") }
	if result.Order.ID == "" { t.Error("expected non-empty order ID") }
	if result.Order.Status != StatusConfirmed {
		t.Errorf("expected confirmed, got %s", result.Order.Status)
	}
}

func TestCreateOrder_SetsTotal(t *testing.T) {
	svc := newTestSvc()
	req := CreateOrderRequest{
		UserID: "user-1",
		Items: []OrderItem{
			{ProductID: "p1", Quantity: 2, UnitPrice: 10.00},
			{ProductID: "p2", Quantity: 3, UnitPrice: 5.00},
		},
	}
	result, err := svc.CreateOrder(req)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if result.Order.TotalAmount != 35.00 {
		t.Errorf("expected total 35.00, got %f", result.Order.TotalAmount)
	}
}

func TestCreateOrder_MissingUserID(t *testing.T) {
	svc := newTestSvc()
	req := validRequest()
	req.UserID = ""
	_, err := svc.CreateOrder(req)
	if err == nil { t.Error("expected error for missing user_id") }
}

func TestCreateOrder_EmptyItems(t *testing.T) {
	svc := newTestSvc()
	req := CreateOrderRequest{UserID: "user-1", Items: []OrderItem{}}
	_, err := svc.CreateOrder(req)
	if err == nil { t.Error("expected error for empty items") }
}

func TestCreateOrder_NegativeQuantity(t *testing.T) {
	svc := newTestSvc()
	req := CreateOrderRequest{
		UserID: "user-1",
		Items:  []OrderItem{{ProductID: "p1", Quantity: -1, UnitPrice: 10.00}},
	}
	_, err := svc.CreateOrder(req)
	if err == nil { t.Error("expected error for negative quantity") }
}

func TestCreateOrder_NegativePrice(t *testing.T) {
	svc := newTestSvc()
	req := CreateOrderRequest{
		UserID: "user-1",
		Items:  []OrderItem{{ProductID: "p1", Quantity: 1, UnitPrice: -5.00}},
	}
	_, err := svc.CreateOrder(req)
	if err == nil { t.Error("expected error for negative unit price") }
}

func TestCreateOrder_SetsRegion(t *testing.T) {
	svc := NewOrderService("region-b")
	result, _ := svc.CreateOrder(validRequest())
	if result.Order.Region != "region-b" {
		t.Errorf("expected region-b, got %s", result.Order.Region)
	}
}

func TestCreateOrder_SetsTimestamps(t *testing.T) {
	svc := newTestSvc()
	before := time.Now()
	result, _ := svc.CreateOrder(validRequest())
	if result.Order.CreatedAt.Before(before) {
		t.Error("CreatedAt should be after test start")
	}
}

// ── Idempotency ───────────────────────────────────────────────────────────────

func TestCreateOrder_IdempotencyReturnsSameOrder(t *testing.T) {
	svc := newTestSvc()
	req := validRequest()
	req.IdempotencyKey = "idem-key-1"

	result1, err := svc.CreateOrder(req)
	if err != nil { t.Fatalf("first call error: %v", err) }

	result2, err := svc.CreateOrder(req)
	if err != nil { t.Fatalf("second call error: %v", err) }

	if result1.Order.ID != result2.Order.ID {
		t.Error("expected same order ID for idempotent calls")
	}
	if !result2.Idempotent {
		t.Error("expected Idempotent=true on second call")
	}
}

func TestCreateOrder_DifferentKeysDifferentOrders(t *testing.T) {
	svc := newTestSvc()
	req1 := validRequest()
	req1.IdempotencyKey = "key-1"
	req2 := validRequest()
	req2.IdempotencyKey = "key-2"

	r1, _ := svc.CreateOrder(req1)
	r2, _ := svc.CreateOrder(req2)

	if r1.Order.ID == r2.Order.ID {
		t.Error("expected different orders for different keys")
	}
}

func TestCreateOrder_NoIdempotencyKeyAlwaysCreates(t *testing.T) {
	svc := newTestSvc()
	r1, _ := svc.CreateOrder(validRequest())
	r2, _ := svc.CreateOrder(validRequest())
	if r1.Order.ID == r2.Order.ID {
		t.Error("expected different orders without idempotency key")
	}
}

// ── Graceful Degradation ──────────────────────────────────────────────────────

func TestCreateOrder_InventoryDown_DegradedMode(t *testing.T) {
	svc := newTestSvc()
	svc.inventory.SetStatus(DownstreamDown)
	result, err := svc.CreateOrder(validRequest())
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if !result.Order.DegradedMode {
		t.Error("expected degraded mode when inventory is down")
	}
	if result.Order.Status != StatusPendingPayment {
		t.Errorf("expected pending_payment in degraded mode, got %s", result.Order.Status)
	}
}

func TestCreateOrder_PaymentDown_DegradedMode(t *testing.T) {
	svc := newTestSvc()
	svc.payment.SetStatus(DownstreamDown)
	result, err := svc.CreateOrder(validRequest())
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if !result.Order.DegradedMode {
		t.Error("expected degraded mode when payment is down")
	}
}

func TestCreateOrder_DegradedHasFailureReason(t *testing.T) {
	svc := newTestSvc()
	svc.inventory.SetStatus(DownstreamDown)
	result, _ := svc.CreateOrder(validRequest())
	if result.Order.FailureReason == "" {
		t.Error("expected non-empty failure reason in degraded mode")
	}
}

// ── Get and Cancel ────────────────────────────────────────────────────────────

func TestGetOrder_Found(t *testing.T) {
	svc := newTestSvc()
	result, _ := svc.CreateOrder(validRequest())
	found, err := svc.GetOrder(result.Order.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if found.ID != result.Order.ID { t.Error("wrong order returned") }
}

func TestGetOrder_NotFound(t *testing.T) {
	svc := newTestSvc()
	_, err := svc.GetOrder("nonexistent")
	if err == nil { t.Error("expected error for missing order") }
}

func TestCancelOrder_Success(t *testing.T) {
	svc := newTestSvc()
	result, _ := svc.CreateOrder(validRequest())
	err := svc.CancelOrder(result.Order.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	order, _ := svc.GetOrder(result.Order.ID)
	if order.Status != StatusCancelled {
		t.Errorf("expected cancelled, got %s", order.Status)
	}
}

func TestCancelOrder_NotFound(t *testing.T) {
	svc := newTestSvc()
	err := svc.CancelOrder("nonexistent")
	if err == nil { t.Error("expected error for nonexistent order") }
}

func TestGetUserOrders_ReturnsUserOrders(t *testing.T) {
	svc := newTestSvc()
	req := validRequest()
	svc.CreateOrder(req)
	svc.CreateOrder(req)

	req2 := validRequest()
	req2.UserID = "user-2"
	svc.CreateOrder(req2)

	orders := svc.GetUserOrders("user-1")
	if len(orders) != 2 {
		t.Errorf("expected 2 orders for user-1, got %d", len(orders))
	}
}

func TestGetUserOrders_EmptyForUnknownUser(t *testing.T) {
	svc := newTestSvc()
	orders := svc.GetUserOrders("nobody")
	if len(orders) != 0 {
		t.Errorf("expected 0 orders, got %d", len(orders))
	}
}

// ── Store Tests ───────────────────────────────────────────────────────────────

func TestOrderStore_CreateAndGet(t *testing.T) {
	s := NewOrderStore()
	order := &Order{ID: "o1", UserID: "u1", Status: StatusPending, CreatedAt: time.Now()}
	s.Create(order)
	got, ok := s.Get("o1")
	if !ok { t.Fatal("expected to find order") }
	if got.ID != "o1" { t.Errorf("wrong order ID") }
}

func TestOrderStore_UpdateNotFound(t *testing.T) {
	s := NewOrderStore()
	err := s.Update("nonexistent", func(o *Order) {})
	if err == nil { t.Error("expected error for nonexistent order") }
}

func TestIdempotencyStore_GetOrSet(t *testing.T) {
	s := NewIdempotencyStore()
	id, exists := s.GetOrSet("key1", "order-1")
	if exists { t.Error("expected not exists on first set") }
	if id != "order-1" { t.Errorf("expected order-1, got %s", id) }

	id2, exists2 := s.GetOrSet("key1", "order-2")
	if !exists2 { t.Error("expected exists on second set") }
	if id2 != "order-1" { t.Errorf("expected order-1 (original), got %s", id2) }
}

func TestStats_ReturnsData(t *testing.T) {
	svc := newTestSvc()
	svc.CreateOrder(validRequest())
	stats := svc.Stats()
	if stats["total_orders"].(int) != 1 {
		t.Errorf("expected 1 total order, got %v", stats["total_orders"])
	}
	if stats["region"].(string) != "region-a" {
		t.Errorf("expected region-a, got %v", stats["region"])
	}
}
// create success
// total calc
// missing user
