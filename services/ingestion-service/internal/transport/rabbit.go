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
	channel        *amqp091.Channel
	queue          string
	processor      EventProcessor
	retryPublisher Publisher
	retryExchange  string
	now            func() time.Time
}

func NewConsumer(channel *amqp091.Channel, queue string, concurrency int, processor EventProcessor, retryPublisher Publisher, retryExchange string) (*Consumer, error) {
	if channel == nil || queue == "" || concurrency < 1 || processor == nil || retryPublisher == nil || retryExchange == "" {
		return nil, errors.New("invalid RabbitMQ consumer")
	}
	if err := channel.Qos(concurrency, 0, false); err != nil {
		return nil, err
	}
	return &Consumer{channel: channel, queue: queue, processor: processor, retryPublisher: retryPublisher, retryExchange: retryExchange, now: time.Now}, nil
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
	if err == nil {
		_ = delivery.Ack(false)
		return
	}
	var deferred application.DeferredError
	if errors.As(err, &deferred) {
		route := retryRoute(time.Until(deferred.RetryAt))
		if c.publishRetry(ctx, delivery, route, nil) != nil {
			_ = delivery.Reject(false)
			return
		}
		_ = delivery.Ack(false)
		return
	}
	if errors.Is(err, application.ErrInvalidEvent) || errors.Is(err, application.ErrConflictingEvent) {
		_ = delivery.Reject(false)
		return
	}
	attempt := retryAttempt(delivery.Headers) + 1
	if attempt > 4 {
		_ = delivery.Reject(false)
		return
	}
	delay := retryDelay(attempt)
	if c.publishRetry(ctx, delivery, retryRoute(delay), amqp091.Table{"x-ingestion-retry-attempt": int32(attempt)}) != nil { // #nosec G115 -- attempt is bounded to four.
		_ = delivery.Reject(false)
		return
	}
	_ = delivery.Ack(false)
}

func (c *Consumer) publishRetry(ctx context.Context, delivery amqp091.Delivery, route string, headers amqp091.Table) error {
	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.retryPublisher.PublishWithContext(publishCtx, c.retryExchange, route, true, false, amqp091.Publishing{
		Headers:       headers,
		ContentType:   delivery.ContentType,
		DeliveryMode:  amqp091.Persistent,
		MessageId:     delivery.MessageId,
		Type:          delivery.Type,
		CorrelationId: delivery.CorrelationId,
		Timestamp:     c.now().UTC(),
		Body:          delivery.Body,
	})
}

func retryAttempt(headers amqp091.Table) int {
	value, exists := headers["x-ingestion-retry-attempt"]
	if !exists {
		return 0
	}
	switch attempt := value.(type) {
	case byte:
		return boundedRetryAttempt(int64(attempt))
	case int8:
		return boundedRetryAttempt(int64(attempt))
	case int:
		if attempt >= 0 && attempt <= 4 {
			return attempt
		}
		return 4
	case int16:
		return boundedRetryAttempt(int64(attempt))
	case int32:
		return boundedRetryAttempt(int64(attempt))
	case int64:
		return boundedRetryAttempt(attempt)
	}
	return 4
}

func boundedRetryAttempt(value int64) int {
	if value < 0 || value > 4 {
		return 4
	}
	return int(value)
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 2 * time.Minute
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
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if err := w.PublishPending(ctx); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *OutboxWorker) PublishPending(ctx context.Context) error {
	now := w.now().UTC()
	events, err := w.repository.ClaimOutbox(ctx, now, w.lease)
	if err != nil {
		return err
	}
	for _, event := range events {
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = w.publisher.PublishWithContext(publishCtx, w.exchange, event.Type, true, false, amqp091.Publishing{ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, MessageId: event.ID, Type: event.Type, Timestamp: now, Body: event.Payload})
		cancel()
		if err != nil {
			_ = w.repository.RetryOutbox(ctx, event.ID, now, event.Attempts)
			return err
		}
		if err = w.repository.MarkPublished(ctx, event.ID, now); err != nil {
			return err
		}
	}
	return nil
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
