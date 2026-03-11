//go:build e2e

package e2e_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// amqpURL returns the RabbitMQ URL for e2e tests.
// Defaults to the standard local dev broker so `make e2e` works out of the box.
func amqpURL() string {
	if u := os.Getenv("E2E_AMQP_URL"); u != "" {
		return u
	}
	return "amqp://guest:guest@localhost:5672/"
}

// dialAMQP opens a connection + channel to the broker, registering both for
// cleanup when the test ends.
func dialAMQP(t *testing.T) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(amqpURL())
	require.NoError(t, err, "AMQP dial failed — is the broker running?")
	t.Cleanup(func() { conn.Close() })

	ch, err := conn.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { ch.Close() })
	return ch
}

// TestMessaging_BrokerReachable verifies the RabbitMQ broker is up and
// accepting connections. This is the baseline check for all messaging e2e tests.
func TestMessaging_BrokerReachable(t *testing.T) {
	ch := dialAMQP(t)
	assert.NotNil(t, ch)
}

// TestMessaging_Exchange_ExistsAndIsTopic verifies the raglibrarian.books
// exchange has been declared by the metadata service at startup.
// The passive declare fails if the exchange does not exist or has wrong type.
func TestMessaging_Exchange_ExistsAndIsTopic(t *testing.T) {
	ch := dialAMQP(t)

	// ExchangeDeclarePassive does not create the exchange — it asserts existence.
	// If the metadata service has not started yet, this test will fail, which is
	// the correct signal: the exchange is declared at service startup.
	err := ch.ExchangeDeclarePassive(
		"raglibrarian.books",
		"topic",
		true,  // durable
		false, // auto-delete
		false, // internal
		false, // no-wait
		nil,
	)
	assert.NoError(t, err, "exchange raglibrarian.books must be declared by the metadata service")
}

// TestMessaging_BookCreated_EventReachesExchange is a smoke test that publishes
// a synthetic book.created event and confirms it reaches a bound queue.
// This validates the exchange routing without requiring the metadata service
// to be running — useful for CI environments that test the broker in isolation.
func TestMessaging_BookCreated_EventReachesExchange(t *testing.T) {
	ch := dialAMQP(t)

	// Idempotent declare — matches the publisher's declaration exactly.
	require.NoError(t, ch.ExchangeDeclare(
		"raglibrarian.books", "topic",
		true, false, false, false, nil,
	))

	// Bind an exclusive test queue so the event has somewhere to land.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, ch.QueueBind(q.Name, "book.created", "raglibrarian.books", false, nil))

	msgs, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	require.NoError(t, err)

	// Publish a synthetic event — in production this comes from the metadata service.
	payload, err := json.Marshal(map[string]string{
		"event":       "book.created",
		"book_id":     "e2e-book-id",
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)

	require.NoError(t, ch.Publish(
		"raglibrarian.books", "book.created",
		false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         payload,
		},
	))

	select {
	case msg := <-msgs:
		var got map[string]string
		require.NoError(t, json.Unmarshal(msg.Body, &got))
		assert.Equal(t, "book.created", got["event"])
		assert.Equal(t, "e2e-book-id", got["book_id"])
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for book.created message")
	}
}

// TestMessaging_ReindexRequested_RoutedSeparately verifies that
// book.reindex_requested messages are routable independently of book.created.
// A queue bound to book.reindex_requested must not receive book.created events.
func TestMessaging_ReindexRequested_RoutedSeparately(t *testing.T) {
	ch := dialAMQP(t)
	require.NoError(t, ch.ExchangeDeclare(
		"raglibrarian.books", "topic",
		true, false, false, false, nil,
	))

	// Queue bound only to reindex events.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, ch.QueueBind(q.Name, "book.reindex_requested", "raglibrarian.books", false, nil))

	msgs, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	require.NoError(t, err)

	// Publish a book.created — must NOT reach the reindex queue.
	createdPayload, _ := json.Marshal(map[string]string{"event": "book.created", "book_id": "b-1"})
	require.NoError(t, ch.Publish("raglibrarian.books", "book.created", false, false,
		amqp.Publishing{ContentType: "application/json", Body: createdPayload},
	))

	// Publish a book.reindex_requested — MUST reach the queue.
	reindexPayload, _ := json.Marshal(map[string]string{
		"event": "book.reindex_requested", "book_id": "b-2", "s3_key": "books/b-2/file.pdf",
	})
	require.NoError(t, ch.Publish("raglibrarian.books", "book.reindex_requested", false, false,
		amqp.Publishing{ContentType: "application/json", Body: reindexPayload},
	))

	select {
	case msg := <-msgs:
		var got map[string]string
		require.NoError(t, json.Unmarshal(msg.Body, &got))
		assert.Equal(t, "book.reindex_requested", got["event"],
			"only reindex_requested events should reach this queue")
		assert.Equal(t, "b-2", got["book_id"])
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for book.reindex_requested message")
	}
}
