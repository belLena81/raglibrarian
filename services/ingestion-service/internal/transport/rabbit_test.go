package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"
)

type recordingAcknowledger struct {
	acked    bool
	rejected bool
	requeued bool
}

func (a *recordingAcknowledger) Ack(uint64, bool) error {
	a.acked = true
	return nil
}
func (a *recordingAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.requeued = requeue
	return nil
}
func (a *recordingAcknowledger) Reject(_ uint64, requeue bool) error {
	a.rejected = true
	a.requeued = requeue
	return nil
}

type countingProcessor struct {
	calls int
	err   error
}

func (p *countingProcessor) Process(context.Context, application.UploadedEvent) error {
	p.calls++
	return p.err
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

func TestConsumerAppliesSharedDeliveryDisposition(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		processErr  error
		wantAck     bool
		wantReject  bool
		wantRequeue bool
	}{
		{name: "success", wantAck: true},
		{name: "durable retry", processErr: application.ErrProcessingDeferred, wantAck: true},
		{name: "invalid", processErr: application.ErrInvalidEvent, wantReject: true},
		{name: "conflict", processErr: application.ErrConflictingEvent, wantReject: true},
		{name: "unsupported", processErr: application.ErrUnsupportedProcessingProfile, wantReject: true},
		{name: "transient", processErr: errors.New("dependency unavailable"), wantRequeue: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{err: test.processErr}
			consumer := &Consumer{processor: processor, now: time.Now}
			consumer.handle(context.Background(), amqp091.Delivery{
				Acknowledger: acknowledger,
				ContentType:  "application/x-protobuf",
				Type:         UploadRoute,
				MessageId:    validUploadMessage().EventId,
				Body:         payload,
			})
			if acknowledger.acked != test.wantAck || acknowledger.rejected != test.wantReject || acknowledger.requeued != test.wantRequeue || processor.calls != 1 {
				t.Fatalf("delivery result = %#v calls=%d", acknowledger, processor.calls)
			}
		})
	}
}

func TestConsumerAppliesDispositionAfterShutdownCancellation(t *testing.T) {
	payload, err := proto.Marshal(validUploadMessage())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		processErr  error
		wantAck     bool
		wantRequeue bool
	}{
		{name: "durable retry", processErr: application.ErrProcessingDeferred, wantAck: true},
		{name: "non-durable transient", processErr: errors.New("database unavailable"), wantRequeue: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			acknowledger := &recordingAcknowledger{}
			processor := &countingProcessor{err: test.processErr}
			consumer := &Consumer{processor: processor, now: time.Now}

			consumer.handle(ctx, amqp091.Delivery{
				Acknowledger: acknowledger,
				ContentType:  "application/x-protobuf",
				Type:         UploadRoute,
				MessageId:    validUploadMessage().EventId,
				Body:         payload,
			})

			if acknowledger.acked != test.wantAck || acknowledger.rejected || acknowledger.requeued != test.wantRequeue || processor.calls != 1 {
				t.Fatalf("shutdown disposition result = %#v calls=%d", acknowledger, processor.calls)
			}
		})
	}
}
