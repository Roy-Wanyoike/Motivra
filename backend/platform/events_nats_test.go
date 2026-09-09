package platform

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNATSPublisherEndToEnd is the one end-to-end publish check of the
// platform event path (issue #28, deferral 2): a real NATS JetStream server
// receives a publisher event on the ADR-0002 subject, the wire body
// round-trips the envelope exactly, and the Nats-Msg-Id header carries the
// event_id so the server can dedupe republished events.
//
// The platform unit tests do not embed a NATS server, so this test is
// skip-gated on TEST_NATS_URL: start the dev stack (`make dev`, which runs
// nats:2.10 with JetStream) and export TEST_NATS_URL=nats://localhost:4222.
// See docs/RUNBOOK.md. CI will execute it once the Actions billing lock
// (issue #10) is lifted.
func TestNATSPublisherEndToEnd(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL is not set; skipping NATS end-to-end publish test — start `make dev` and export TEST_NATS_URL='nats://localhost:4222' (see docs/RUNBOOK.md)")
	}
	ctx := context.Background()

	// Control connection for stream/consumer setup; the publisher owns its
	// own connection (exactly as a service main builds it).
	conn, err := nats.Connect(url, nats.Timeout(10*time.Second))
	require.NoError(t, err)
	defer conn.Close()
	js, err := jetstream.New(conn)
	require.NoError(t, err)

	const streamName = "MOTIVRA_E2E_TEST"
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{"motivra.e2e.>"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })

	pub, err := NewNATSPublisher(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pub.Close)

	e, err := NewEvent("report.signed.v1", "report-42", "org-7", "user-9", "corr-1",
		map[string]any{"note": "end-to-end publish"})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, "e2e", e))

	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       "e2e-verify",
		FilterSubject: Subject("e2e", e),
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(10*time.Second))
	require.NoError(t, err)
	var msg jetstream.Msg
	select {
	case m, ok := <-batch.Messages():
		require.True(t, ok, "message channel closed before delivery")
		msg = m
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the published event")
	}

	subject := Subject("e2e", e)
	assert.Equal(t, subject, msg.Subject(), "subject must be motivra.<domain>.<event_type> (ADR-0002)")
	assert.Equal(t, e.EventID, msg.Headers().Get("Nats-Msg-Id"), "event_id doubles as the JetStream dedupe key")
	assert.Equal(t, "application/json", msg.Headers().Get("Content-Type"))

	var got Event
	require.NoError(t, json.Unmarshal(msg.Data(), &got))
	assert.Equal(t, e.EventID, got.EventID)
	assert.Equal(t, e.EventType, got.EventType)
	assert.Equal(t, e.SchemaVersion, got.SchemaVersion)
	assert.Equal(t, e.AggregateID, got.AggregateID)
	assert.Equal(t, e.TenantID, got.TenantID)
	assert.Equal(t, e.ActorID, got.ActorID)
	assert.Equal(t, e.CorrelationID, got.CorrelationID)
	assert.WithinDuration(t, e.Timestamp, got.Timestamp, time.Second)
	assert.JSONEq(t, string(e.Payload), string(got.Payload))
}
