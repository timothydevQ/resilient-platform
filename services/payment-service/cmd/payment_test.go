package main

import (
	"testing"
)

func newTestPaymentSvc() *PaymentService {
	return NewPaymentService("region-a")
}

func validChargeReq() ChargeRequest {
	return ChargeRequest{
		OrderID:  "order-1",
		UserID:   "user-1",
		Amount:   99.99,
		Currency: "USD",
	}
}

// ── Charge Tests ──────────────────────────────────────────────────────────────

func TestCharge_Success(t *testing.T) {
	svc := newTestPaymentSvc()
	p, err := svc.Charge(validChargeReq())
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if p.Status != PaymentSucceeded {
		t.Errorf("expected succeeded, got %s", p.Status)
	}
}

func TestCharge_SetsFields(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	p, _ := svc.Charge(req)
	if p.ID == "" { t.Error("expected non-empty payment ID") }
	if p.OrderID != req.OrderID { t.Errorf("wrong order ID: %s", p.OrderID) }
	if p.Amount != req.Amount { t.Errorf("wrong amount: %f", p.Amount) }
	if p.Region != "region-a" { t.Errorf("wrong region: %s", p.Region) }
}

func TestCharge_DefaultsCurrency(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	req.Currency = ""
	p, _ := svc.Charge(req)
	if p.Currency != "USD" { t.Errorf("expected USD default, got %s", p.Currency) }
}

func TestCharge_MissingOrderID(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	req.OrderID = ""
	_, err := svc.Charge(req)
	if err == nil { t.Error("expected error for missing order_id") }
}

func TestCharge_ZeroAmount(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	req.Amount = 0
	_, err := svc.Charge(req)
	if err == nil { t.Error("expected error for zero amount") }
}

func TestCharge_NegativeAmount(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	req.Amount = -10
	_, err := svc.Charge(req)
	if err == nil { t.Error("expected error for negative amount") }
}

func TestCharge_GatewayDown_ReturnsError(t *testing.T) {
	svc := newTestPaymentSvc()
	svc.gateway.SetStatus(GatewayDown)
	_, err := svc.Charge(validChargeReq())
	if err == nil { t.Error("expected error when gateway is down") }
}

func TestCharge_GatewayDown_SetsFailedStatus(t *testing.T) {
	svc := newTestPaymentSvc()
	svc.gateway.SetStatus(GatewayDown)
	p, _ := svc.Charge(validChargeReq())
	if p == nil { t.Fatal("expected non-nil payment even on failure") }
	if p.Status != PaymentFailed {
		t.Errorf("expected failed status, got %s", p.Status)
	}
}

func TestCharge_GatewayDown_SetsFailureReason(t *testing.T) {
	svc := newTestPaymentSvc()
	svc.gateway.SetStatus(GatewayDown)
	p, _ := svc.Charge(validChargeReq())
	if p.FailureReason == "" { t.Error("expected non-empty failure reason") }
}

// ── Idempotency Tests ─────────────────────────────────────────────────────────

func TestCharge_IdempotencyReturnsSamePayment(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	req.IdempotencyKey = "idem-1"

	p1, err := svc.Charge(req)
	if err != nil { t.Fatalf("first charge error: %v", err) }

	p2, err := svc.Charge(req)
	if err != nil { t.Fatalf("second charge error: %v", err) }

	if p1.ID != p2.ID {
		t.Error("expected same payment ID for idempotent requests")
	}
}

func TestCharge_DifferentIdempotencyKeys_DifferentPayments(t *testing.T) {
	svc := newTestPaymentSvc()
	req1 := validChargeReq()
	req1.IdempotencyKey = "key-1"
	req2 := validChargeReq()
	req2.IdempotencyKey = "key-2"

	p1, _ := svc.Charge(req1)
	p2, _ := svc.Charge(req2)
	if p1.ID == p2.ID {
		t.Error("expected different payment IDs for different keys")
	}
}

// ── Refund Tests ──────────────────────────────────────────────────────────────

