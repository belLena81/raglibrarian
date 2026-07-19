// Package processing consumes sanitized Ingestion facts for Catalog.
package processing

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/rabbitmq/amqp091-go"

	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

const Queue = "catalog.book-processing.v1"

type handler interface {
	HandleEnvelope(context.Context, string, string, []byte) (bool, error)
}

type Recorder interface {
	ProcessingConsumerUnavailable()
	ProcessingEventRejected()
	ProcessingEventConflict()
	ProcessingEventApplyFailed()
}

// Run reconnects until shutdown. RabbitMQ is asynchronous and therefore does
// not participate in Catalog readiness.
func Run(ctx context.Context, uri string, service handler, recorder Recorder) {
	if uri == "" || service == nil || recorder == nil {
		panic("catalog processing consumer dependencies are required")
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := consumeConnection(ctx, uri, service, recorder)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			recorder.ProcessingConsumerUnavailable()
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func consumeConnection(ctx context.Context, uri string, service handler, recorder Recorder) error {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := amqp091.DialConfig(uri, amqp091.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}})
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = channel.Close() }()
	if err = channel.Qos(1, 0, false); err != nil {
		return err
	}
	deliveries, err := channel.Consume(Queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	closed := connection.NotifyClose(make(chan *amqp091.Error, 1))
	for {
		select {
		case <-ctx.Done():
			return nil
		case closeErr := <-closed:
			if closeErr == nil {
				return errors.New("catalog processing broker connection closed")
			}
			return closeErr
		case delivery, open := <-deliveries:
			if !open {
				return errors.New("catalog processing delivery channel closed")
			}
			handleDelivery(ctx, service, recorder, delivery)
		}
	}
}

func handleDelivery(ctx context.Context, service handler, recorder Recorder, delivery amqp091.Delivery) {
	if delivery.ContentType != "application/x-protobuf" || len(delivery.Body) == 0 || len(delivery.Body) > 64<<10 {
		recorder.ProcessingEventRejected()
		_ = delivery.Nack(false, false)
		return
	}
	handleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err := service.HandleEnvelope(handleCtx, delivery.Type, delivery.MessageId, delivery.Body)
	cancel()
	switch {
	case err == nil:
		_ = delivery.Ack(false)
	case errors.Is(err, catalog.ErrInvalidProcessingEvent), errors.Is(err, catalog.ErrNotFound):
		recorder.ProcessingEventRejected()
		_ = delivery.Nack(false, false)
	case errors.Is(err, catalog.ErrProcessingEventConflict), errors.Is(err, catalog.ErrConflictingProcessingFact):
		recorder.ProcessingEventConflict()
		_ = delivery.Nack(false, false)
	default:
		recorder.ProcessingEventApplyFailed()
		attempt := deliveryAttempt(delivery.Headers)
		if attempt >= 5 {
			_ = delivery.Nack(false, false)
			return
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = delivery.Nack(false, true)
		case <-timer.C:
			_ = delivery.Nack(false, true)
		}
	}
}

func deliveryAttempt(headers amqp091.Table) int64 {
	value, ok := headers["x-delivery-count"]
	if !ok {
		return 0
	}
	switch count := value.(type) {
	case int64:
		if count >= 0 {
			return count
		}
	case int32:
		if count >= 0 {
			return int64(count)
		}
	}
	return 5
}
