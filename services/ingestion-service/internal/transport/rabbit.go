package transport

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/repository"
	"github.com/rabbitmq/amqp091-go"
)

const UploadRoute = "catalog.book.uploaded.v1"
const RetryExchange = "raglibrarian.ingestion.retry.v1"

type EventProcessor interface {
	Process(context.Context, application.UploadedEvent) error
}

type Consumer struct {
	channel   *amqp091.Channel
	queue     string
	processor EventProcessor
	now       func() time.Time
}

func NewConsumer(channel *amqp091.Channel, queue string, concurrency int, processor EventProcessor) (*Consumer, error) {
	if channel == nil || queue == "" || concurrency < 1 || processor == nil {
		return nil, errors.New("invalid RabbitMQ consumer")
	}
	if err := channel.Qos(concurrency, 0, false); err != nil {
		return nil, err
	}
	return &Consumer{channel: channel, queue: queue, processor: processor, now: time.Now}, nil
}

func (c *Consumer) Run(ctx context.Context, concurrency int) error {
	deliveries, err := c.channel.Consume(c.queue, "ingestion-worker", false, false, false, false, nil)
	if err != nil {
		return errors.New("broker consumer unavailable")
	}
	var wait sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for {
		select {
		case <-ctx.Done():
			wait.Wait()
			return ctx.Err()
		case delivery, open := <-deliveries:
			if !open {
				wait.Wait()
				return errors.New("broker delivery channel closed")
			}
			sem <- struct{}{}
			wait.Add(1)
			go func() {
				defer func() { <-sem; wait.Done() }()
				c.handle(ctx, delivery)
			}()
		}
	}
}

func (c *Consumer) handle(ctx context.Context, delivery amqp091.Delivery) {
	if delivery.Type != UploadRoute || delivery.ContentType != "application/x-protobuf" || len(delivery.Body) == 0 || len(delivery.Body) > 256<<10 {
		_ = delivery.Reject(false)
		return
	}
	event, err := DecodeUploaded(delivery.Body)
	if err != nil || delivery.MessageId == "" || delivery.MessageId != event.EventID {
		_ = delivery.Reject(false)
		return
	}
	err = c.processor.Process(ctx, event)
	if ctx.Err() != nil {
		_ = delivery.Nack(false, true)
		return
	}
	switch application.DeliveryDisposition(err) {
	case application.DeliveryAcknowledge:
		_ = delivery.Ack(false)
	case application.DeliveryReject:
		_ = delivery.Reject(false)
	case application.DeliveryRequeue:
		_ = delivery.Nack(false, true)
	}
}

func retryRoute(delay time.Duration) string {
	if delay <= 5*time.Second {
		return "ingestion.retry.5s"
	}
	if delay <= 30*time.Second {
		return "ingestion.retry.30s"
	}
	return "ingestion.retry.2m"
}

type Publisher interface {
	PublishWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) error
}

type OutboxWorker struct {
	repository *repository.Postgres
	publisher  Publisher
	exchange   string
	interval   time.Duration
	lease      time.Duration
	now        func() time.Time
}

func NewOutboxWorker(repo *repository.Postgres, publisher Publisher, exchange string, interval time.Duration) (*OutboxWorker, error) {
	if repo == nil || publisher == nil || exchange == "" || interval <= 0 {
		return nil, errors.New("invalid outbox worker")
	}
	return &OutboxWorker{repository: repo, publisher: publisher, exchange: exchange, interval: interval, lease: 30 * time.Second, now: time.Now}, nil
}

func (w *OutboxWorker) Run(ctx context.Context) error {
	// One-second recovery polling remains even when a larger legacy interval is
	// configured; normal latency comes from the post-commit buffered wake.
	recovery := min(w.interval, time.Second)
	ticker := time.NewTicker(recovery)
	defer ticker.Stop()
	for {
		if err := w.PublishPending(ctx); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.repository.Wake():
		case <-ticker.C:
		}
	}
}

func (w *OutboxWorker) PublishPending(ctx context.Context) error {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		published, err := w.publishBatch(ctx)
		if err != nil || !published || time.Now().After(deadline) {
			return err
		}
	}
}