func TestRefund_Success(t *testing.T) {
	svc := newTestPaymentSvc()
	p, _ := svc.Charge(validChargeReq())
	err := svc.Refund(p.ID)
	if err != nil { t.Fatalf("refund error: %v", err) }

	refunded, _ := svc.GetPayment(p.ID)
	if refunded.Status != PaymentRefunded {
		t.Errorf("expected refunded status, got %s", refunded.Status)
	}
}

func TestRefund_NonExistentPayment(t *testing.T) {
	svc := newTestPaymentSvc()
	err := svc.Refund("nonexistent")
	if err == nil { t.Error("expected error for nonexistent payment") }
}

func TestRefund_CannotRefundFailed(t *testing.T) {
	svc := newTestPaymentSvc()
	svc.gateway.SetStatus(GatewayDown)
	p, _ := svc.Charge(validChargeReq())
	err := svc.Refund(p.ID)
	if err == nil { t.Error("expected error when refunding a failed payment") }
}

func TestRefund_GatewayDown(t *testing.T) {
	svc := newTestPaymentSvc()
	p, _ := svc.Charge(validChargeReq())
	svc.gateway.SetStatus(GatewayDown)
	err := svc.Refund(p.ID)
	if err == nil { t.Error("expected error when gateway down during refund") }
}

// ── Get Tests ─────────────────────────────────────────────────────────────────

func TestGetPayment_Found(t *testing.T) {
	svc := newTestPaymentSvc()
	p, _ := svc.Charge(validChargeReq())
	found, err := svc.GetPayment(p.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if found.ID != p.ID { t.Error("wrong payment returned") }
}

func TestGetPayment_NotFound(t *testing.T) {
	svc := newTestPaymentSvc()
	_, err := svc.GetPayment("nonexistent")
	if err == nil { t.Error("expected error for missing payment") }
}

func TestGetByOrderID_Found(t *testing.T) {
	svc := newTestPaymentSvc()
	req := validChargeReq()
	req.OrderID = "order-xyz"
	p, _ := svc.Charge(req)
	found, err := svc.GetByOrderID("order-xyz")
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if found.ID != p.ID { t.Error("wrong payment for order") }
}

func TestGetByOrderID_NotFound(t *testing.T) {
	svc := newTestPaymentSvc()
	_, err := svc.GetByOrderID("unknown-order")
	if err == nil { t.Error("expected error for unknown order") }
}

// ── Store Tests ───────────────────────────────────────────────────────────────

func TestPaymentStore_CountByStatus(t *testing.T) {
	svc := newTestPaymentSvc()
	svc.Charge(validChargeReq())
	svc.Charge(validChargeReq())
	svc.gateway.SetStatus(GatewayDown)
	svc.Charge(validChargeReq())

	stats := svc.Stats()
	if stats["succeeded"].(int) != 2 { t.Errorf("expected 2 succeeded, got %v", stats["succeeded"]) }
	if stats["failed"].(int) != 1 { t.Errorf("expected 1 failed, got %v", stats["failed"]) }
}

func TestStats_Region(t *testing.T) {
	svc := NewPaymentService("region-b")
	stats := svc.Stats()
	if stats["region"].(string) != "region-b" {
		t.Errorf("expected region-b, got %v", stats["region"])
	}
}

func TestIdempotencyStore_GetOrSet_Existing(t *testing.T) {
	s := NewIdempotencyStore()
	s.GetOrSet("k1", "pay-1")
	id, exists := s.GetOrSet("k1", "pay-2")
	if !exists { t.Error("expected exists on second call") }
	if id != "pay-1" { t.Errorf("expected original pay-1, got %s", id) }
}
// charge success
// charge fields
// default currency
// missing order
// zero amount
// negative amount
// gateway down
// gateway failed status
// failure reason
// idem same
// idem different
// refund success
// refund not found
// refund failed
// refund gw down
// get found
// get not found
// get by order
// count by status
// stats region
// idem original
// concurrent charge
// tst_38:03
// tst_22:33
// tst_07:03
// tst_51:33
// tst_36:03
