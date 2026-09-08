package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// SchemaVersion is the current envelope schema version.
const SchemaVersion = 1

// Event is the canonical Motivra domain-event envelope (ADR-0002). Every
// domain event published on JetStream carries these fields; consumers are
// idempotent and dedupe on EventID.
type Event struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	SchemaVersion int             `json:"schema_version"`
	AggregateID   string          `json:"aggregate_id"`
	TenantID      string          `json:"tenant_id"`
	ActorID       string          `json:"actor_id"`
	Timestamp     time.Time       `json:"timestamp"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// NewEvent builds an envelope with fresh identifiers and the current UTC
// timestamp. eventType follows "<aggregate>.<event>.v<n>" (for example
// "vehicle.created.v1"); domain prefixes the JetStream subject.
func NewEvent(eventType, aggregateID, tenantID, actorID, correlationID string, payload any) (Event, error) {
	if eventType == "" || aggregateID == "" {
		return Event{}, fmt.Errorf("event: event_type and aggregate_id are required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("event: marshal payload: %w", err)
	}
	return Event{
		EventID:       uuid.NewString(),
		EventType:     eventType,
		SchemaVersion: SchemaVersion,
		AggregateID:   aggregateID,
		TenantID:      tenantID,
		ActorID:       actorID,
		Timestamp:     time.Now().UTC(),
		CorrelationID: correlationID,
		Payload:       raw,
	}, nil
}

// Subject returns the JetStream subject for the event within domain,
// following motivra.<domain>.<event_type> (ADR-0002).
func Subject(domain string, e Event) string {
	return fmt.Sprintf("motivra.%s.%s", domain, e.EventType)
}

// Publisher publishes domain events.
type Publisher interface {
	Publish(ctx context.Context, domain string, e Event) error
}

// NATSPublisher publishes events to NATS JetStream. The Nats-Msg-Id header
// carries the event ID so the server can also dedupe republished events.
type NATSPublisher struct {
	js jetstream.JetStream
}

// Compile-time assertion that NATSPublisher satisfies Publisher.
var _ Publisher = (*NATSPublisher)(nil)

// NewNATSPublisher connects to url and returns a JetStream publisher.
func NewNATSPublisher(ctx context.Context, url string) (*NATSPublisher, error) {
	conn, err := nats.Connect(url,
		nats.Timeout(10*time.Second),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("nats jetstream: %w", err)
	}
	return &NATSPublisher{js: js}, nil
}

// Publish publishes e to motivra.<domain>.<event_type> with a context
// deadline. It fails fast on context cancellation and wraps transport
// errors.
func (p *NATSPublisher) Publish(ctx context.Context, domain string, e Event) error {
	if ctx.Err() != nil {
		return fmt.Errorf("publish: %w", ctx.Err())
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("publish: marshal event: %w", err)
	}
	msg := nats.NewMsg(Subject(domain, e))
	msg.Data = body
	msg.Header.Set("Nats-Msg-Id", e.EventID)
	msg.Header.Set("Content-Type", "application/json")

	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := p.js.PublishMsg(pctx, msg); err != nil {
		return fmt.Errorf("publish %s: %w", msg.Subject, err)
	}
	return nil
}

// Close drains the underlying connection.
func (p *NATSPublisher) Close() {
	_ = p.js.Conn().Drain()
}

// DedupeStore marks processed event IDs so idempotent consumers can skip
// redeliveries (at-least-once delivery means duplicates happen).
type DedupeStore interface {
	Seen(ctx context.Context, eventID string) (bool, error)
	MarkSeen(ctx context.Context, eventID string, ttl time.Duration) error
}

// NoopDedupe never reports duplicates; suitable for tests and consumers
// whose handlers are naturally idempotent.
type NoopDedupe struct{}

// Seen always reports false.
func (NoopDedupe) Seen(context.Context, string) (bool, error) { return false, nil }

// MarkSeen is a no-op.
func (NoopDedupe) MarkSeen(context.Context, string, time.Duration) error { return nil }

// HandlerFunc processes a decoded event. Returning an error naks the
// message for redelivery.
type HandlerFunc func(ctx context.Context, e Event) error

// Consume creates (or binds) a durable consumer for stream filtered to
// subject and runs handler for every event, skipping duplicates through
// dedupe. Failures nak for redelivery; malformed events are terminated.
func Consume(ctx context.Context, js jetstream.JetStream, stream, subject, durable string, dedupe DedupeStore, handler HandlerFunc) (jetstream.ConsumeContext, error) {
	consumer, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("nats consumer %s: %w", subject, err)
	}
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		var e Event
		if err := json.Unmarshal(msg.Data(), &e); err != nil {
			// A malformed event can never succeed on retry; terminate it.
			_ = msg.Term()
			return
		}
		seen, err := dedupe.Seen(ctx, e.EventID)
		if err == nil && seen {
			_ = msg.Ack()
			return
		}
		if err := handler(ctx, e); err != nil {
			_ = msg.Nak()
			return
		}
		if err := dedupe.MarkSeen(ctx, e.EventID, 24*time.Hour); err != nil {
			// The handler succeeded; losing the dedupe marker only risks a
			// later redelivery re-running an idempotent handler.
			_ = msg.Ack()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return nil, fmt.Errorf("nats consume %s: %w", subject, err)
	}
	return cc, nil
}
