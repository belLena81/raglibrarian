package layoutworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/rabbitmq/amqp091-go"
)

type consumerAcknowledger struct {
	acked, rejected, nacked bool
	requeue                 bool
}

func (a *consumerAcknowledger) Ack(uint64, bool) error {
	a.acked = true
	return nil
}

func (a *consumerAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.nacked = true
	a.requeue = requeue
	return nil
}

func (a *consumerAcknowledger) Reject(uint64, bool) error {
	a.rejected = true
	return nil
}

type consumerPublisher struct {
	err       error
	published int
}

func (p *consumerPublisher) PublishWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) error {
	p.published++
	return p.err
}

func TestConsumerSettlesOnlyAfterCompletionPublish(t *testing.T) {
	source := []byte("private uploaded book")
	service := newTestService(t, testSource{contents: source, size: int64(len(source))}, &testAnalyzer{err: errors.New("sanitized fallback")})
	for name, publishErr := range map[string]error{"confirmed": nil, "publish failure": errors.New("broker unavailable")} {
		t.Run(name, func(t *testing.T) {
			publisher := &consumerPublisher{err: publishErr}
			consumer := &Consumer{service: service, publisher: publisher, resultExchange: contracts.ExchangeIngestionContentSelectionResults, publishTimeout: time.Second}
			acknowledger := &consumerAcknowledger{}
			consumer.handle(context.Background(), amqp091.Delivery{
				Acknowledger: acknowledger, DeliveryTag: 1, Type: contracts.EventIngestionContentSelectionRequested,
				ContentType: "application/x-protobuf", Body: requestPayload(t, source, 3),
			})
			if publisher.published != 1 {
				t.Fatalf("publish count = %d", publisher.published)
			}
			if publishErr == nil && (!acknowledger.acked || acknowledger.nacked || acknowledger.rejected) {
				t.Fatalf("successful settlement = %+v", acknowledger)
			}
			if publishErr != nil && (!acknowledger.nacked || !acknowledger.requeue || acknowledger.acked) {
				t.Fatalf("failed settlement = %+v", acknowledger)
			}
		})
	}
}

func TestConsumerRejectsInvalidRequestWithoutPublishing(t *testing.T) {
	publisher := &consumerPublisher{}
	service := newTestService(t, testSource{}, &testAnalyzer{})
	consumer := &Consumer{publisher: publisher, service: service}
	acknowledger := &consumerAcknowledger{}
	consumer.handle(context.Background(), amqp091.Delivery{
		Acknowledger: acknowledger, DeliveryTag: 1, Type: contracts.EventIngestionContentSelectionRequested,
		ContentType: "application/x-protobuf", Body: []byte("invalid"),
	})
	if !acknowledger.rejected || publisher.published != 0 {
		t.Fatalf("settlement = %+v publishes=%d", acknowledger, publisher.published)
	}
}
