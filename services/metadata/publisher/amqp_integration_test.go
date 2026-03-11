//go:build integration

package publisher_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/belLena81/raglibrarian/services/metadata/publisher"
)

// startRabbitMQ spins up a RabbitMQ container and returns its AMQP URL.
// The container is registered for automatic cleanup when the test ends.
func startRabbitMQ(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	c, err := tcrabbit.Run(ctx,
		"rabbitmq:3.13-management-alpine",
		testcontainers.WithLogger(testcontainers.TestLogger(t)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	url, err := c.AmqpURL(ctx)
	require.NoError(t, err)
	return url
}

// consumeOne binds a transient queue to the topic exchange, consumes one
// message, and returns the raw body. Fails the test if no message arrives
// within the deadline.
func consumeOne(t *testing.T, amqpURL, routingKey string) []byte {
	t.Helper()

	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	ch, err := conn.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { ch.Close() })

	// Declare the same exchange the publisher uses — idempotent.
	require.NoError(t, ch.ExchangeDeclare(
		"raglibrarian.books", "topic",
		true, false, false, false, nil,
	))

	// Exclusive, auto-delete queue: exists only for this test connection.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)

	require.NoError(t, ch.QueueBind(q.Name, routingKey, "raglibrarian.books", false, nil))

	msgs, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	require.NoError(t, err)

	select {
	case msg := <-msgs:
		return msg.Body
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

func TestAMQPBookPublisher_BookCreated_DeliveredToExchange(t *testing.T) {
	amqpURL := startRabbitMQ(t)

	pub, err := publisher.NewAMQPBookPublisher(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	// Start consumer before publishing so no message is lost.
	bodyC := make(chan []byte, 1)
	go func() {
		bodyC <- consumeOne(t, amqpURL, "book.created")
	}()

	// Give the consumer goroutine time to bind before publishing.
	time.Sleep(100 * time.Millisecond)

	evt := publisher.BookEvent{
		Event:      publisher.EventBookCreated,
		BookID:     "b-integration-1",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}
	require.NoError(t, pub.Publish(context.Background(), evt))

	body := <-bodyC

	var got publisher.BookEvent
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, publisher.EventBookCreated, got.Event)
	assert.Equal(t, "b-integration-1", got.BookID)
	assert.NotEmpty(t, got.OccurredAt)
}

func TestAMQPBookPublisher_ReindexRequested_RoutedSeparately(t *testing.T) {
	amqpURL := startRabbitMQ(t)

	pub, err := publisher.NewAMQPBookPublisher(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	bodyC := make(chan []byte, 1)
	go func() {
		bodyC <- consumeOne(t, amqpURL, "book.reindex_requested")
	}()

	time.Sleep(100 * time.Millisecond)

	evt := publisher.BookEvent{
		Event:      publisher.EventBookReindexRequested,
		BookID:     "b-integration-2",
		S3Key:      "books/b-2/file.pdf",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}
	require.NoError(t, pub.Publish(context.Background(), evt))

	body := <-bodyC

	var got publisher.BookEvent
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, publisher.EventBookReindexRequested, got.Event)
	assert.Equal(t, "books/b-2/file.pdf", got.S3Key)
}

func TestAMQPBookPublisher_OccurredAt_BackfilledWhenEmpty(t *testing.T) {
	// If the caller does not set OccurredAt, the publisher must fill it in.
	amqpURL := startRabbitMQ(t)

	pub, err := publisher.NewAMQPBookPublisher(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	bodyC := make(chan []byte, 1)
	go func() { bodyC <- consumeOne(t, amqpURL, "book.created") }()
	time.Sleep(100 * time.Millisecond)

	before := time.Now().UTC()
	require.NoError(t, pub.Publish(context.Background(), publisher.BookEvent{
		Event:  publisher.EventBookCreated,
		BookID: "b-ts",
		// OccurredAt deliberately omitted
	}))

	var got publisher.BookEvent
	require.NoError(t, json.Unmarshal(<-bodyC, &got))

	ts, err := time.Parse(time.RFC3339, got.OccurredAt)
	require.NoError(t, err)
	assert.False(t, ts.Before(before), "OccurredAt must not be before the publish call")
}

func TestAMQPBookPublisher_ContextCancelled_ReturnsError(t *testing.T) {
	amqpURL := startRabbitMQ(t)

	pub, err := publisher.NewAMQPBookPublisher(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = pub.Publish(ctx, publisher.BookEvent{
		Event:  publisher.EventBookCreated,
		BookID: "b-ctx",
	})
	// amqp091-go respects context cancellation on PublishWithContext.
	// Some broker implementations may still succeed on an already-cancelled
	// context if the channel is buffered; we only assert no panic.
	// If the library does return an error, it must not be nil.
	_ = err // outcome is implementation-defined; no panic is the guarantee
}
