package events

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ── Event Types ───────────────────────────────────────────────────────────────

type EventType string

const (
	// Order events
	EventOrderCreated    EventType = "order.created"
	EventOrderConfirmed  EventType = "order.confirmed"
	EventOrderCancelled  EventType = "order.cancelled"
	EventOrderShipped    EventType = "order.shipped"
	EventOrderDelivered  EventType = "order.delivered"

	// Payment events
	EventPaymentRequested EventType = "payment.requested"
	EventPaymentSucceeded EventType = "payment.succeeded"
	EventPaymentFailed    EventType = "payment.failed"

	// Inventory events
	EventInventoryReserved EventType = "inventory.reserved"
	EventInventoryReleased EventType = "inventory.released"
	EventInventoryLow      EventType = "inventory.low"

	// Notification events
	EventNotificationRequested EventType = "notification.requested"
	EventNotificationSent      EventType = "notification.sent"
	EventNotificationFailed    EventType = "notification.failed"
)

// ── Event ─────────────────────────────────────────────────────────────────────

type Event struct {
	ID            string          `json:"id"`
	Type          EventType       `json:"type"`
	AggregateID   string          `json:"aggregate_id"`
	AggregateType string          `json:"aggregate_type"`
	Payload       json.RawMessage `json:"payload"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	Version       int             `json:"version"`
	Region        string          `json:"region"`
}

func NewEvent(eventType EventType, aggregateID, aggregateType string, payload any) (*Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Event{
		ID:            newID(),
		Type:          eventType,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Payload:       raw,
		CreatedAt:     time.Now(),
		Version:       1,
		Region:        "region-a",
	}, nil
}

func (e *Event) UnmarshalPayload(v any) error {
	return json.Unmarshal(e.Payload, v)
}

// ── Outbox Pattern ────────────────────────────────────────────────────────────

type OutboxStatus string

const (
	OutboxPending   OutboxStatus = "pending"
	OutboxPublished OutboxStatus = "published"
	OutboxFailed    OutboxStatus = "failed"
	OutboxDead      OutboxStatus = "dead" // moved to DLQ
)

type OutboxEntry struct {
	ID          string       `json:"id"`
	Event       *Event       `json:"event"`
	Status      OutboxStatus `json:"status"`
	Attempts    int          `json:"attempts"`
	MaxAttempts int          `json:"max_attempts"`
	LastError   string       `json:"last_error,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	ProcessedAt *time.Time   `json:"processed_at,omitempty"`
}

// Outbox is an in-memory outbox for reliable event publishing.
// In production this would be backed by the same DB transaction as the business write.
type Outbox struct {
	mu      sync.RWMutex
	entries map[string]*OutboxEntry
	dlq     []*OutboxEntry
}

func NewOutbox() *Outbox {
	return &Outbox{entries: make(map[string]*OutboxEntry)}
}

func (o *Outbox) Add(event *Event) *OutboxEntry {
	entry := &OutboxEntry{
		ID:          newID(),
		Event:       event,
		Status:      OutboxPending,
		MaxAttempts: 5,
		CreatedAt:   time.Now(),
	}
	o.mu.Lock()
	o.entries[entry.ID] = entry
	o.mu.Unlock()
	return entry
}

func (o *Outbox) MarkPublished(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if entry, ok := o.entries[id]; ok {
		entry.Status = OutboxPublished
		now := time.Now()
		entry.ProcessedAt = &now
	}
}

func (o *Outbox) MarkFailed(id string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[id]
	if !ok {
		return
	}
	entry.Attempts++
	entry.LastError = err.Error()
	if entry.Attempts >= entry.MaxAttempts {
		entry.Status = OutboxDead
		o.dlq = append(o.dlq, entry)
	} else {
		entry.Status = OutboxFailed
	}
}

func (o *Outbox) GetPending() []*OutboxEntry {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var out []*OutboxEntry
	for _, e := range o.entries {
		if e.Status == OutboxPending || e.Status == OutboxFailed {
			out = append(out, e)
		}
	}
	return out
}

func (o *Outbox) GetDLQ() []*OutboxEntry {
	o.mu.RLock()
	defer o.mu.RUnlock()
	cp := make([]*OutboxEntry, len(o.dlq))
	copy(cp, o.dlq)
	return cp
}

func (o *Outbox) Stats() map[string]int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	stats := map[string]int{"dlq": len(o.dlq)}
	for _, e := range o.entries {
		stats[string(e.Status)]++
	}
	return stats
}

// ── In-Memory Event Bus (simulates Kafka for local dev) ───────────────────────

type Handler func(event *Event) error

type EventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
	history  []*Event
	maxHist  int
}

func NewEventBus() *EventBus {
	return &EventBus{
		handlers: make(map[EventType][]Handler),
		maxHist:  10000,
	}
}

func (b *EventBus) Subscribe(eventType EventType, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

func (b *EventBus) Publish(event *Event) error {
	b.mu.Lock()
	b.history = append(b.history, event)
	if len(b.history) > b.maxHist {
		b.history = b.history[len(b.history)-b.maxHist:]
	}
	handlers := make([]Handler, len(b.handlers[event.Type]))
	copy(handlers, b.handlers[event.Type])
	b.mu.Unlock()

	for _, h := range handlers {
		if err := h(event); err != nil {
			return err
		}
	}
	return nil
}

func (b *EventBus) History(limit int) []*Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.history) <= limit {
		cp := make([]*Event, len(b.history))
		copy(cp, b.history)
		return cp
	}
	cp := make([]*Event, limit)
	copy(cp, b.history[len(b.history)-limit:])
	return cp
}

// ── Dead Letter Queue ─────────────────────────────────────────────────────────

type DLQ struct {
	mu      sync.RWMutex
	entries []*OutboxEntry
	maxSize int
}

func NewDLQ(maxSize int) *DLQ {
	return &DLQ{maxSize: maxSize}
}

func (d *DLQ) Add(entry *OutboxEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, entry)
	if len(d.entries) > d.maxSize {
		d.entries = d.entries[len(d.entries)-d.maxSize:]
	}
}

func (d *DLQ) List() []*OutboxEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cp := make([]*OutboxEntry, len(d.entries))
	copy(cp, d.entries)
	return cp
}

func (d *DLQ) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.entries)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
// event types
// event struct
// new event
// unmarshal
// outbox status
// outbox entry
// outbox struct
// outbox add
// outbox published
// outbox failed
// outbox dlq
// outbox pending
// outbox stats
// event bus
