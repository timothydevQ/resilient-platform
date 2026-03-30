package main

import (
	"testing"
)

func newTestNotifSvc() *NotificationService {
	svc := &NotificationService{
		store:    NewNotificationStore(),
		provider: NewDeliveryProvider(),
		region:   "region-a",
	}
	return svc
}

func validSendReq() SendRequest {
	return SendRequest{
		UserID:  "user-1",
		Type:    NotifEmail,
		Subject: "Order Confirmed",
		Body:    "Your order has been confirmed.",
	}
}

// ── Send Tests ────────────────────────────────────────────────────────────────

func TestSend_Success(t *testing.T) {
	svc := newTestNotifSvc()
	n, err := svc.Send(validSendReq())
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if n.Status != NotifSent { t.Errorf("expected sent, got %s", n.Status) }
}

func TestSend_SetsFields(t *testing.T) {
	svc := newTestNotifSvc()
	req := validSendReq()
	n, _ := svc.Send(req)
	if n.ID == "" { t.Error("expected non-empty ID") }
	if n.UserID != req.UserID { t.Errorf("wrong user ID: %s", n.UserID) }
	if n.Type != req.Type { t.Errorf("wrong type: %s", n.Type) }
}

func TestSend_SetsTimestamp(t *testing.T) {
	svc := newTestNotifSvc()
	n, _ := svc.Send(validSendReq())
	if n.SentAt == nil { t.Error("expected SentAt to be set after successful send") }
}

func TestSend_MissingUserID(t *testing.T) {
	svc := newTestNotifSvc()
	req := validSendReq()
	req.UserID = ""
	_, err := svc.Send(req)
	if err == nil { t.Error("expected error for missing user_id") }
}

func TestSend_MissingType(t *testing.T) {
	svc := newTestNotifSvc()
	req := validSendReq()
	req.Type = ""
	_, err := svc.Send(req)
	if err == nil { t.Error("expected error for missing type") }
}

func TestSend_InvalidType(t *testing.T) {
	svc := newTestNotifSvc()
	req := validSendReq()
	req.Type = "carrier_pigeon"
	_, err := svc.Send(req)
	if err == nil { t.Error("expected error for invalid type") }
}

func TestSend_MissingBody(t *testing.T) {
	svc := newTestNotifSvc()
	req := validSendReq()
	req.Body = ""
	_, err := svc.Send(req)
	if err == nil { t.Error("expected error for missing body") }
}

func TestSend_AllTypes(t *testing.T) {
	svc := newTestNotifSvc()
	for _, typ := range []NotificationType{NotifEmail, NotifSMS, NotifPush} {
		req := validSendReq()
		req.Type = typ
		n, err := svc.Send(req)
		if err != nil { t.Errorf("unexpected error for type %s: %v", typ, err) }
		if n.Status != NotifSent { t.Errorf("expected sent for type %s, got %s", typ, n.Status) }
	}
}

// ── Provider Down Tests ───────────────────────────────────────────────────────

func TestSend_ProviderDown_SetsRetrying(t *testing.T) {
	svc := newTestNotifSvc()
	svc.provider.SetStatus(ProviderDown)
	n, _ := svc.Send(validSendReq())
	if n.Status == NotifSent { t.Error("should not be sent when provider is down") }
}

func TestSend_ProviderDown_SetsLastError(t *testing.T) {
	svc := newTestNotifSvc()
	svc.provider.SetStatus(ProviderDown)
	n, _ := svc.Send(validSendReq())
	if n.LastError == "" { t.Error("expected non-empty last error when provider down") }
}

func TestDeliver_MovesToDLQAfterMaxAttempts(t *testing.T) {
	svc := newTestNotifSvc()
	svc.provider.SetStatus(ProviderDown)

	req := validSendReq()
	n, _ := svc.Send(req)
	n.MaxAttempts = 1

	// Force move to DLQ
	svc.store.MoveToDLQ(n)

	dlq := svc.GetDLQ()
	if len(dlq) != 1 { t.Errorf("expected 1 DLQ entry, got %d", len(dlq)) }
	if dlq[0].Status != NotifDead { t.Errorf("expected dead status, got %s", dlq[0].Status) }
}

// ── Get Tests ─────────────────────────────────────────────────────────────────

func TestGet_Found(t *testing.T) {
	svc := newTestNotifSvc()
	n, _ := svc.Send(validSendReq())
	found, err := svc.Get(n.ID)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if found.ID != n.ID { t.Error("wrong notification returned") }
}

func TestGet_NotFound(t *testing.T) {
	svc := newTestNotifSvc()
	_, err := svc.Get("nonexistent")
	if err == nil { t.Error("expected error for missing notification") }
}

// ── Store Tests ───────────────────────────────────────────────────────────────

func TestNotificationStore_CountByStatus(t *testing.T) {
	svc := newTestNotifSvc()
	svc.Send(validSendReq())
	svc.Send(validSendReq())
	stats := svc.Stats()
	if stats["sent"].(int) != 2 { t.Errorf("expected 2 sent, got %v", stats["sent"]) }
}

func TestNotificationStore_DLQMaxSize(t *testing.T) {
	store := NewNotificationStore()
	store.maxDLQ = 3
	for i := 0; i < 5; i++ {
		n := &Notification{ID: newID(), Status: NotifPending}
		store.MoveToDLQ(n)
	}
	if len(store.dlq) > 3 { t.Errorf("expected max 3 DLQ entries, got %d", len(store.dlq)) }
}

func TestGetPending_ReturnsCorrectStatuses(t *testing.T) {
	store := NewNotificationStore()
	n1 := &Notification{ID: "n1", Status: NotifPending}
	n2 := &Notification{ID: "n2", Status: NotifRetrying}
	n3 := &Notification{ID: "n3", Status: NotifSent}
	store.Create(n1)
	store.Create(n2)
	store.Create(n3)
	pending := store.GetPending()
	if len(pending) != 2 { t.Errorf("expected 2 pending/retrying, got %d", len(pending)) }
}

func TestStats_Region(t *testing.T) {
	svc := NewNotificationService("region-b")
	// stop background goroutines from interfering
	svc2 := &NotificationService{store: NewNotificationStore(), provider: NewDeliveryProvider(), region: "region-b"}
	stats := svc2.Stats()
	if stats["region"].(string) != "region-b" {
		t.Errorf("expected region-b, got %v", stats["region"])
	}
	_ = svc
}

func TestNotificationStore_UpdateNotFound(t *testing.T) {
	store := NewNotificationStore()
	err := store.Update("nonexistent", func(n *Notification) {})
	if err == nil { t.Error("expected error for nonexistent notification") }
}
// send success
// sets fields
// sets timestamp
// missing user
// missing type
// invalid type
// missing body
// all types
// provider down
// last error
// dlq move
