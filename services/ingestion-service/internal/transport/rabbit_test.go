package transport

import (
	"context"
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

func TestConsumerRejectsMissingOrMismatchedMessageID(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"", "different-event"} {
		t.Run(messageID, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{}
			consumer := &Consumer{processor: processor, now: time.Now}
			consumer.handle(context.Background(), amqp091.Delivery{Acknowledger: acknowledger, ContentType: "application/x-protobuf", Type: UploadRoute, MessageId: messageID, Body: payload})
			if !acknowledger.rejected || acknowledger.requeued || processor.calls != 0 {
				t.Fatalf("invalid identity must be rejected without processing: %#v calls=%d", acknowledger, processor.calls)
			}
		})
	}
}
