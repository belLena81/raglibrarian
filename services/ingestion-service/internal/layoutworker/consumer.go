package layoutworker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/rabbitmq/amqp091-go"
)

type Publisher interface {
	PublishWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) error
}

type Consumer struct {
	channel        *amqp091.Channel
	queue          string
	resultExchange string
	service        *Service
	publisher      Publisher
	publishTimeout time.Duration
}

func NewConsumer(channel *amqp091.Channel, queue, resultExchange string, concurrency int, service *Service, publisher Publisher, publishTimeout time.Duration) (*Consumer, error) {
	if channel == nil || queue == "" || resultExchange == "" || concurrency < 1 || service == nil || publisher == nil || publishTimeout <= 0 {
		return nil, errors.New("invalid layout worker consumer")
	}
	if err := channel.Qos(concurrency, 0, false); err != nil {
		return nil, errors.New("layout worker broker qos unavailable")
	}
	return &Consumer{channel: channel, queue: queue, resultExchange: resultExchange, service: service, publisher: publisher, publishTimeout: publishTimeout}, nil
}

func (c *Consumer) Run(ctx context.Context, concurrency int) error {
	deliveries, err := c.channel.Consume(c.queue, "ingestion-layout-worker", false, false, false, false, nil)
	if err != nil {
		return errors.New("layout worker broker consumer unavailable")
	}
	semaphore := make(chan struct{}, concurrency)
	var workers sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return ctx.Err()
		case delivery, open := <-deliveries:
			if !open {
				workers.Wait()
				return errors.New("layout worker broker delivery channel closed")
			}
			semaphore <- struct{}{}
			workers.Add(1)
			go func(item amqp091.Delivery) {
				defer func() { <-semaphore; workers.Done() }()
				c.handle(ctx, item)
			}(delivery)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, delivery amqp091.Delivery) {
	if delivery.Type != contracts.EventIngestionContentSelectionRequested ||
		delivery.ContentType != "application/x-protobuf" || len(delivery.Body) == 0 ||
		len(delivery.Body) > contracts.MaximumBrokerMessageBytes {
		_ = delivery.Reject(false)
		return
	}
	eventID, payload, err := c.service.Process(ctx, delivery.Body)
	if errors.Is(err, ErrInvalidRequest) {
		_ = delivery.Reject(false)
		return
	}
	if err != nil {
		_ = delivery.Nack(false, true)
		return
	}
	publishCtx, cancel := context.WithTimeout(ctx, c.publishTimeout)
	err = c.publisher.PublishWithContext(publishCtx, c.resultExchange, contracts.EventIngestionContentSelectionCompleted, true, false, amqp091.Publishing{
		ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, MessageId: eventID,
		Type: contracts.EventIngestionContentSelectionCompleted, Timestamp: time.Now().UTC(), Body: payload,
	})
	cancel()
	if err != nil {
		_ = delivery.Nack(false, true)
		return
	}
	_ = delivery.Ack(false)
}
