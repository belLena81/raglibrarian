// Package outbox delivers Catalog's durable publication records.
package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/rabbitmq/amqp091-go"

	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

type store interface {
	ClaimOutbox(context.Context, time.Time, time.Duration) ([]repository.PendingOutboxEvent, error)
	MarkPublished(context.Context, string, time.Time) error
	RetryOutbox(context.Context, string, time.Time, int) error
}

type publisher interface {
	PublishWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) error
}

// Recorder exposes operation-specific failure outcomes without leaking an
// event ID, object reference, payload, or broker/database error text.
type Recorder interface {
	OutboxClaimFailed()
	OutboxPublishFailed()
	OutboxRetryFailed()
	OutboxMarkFailed()
}

type Policy struct {
	PollInterval   time.Duration
	DrainBudget    time.Duration
	Lease          time.Duration
	PublishTimeout time.Duration
}

// Run keeps uploads durable during broker loss: failed publication only retries
// the existing outbox record and never changes its event ID or payload.
func Run(ctx context.Context, store store, publisher publisher, recorder Recorder) {
	RunWithWake(ctx, store, publisher, recorder, nil, Policy{})
}

// RunWithWake publishes immediately after a committed write, while retaining
// a periodic poll so a lost in-memory notification cannot strand durable work.
func RunWithWake(ctx context.Context, store store, publisher publisher, recorder Recorder, wake <-chan struct{}, policy Policy) {
	if recorder == nil {
		panic("outbox recorder is required")
	}
	policy = normalizePolicy(policy)
	ticker := time.NewTicker(policy.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			drainPending(ctx, store, publisher, recorder, time.Now().UTC(), policy)
		case now := <-ticker.C:
			drainPending(ctx, store, publisher, recorder, now, policy)
		}
	}
}

func drainPending(ctx context.Context, store store, publisher publisher, recorder Recorder, now time.Time, policy Policy) {
	deadline := time.Now().Add(policy.DrainBudget)
	for {
		if !publishPending(ctx, store, publisher, recorder, now, policy) || time.Now().After(deadline) {
			return
		}
		now = time.Now().UTC()
	}
}

func publishPending(ctx context.Context, store store, publisher publisher, recorder Recorder, now time.Time, policy Policy) bool {
	now = now.UTC()
	policy = normalizePolicy(policy)
	events, err := store.ClaimOutbox(ctx, now, policy.Lease)
	if err != nil {
		recorder.OutboxClaimFailed()
		return false
	}
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		exchange, routingKey, mandatory, routeErr := publicationRoute(event.Type)
		if routeErr != nil {
			recorder.OutboxPublishFailed()
			if retryErr := store.RetryOutbox(ctx, event.ID, now, event.Attempts); retryErr != nil {
				recorder.OutboxRetryFailed()
			}
			continue
		}
		publishCtx, cancel := context.WithTimeout(ctx, policy.PublishTimeout)
		err = publisher.PublishWithContext(publishCtx, exchange, routingKey, mandatory, false, amqp091.Publishing{
			ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent,
			MessageId: event.ID, Type: event.Type, Body: event.Payload,
		})
		cancel()
		if err != nil {
			recorder.OutboxPublishFailed()
			if retryErr := store.RetryOutbox(ctx, event.ID, now, event.Attempts); retryErr != nil {
				recorder.OutboxRetryFailed()
			}
			continue
		}
		if markErr := store.MarkPublished(ctx, event.ID, now); markErr != nil {
			recorder.OutboxMarkFailed()
		}
	}
	return true
}

func normalizePolicy(policy Policy) Policy {
	if policy.PollInterval <= 0 {
		policy.PollInterval = time.Second
	}
	if policy.DrainBudget <= 0 {
		policy.DrainBudget = 250 * time.Millisecond
	}
	if policy.Lease <= 0 {
		policy.Lease = 30 * time.Second
	}
	if policy.PublishTimeout <= 0 {
		policy.PublishTimeout = 5 * time.Second
	}
	return policy
}

func publicationRoute(eventType string) (exchange, routingKey string, mandatory bool, err error) {
	switch eventType {
	case contracts.EventCatalogBookUploaded,
		contracts.EventCatalogBookReindexRequested,
		contracts.EventCatalogBookDeletionRequested:
		return contracts.ExchangeEvents, eventType, true, nil
	case contracts.EventCatalogBookProcessingStatusChange:
		return contracts.ExchangeEdgeStatus, eventType, false, nil
	default:
		return "", "", false, errors.New("unsupported catalog outbox event")
	}
}
