// Package processing consumes sanitized Ingestion and Retrieval facts for Catalog.
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
const RetrievalQueue = "catalog.retrieval-terminal.v1"

const applicationDeliveryCountHeader = "x-raglibrarian-delivery-count"

type handler interface {
	HandleEnvelope(context.Context, string, string, []byte) (bool, error)
}

type Recorder interface {
	ProcessingConsumerUnavailable()
	ProcessingEventRejected()
	ProcessingEventConflict()
	ProcessingEventApplyFailed()
}

type retryPublisher interface {
	PublishRetry(context.Context, string, amqp091.Publishing) error
}

// confirmedRetryPublisher makes the retry budget independent of quorum-only
// x-delivery-count headers. That keeps retries bounded on classic test queues
// without weakening the production quorum topology.
type confirmedRetryPublisher struct {
	channel *amqp091.Channel
	returns <-chan amqp091.Return
}

func newConfirmedRetryPublisher(connection *amqp091.Connection) (*confirmedRetryPublisher, error) {
	channel, err := connection.Channel()
	if err != nil {
		return nil, err
	}
	if err = channel.Confirm(false); err != nil {
		_ = channel.Close()
		return nil, err
	}
	return &confirmedRetryPublisher{channel: channel, returns: channel.NotifyReturn(make(chan amqp091.Return, 1))}, nil
}

func (p *confirmedRetryPublisher) Close() error {
	return p.channel.Close()
}

func (p *confirmedRetryPublisher) PublishRetry(ctx context.Context, queue string, message amqp091.Publishing) error {
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx, "", queue, true, false, message)
	if err != nil || confirmation == nil {
		return errors.New("catalog retry publish failed")
	}
	acknowledged, err := confirmation.WaitContext(ctx)
	if err != nil || !acknowledged {
		return errors.New("catalog retry publish was not confirmed")
	}
	select {
	case <-p.returns:
		return errors.New("catalog retry publish was returned")
	default:
	}
	return nil
}

// Run reconnects until shutdown. RabbitMQ is asynchronous and therefore does
// not participate in Catalog readiness.
func Run(ctx context.Context, uri string, service handler, recorder Recorder) {
	RunQueue(ctx, uri, Queue, service, recorder)
}

// RunQueue consumes one explicitly provisioned queue using the shared delivery policy.
func RunQueue(ctx context.Context, uri, queue string, service handler, recorder Recorder) {
	if uri == "" || queue == "" || service == nil || recorder == nil {
		panic("catalog processing consumer dependencies are required")
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := consumeConnection(ctx, uri, queue, service, recorder)
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

func consumeConnection(ctx context.Context, uri, queue string, service handler, recorder Recorder) error {
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
	retry, err := newConfirmedRetryPublisher(connection)
	if err != nil {
		return err
	}
	defer func() { _ = retry.Close() }()
	if err = channel.Qos(1, 0, false); err != nil {
		return err
	}
	deliveries, err := channel.Consume(queue, "", false, false, false, false, nil)
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
			handleDelivery(ctx, queue, service, recorder, retry, delivery)
		}
	}
}

func handleDelivery(ctx context.Context, queue string, service handler, recorder Recorder, retry retryPublisher, delivery amqp091.Delivery) {
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
			// Closing the consumer connection recovers unsettled deliveries. Do not
			// explicitly requeue with an unchanged application retry count.
		case <-timer.C:
			publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			publishErr := retry.PublishRetry(publishCtx, queue, retryMessage(delivery, attempt+1))
			cancel()
			if publishErr != nil {
				if ctx.Err() == nil {
					_ = delivery.Nack(false, false)
				}
				return
			}
			_ = delivery.Ack(false)
		}
	}
}

func deliveryAttempt(headers amqp091.Table) int64 {
	value, ok := headers[applicationDeliveryCountHeader]
	if ok {
		return boundedDeliveryAttempt(value)
	}
	value, ok = headers["x-delivery-count"]
	if !ok {
		return 0
	}
	return boundedDeliveryAttempt(value)
}

func boundedDeliveryAttempt(value any) int64 {
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

func retryMessage(delivery amqp091.Delivery, attempt int64) amqp091.Publishing {
	return amqp091.Publishing{
		// Never republish broker-controlled routing or death headers. In
		// particular, CC and BCC can select additional default-exchange queues.
		Headers:         amqp091.Table{applicationDeliveryCountHeader: attempt},
		ContentType:     delivery.ContentType,
		ContentEncoding: delivery.ContentEncoding,
		DeliveryMode:    amqp091.Persistent,
		Priority:        delivery.Priority,
		CorrelationId:   delivery.CorrelationId,
		MessageId:       delivery.MessageId,
		Timestamp:       delivery.Timestamp,
		Type:            delivery.Type,
		AppId:           delivery.AppId,
		Body:            append([]byte(nil), delivery.Body...),
	}
}
