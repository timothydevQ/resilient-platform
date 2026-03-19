package events

import (
	"errors"
	"testing"
	"time"
)

func TestNewEvent_SetsFields(t *testing.T) {
	e, err := NewEvent(EventOrderCreated, "order-1", "order", map[string]string{"key": "val"})
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if e.ID == "" { t.Error("expected non-empty ID") }
	if e.Type != EventOrderCreated { t.Errorf("wrong type: %s", e.Type) }
	if e.AggregateID != "order-1" { t.Errorf("wrong aggregate ID: %s", e.AggregateID) }
	if e.CreatedAt.IsZero() { t.Error("expected non-zero CreatedAt") }
	if e.Version != 1 { t.Errorf("expected version 1, got %d", e.Version) }
}

func TestNewEvent_MarshalPayload(t *testing.T) {
	type Payload struct{ Amount float64 }
	e, err := NewEvent(EventPaymentSucceeded, "pay-1", "payment", Payload{Amount: 99.99})
	if err != nil { t.Fatalf("unexpected error: %v", err) }

	var p Payload
	if err := e.UnmarshalPayload(&p); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if p.Amount != 99.99 { t.Errorf("expected 99.99, got %f", p.Amount) }
}

func TestNewEvent_UniqueIDs(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		e, _ := NewEvent(EventOrderCreated, "agg", "type", nil)
		if ids[e.ID] { t.Errorf("duplicate ID: %s", e.ID) }
		ids[e.ID] = true
	}
}

func TestOutbox_AddAndGetPending(t *testing.T) {
	ob := NewOutbox()
	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	entry := ob.Add(e)
	if entry.Status != OutboxPending { t.Errorf("expected pending, got %s", entry.Status) }

	pending := ob.GetPending()
	if len(pending) != 1 { t.Errorf("expected 1 pending, got %d", len(pending)) }
}

func TestOutbox_MarkPublished(t *testing.T) {
	ob := NewOutbox()
	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	entry := ob.Add(e)
	ob.MarkPublished(entry.ID)

	pending := ob.GetPending()
	if len(pending) != 0 { t.Errorf("expected 0 pending after publish, got %d", len(pending)) }
}

func TestOutbox_MarkFailed_IncreasesAttempts(t *testing.T) {
	ob := NewOutbox()
	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	entry := ob.Add(e)
	ob.MarkFailed(entry.ID, errors.New("connection refused"))

	ob.mu.RLock()
	storedEntry := ob.entries[entry.ID]
	ob.mu.RUnlock()

	if storedEntry.Attempts != 1 { t.Errorf("expected 1 attempt, got %d", storedEntry.Attempts) }
	if storedEntry.LastError == "" { t.Error("expected non-empty last error") }
}

func TestOutbox_MovesToDLQAfterMaxAttempts(t *testing.T) {
	ob := NewOutbox()
	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	entry := ob.Add(e)
	entry.MaxAttempts = 2

	ob.MarkFailed(entry.ID, errors.New("err"))
	ob.MarkFailed(entry.ID, errors.New("err"))

	dlq := ob.GetDLQ()
	if len(dlq) != 1 { t.Errorf("expected 1 DLQ entry, got %d", len(dlq)) }
	if dlq[0].Status != OutboxDead { t.Errorf("expected dead status, got %s", dlq[0].Status) }
}

func TestOutbox_Stats(t *testing.T) {
	ob := NewOutbox()
	e1, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	e2, _ := NewEvent(EventOrderCreated, "o2", "order", nil)
	entry1 := ob.Add(e1)
	ob.Add(e2)
	ob.MarkPublished(entry1.ID)

	stats := ob.Stats()
	if stats["published"] != 1 { t.Errorf("expected 1 published, got %d", stats["published"]) }
	if stats["pending"] != 1 { t.Errorf("expected 1 pending, got %d", stats["pending"]) }
}

func TestEventBus_PublishAndSubscribe(t *testing.T) {
	bus := NewEventBus()
	received := make([]*Event, 0)

	bus.Subscribe(EventOrderCreated, func(e *Event) error {
		received = append(received, e)
		return nil
	})

	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	if err := bus.Publish(e); err != nil {
		t.Fatalf("publish error: %v", err)
	}

	if len(received) != 1 { t.Errorf("expected 1 received, got %d", len(received)) }
	if received[0].ID != e.ID { t.Errorf("wrong event ID") }
}

func TestEventBus_OnlyDeliverToMatchingSubscribers(t *testing.T) {
	bus := NewEventBus()
	orderCount := 0
	paymentCount := 0

	bus.Subscribe(EventOrderCreated, func(e *Event) error { orderCount++; return nil })
	bus.Subscribe(EventPaymentSucceeded, func(e *Event) error { paymentCount++; return nil })

	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	bus.Publish(e)

	if orderCount != 1 { t.Errorf("expected 1 order event, got %d", orderCount) }
	if paymentCount != 0 { t.Errorf("expected 0 payment events, got %d", paymentCount) }
}

func TestEventBus_MultipleSubscribersSameEvent(t *testing.T) {
	bus := NewEventBus()
	count := 0
	bus.Subscribe(EventOrderCreated, func(e *Event) error { count++; return nil })
	bus.Subscribe(EventOrderCreated, func(e *Event) error { count++; return nil })

	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	bus.Publish(e)

	if count != 2 { t.Errorf("expected 2 deliveries, got %d", count) }
}

func TestEventBus_History(t *testing.T) {
	bus := NewEventBus()
	for i := 0; i < 5; i++ {
		e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
		bus.Publish(e)
	}
	hist := bus.History(3)
	if len(hist) != 3 { t.Errorf("expected 3 history entries, got %d", len(hist)) }
}

func TestEventBus_HistoryLessThanLimit(t *testing.T) {
	bus := NewEventBus()
	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	bus.Publish(e)
	hist := bus.History(10)
	if len(hist) != 1 { t.Errorf("expected 1, got %d", len(hist)) }
}

func TestDLQ_AddAndCount(t *testing.T) {
	dlq := NewDLQ(100)
	e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
	entry := &OutboxEntry{Event: e, Status: OutboxDead, CreatedAt: time.Now()}
	dlq.Add(entry)
	if dlq.Count() != 1 { t.Errorf("expected 1, got %d", dlq.Count()) }
}

func TestDLQ_MaxSizeEviction(t *testing.T) {
	dlq := NewDLQ(3)
	for i := 0; i < 5; i++ {
		e, _ := NewEvent(EventOrderCreated, "o1", "order", nil)
		dlq.Add(&OutboxEntry{Event: e, Status: OutboxDead})
	}
	if dlq.Count() > 3 { t.Errorf("expected max 3, got %d", dlq.Count()) }
}

func TestOutboxStatus_Values(t *testing.T) {
	statuses := []OutboxStatus{OutboxPending, OutboxPublished, OutboxFailed, OutboxDead}
	for _, s := range statuses {
		if s == "" { t.Error("expected non-empty status") }
	}
}
// new event fields
// marshal payload
// unique ids
// outbox add pending
