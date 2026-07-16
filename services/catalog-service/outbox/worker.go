// Package outbox delivers Catalog's durable publication records.
package outbox

import (
	"context"
	"time"

	"github.com/rabbitmq/amqp091-go"

	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

const (
	exchange   = "raglibrarian.events.v1"
	routingKey = "catalog.book.uploaded.v1"
)

type store interface {
	ClaimOutbox(context.Context, time.Time, time.Duration) ([]repository.PendingOutboxEvent, error)
	MarkPublished(context.Context, string, time.Time) error
	RetryOutbox(context.Context, string, time.Time, int) error
}

type publisher interface {
	PublishWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) error
}

// Run keeps uploads durable during broker loss: failed publication only retries
// the existing outbox record and never changes its event ID or payload.
func Run(ctx context.Context, store store, publisher publisher) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			events, err := store.ClaimOutbox(ctx, now.UTC(), 30*time.Second)
			if err != nil {
				continue
			}
			for _, event := range events {
				publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = publisher.PublishWithContext(publishCtx, exchange, routingKey, true, false, amqp091.Publishing{
					ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent,
					MessageId: event.ID, Type: event.Type, Body: event.Payload,
				})
				cancel()
				if err != nil {
					_ = store.RetryOutbox(ctx, event.ID, now.UTC(), event.Attempts)
					continue
				}
				_ = store.MarkPublished(ctx, event.ID, now.UTC())
			}
		}
	}
}
