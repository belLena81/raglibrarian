package transport

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"
)

type recordingAcknowledger struct {
	rejected bool
	requeued bool
}

func (*recordingAcknowledger) Ack(uint64, bool) error { return nil }
func (a *recordingAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.requeued = requeue
	return nil
}
func (a *recordingAcknowledger) Reject(_ uint64, requeue bool) error {
	a.rejected = true
	a.requeued = requeue
	return nil
}

type countingProcessor struct{ calls int }

func (p *countingProcessor) Process(context.Context, application.UploadedEvent) error {
	p.calls++
	return nil
}

type unusedPublisher struct{}

func (unusedPublisher) PublishWithContext(context.Context, string, string, bool, bool, amqp091.Publishing) error {
	return nil
}

func TestConsumerRejectsMissingOrMismatchedMessageID(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"", "different-event"} {
		t.Run(messageID, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{}
			consumer := &Consumer{processor: processor, retryPublisher: unusedPublisher{}, retryExchange: RetryExchange, now: time.Now}
			consumer.handle(context.Background(), amqp091.Delivery{Acknowledger: acknowledger, ContentType: "application/x-protobuf", Type: UploadRoute, MessageId: messageID, Body: payload})
			if !acknowledger.rejected || acknowledger.requeued || processor.calls != 0 {
				t.Fatalf("invalid identity must be rejected without processing: %#v calls=%d", acknowledger, processor.calls)
			}
		})
	}
}

func TestRetryAttemptAcceptsBoundedIntegerRepresentations(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "byte", value: byte(2)},
		{name: "int8", value: int8(2)},
		{name: "int", value: int(2)},
		{name: "int16", value: int16(2)},
		{name: "int32", value: int32(2)},
		{name: "int64", value: int64(2)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := retryAttempt(amqp091.Table{"x-ingestion-retry-attempt": test.value})
			if got != 2 {
				t.Fatalf("expected attempt 2, got %d", got)
			}
		})
	}
}

func TestRetryAttemptTreatsMalformedValuesAsExhausted(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "negative int8", value: int8(-1)},
		{name: "negative int", value: int(-1)},
		{name: "negative int16", value: int16(-1)},
		{name: "negative int32", value: int32(-1)},
		{name: "negative int64", value: int64(-1)},
		{name: "large byte", value: byte(5)},
		{name: "large int8", value: int8(5)},
		{name: "large int", value: math.MaxInt},
		{name: "large int16", value: int16(5)},
		{name: "large int32", value: int32(5)},
		{name: "large int64", value: int64(5)},
		{name: "float", value: float64(1)},
		{name: "string", value: "1"},
		{name: "nil", value: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := retryAttempt(amqp091.Table{"x-ingestion-retry-attempt": test.value})
			if got != 4 {
				t.Fatalf("malformed value must be exhausted, got %d", got)
			}
		})
	}
}

func TestRetryAttemptDefaultsToFirstDeliveryWhenHeaderMissing(t *testing.T) {
	if got := retryAttempt(nil); got != 0 {
		t.Fatalf("expected first delivery, got %d", got)
	}
}