func (w *OutboxWorker) publishBatch(ctx context.Context) (bool, error) {
	now := w.now().UTC()
	events, err := w.repository.ClaimOutbox(ctx, now, w.lease)
	if err != nil {
		return false, err
	}
	published := len(events) > 0
	for _, event := range events {
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = w.publisher.PublishWithContext(publishCtx, w.exchange, event.Type, true, false, amqp091.Publishing{ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, MessageId: event.ID, Type: event.Type, Timestamp: now, Body: event.Payload})
		cancel()
		if err != nil {
			_ = w.repository.RetryOutbox(ctx, event.ID, now, event.Attempts)
			return published, err
		}
		if err = w.repository.MarkPublished(ctx, event.ID, now); err != nil {
			return published, err
		}
	}
	retries, err := w.repository.ClaimRetryDispatches(ctx, now, w.lease)
	if err != nil {
		return published, err
	}
	published = published || len(retries) > 0
	for _, retry := range retries {
		delay := retry.DispatchAfter.Sub(now)
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = w.publisher.PublishWithContext(publishCtx, RetryExchange, retryRoute(delay), true, false, amqp091.Publishing{ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, MessageId: retry.EventID, Type: UploadRoute, Timestamp: now, Body: retry.Payload})
		cancel()
		if err != nil {
			_ = w.repository.RetryRetryDispatch(ctx, retry.JobID, retry.Attempt, now)
			return published, err
		}
		if err = w.repository.MarkRetryPublished(ctx, retry.JobID, retry.Attempt, now); err != nil {
			return published, err
		}
	}
	return published, nil
}

type ReconnectingPublisher struct {
	uri           string
	mu            sync.Mutex
	connection    *amqp091.Connection
	channel       *amqp091.Channel
	confirmations <-chan amqp091.Confirmation
	returns       <-chan amqp091.Return
}

func NewReconnectingPublisher(uri string) *ReconnectingPublisher {
	return &ReconnectingPublisher{uri: uri}
}

func (p *ReconnectingPublisher) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, message amqp091.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.connect(ctx); err != nil {
		return err
	}
	tag := p.channel.GetNextPublishSeqNo()
	if err := p.channel.PublishWithContext(ctx, exchange, key, mandatory, immediate, message); err != nil {
		_ = p.close()
		return errors.New("broker publish unavailable")
	}
	for {
		select {
		case <-ctx.Done():
			_ = p.close()
			return ctx.Err()
		case _, open := <-p.returns:
			if !open {
				_ = p.close()
				return errors.New("broker return channel closed")
			}
			_ = p.close()
			return errors.New("broker returned message")
		case confirmation, open := <-p.confirmations:
			if !open || !confirmation.Ack || confirmation.DeliveryTag != tag {
				_ = p.close()
				return errors.New("broker confirmation failed")
			}
			select {
			case <-p.returns:
				_ = p.close()
				return errors.New("broker returned message")
			default:
				return nil
			}
		}
	}
}

func (p *ReconnectingPublisher) connect(ctx context.Context) error {
	if p.channel != nil {
		return nil
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := amqp091.DialConfig(p.uri, amqp091.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) { return dialer.DialContext(ctx, network, address) }})
	if err != nil {
		return errors.New("broker unavailable")
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return errors.New("broker unavailable")
	}
	if err = channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = connection.Close()
		return errors.New("broker confirms unavailable")
	}
	p.connection, p.channel = connection, channel
	p.confirmations = channel.NotifyPublish(make(chan amqp091.Confirmation, 1))
	p.returns = channel.NotifyReturn(make(chan amqp091.Return, 1))
	return nil
}

func (p *ReconnectingPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.close()
}
func (p *ReconnectingPublisher) close() error {
	if p.connection == nil {
		return nil
	}
	err := p.connection.Close()
	p.connection, p.channel = nil, nil
	return err
}

func DialConsumer(ctx context.Context, uri string) (*amqp091.Connection, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := amqp091.DialConfig(uri, amqp091.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) { return dialer.DialContext(ctx, network, address) }})
	if err != nil {
		return nil, errors.New("broker unavailable")
	}
	return connection, nil
}
