package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// exchangeName is the topic exchange all book events are routed through.
	// Topic exchange (not direct) lets future consumers subscribe to book.*
	// without requiring code changes in this publisher.
	exchangeName = "raglibrarian.books"

	// exchangeType must be "topic" to support wildcard routing key bindings.
	exchangeType = "topic"

	// contentType for all published messages.
	contentType = "application/json"

	// reconnectDelay is the pause between reconnection attempts.
	reconnectDelay = time.Second
)

// AMQPBookPublisher publishes book domain events to a RabbitMQ topic exchange.
// It automatically reconnects when the broker drops the connection, so callers
// can treat it as a long-lived singleton.
//
// All exported methods are safe for concurrent use.
type AMQPBookPublisher struct {
	url  string
	conn *amqp.Connection
	ch   *amqp.Channel
	mu   sync.RWMutex

	// done is closed by Close to stop the reconnect goroutine.
	done chan struct{}
}

// NewAMQPBookPublisher dials the broker, declares the exchange, and returns a
// ready publisher. It also starts a background goroutine that re-establishes
// the connection automatically if the broker drops it.
//
// url is an AMQP URI, e.g. "amqp://guest:guest@localhost:5672/".
func NewAMQPBookPublisher(url string) (*AMQPBookPublisher, error) {
	p := &AMQPBookPublisher{
		url:  url,
		done: make(chan struct{}),
	}

	if err := p.connect(); err != nil {
		return nil, err
	}

	go p.reconnectLoop()

	return p, nil
}

// connect dials the broker and (re-)declares the exchange on a fresh channel.
// It is called both at startup and from the reconnect loop.
func (p *AMQPBookPublisher) connect() error {
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return fmt.Errorf("publisher: dial %q: %w", p.url, err)
	}

	ch, err := conn.Channel()
	if err != nil {
		defer conn.Close() //nolint:errcheck // best-effort cleanup on error path
		return fmt.Errorf("publisher: open channel: %w", err)
	}

	// Declare the exchange idempotently. A mismatch in attributes causes a
	// channel-level error — intentional: misconfiguration should be loud.
	if err = ch.ExchangeDeclare(
		exchangeName,
		exchangeType,
		true,  // durable — survives broker restart
		false, // auto-delete — kept alive when no queues are bound
		false, // internal — reachable by external publishers
		false, // no-wait — wait for broker confirmation
		nil,
	); err != nil {
		defer ch.Close()   //nolint:errcheck
		defer conn.Close() //nolint:errcheck
		return fmt.Errorf("publisher: declare exchange %q: %w", exchangeName, err)
	}

	p.mu.Lock()
	p.conn = conn
	p.ch = ch
	p.mu.Unlock()

	log.Printf("publisher: connected to %q", p.url)
	return nil
}

// reconnectLoop watches the NotifyClose channel and re-dials whenever the
// broker drops the connection. It exits cleanly when Close is called.
func (p *AMQPBookPublisher) reconnectLoop() {
	for {
		p.mu.RLock()
		conn := p.conn
		p.mu.RUnlock()

		closeCh := conn.NotifyClose(make(chan *amqp.Error, 1))

		select {
		case <-p.done:
			// Graceful shutdown — stop the loop.
			return

		case reason, ok := <-closeCh:
			if !ok {
				// Channel closed without an error means we called Close()
				// ourselves; the done channel will also be closed so the next
				// iteration will exit via the case above.
				log.Print("publisher: connection closed normally")
				return
			}
			log.Printf("publisher: connection closed unexpectedly: %v — reconnecting", reason)
		}

		// Retry until we re-establish or are asked to stop.
		for {
			select {
			case <-p.done:
				return
			case <-time.After(reconnectDelay):
			}

			if err := p.connect(); err != nil {
				log.Printf("publisher: reconnect failed: %v", err)
				continue
			}
			log.Print("publisher: reconnect success")
			break
		}
	}
}

// Publish serialises the event to JSON and publishes it to the topic exchange.
// The routing key is the event type string (e.g. "book.created"), which lets
// consumers bind queues with patterns like "book.*".
//
// DeliveryMode is Persistent so the broker writes the message to disk before
// acknowledging, giving at-least-once delivery across broker restarts.
//
// ctx is forwarded to PublishWithContext; a cancelled context aborts the
// publish without corrupting the channel state.
func (p *AMQPBookPublisher) Publish(ctx context.Context, event BookEvent) error {
	if event.OccurredAt == "" {
		event.OccurredAt = time.Now().UTC().Format(time.RFC3339)
	}

	body, err := json.Marshal(event)
	if err != nil {
		// json.Marshal only fails for unmarshalable types (channels, funcs).
		// BookEvent contains only strings — this branch is unreachable in
		// practice, but we keep the explicit error surface.
		return fmt.Errorf("publisher: marshal event %q: %w", event.Event, err)
	}

	routingKey := string(event.Event)

	p.mu.RLock()
	ch := p.ch
	p.mu.RUnlock()

	if err = ch.PublishWithContext(
		ctx,
		exchangeName,
		routingKey,
		false, // mandatory — drop unroutable messages rather than block;
		//         consumers must pre-declare their queues.
		false, // immediate — deprecated in AMQP 0-9-1; always false
		amqp.Publishing{
			ContentType:  contentType,
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now().UTC(),
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("publisher: publish %q to %q: %w", routingKey, exchangeName, err)
	}

	return nil
}

// Close stops the reconnect goroutine then releases the channel and connection
// in the correct protocol order (channel before connection).
func (p *AMQPBookPublisher) Close() error {
	// Signal the reconnect goroutine to exit before we tear down resources.
	close(p.done)

	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error

	if p.ch != nil {
		if err := p.ch.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publisher: close channel: %w", err))
		}
	}

	if p.conn != nil {
		if err := p.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publisher: close connection: %w", err))
		}
	}

	return errors.Join(errs...)
}
